package sqlite

import (
	"context"
	"errors"
	"excalidraw-complete/core"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// newShellStore 用临时文件建库（Workspace Shell 迁移涉及多连接，落盘更接近真实运行）。
func newShellStore(t *testing.T) *sqliteStore {
	t.Helper()
	return newShellStoreAt(t, filepath.Join(t.TempDir(), "shell-test.db"))
}

func newShellStoreAt(t *testing.T, dsn string) *sqliteStore {
	t.Helper()
	store := NewStore(dsn)
	t.Cleanup(func() {
		store.db.Close()
	})
	return store
}

// TestEnsurePersonalWorkspace 校验个人工作区幂等创建 + 自带默认可分享集合。
func TestEnsurePersonalWorkspace(t *testing.T) {
	store := newShellStore(t)
	ctx := context.Background()
	const userID = "user-alice"

	ws, err := store.EnsurePersonalWorkspace(ctx, userID, "Alice")
	if err != nil {
		t.Fatalf("EnsurePersonalWorkspace() 错误 = %v", err)
	}
	if ws.Type != core.WorkspacePersonal {
		t.Fatalf("工作区类型 = %q，期望 %q", ws.Type, core.WorkspacePersonal)
	}
	if ws.Role != core.RoleAdmin {
		t.Fatalf("创建者角色 = %q，期望 %q", ws.Role, core.RoleAdmin)
	}
	if ws.Name != "Alice's Workspace" {
		t.Fatalf("工作区名 = %q，期望 %q", ws.Name, "Alice's Workspace")
	}
	if ws.MemberCount != 1 {
		t.Fatalf("成员数 = %d，期望 1", ws.MemberCount)
	}

	// 幂等：再次调用应复用同一工作区
	again, err := store.EnsurePersonalWorkspace(ctx, userID, "Alice")
	if err != nil {
		t.Fatalf("重复 EnsurePersonalWorkspace() 错误 = %v", err)
	}
	if again.ID != ws.ID {
		t.Fatalf("重复调用生成了新工作区 %q，期望复用 %q", again.ID, ws.ID)
	}

	// 自带一个默认可分享集合
	collections, err := store.ListCollections(ctx, userID, ws.ID)
	if err != nil {
		t.Fatalf("ListCollections() 错误 = %v", err)
	}
	if len(collections) != 1 {
		t.Fatalf("集合数 = %d，期望 1", len(collections))
	}
	if collections[0].IsPrivate || collections[0].Name != "默认" {
		t.Fatalf("默认集合 = %+v，期望可分享的「默认」", collections[0])
	}
	if !collections[0].IsOwner || !collections[0].CanWrite {
		t.Fatalf("所有者对默认集合应可写且为 owner，实得 %+v", collections[0])
	}

	// 未登录用户不应看到别人的工作区
	if list, err := store.ListShellWorkspaces(ctx, "user-bob"); err != nil {
		t.Fatalf("ListShellWorkspaces() 错误 = %v", err)
	} else if len(list) != 0 {
		t.Fatalf("非成员可见工作区数 = %d，期望 0", len(list))
	}
}

// TestCollectionAndSceneLifecycle 覆盖建集合 → 建场景 → 读数据 → 移动 → 复制 → 删除。
func TestCollectionAndSceneLifecycle(t *testing.T) {
	store := newShellStore(t)
	ctx := context.Background()
	const userID = "user-alice"

	ws, err := store.EnsurePersonalWorkspace(ctx, userID, "Alice")
	if err != nil {
		t.Fatalf("EnsurePersonalWorkspace() 错误 = %v", err)
	}

	icon := "📁"
	coll, err := store.CreateCollection(ctx, userID, ws.ID, "设计稿", &icon, nil, false)
	if err != nil {
		t.Fatalf("CreateCollection() 错误 = %v", err)
	}
	if coll.Name != "设计稿" || coll.IsPrivate {
		t.Fatalf("集合 = %+v，期望公开的“设计稿”", coll)
	}
	if coll.Icon == nil || *coll.Icon != icon {
		t.Fatalf("集合图标 = %v，期望 %q", coll.Icon, icon)
	}

	payload := []byte(`{"type":"excalidraw","elements":[]}`)
	scene, err := store.CreateScene(ctx, userID, "首页线框", nil, payload, &coll.ID)
	if err != nil {
		t.Fatalf("CreateScene() 错误 = %v", err)
	}
	if scene.Title != "首页线框" {
		t.Fatalf("场景标题 = %q，期望 %q", scene.Title, "首页线框")
	}
	if scene.StorageKey != scene.ID {
		t.Fatalf("StorageKey = %q，期望等于场景 id %q", scene.StorageKey, scene.ID)
	}
	if scene.CollectionID == nil || *scene.CollectionID != coll.ID {
		t.Fatalf("场景集合 = %v，期望 %q", scene.CollectionID, coll.ID)
	}
	if !scene.CanEdit {
		t.Fatal("创建者应可编辑自己的场景")
	}

	data, err := store.GetSceneData(ctx, userID, scene.ID)
	if err != nil {
		t.Fatalf("GetSceneData() 错误 = %v", err)
	}
	if string(data) != string(payload) {
		t.Fatalf("场景数据 = %q，期望 %q", data, payload)
	}

	// 集合内场景计数
	reloaded, err := store.GetCollection(ctx, userID, coll.ID)
	if err != nil {
		t.Fatalf("GetCollection() 错误 = %v", err)
	}
	if reloaded.SceneCount != 1 {
		t.Fatalf("集合场景数 = %d，期望 1", reloaded.SceneCount)
	}

	// 按集合与按工作区列举都应命中
	if scenes, err := store.ListScenes(ctx, userID, nil, &coll.ID); err != nil {
		t.Fatalf("ListScenes(collection) 错误 = %v", err)
	} else if len(scenes) != 1 {
		t.Fatalf("集合内场景数 = %d，期望 1", len(scenes))
	}
	if scenes, err := store.ListScenes(ctx, userID, &ws.ID, nil); err != nil {
		t.Fatalf("ListScenes(workspace) 错误 = %v", err)
	} else if len(scenes) != 1 {
		t.Fatalf("工作区内场景数 = %d，期望 1", len(scenes))
	}

	// 复制场景
	dup, err := store.DuplicateScene(ctx, userID, scene.ID)
	if err != nil {
		t.Fatalf("DuplicateScene() 错误 = %v", err)
	}
	if dup.ID == scene.ID {
		t.Fatal("复制后的场景应有新 id")
	}
	if dupData, err := store.GetSceneData(ctx, userID, dup.ID); err != nil {
		t.Fatalf("读取复制场景数据错误 = %v", err)
	} else if string(dupData) != string(payload) {
		t.Fatalf("复制场景数据 = %q，期望 %q", dupData, payload)
	}

	// 移动到私有集合
	privateColl, err := store.CreateCollection(ctx, userID, ws.ID, "草稿", nil, nil, true)
	if err != nil {
		t.Fatalf("CreateCollection(private) 错误 = %v", err)
	}
	moved, err := store.MoveScene(ctx, userID, scene.ID, &privateColl.ID)
	if err != nil {
		t.Fatalf("MoveScene() 错误 = %v", err)
	}
	if moved.CollectionID == nil || *moved.CollectionID != privateColl.ID {
		t.Fatalf("移动后集合 = %v，期望 %q", moved.CollectionID, privateColl.ID)
	}

	// 删集合：所属场景必须一并删除，旧 URL 也不能继续访问。
	if err := store.DeleteCollection(ctx, userID, privateColl.ID); err != nil {
		t.Fatalf("DeleteCollection() 错误 = %v", err)
	}
	if _, err := store.GetScene(ctx, userID, scene.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("删集合后 GetScene() 错误 = %v，期望 ErrNotFound", err)
	}

	if _, err := store.GetScene(ctx, userID, dup.ID); err != nil {
		t.Fatalf("其他集合中的复制场景不应被删除: %v", err)
	}
}

func TestCopyAndMoveCollectionKeepSceneWorkspaceInSync(t *testing.T) {
	store := newShellStore(t)
	ctx := context.Background()
	const userID = "user-alice"

	sourceWorkspace, err := store.EnsurePersonalWorkspace(ctx, userID, "Alice")
	if err != nil {
		t.Fatalf("EnsurePersonalWorkspace() 错误 = %v", err)
	}
	targetWorkspace, err := store.CreateShellWorkspace(ctx, userID, "目标空间", "", core.WorkspaceShared)
	if err != nil {
		t.Fatalf("CreateShellWorkspace() 错误 = %v", err)
	}
	sourceCollection, err := store.CreateCollection(ctx, userID, sourceWorkspace.ID, "待迁移", nil, nil, false)
	if err != nil {
		t.Fatalf("CreateCollection() 错误 = %v", err)
	}
	sourceScene, err := store.CreateScene(ctx, userID, "源画布", nil, []byte(`{"elements":[1]}`), &sourceCollection.ID)
	if err != nil {
		t.Fatalf("CreateScene() 错误 = %v", err)
	}

	copied, err := store.CopyCollectionToWorkspace(ctx, userID, sourceCollection.ID, targetWorkspace.ID)
	if err != nil {
		t.Fatalf("CopyCollectionToWorkspace() 错误 = %v", err)
	}
	if copied.WorkspaceID != targetWorkspace.ID {
		t.Fatalf("复制集合工作区 = %q，期望 %q", copied.WorkspaceID, targetWorkspace.ID)
	}
	var copiedSceneWorkspaceID string
	if err := store.db.QueryRowContext(ctx, `SELECT workspace_id FROM canvases WHERE collection_id = ?`, copied.ID).Scan(&copiedSceneWorkspaceID); err != nil {
		t.Fatalf("读取复制场景工作区错误 = %v", err)
	}
	if copiedSceneWorkspaceID != targetWorkspace.ID {
		t.Fatalf("复制场景工作区 = %q，期望 %q", copiedSceneWorkspaceID, targetWorkspace.ID)
	}

	moved, err := store.MoveCollectionToWorkspace(ctx, userID, sourceCollection.ID, targetWorkspace.ID)
	if err != nil {
		t.Fatalf("MoveCollectionToWorkspace() 错误 = %v", err)
	}
	if moved.WorkspaceID != targetWorkspace.ID {
		t.Fatalf("移动集合工作区 = %q，期望 %q", moved.WorkspaceID, targetWorkspace.ID)
	}
	var movedSceneWorkspaceID string
	if err := store.db.QueryRowContext(ctx, `SELECT workspace_id FROM canvases WHERE id = ?`, sourceScene.ID).Scan(&movedSceneWorkspaceID); err != nil {
		t.Fatalf("读取移动场景工作区错误 = %v", err)
	}
	if movedSceneWorkspaceID != targetWorkspace.ID {
		t.Fatalf("移动场景工作区 = %q，期望 %q", movedSceneWorkspaceID, targetWorkspace.ID)
	}
}

func TestCrossWorkspaceMovesPreserveCurrentWriterLock(t *testing.T) {
	store := newShellStore(t)
	ctx := context.Background()
	const userID = "user-alice"
	const clientID = "tab-a"
	clientCtx := core.WithSceneClientID(ctx, clientID)

	sourceWorkspace, err := store.CreateShellWorkspace(ctx, userID, "源空间", "", core.WorkspaceShared)
	if err != nil {
		t.Fatal(err)
	}
	targetWorkspace, err := store.CreateShellWorkspace(ctx, userID, "目标空间", "", core.WorkspaceShared)
	if err != nil {
		t.Fatal(err)
	}
	targetCollection, err := store.CreateCollection(ctx, userID, targetWorkspace.ID, "目标集合", nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}

	collectionToMove, err := store.CreateCollection(ctx, userID, sourceWorkspace.ID, "整体移动", nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	collectionScene, err := store.CreateScene(ctx, userID, "集合画布", nil, []byte(`{"elements":[]}`), &collectionToMove.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireSceneLock(ctx, userID, collectionScene.ID, clientID, "Alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MoveCollectionToWorkspace(clientCtx, userID, collectionToMove.ID, targetWorkspace.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateSceneData(clientCtx, userID, collectionScene.ID, []byte(`{"elements":[1]}`)); err != nil {
		t.Fatalf("Collection 移动后当前持锁者应可继续写入: %v", err)
	}
	if _, err := store.AcquireSceneLock(ctx, userID, collectionScene.ID, "tab-b", "Bob"); err == nil {
		t.Fatal("Collection 移动后其他客户端不应绕过原持锁者")
	}

	sceneSourceCollection, err := store.CreateCollection(ctx, userID, sourceWorkspace.ID, "单场景移动", nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	sceneToMove, err := store.CreateScene(ctx, userID, "单画布", nil, []byte(`{"elements":[]}`), &sceneSourceCollection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireSceneLock(ctx, userID, sceneToMove.ID, clientID, "Alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MoveScene(clientCtx, userID, sceneToMove.ID, &targetCollection.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateSceneData(clientCtx, userID, sceneToMove.ID, []byte(`{"elements":[2]}`)); err != nil {
		t.Fatalf("Scene 移动后当前持锁者应可继续写入: %v", err)
	}
}

func TestSceneAccessAndDuplicateFollowWorkspaceMembership(t *testing.T) {
	store := newShellStore(t)
	ctx := context.Background()
	const ownerID = "user-owner"
	const memberID = "user-member"
	const viewerID = "user-viewer"

	ws, err := store.CreateShellWorkspace(ctx, ownerID, "团队", "", core.WorkspaceShared)
	if err != nil {
		t.Fatal(err)
	}
	coll, err := store.CreateCollection(ctx, ownerID, ws.ID, "项目", nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}

	memberLink, err := store.CreateInviteLink(ctx, ownerID, ws.ID, core.RoleMember, nil, intPtr(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.JoinViaInviteLink(ctx, memberID, memberLink.Code, core.MemberUser{ID: memberID}); err != nil {
		t.Fatal(err)
	}
	memberScene, err := store.CreateScene(ctx, memberID, "成员画布", nil, []byte(`{"elements":[]}`), &coll.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveMember(ctx, ownerID, ws.ID, memberID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetScene(ctx, memberID, memberScene.ID); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("被移出工作区的 Scene 创建者仍可访问，错误 = %v", err)
	}

	viewerLink, err := store.CreateInviteLink(ctx, ownerID, ws.ID, core.RoleViewer, nil, intPtr(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.JoinViaInviteLink(ctx, viewerID, viewerLink.Code, core.MemberUser{ID: viewerID}); err != nil {
		t.Fatal(err)
	}
	ownerScene, err := store.CreateScene(ctx, ownerID, "只读画布", nil, []byte(`{"elements":[]}`), &coll.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DuplicateScene(ctx, viewerID, ownerScene.ID); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("VIEWER 复制 Scene 错误 = %v，期望 ErrForbidden", err)
	}
}

func TestViewerCannotDeleteOwnedSceneOrCopyCollection(t *testing.T) {
	store := newShellStore(t)
	ctx := context.Background()
	const ownerID = "workspace-owner"
	const memberID = "scene-owner"

	workspace, err := store.CreateShellWorkspace(ctx, ownerID, "团队", "", core.WorkspaceShared)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := store.CreateCollection(ctx, ownerID, workspace.ID, "项目", nil, nil, false)
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
	scene, err := store.CreateScene(ctx, memberID, "成员创建", nil, []byte(`{"elements":[]}`), &collection.ID)
	if err != nil {
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
		t.Fatal("未找到成员记录")
	}
	if _, err := store.UpdateMemberRole(ctx, ownerID, workspace.ID, memberRecordID, core.RoleViewer); err != nil {
		t.Fatal(err)
	}

	if err := store.DeleteScene(ctx, memberID, scene.ID); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("VIEWER 删除自己创建的 Scene 错误 = %v，期望 ErrForbidden", err)
	}
	personal, err := store.EnsurePersonalWorkspace(ctx, memberID, "成员")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CopyCollectionToWorkspace(ctx, memberID, collection.ID, personal.ID); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("VIEWER 复制只读 Collection 错误 = %v，期望 ErrForbidden", err)
	}
}

func TestMemberCannotMoveSceneAcrossWorkspaceBoundary(t *testing.T) {
	store := newShellStore(t)
	ctx := context.Background()
	const ownerID = "workspace-owner"
	const memberID = "workspace-member"

	sourceWorkspace, err := store.CreateShellWorkspace(ctx, ownerID, "团队", "", core.WorkspaceShared)
	if err != nil {
		t.Fatal(err)
	}
	sourceCollection, err := store.CreateCollection(ctx, ownerID, sourceWorkspace.ID, "共享", nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	scene, err := store.CreateScene(ctx, ownerID, "共享画布", nil, []byte(`{"elements":[]}`), &sourceCollection.ID)
	if err != nil {
		t.Fatal(err)
	}
	invite, err := store.CreateInviteLink(ctx, ownerID, sourceWorkspace.ID, core.RoleMember, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.JoinViaInviteLink(ctx, memberID, invite.Code, core.MemberUser{ID: memberID}); err != nil {
		t.Fatal(err)
	}
	personal, err := store.EnsurePersonalWorkspace(ctx, memberID, "成员")
	if err != nil {
		t.Fatal(err)
	}
	collections, err := store.ListCollections(ctx, memberID, personal.ID)
	if err != nil || len(collections) == 0 {
		t.Fatalf("个人默认集合错误 = %v，数量 = %d", err, len(collections))
	}

	if _, err := store.MoveScene(ctx, memberID, scene.ID, &collections[0].ID); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("MEMBER 跨 Workspace 移动 Scene 错误 = %v，期望 ErrForbidden", err)
	}
	unchanged, err := store.GetScene(ctx, ownerID, scene.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.WorkspaceID != sourceWorkspace.ID || unchanged.CollectionID == nil || *unchanged.CollectionID != sourceCollection.ID {
		t.Fatalf("失败移动后 Scene 归属被修改: %+v", unchanged)
	}
}

func TestMoveSceneKeepsCollectionWorkspaceInSync(t *testing.T) {
	store := newShellStore(t)
	ctx := context.Background()
	const userID = "user-alice"

	sourceWorkspace, err := store.EnsurePersonalWorkspace(ctx, userID, "Alice")
	if err != nil {
		t.Fatal(err)
	}
	sourceCollections, err := store.ListCollections(ctx, userID, sourceWorkspace.ID)
	if err != nil || len(sourceCollections) == 0 {
		t.Fatalf("默认集合错误 = %v, 数量 = %d", err, len(sourceCollections))
	}
	scene, err := store.CreateScene(ctx, userID, "待移动", nil, []byte(`{"elements":[]}`), &sourceCollections[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	targetWorkspace, err := store.CreateShellWorkspace(ctx, userID, "目标", "", core.WorkspaceShared)
	if err != nil {
		t.Fatal(err)
	}
	targetCollection, err := store.CreateCollection(ctx, userID, targetWorkspace.ID, "目标集合", nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}

	moved, err := store.MoveScene(ctx, userID, scene.ID, &targetCollection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if moved.WorkspaceID != targetWorkspace.ID || moved.CollectionID == nil || *moved.CollectionID != targetCollection.ID {
		t.Fatalf("移动后 Scene = %+v", moved)
	}
	duplicate, err := store.DuplicateScene(ctx, userID, scene.ID)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.WorkspaceID != targetWorkspace.ID || duplicate.CollectionID == nil || *duplicate.CollectionID != targetCollection.ID {
		t.Fatalf("复制后 Scene 归属不一致 = %+v", duplicate)
	}

	movedBack, err := store.MoveScene(ctx, userID, scene.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if movedBack.CollectionID == nil || movedBack.WorkspaceID != sourceWorkspace.ID {
		t.Fatalf("移回默认集合后 Scene = %+v", movedBack)
	}
}

func TestMoveSceneFailureDoesNotCreatePersonalWorkspaceOutsideTransaction(t *testing.T) {
	store := newShellStore(t)
	ctx := context.Background()
	const userID = "user-alice"

	workspace, err := store.CreateShellWorkspace(ctx, userID, "共享", "", core.WorkspaceShared)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := store.CreateCollection(ctx, userID, workspace.ID, "集合", nil, nil, false)
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

	var lockErr *core.SceneLockError
	if _, err := store.MoveScene(core.WithSceneClientID(ctx, "tab-b"), userID, scene.ID, nil); !errors.As(err, &lockErr) {
		t.Fatalf("其他客户端持锁时 MoveScene() 错误 = %v，期望 SceneLockError", err)
	}

	workspaces, err := store.ListShellWorkspaces(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(workspaces) != 1 || workspaces[0].ID != workspace.ID {
		t.Fatalf("失败移动后工作区 = %+v，期望只保留源 Workspace", workspaces)
	}
}

func TestListScenesRejectsMismatchedWorkspaceAndCollection(t *testing.T) {
	store := newShellStore(t)
	ctx := context.Background()
	const userID = "user-alice"

	firstWorkspace, err := store.CreateShellWorkspace(ctx, userID, "一组", "", core.WorkspaceShared)
	if err != nil {
		t.Fatal(err)
	}
	firstCollection, err := store.CreateCollection(ctx, userID, firstWorkspace.ID, "一组集合", nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateScene(ctx, userID, "不应泄漏", nil, []byte(`{"elements":[]}`), &firstCollection.ID); err != nil {
		t.Fatal(err)
	}
	secondWorkspace, err := store.CreateShellWorkspace(ctx, userID, "二组", "", core.WorkspaceShared)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.ListScenes(ctx, userID, &secondWorkspace.ID, &firstCollection.ID); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("不匹配的 workspaceId + collectionId 错误 = %v，期望 ErrForbidden", err)
	}
}

func TestDeleteWorkspaceMakesScenesUnreachable(t *testing.T) {
	store := newShellStore(t)
	ctx := context.Background()
	const userID = "user-alice"

	ws, err := store.CreateShellWorkspace(ctx, userID, "临时空间", "", core.WorkspaceShared)
	if err != nil {
		t.Fatal(err)
	}
	coll, err := store.CreateCollection(ctx, userID, ws.ID, "待删除", nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	scene, err := store.CreateScene(ctx, userID, "旧链接", nil, []byte(`{"elements":[]}`), &coll.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteShellWorkspace(ctx, userID, ws.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetScene(ctx, userID, scene.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("删除 Workspace 后旧 Scene URL 仍可达，错误 = %v", err)
	}
}

func TestLockedDeletesRollbackAllDependents(t *testing.T) {
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
	now := time.Now().UTC()
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO comment_threads (id, scene_id, created_by, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`, []any{"thread-1", scene.ID, userID, now, now}},
		{`INSERT INTO comments (id, thread_id, content, created_by, created_at) VALUES (?, ?, ?, ?, ?)`, []any{"comment-1", "thread-1", "内容", userID, now}},
		{`INSERT INTO notifications (id, user_id, type, scene_id, thread_id, comment_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, []any{"notification-1", userID, "MENTION", scene.ID, "thread-1", "comment-1", now}},
	} {
		if _, err := store.db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.AcquireSceneLock(ctx, userID, scene.ID, "other-tab", "Alice"); err != nil {
		t.Fatal(err)
	}

	var lockErr *core.SceneLockError
	if err := store.DeleteScene(core.WithSceneClientID(ctx, "current-tab"), userID, scene.ID); !errors.As(err, &lockErr) {
		t.Fatalf("其他客户端持锁时删除 Scene 错误 = %v，期望 SceneLockError", err)
	}
	for table, id := range map[string]string{
		"canvases":        scene.ID,
		"comment_threads": "thread-1",
		"comments":        "comment-1",
		"notifications":   "notification-1",
	} {
		var count int
		if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE id = ?`, id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("锁冲突回滚后 %s/%s 数量 = %d，期望 1", table, id, count)
		}
	}

	if err := store.DeleteCollection(core.WithSceneClientID(ctx, "current-tab"), userID, collection.ID); !errors.As(err, &lockErr) {
		t.Fatalf("其他客户端持锁时删除 Collection 错误 = %v，期望 SceneLockError", err)
	}
	if _, err := store.GetCollection(ctx, userID, collection.ID); err != nil {
		t.Fatalf("Collection 删除失败后不应丢失: %v", err)
	}
	if err := store.DeleteShellWorkspace(core.WithSceneClientID(ctx, "current-tab"), userID, workspace.ID); !errors.As(err, &lockErr) {
		t.Fatalf("其他客户端持锁时删除 Workspace 错误 = %v，期望 SceneLockError", err)
	}
	if _, err := store.GetShellWorkspace(ctx, userID, workspace.ID); err != nil {
		t.Fatalf("Workspace 删除失败后不应丢失: %v", err)
	}
}

func TestCollabSceneCannotBeDeletedOrMovedAcrossWorkspace(t *testing.T) {
	store := newShellStore(t)
	ctx := context.Background()
	const userID = "user-alice"
	clientCtx := core.WithSceneClientID(ctx, "tab-a")

	sourceWorkspace, err := store.CreateShellWorkspace(ctx, userID, "源空间", "", core.WorkspaceShared)
	if err != nil {
		t.Fatal(err)
	}
	sourceCollection, err := store.CreateCollection(ctx, userID, sourceWorkspace.ID, "源集合", nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	scene, err := store.CreateScene(ctx, userID, "协作画布", nil, []byte(`{"elements":[]}`), &sourceCollection.ID)
	if err != nil {
		t.Fatal(err)
	}
	targetWorkspace, err := store.CreateShellWorkspace(ctx, userID, "目标空间", "", core.WorkspaceShared)
	if err != nil {
		t.Fatal(err)
	}
	targetCollection, err := store.CreateCollection(ctx, userID, targetWorkspace.ID, "目标集合", nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireSceneLock(ctx, userID, scene.ID, "tab-a", "Alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetSceneCollab(clientCtx, userID, scene.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateSceneData(clientCtx, userID, scene.ID, []byte(`{"elements":[1]}`)); err != nil {
		t.Fatal(err)
	}

	var lockErr *core.SceneLockError
	if err := store.DeleteScene(clientCtx, userID, scene.ID); !errors.As(err, &lockErr) {
		t.Fatalf("协作开启时删除 Scene 错误 = %v，期望 SceneLockError", err)
	}
	if _, err := store.MoveScene(clientCtx, userID, scene.ID, &targetCollection.ID); !errors.As(err, &lockErr) {
		t.Fatalf("协作开启时跨 Workspace 移动 Scene 错误 = %v，期望 SceneLockError", err)
	}
	if _, err := store.MoveCollectionToWorkspace(clientCtx, userID, sourceCollection.ID, targetWorkspace.ID); !errors.As(err, &lockErr) {
		t.Fatalf("协作开启时跨 Workspace 移动 Collection 错误 = %v，期望 SceneLockError", err)
	}

	unchangedScene, err := store.GetScene(ctx, userID, scene.ID)
	if err != nil {
		t.Fatal(err)
	}
	unchangedCollection, err := store.GetCollection(ctx, userID, sourceCollection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchangedScene.WorkspaceID != sourceWorkspace.ID || unchangedScene.CollectionID == nil || *unchangedScene.CollectionID != sourceCollection.ID {
		t.Fatalf("失败操作后 Scene 归属被修改: %+v", unchangedScene)
	}
	if unchangedCollection.WorkspaceID != sourceWorkspace.ID {
		t.Fatalf("失败操作后 Collection 归属被修改: %+v", unchangedCollection)
	}
}

func TestCopyAndMoveCollectionRollbackOnSceneFailure(t *testing.T) {
	store := newShellStore(t)
	ctx := context.Background()
	const userID = "user-alice"

	sourceWorkspace, err := store.CreateShellWorkspace(ctx, userID, "源空间", "", core.WorkspaceShared)
	if err != nil {
		t.Fatal(err)
	}
	sourceCollection, err := store.CreateCollection(ctx, userID, sourceWorkspace.ID, "源集合", nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	sourceScene, err := store.CreateScene(ctx, userID, "源画布", nil, []byte(`{"elements":[]}`), &sourceCollection.ID)
	if err != nil {
		t.Fatal(err)
	}
	targetWorkspace, err := store.CreateShellWorkspace(ctx, userID, "目标空间", "", core.WorkspaceShared)
	if err != nil {
		t.Fatal(err)
	}

	var collectionsBefore int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM shell_collections WHERE workspace_id = ?`, targetWorkspace.ID).Scan(&collectionsBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `CREATE TEMP TRIGGER fail_copy_scene BEFORE INSERT ON canvases
		BEGIN SELECT RAISE(ABORT, 'forced copy failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CopyCollectionToWorkspace(ctx, userID, sourceCollection.ID, targetWorkspace.ID); err == nil {
		t.Fatal("复制 Scene 失败时 CopyCollectionToWorkspace 应整体失败")
	}
	if _, err := store.db.ExecContext(ctx, `DROP TRIGGER fail_copy_scene`); err != nil {
		t.Fatal(err)
	}
	var collectionsAfter int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM shell_collections WHERE workspace_id = ?`, targetWorkspace.ID).Scan(&collectionsAfter); err != nil {
		t.Fatal(err)
	}
	if collectionsAfter != collectionsBefore {
		t.Fatalf("复制失败后目标 Collection 数量 = %d，期望 %d", collectionsAfter, collectionsBefore)
	}

	if _, err := store.db.ExecContext(ctx, `CREATE TEMP TRIGGER fail_move_scene BEFORE UPDATE OF workspace_id ON canvases
		WHEN OLD.collection_id = '`+sourceCollection.ID+`'
		BEGIN SELECT RAISE(ABORT, 'forced move failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MoveCollectionToWorkspace(ctx, userID, sourceCollection.ID, targetWorkspace.ID); err == nil {
		t.Fatal("Scene 冗余字段更新失败时 MoveCollectionToWorkspace 应整体失败")
	}
	if _, err := store.db.ExecContext(ctx, `DROP TRIGGER fail_move_scene`); err != nil {
		t.Fatal(err)
	}

	unchangedCollection, err := store.GetCollection(ctx, userID, sourceCollection.ID)
	if err != nil {
		t.Fatal(err)
	}
	unchangedScene, err := store.GetScene(ctx, userID, sourceScene.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchangedCollection.WorkspaceID != sourceWorkspace.ID || unchangedScene.WorkspaceID != sourceWorkspace.ID {
		t.Fatalf("移动失败后归属未整体回滚：Collection=%+v Scene=%+v", unchangedCollection, unchangedScene)
	}
}

// TestInviteLinkJoin 校验共享工作区邀请链接：加入、次数消耗、成员可见性与只读角色。
func TestInviteLinkJoin(t *testing.T) {
	store := newShellStore(t)
	ctx := context.Background()
	const ownerID = "user-alice"
	const guestID = "user-bob"

	ws, err := store.CreateShellWorkspace(ctx, ownerID, "团队空间", "", core.WorkspaceShared)
	if err != nil {
		t.Fatalf("CreateShellWorkspace() 错误 = %v", err)
	}
	if ws.Type != core.WorkspaceShared || ws.Slug == "" {
		t.Fatalf("共享工作区 = %+v，期望 SHARED 且 slug 非空", ws)
	}

	maxUses := 1
	expires := time.Now().Add(time.Hour)
	link, err := store.CreateInviteLink(ctx, ownerID, ws.ID, core.RoleViewer, &expires, &maxUses)
	if err != nil {
		t.Fatalf("CreateInviteLink() 错误 = %v", err)
	}
	if len(link.Code) < 8 {
		t.Fatalf("邀请码 = %q，期望至少 8 位", link.Code)
	}

	joined, err := store.JoinViaInviteLink(ctx, guestID, link.Code, core.MemberUser{
		ID:    guestID,
		Email: "bob@example.com",
	})
	if err != nil {
		t.Fatalf("JoinViaInviteLink() 错误 = %v", err)
	}
	if joined.ID != ws.ID {
		t.Fatalf("加入的工作区 = %q，期望 %q", joined.ID, ws.ID)
	}
	if joined.Role != core.RoleViewer {
		t.Fatalf("加入角色 = %q，期望 %q", joined.Role, core.RoleViewer)
	}
	if joined.MemberCount != 2 {
		t.Fatalf("成员数 = %d，期望 2", joined.MemberCount)
	}

	// VIEWER 只读：不能建集合
	if _, err := store.CreateCollection(ctx, guestID, ws.ID, "禁止", nil, nil, false); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("VIEWER 建集合错误 = %v，期望 ErrForbidden", err)
	}

	// 成员列表带上缓存的邮箱
	members, err := store.ListMembers(ctx, ownerID, ws.ID)
	if err != nil {
		t.Fatalf("ListMembers() 错误 = %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("成员数 = %d，期望 2", len(members))
	}
	var guestEmail string
	for _, m := range members {
		if m.UserID == guestID {
			guestEmail = m.User.Email
		}
	}
	if guestEmail != "bob@example.com" {
		t.Fatalf("访客邮箱 = %q，期望 %q", guestEmail, "bob@example.com")
	}

	// 次数用尽后第三人无法加入
	if _, err := store.JoinViaInviteLink(ctx, "user-carol", link.Code, core.MemberUser{ID: "user-carol"}); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("次数用尽时加入错误 = %v，期望 ErrForbidden", err)
	}

	// 已是成员重复加入不报错、不再消耗次数
	if _, err := store.JoinViaInviteLink(ctx, guestID, link.Code, core.MemberUser{ID: guestID}); err != nil {
		t.Fatalf("重复加入错误 = %v，期望成功", err)
	}

	// 提升为 MEMBER 后可写
	if _, err := store.UpdateMemberRole(ctx, ownerID, ws.ID, guestID, core.RoleMember); err != nil {
		t.Fatalf("UpdateMemberRole() 错误 = %v", err)
	}
	if _, err := store.CreateCollection(ctx, guestID, ws.ID, "允许", nil, nil, false); err != nil {
		t.Fatalf("MEMBER 建集合错误 = %v", err)
	}

	// 个人工作区不可删除
	personal, err := store.EnsurePersonalWorkspace(ctx, ownerID, "Alice")
	if err != nil {
		t.Fatalf("EnsurePersonalWorkspace() 错误 = %v", err)
	}
	if err := store.DeleteShellWorkspace(ctx, ownerID, personal.ID); !errors.Is(err, core.ErrDeletePersonal) {
		t.Fatalf("删除个人工作区错误 = %v，期望 ErrDeletePersonal", err)
	}
}

func TestInviteLinkConcurrentJoinHonorsMaxUses(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "invite-concurrency.db")
	storeA := newShellStoreAt(t, dsn)
	storeB := newShellStoreAt(t, dsn)
	ctx := context.Background()
	const ownerID = "invite-owner"

	workspace, err := storeA.CreateShellWorkspace(ctx, ownerID, "并发邀请", "", core.WorkspaceShared)
	if err != nil {
		t.Fatal(err)
	}
	maxUses := 1
	link, err := storeA.CreateInviteLink(ctx, ownerID, workspace.ID, core.RoleMember, nil, &maxUses)
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for index, store := range []*sqliteStore{storeA, storeB} {
		wait.Add(1)
		go func(index int, store *sqliteStore) {
			defer wait.Done()
			<-start
			userID := fmt.Sprintf("invite-guest-%d", index)
			_, joinErr := store.JoinViaInviteLink(ctx, userID, link.Code, core.MemberUser{ID: userID})
			results <- joinErr
		}(index, store)
	}
	close(start)
	wait.Wait()
	close(results)

	successes := 0
	for joinErr := range results {
		switch {
		case joinErr == nil:
			successes++
		case errors.Is(joinErr, core.ErrForbidden):
		default:
			t.Fatalf("并发加入返回意外错误：%v", joinErr)
		}
	}
	if successes != 1 {
		t.Fatalf("并发加入成功数 = %d，期望 1", successes)
	}

	var uses, members int
	if err := storeA.db.QueryRowContext(ctx, `SELECT uses FROM shell_invite_links WHERE id = ?`, link.ID).Scan(&uses); err != nil {
		t.Fatal(err)
	}
	if err := storeA.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM shell_members WHERE workspace_id = ?`, workspace.ID).Scan(&members); err != nil {
		t.Fatal(err)
	}
	if uses != 1 || members != 2 {
		t.Fatalf("邀请 uses=%d，成员数=%d；期望 uses=1、成员数=2", uses, members)
	}
}

func TestInviteWriteRechecksAdminAfterWaitingForWorkspaceLock(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "invite-admin-race.db")
	storeA := newShellStoreAt(t, dsn)
	storeB := newShellStoreAt(t, dsn)
	ctx := context.Background()
	const ownerID = "admin-owner"
	const adminID = "admin-to-demote"

	workspace, err := storeA.CreateShellWorkspace(ctx, ownerID, "权限撤销", "", core.WorkspaceShared)
	if err != nil {
		t.Fatal(err)
	}
	link, err := storeA.CreateInviteLink(ctx, ownerID, workspace.ID, core.RoleAdmin, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storeA.JoinViaInviteLink(ctx, adminID, link.Code, core.MemberUser{ID: adminID}); err != nil {
		t.Fatal(err)
	}

	tx, err := storeA.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE shell_workspaces SET updated_at = updated_at WHERE id = ?`, workspace.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE shell_members SET role = ? WHERE workspace_id = ? AND user_id = ?`, core.RoleMember, workspace.ID, adminID); err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() {
		_, createErr := storeB.CreateInviteLink(ctx, adminID, workspace.ID, core.RoleMember, nil, nil)
		result <- createErr
	}()
	select {
	case earlyErr := <-result:
		t.Fatalf("邀请创建未等待 Workspace 写锁，提前返回：%v", earlyErr)
	case <-time.After(100 * time.Millisecond):
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case createErr := <-result:
		if !errors.Is(createErr, core.ErrForbidden) {
			t.Fatalf("权限撤销后创建邀请错误 = %v，期望 ErrForbidden", createErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("权限撤销提交后邀请创建未返回")
	}
}

// TestMigrateLegacyGroupsToShell 走真实升级路径：先用一个 store 写入阶段一数据并关闭，
// 再用新 store 打开同一个库（NewStore 内部补 default 分组 + 建 shell 表 + 迁移）。
// 期望 default → 私有集合、自定义分组 → 公开集合，画布 collection_id 正确回填。
func TestMigrateLegacyGroupsToShell(t *testing.T) {
	ctx := context.Background()
	const userID = "legacy-user"
	dsn := filepath.Join(t.TempDir(), "legacy.db")

	// 阶段一的老库：default 分组 + 一个自定义分组，各一张画布
	legacy := newShellStoreAt(t, dsn)
	saveTestCanvas(t, legacy, userID, "canvas-1", "默认里的画布")
	if _, err := legacy.ListWorkspaces(ctx, userID); err != nil {
		t.Fatalf("ListWorkspaces() 错误 = %v", err)
	}
	group, err := legacy.CreateWorkspace(ctx, userID, "旧项目", "note")
	if err != nil {
		t.Fatalf("CreateWorkspace() 错误 = %v", err)
	}
	saveTestCanvas(t, legacy, userID, "canvas-2", "旧项目里的画布")
	if _, err := legacy.db.ExecContext(ctx, `UPDATE canvases SET workspace_id = ? WHERE user_id = ? AND id = ?`, group.ID, userID, "canvas-2"); err != nil {
		t.Fatalf("构造旧分组画布失败 = %v", err)
	}
	if err := legacy.db.Close(); err != nil {
		t.Fatalf("关闭老库失败: %v", err)
	}

	// 新二进制启动：NewStore 应自动完成迁移
	store := newShellStoreAt(t, dsn)

	workspaces, err := store.ListShellWorkspaces(ctx, userID)
	if err != nil {
		t.Fatalf("ListShellWorkspaces() 错误 = %v", err)
	}
	if len(workspaces) != 1 {
		t.Fatalf("迁移后工作区数 = %d，期望 1", len(workspaces))
	}
	ws := workspaces[0]

	collections, err := store.ListCollections(ctx, userID, ws.ID)
	if err != nil {
		t.Fatalf("ListCollections() 错误 = %v", err)
	}
	if len(collections) != 2 {
		t.Fatalf("迁移后集合数 = %d，期望 2", len(collections))
	}

	byName := map[string]*core.Collection{}
	for _, c := range collections {
		byName[c.Name] = c
	}
	def, ok := byName["默认分组"]
	if !ok {
		t.Fatalf("未找到迁移自 default 的集合，实得 %v", byName)
	}
	if def.IsPrivate {
		t.Fatal("default 分组不应再标成私有集合")
	}
	if def.SceneCount != 1 {
		t.Fatalf("私有集合场景数 = %d，期望 1", def.SceneCount)
	}
	custom, ok := byName["旧项目"]
	if !ok {
		t.Fatalf("未找到迁移自自定义分组的集合，实得 %v", byName)
	}
	if custom.IsPrivate {
		t.Fatal("自定义分组应迁移为公开集合")
	}
	if custom.SceneCount != 1 {
		t.Fatalf("公开集合场景数 = %d，期望 1", custom.SceneCount)
	}

	// 画布 → 场景映射正确
	scene, err := store.GetScene(ctx, userID, "canvas-2")
	if err != nil {
		t.Fatalf("GetScene() 错误 = %v", err)
	}
	if scene.CollectionID == nil || *scene.CollectionID != custom.ID {
		t.Fatalf("canvas-2 集合 = %v，期望 %q", scene.CollectionID, custom.ID)
	}
	if scene.WorkspaceID != custom.WorkspaceID {
		t.Fatalf("canvas-2 workspace_id = %q，Collection workspace_id = %q", scene.WorkspaceID, custom.WorkspaceID)
	}
	if scene.Title != "旧项目里的画布" {
		t.Fatalf("场景标题 = %q，期望 %q", scene.Title, "旧项目里的画布")
	}

	// 幂等：重复迁移不新增集合
	if err := store.MigrateLegacyGroupsToShell(ctx); err != nil {
		t.Fatalf("重复 MigrateLegacyGroupsToShell() 错误 = %v", err)
	}
	if again, err := store.ListCollections(ctx, userID, ws.ID); err != nil {
		t.Fatalf("ListCollections() 错误 = %v", err)
	} else if len(again) != 2 {
		t.Fatalf("重复迁移后集合数 = %d，期望 2", len(again))
	}

	// 老 WorkspaceStore 接口仍可用（过渡期共存）
	if legacy, err := store.ListWorkspaces(ctx, userID); err != nil {
		t.Fatalf("ListWorkspaces() 错误 = %v", err)
	} else if len(legacy) != 2 {
		t.Fatalf("老分组数 = %d，期望 2", len(legacy))
	}
}

func TestUpdateShellWorkspaceAvatar(t *testing.T) {
	store := newShellStore(t)
	ctx := context.Background()
	const userID = "user-alice"

	ws, err := store.EnsurePersonalWorkspace(ctx, userID, "Alice")
	if err != nil {
		t.Fatalf("EnsurePersonalWorkspace() 错误 = %v", err)
	}

	const dataURL = "data:image/png;base64,iVBORw0KGgo="
	updated, err := store.UpdateShellWorkspaceAvatar(ctx, userID, ws.ID, dataURL)
	if err != nil {
		t.Fatalf("UpdateShellWorkspaceAvatar() 错误 = %v", err)
	}
	if updated.AvatarURL == nil || *updated.AvatarURL != dataURL {
		t.Fatalf("avatar_url = %v，期望 %q", updated.AvatarURL, dataURL)
	}

	if _, err := store.UpdateShellWorkspaceAvatar(ctx, "user-bob", ws.ID, dataURL); !errors.Is(err, core.ErrNotFound) && !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("非成员改头像错误 = %v，期望 not found / forbidden", err)
	}
}

func TestUpdateShellWorkspaceRollsBackNameWhenSlugConflicts(t *testing.T) {
	store := newShellStore(t)
	ctx := context.Background()
	const userID = "user-alice"

	first, err := store.CreateShellWorkspace(ctx, userID, "原名称", "first", core.WorkspaceShared)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateShellWorkspace(ctx, userID, "占用 Slug", "second", core.WorkspaceShared)
	if err != nil {
		t.Fatal(err)
	}
	changedName := "不应提交"
	conflictingSlug := second.Slug
	if _, err := store.UpdateShellWorkspace(ctx, userID, first.ID, &changedName, &conflictingSlug); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("冲突更新错误 = %v，期望 ErrConflict", err)
	}

	unchanged, err := store.GetShellWorkspace(ctx, userID, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Name != "原名称" || unchanged.Slug != first.Slug {
		t.Fatalf("slug 冲突后 Workspace 被部分更新: %+v", unchanged)
	}
}

func TestListScenesExcludesIndexedDBCanvasID(t *testing.T) {
	store := newShellStore(t)
	ctx := context.Background()
	const userID = "user-alice"

	ws, err := store.EnsurePersonalWorkspace(ctx, userID, "Alice")
	if err != nil {
		t.Fatalf("EnsurePersonalWorkspace() 错误 = %v", err)
	}
	collections, err := store.ListCollections(ctx, userID, ws.ID)
	if err != nil || len(collections) == 0 {
		t.Fatalf("ListCollections() 错误 = %v 集合=%d", err, len(collections))
	}
	collID := collections[0].ID

	scene, err := store.CreateScene(ctx, userID, "正经场景", nil, []byte(`{"elements":[]}`), &collID)
	if err != nil {
		t.Fatalf("CreateScene() 错误 = %v", err)
	}

	const uuid = "a1b2c3d4-e5f6-7890-abcd-ef1234567890"
	now := time.Now().UTC()
	if _, err := store.db.ExecContext(ctx, `INSERT INTO canvases (id, user_id, name, thumbnail, data, workspace_id, collection_id, created_at, updated_at)
		VALUES (?, ?, ?, '', ?, 'default', ?, ?, ?)`,
		uuid, userID, "来自 IndexedDB", []byte(`{"elements":[1]}`), collID, now, now); err != nil {
		t.Fatalf("插入 UUID 脏行失败: %v", err)
	}

	scenes, err := store.ListScenes(ctx, userID, &ws.ID, nil)
	if err != nil {
		t.Fatalf("ListScenes() 错误 = %v", err)
	}
	for _, item := range scenes {
		if item.ID == uuid {
			t.Fatal("Workspace 列表不应包含 IndexedDB UUID")
		}
	}
	if len(scenes) != 1 || scenes[0].ID != scene.ID {
		t.Fatalf("Workspace 场景 = %+v，期望仅 %q", scenes, scene.ID)
	}
}

func TestCreateSceneDefaultsCollectionAndEmptyJSON(t *testing.T) {
	store := newShellStore(t)
	ctx := context.Background()
	const userID = "user-alice"

	scene, err := store.CreateScene(ctx, userID, "空画布", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateScene() 错误 = %v", err)
	}
	if scene.CollectionID == nil || *scene.CollectionID == "" {
		t.Fatal("未指定集合时应落入个人工作区默认集合")
	}
	data, err := store.GetSceneData(ctx, userID, scene.ID)
	if err != nil {
		t.Fatalf("GetSceneData() 错误 = %v", err)
	}
	if string(data) != `{"elements":[],"appState":{},"files":{}}` {
		t.Fatalf("空场景 data = %q", data)
	}
}

func TestGetCanvasAllowsWorkspaceMember(t *testing.T) {
	store := newShellStore(t)
	ctx := context.Background()
	const ownerID = "user-alice"
	const guestID = "user-bob"

	ws, err := store.CreateShellWorkspace(ctx, ownerID, "团队", "", core.WorkspaceShared)
	if err != nil {
		t.Fatalf("CreateShellWorkspace() 错误 = %v", err)
	}
	coll, err := store.CreateCollection(ctx, ownerID, ws.ID, "共享", nil, nil, false)
	if err != nil {
		t.Fatalf("CreateCollection() 错误 = %v", err)
	}
	scene, err := store.CreateScene(ctx, ownerID, "共享画布", nil, []byte(`{"elements":[]}`), &coll.ID)
	if err != nil {
		t.Fatalf("CreateScene() 错误 = %v", err)
	}

	if _, err := store.Get(ctx, guestID, scene.ID); err == nil {
		t.Fatal("非成员不应读到场景")
	}

	maxUses := 1
	link, err := store.CreateInviteLink(ctx, ownerID, ws.ID, core.RoleMember, nil, &maxUses)
	if err != nil {
		t.Fatalf("CreateInviteLink() 错误 = %v", err)
	}
	if _, err := store.JoinViaInviteLink(ctx, guestID, link.Code, core.MemberUser{ID: guestID}); err != nil {
		t.Fatalf("JoinViaInviteLink() 错误 = %v", err)
	}

	got, err := store.Get(ctx, guestID, scene.ID)
	if err != nil {
		t.Fatalf("成员 Get() 错误 = %v", err)
	}
	if got.ID != scene.ID {
		t.Fatalf("成员读到 id = %q，期望 %q", got.ID, scene.ID)
	}
}
