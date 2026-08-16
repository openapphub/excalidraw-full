package main

import (
	"context"
	"encoding/json"
	"excalidraw-complete/core"
	"excalidraw-complete/handlers/api/roomaccess"
	"excalidraw-complete/stores"
	"strings"
	"sync"
	"time"

	socketio "github.com/zishang520/socket.io/v2/socket"
)

const followRoomPrefix = "follow@"

type roomJoinedPayload struct {
	RoomID   string              `json:"roomId"`
	SocketID string              `json:"socketId"`
	Clients  []socketio.SocketId `json:"clients"`
}

func newRoomJoinedPayload(
	room socketio.Room,
	socketID socketio.SocketId,
	clients []socketio.SocketId,
) roomJoinedPayload {
	return roomJoinedPayload{
		RoomID:   string(room),
		SocketID: string(socketID),
		Clients:  clients,
	}
}

type collabSession struct {
	room        socketio.Room
	clientID    string
	access      roomaccess.Access
	validatedAt time.Time
}

type collabSessionRegistry struct {
	mu              sync.RWMutex
	bySocket        map[socketio.SocketId]collabSession
	socketBySession map[string]socketio.SocketId
}

type sceneRealtimeWriteChecker interface {
	CheckSceneRealtimeWrite(ctx context.Context, userID, sceneID string) error
}

func newCollabSessionRegistry() *collabSessionRegistry {
	return &collabSessionRegistry{
		bySocket:        make(map[socketio.SocketId]collabSession),
		socketBySession: make(map[string]socketio.SocketId),
	}
}

func collabSessionKey(room socketio.Room, clientID string) string {
	return string(room) + "\x00" + clientID
}

func (r *collabSessionRegistry) claim(
	socketID socketio.SocketId,
	room socketio.Room,
	clientID string,
	accessValues ...roomaccess.Access,
) (previousRoom socketio.Room, replacedSocket socketio.SocketId) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if previous, ok := r.bySocket[socketID]; ok {
		previousRoom = previous.room
		if previous.clientID != "" {
			key := collabSessionKey(previous.room, previous.clientID)
			if r.socketBySession[key] == socketID {
				delete(r.socketBySession, key)
			}
		}
	}

	access := roomaccess.Access{}
	if len(accessValues) > 0 {
		access = accessValues[0]
	}
	r.bySocket[socketID] = collabSession{
		room:        room,
		clientID:    clientID,
		access:      access,
		validatedAt: time.Now(),
	}
	if clientID != "" {
		key := collabSessionKey(room, clientID)
		replacedSocket = r.socketBySession[key]
		r.socketBySession[key] = socketID
	}

	if replacedSocket == socketID {
		replacedSocket = ""
	}
	return previousRoom, replacedSocket
}

func (r *collabSessionRegistry) release(socketID socketio.SocketId) {
	r.mu.Lock()
	defer r.mu.Unlock()

	session, ok := r.bySocket[socketID]
	if !ok {
		return
	}
	delete(r.bySocket, socketID)
	if session.clientID != "" {
		key := collabSessionKey(session.room, session.clientID)
		if r.socketBySession[key] == socketID {
			delete(r.socketBySession, key)
		}
	}
}

func (r *collabSessionRegistry) roomFor(socketID socketio.SocketId) (socketio.Room, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	session, ok := r.bySocket[socketID]
	return session.room, ok
}

func (r *collabSessionRegistry) sessionFor(socketID socketio.SocketId) (collabSession, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	session, ok := r.bySocket[socketID]
	return session, ok
}

func (r *collabSessionRegistry) sessionsInRoom(room socketio.Room) map[socketio.SocketId]collabSession {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make(map[socketio.SocketId]collabSession)
	for socketID, session := range r.bySocket {
		if session.room == room {
			result[socketID] = session
		}
	}
	return result
}

// validate 重新确认持久化 Scene 的 ACL。高频光标事件最多每秒查一次，可靠
// 内容事件强制实时检查，从而使成员移除或角色降级尽快生效。
func (r *collabSessionRegistry) validate(
	ctx context.Context,
	store stores.Store,
	socketID socketio.SocketId,
	requireWrite bool,
	force bool,
) bool {
	session, ok := r.sessionFor(socketID)
	if !ok {
		return false
	}
	if !session.access.WorkspaceScene {
		return true
	}
	if !force && time.Since(session.validatedAt) < time.Second {
		return !requireWrite || session.access.CanEdit
	}

	scene, err := store.GetScene(ctx, session.access.UserID, string(session.room))
	if err != nil {
		return false
	}

	r.mu.Lock()
	current, exists := r.bySocket[socketID]
	if exists && current.room == session.room && current.access.UserID == session.access.UserID {
		current.access.CanEdit = scene.CanEdit
		current.validatedAt = time.Now()
		r.bySocket[socketID] = current
	}
	r.mu.Unlock()
	return !requireWrite || scene.CanEdit
}

// validateContentWrite 除成员 ACL 外，还校验 Workspace Scene 的编辑模式：
// 独占模式必须由当前 clientId 持锁，协作模式则允许所有可编辑成员广播，
// 最终 SQLite 持久化仍由主写者租约约束。匿名临时房间不涉及 Workspace 锁。
func (r *collabSessionRegistry) validateContentWrite(
	ctx context.Context,
	store stores.Store,
	socketID socketio.SocketId,
) bool {
	if !r.validate(ctx, store, socketID, true, true) {
		return false
	}

	session, ok := r.sessionFor(socketID)
	if !ok {
		return false
	}
	if !session.access.WorkspaceScene {
		return true
	}

	checker, ok := store.(sceneRealtimeWriteChecker)
	if !ok {
		return false
	}
	ctx = core.WithSceneClientID(ctx, session.clientID)
	return checker.CheckSceneRealtimeWrite(
		ctx,
		session.access.UserID,
		string(session.room),
	) == nil
}

