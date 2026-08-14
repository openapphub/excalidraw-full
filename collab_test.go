package main

import (
	"testing"

	socketio "github.com/zishang520/socket.io/v2/socket"
)

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
