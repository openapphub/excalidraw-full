package main

import (
	"encoding/json"
	"strings"
	"sync"

	socketio "github.com/zishang520/socket.io/v2/socket"
)

const followRoomPrefix = "follow@"

type collabSession struct {
	room     socketio.Room
	clientID string
}

type collabSessionRegistry struct {
	mu              sync.RWMutex
	bySocket        map[socketio.SocketId]collabSession
	socketBySession map[string]socketio.SocketId
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

	r.bySocket[socketID] = collabSession{room: room, clientID: clientID}
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

func (r *collabSessionRegistry) sameRoom(first, second socketio.SocketId) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	firstSession, firstOK := r.bySocket[first]
	secondSession, secondOK := r.bySocket[second]
	return firstOK && secondOK && firstSession.room == secondSession.room
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
