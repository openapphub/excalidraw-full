package main

import (
	"context"
	"excalidraw-complete/core"
	"excalidraw-complete/handlers/api/roomaccess"
	"excalidraw-complete/stores/sqlite"
	"testing"

	socketio "github.com/zishang520/socket.io/v2/socket"
)

func TestCollabSessionRegistryRevalidatesRoleChanges(t *testing.T) {
	store := sqlite.NewStore(t.TempDir() + "/collab-acl.db")
	ctx := context.Background()
	const ownerID = "owner"
	const memberID = "member"

	workspace, err := store.CreateShellWorkspace(ctx, ownerID, "团队", "", core.WorkspaceShared)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := store.CreateCollection(ctx, ownerID, workspace.ID, "项目", nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	scene, err := store.CreateScene(ctx, ownerID, "画布", nil, []byte(`{"elements":[]}`), &collection.ID)
	if err != nil {
		t.Fatal(err)
	}
	invite, err := store.CreateInviteLink(ctx, ownerID, workspace.ID, core.RoleMember, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.JoinViaInviteLink(ctx, memberID, invite.Code, core.MemberUser{ID: memberID}); err != nil {
		t.Fatal(err)
	}
	members, err := store.ListMembers(ctx, ownerID, workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	var memberRecordID string
	for _, member := range members {
		if member.UserID == memberID {
			memberRecordID = member.ID
			break
		}
	}
	if memberRecordID == "" {
		t.Fatal("未找到测试成员")
	}

	registry := newCollabSessionRegistry()
	registry.claim("member-socket", socketio.Room(scene.ID), "member-tab", roomaccess.Access{
		UserID:         memberID,
		WorkspaceScene: true,
		CanEdit:        true,
	})

	if _, err := store.UpdateMemberRole(ctx, ownerID, workspace.ID, memberRecordID, core.RoleViewer); err != nil {
		t.Fatal(err)
	}
	if registry.validate(ctx, store, "member-socket", true, true) {
		t.Fatal("成员降为 VIEWER 后可靠内容事件必须拒绝写入")
	}
	if !registry.validate(ctx, store, "member-socket", false, true) {
		t.Fatal("VIEWER 仍应保留协作房间读取权限")
	}

	if err := store.RemoveMember(ctx, ownerID, workspace.ID, memberRecordID); err != nil {
		t.Fatal(err)
	}
	if registry.validate(ctx, store, "member-socket", false, true) {
		t.Fatal("成员移除后必须失去协作房间读取权限")
	}
}

func TestCollabSessionRegistryValidatesSceneLockForContentBroadcast(t *testing.T) {
	store := sqlite.NewStore(t.TempDir() + "/collab-lock.db")
	ctx := context.Background()
	const ownerID = "owner"

	workspace, err := store.CreateShellWorkspace(ctx, ownerID, "团队", "", core.WorkspaceShared)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := store.CreateCollection(ctx, ownerID, workspace.ID, "项目", nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	scene, err := store.CreateScene(ctx, ownerID, "画布", nil, []byte(`{"elements":[]}`), &collection.ID)
	if err != nil {
		t.Fatal(err)
	}

	registry := newCollabSessionRegistry()
	registry.claim("owner-socket", socketio.Room(scene.ID), "owner-tab", roomaccess.Access{
		UserID:         ownerID,
		WorkspaceScene: true,
		CanEdit:        true,
	})

	if registry.validateContentWrite(ctx, store, "owner-socket") {
		t.Fatal("独占模式下未持锁的可靠元素广播必须拒绝")
	}
	if _, err := store.AcquireSceneLock(ctx, ownerID, scene.ID, "owner-tab", "Owner"); err != nil {
		t.Fatal(err)
	}
	if !registry.validateContentWrite(ctx, store, "owner-socket") {
		t.Fatal("独占模式下当前持锁 clientId 应允许可靠元素广播")
	}

	if _, err := store.SetSceneCollab(core.WithSceneClientID(ctx, "owner-tab"), ownerID, scene.ID, true); err != nil {
		t.Fatal(err)
	}
	registry.claim("second-socket", socketio.Room(scene.ID), "second-tab", roomaccess.Access{
		UserID:         ownerID,
		WorkspaceScene: true,
		CanEdit:        true,
	})
	if !registry.validateContentWrite(ctx, store, "second-socket") {
		t.Fatal("协作模式下可编辑成员无需独占锁即可广播实时增量")
	}
}

func TestCollabSessionRegistryReplacesSameTabSocket(t *testing.T) {
	registry := newCollabSessionRegistry()
	room := socketio.Room("room-1")

	_, replaced := registry.claim("socket-1", room, "tab-1")
	if replaced != "" {
		t.Fatalf("首次加入不应替换 socket，实际为 %q", replaced)
	}

	_, replaced = registry.claim("socket-2", room, "tab-1")
	if replaced != "socket-1" {
		t.Fatalf("同一标签重连应替换 socket-1，实际为 %q", replaced)
	}

	registry.release("socket-1")
	_, replaced = registry.claim("socket-3", room, "tab-1")
	if replaced != "socket-2" {
		t.Fatalf("旧 socket 清理不应删除新归属，实际替换 %q", replaced)
	}
}

func TestCollabSessionRegistryChecksRoomBoundary(t *testing.T) {
	registry := newCollabSessionRegistry()
	registry.claim("socket-1", "room-1", "tab-1")
	registry.claim("socket-2", "room-1", "tab-2")
	registry.claim("socket-3", "room-2", "tab-3")

	if !registry.sameRoom("socket-1", "socket-2") {
		t.Fatal("同一房间的 socket 应可建立关注关系")
	}
	if registry.sameRoom("socket-1", "socket-3") {
		t.Fatal("不同房间的 socket 不应建立关注关系")
	}
}

func TestParseJoinRoomData(t *testing.T) {
	room, clientID, ok := parseJoinRoomData([]any{"room-1", "tab-1"})
	if !ok || room != "room-1" || clientID != "tab-1" {
		t.Fatalf("有效 join-room 解析失败：room=%q clientID=%q ok=%v", room, clientID, ok)
	}

	if _, _, ok := parseJoinRoomData([]any{"follow@socket-1", "tab-1"}); ok {
		t.Fatal("普通加入事件不得直接加入关注专用房间")
	}
	if _, _, ok := parseJoinRoomData(nil); ok {
		t.Fatal("空 join-room 载荷应被拒绝")
	}
}

func TestNewRoomJoinedPayload(t *testing.T) {
	payload := newRoomJoinedPayload(
		"room-1",
		"socket-1",
		[]socketio.SocketId{"socket-1", "socket-2"},
	)

	if payload.RoomID != "room-1" || payload.SocketID != "socket-1" {
		t.Fatalf("入房确认标识错误: %+v", payload)
	}
	if len(payload.Clients) != 2 || payload.Clients[1] != "socket-2" {
		t.Fatalf("入房确认成员错误: %+v", payload.Clients)
	}
}

func TestParseUserFollowPayload(t *testing.T) {
	targetID, action, ok := parseUserFollowPayload(map[string]any{
		"userToFollow": map[string]any{
			"socketId": "target-1",
			"username": "用户",
		},
		"action": "FOLLOW",
	})
	if !ok || targetID != "target-1" || action != "FOLLOW" {
		t.Fatalf("有效关注载荷解析失败：target=%q action=%q ok=%v", targetID, action, ok)
	}

	if _, _, ok := parseUserFollowPayload(map[string]any{
		"userToFollow": map[string]any{"socketId": "target-1"},
		"action":       "INVALID",
	}); ok {
		t.Fatal("未知关注动作应被拒绝")
	}
	if _, _, ok := parseUserFollowPayload("invalid"); ok {
		t.Fatal("畸形关注载荷应被拒绝")
	}
}

func TestFollowTargetFromRoom(t *testing.T) {
	targetID, ok := followTargetFromRoom("follow@target-1")
	if !ok || targetID != "target-1" {
		t.Fatalf("关注房间解析失败：target=%q ok=%v", targetID, ok)
	}
	if _, ok := followTargetFromRoom("room-1"); ok {
		t.Fatal("普通协作房间不应被识别为关注房间")
	}
}
