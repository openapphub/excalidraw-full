package sqlite

import (
	"context"
	"errors"
	"excalidraw-complete/core"
	"testing"
	"time"
)

func TestSceneExclusiveLockAndCollab(t *testing.T) {
	store := newShellStore(t)
	ctx := context.Background()
	const alice = "user-alice"
	const bob = "user-bob"

	ws, err := store.CreateShellWorkspace(ctx, alice, "团队", "", core.WorkspaceShared)
	if err != nil {
		t.Fatalf("CreateShellWorkspace() 错误 = %v", err)
	}
	coll, err := store.CreateCollection(ctx, alice, ws.ID, "共享", nil, nil, false)
	if err != nil {
		t.Fatalf("CreateCollection() 错误 = %v", err)
	}
	scene, err := store.CreateScene(ctx, alice, "画布", nil, []byte(`{"elements":[]}`), &coll.ID)
	if err != nil {
		t.Fatalf("CreateScene() 错误 = %v", err)
	}

	maxUses := 2
	link, err := store.CreateInviteLink(ctx, alice, ws.ID, core.RoleMember, nil, &maxUses)
	if err != nil {
		t.Fatalf("CreateInviteLink() 错误 = %v", err)
	}
	if _, err := store.JoinViaInviteLink(ctx, bob, link.Code, core.MemberUser{ID: bob, Name: strPtr("Bob")}); err != nil {
		t.Fatalf("JoinViaInviteLink() 错误 = %v", err)
	}

	got, err := store.AcquireSceneLock(ctx, alice, scene.ID, "tab-a", "Alice")
	if err != nil {
		t.Fatalf("Alice 占锁错误 = %v", err)
	}
	if got.CollabEnabled {
		t.Fatal("默认不应开启协作")
	}
	if got.Editor == nil || got.Editor.UserID != alice || !got.Editor.IsSelf {
		t.Fatalf("Alice 的 editor = %+v", got.Editor)
	}

	_, err = store.AcquireSceneLock(ctx, bob, scene.ID, "tab-b", "Bob")
	var lockErr *core.SceneLockError
	if !errors.As(err, &lockErr) {
		t.Fatalf("Bob 占锁错误 = %v，期望 SceneLockError", err)
	}
	if lockErr.Editor == nil || lockErr.Editor.Name != "Alice" {
		t.Fatalf("冲突 editor = %+v，期望 Alice", lockErr.Editor)
	}

	_, err = store.AcquireSceneLock(ctx, alice, scene.ID, "tab-a2", "Alice")
	if !errors.As(err, &lockErr) {
		t.Fatalf("Alice 第二标签占锁错误 = %v，期望 SceneLockError", err)
	}

	if err := store.UpdateSceneData(ctx, bob, scene.ID, []byte(`{"elements":[{"id":"x"}]}`)); err == nil {
		t.Fatal("Bob 在独占锁下不应能 PUT")
	} else if !errors.As(err, &lockErr) {
		t.Fatalf("Bob PUT 错误 = %v，期望 SceneLockError", err)
	}

	if _, err := store.db.ExecContext(ctx, `UPDATE canvases SET edit_lock_until = ? WHERE id = ?`,
		time.Now().UTC().Add(-time.Minute), scene.ID); err != nil {
		t.Fatalf("过期锁 UPDATE 错误 = %v", err)
	}
	if _, err := store.AcquireSceneLock(ctx, bob, scene.ID, "tab-b", "Bob"); err != nil {
		t.Fatalf("过期后 Bob 占锁错误 = %v", err)
	}

	if _, err := store.SetSceneCollab(ctx, alice, scene.ID, true); err != nil {
		t.Fatalf("开启协作错误 = %v", err)
	}
	if err := store.UpdateSceneData(ctx, bob, scene.ID, []byte(`{"elements":[{"id":"y"}]}`)); err != nil {
		t.Fatalf("协作开启后 Bob PUT 错误 = %v", err)
	}

	if _, err := store.SetSceneCollab(ctx, alice, scene.ID, false); err != nil {
		t.Fatalf("关闭协作错误 = %v", err)
	}

	viewerID := "user-carol"
	vLink, err := store.CreateInviteLink(ctx, alice, ws.ID, core.RoleViewer, nil, intPtr(1))
	if err != nil {
		t.Fatalf("VIEWER 邀请错误 = %v", err)
	}
	if _, err := store.JoinViaInviteLink(ctx, viewerID, vLink.Code, core.MemberUser{ID: viewerID}); err != nil {
		t.Fatalf("VIEWER 加入错误 = %v", err)
	}
	if _, err := store.AcquireSceneLock(ctx, viewerID, scene.ID, "tab-c", "Carol"); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("VIEWER 占锁错误 = %v，期望 ErrForbidden", err)
	}
}

func strPtr(s string) *string { return &s }

func intPtr(n int) *int { return &n }