func (r *collabSessionRegistry) pruneUnauthorized(
	ctx context.Context,
	ioo *socketio.Server,
	store stores.Store,
	room socketio.Room,
	force bool,
) {
	for socketID, session := range r.sessionsInRoom(room) {
		if !session.access.WorkspaceScene || r.validate(ctx, store, socketID, false, force) {
			continue
		}
		r.release(socketID)
		ioo.In(socketio.Room(socketID)).DisconnectSockets(true)
	}
}

func (r *collabSessionRegistry) sameRoom(first, second socketio.SocketId) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	firstSession, firstOK := r.bySocket[first]
	secondSession, secondOK := r.bySocket[second]
	return firstOK && secondOK && firstSession.room == secondSession.room
}

// commentEventName 是 AstraDraw 客户端广播评论变更用的事件名
// （excalidraw-app/app_constants.ts 的 WS_EVENTS.COMMENT_EVENT）。
const commentEventName = "comment:event"

// registerCommentRelay 在协作房间内转发评论事件，让同房间的其他人无需刷新
// 即可看到新线程/回复。非协作场景（无房间）下客户端靠 react-query 重新拉取。
//
// 载荷不落库，只做转发；权限由 REST 层把关（这里仅限制必须在自己房间内）。
func registerCommentRelay(
	ioo *socketio.Server,
	socket *socketio.Socket,
	sessions *collabSessionRegistry,
	store stores.Store,
	me socketio.SocketId,
) {
	socket.On(commentEventName, func(datas ...any) {
		if len(datas) < 2 {
			return
		}
		roomID, ok := datas[0].(string)
		if !ok {
			return
		}
		joinedRoom, joined := sessions.roomFor(me)
		if !joined || string(joinedRoom) != roomID {
			return
		}
		if !sessions.validate(context.Background(), store, me, false, true) {
			sessions.release(me)
			socket.Disconnect(true)
			return
		}
		sessions.pruneUnauthorized(context.Background(), ioo, store, joinedRoom, true)
		socket.Broadcast().To(socketio.Room(roomID)).Emit(commentEventName, datas[1])
	})
}

func socketAuthToken(socket *socketio.Socket) string {
	handshake := socket.Handshake()
	if handshake == nil || handshake.Auth == nil {
		return ""
	}
	encoded, err := json.Marshal(handshake.Auth)
	if err != nil {
		return ""
	}
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.Token)
}

func parseJoinRoomData(datas []any) (socketio.Room, string, bool) {
	if len(datas) == 0 {
		return "", "", false
	}
	roomID, ok := datas[0].(string)
	if !ok || roomID == "" || len(roomID) > 256 || strings.HasPrefix(roomID, followRoomPrefix) {
		return "", "", false
	}

	clientID := ""
	if len(datas) > 1 {
		if value, ok := datas[1].(string); ok && len(value) <= 128 {
			clientID = value
		}
	}
	return socketio.Room(roomID), clientID, true
}

func parseUserFollowPayload(data any) (socketio.SocketId, string, bool) {
	encoded, err := json.Marshal(data)
	if err != nil {
		return "", "", false
	}

	var payload OnUserFollowedPayload
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return "", "", false
	}
	targetID := socketio.SocketId(payload.UserToFollow.SocketId)
	if targetID == "" || (payload.Action != "FOLLOW" && payload.Action != "UNFOLLOW") {
		return "", "", false
	}
	return targetID, payload.Action, true
}

func followTargetFromRoom(room socketio.Room) (socketio.SocketId, bool) {
	roomID := string(room)
	if !strings.HasPrefix(roomID, followRoomPrefix) {
		return "", false
	}
	targetID := strings.TrimPrefix(roomID, followRoomPrefix)
	if targetID == "" {
		return "", false
	}
	return socketio.SocketId(targetID), true
}

func socketIDsExcluding(
	sockets []*socketio.RemoteSocket,
	excluded ...socketio.SocketId,
) []socketio.SocketId {
	excludedSet := make(map[socketio.SocketId]struct{}, len(excluded))
	for _, socketID := range excluded {
		if socketID != "" {
			excludedSet[socketID] = struct{}{}
		}
	}

	ids := make([]socketio.SocketId, 0, len(sockets))
	for _, current := range sockets {
		if _, skip := excludedSet[current.Id()]; !skip {
			ids = append(ids, current.Id())
		}
	}
	return ids
}

func emitFollowRoomChange(
	ioo *socketio.Server,
	followRoom socketio.Room,
	excluded ...socketio.SocketId,
) {
	targetID, ok := followTargetFromRoom(followRoom)
	if !ok {
		return
	}
	ioo.In(followRoom).FetchSockets()(func(followers []*socketio.RemoteSocket, err error) {
		if err != nil {
			return
		}
		ioo.To(socketio.Room(targetID)).Emit(
			"user-follow-room-change",
			socketIDsExcluding(followers, excluded...),
		)
	})
}
