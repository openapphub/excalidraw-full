package sqlite

import (
	"context"
	"errors"
	"excalidraw-complete/core"
	"path/filepath"
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

	// 删集合：场景保留但脱离集合
	if err := store.DeleteCollection(ctx, userID, privateColl.ID); err != nil {
		t.Fatalf("DeleteCollection() 错误 = %v", err)
	}
	orphan, err := store.GetScene(ctx, userID, scene.ID)
	if err != nil {
		t.Fatalf("删集合后 GetScene() 错误 = %v", err)
	}
	if orphan.CollectionID != nil {
		t.Fatalf("删集合后场景集合 = %v，期望 nil", orphan.CollectionID)
	}

	// 删场景
	if err := store.DeleteScene(ctx, userID, scene.ID); err != nil {
		t.Fatalf("DeleteScene() 错误 = %v", err)
	}
	if _, err := store.GetScene(ctx, userID, scene.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("删除后 GetScene() 错误 = %v，期望 ErrNotFound", err)
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
	if err := legacy.MoveCanvasWorkspace(ctx, userID, "canvas-2", group.ID); err != nil {
		t.Fatalf("MoveCanvasWorkspace() 错误 = %v", err)
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
