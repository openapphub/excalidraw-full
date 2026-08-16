package sqlite

import (
	"context"
	"errors"
	"excalidraw-complete/core"
	"sync"
	"testing"
	"time"
)

func TestSceneExclusiveLockAndCollab(t *testing.T) {
	store := newShellStore(t)
	ctx := context.Background()
	const alice = "user-alice"
	const bob = "user-bob"
	aliceCtx := core.WithSceneClientID(ctx, "tab-a")
	bobCtx := core.WithSceneClientID(ctx, "tab-b")

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
	if err := store.UpdateSceneData(core.WithSceneClientID(ctx, "tab-a2"), alice, scene.ID, []byte(`{"elements":[{"id":"other-tab"}]}`)); !errors.As(err, &lockErr) {
		t.Fatalf("Alice 第二标签写入错误 = %v，期望 SceneLockError", err)
	}
	if err := store.UpdateSceneData(aliceCtx, alice, scene.ID, []byte(`{"elements":[{"id":"owner"}]}`)); err != nil {
		t.Fatalf("锁持有标签写入错误 = %v", err)
	}

	if err := store.UpdateSceneData(bobCtx, bob, scene.ID, []byte(`{"elements":[{"id":"x"}]}`)); err == nil {
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

	if _, err := store.SetSceneCollab(bobCtx, bob, scene.ID, true); err != nil {
		t.Fatalf("锁持有者开启协作错误 = %v", err)
	}
	if err := store.UpdateSceneData(bobCtx, bob, scene.ID, []byte(`{"elements":[{"id":"y"}]}`)); err != nil {
		t.Fatalf("协作开启后 Bob PUT 错误 = %v", err)
	}

	if _, err := store.SetSceneCollab(bobCtx, bob, scene.ID, false); err != nil {
		t.Fatalf("关闭协作错误 = %v", err)
	}

	if err := store.UpdateSceneData(aliceCtx, alice, scene.ID, []byte(`{"elements":[]}`)); err == nil {
		t.Fatal("同一用户之外的旧 clientId 不应绕过当前锁")
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

func TestConcurrentDisableCollabAllowsSingleWriter(t *testing.T) {
	store := newShellStore(t)
	ctx := context.Background()
	const userID = "user-alice"

	workspace, err := store.CreateShellWorkspace(ctx, userID, "团队", "", core.WorkspaceShared)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := store.CreateCollection(ctx, userID, workspace.ID, "项目", nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	scene, err := store.CreateScene(ctx, userID, "画布", nil, []byte(`{"elements":[]}`), &collection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireSceneLock(ctx, userID, scene.ID, "tab-a", "Alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetSceneCollab(core.WithSceneClientID(ctx, "tab-a"), userID, scene.ID, true); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	var waitGroup sync.WaitGroup
	for _, clientID := range []string{"tab-a", "tab-b"} {
		clientID := clientID
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			_, err := store.SetSceneCollab(core.WithSceneClientID(ctx, clientID), userID, scene.ID, false)
			results <- err
		}()
	}
	close(start)
	waitGroup.Wait()
	close(results)

	successes := 0
	conflicts := 0
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		var lockErr *core.SceneLockError
		if errors.As(err, &lockErr) {
			conflicts++
			continue
		}
		t.Fatalf("关闭协作返回意外错误: %v", err)
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("并发关闭协作结果：成功 %d，冲突 %d；期望各 1", successes, conflicts)
	}

	state, err := store.readCanvasEditState(ctx, store.db, scene.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.collabEnabled || !state.leaseLive() || (state.lockClientID != "tab-a" && state.lockClientID != "tab-b") {
		t.Fatalf("关闭协作后的独占状态无效: %+v", state)
	}
}

func strPtr(s string) *string { return &s }

func intPtr(n int) *int { return &n }
