package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"excalidraw-complete/core"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

var dsnCounter int64

// newTestStore 创建共享内存 SQLite（cache=shared 保证多连接共享同一内存库，
// 避免 :memory: 每连接独立导致建表在别的连接上看不到）。
func newTestStore(t *testing.T) *sqliteStore {
	t.Helper()
	dsn := fmt.Sprintf("file:excalidraw-test-%d?mode=memory&cache=shared", atomic.AddInt64(&dsnCounter, 1))
	store := NewStore(dsn)
	t.Cleanup(func() {
		store.db.Close()
	})
	return store
}

// saveTestCanvas 构造阶段一旧数据：尚无 Collection，workspace_id 使用旧分组。
func saveTestCanvas(t *testing.T, store *sqliteStore, userID, id, name string) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := store.db.ExecContext(context.Background(), `
		INSERT INTO canvases (id, user_id, name, thumbnail, data, workspace_id, collection_id, created_at, updated_at)
		VALUES (?, ?, ?, '', ?, ?, NULL, ?, ?)`,
		id, userID, name, []byte(`{"type":"excalidraw"}`), core.DefaultWorkspaceID, now, now); err != nil {
		t.Fatalf("保存画布失败: %v", err)
	}
}

// TestWorkspacesLifecycle 覆盖完整生命周期：
// 懒创建 default → 建组 → 列出 → 更新 → 画布移组 → 删组（画布迁回 default）→ 删 default 报错。
func TestWorkspacesLifecycle(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	const userID = "test-user"

	// 懒创建：首次 ListWorkspaces 无任何行时补 default 分组
	workspaces, err := store.ListWorkspaces(ctx, userID)
	if err != nil {
		t.Fatalf("ListWorkspaces() 错误 = %v", err)
	}
	if len(workspaces) != 1 {
		t.Fatalf("懒创建后分组数 = %d，期望 1", len(workspaces))
	}
	if workspaces[0].ID != core.DefaultWorkspaceID {
		t.Fatalf("懒创建分组 id = %q，期望 %q", workspaces[0].ID, core.DefaultWorkspaceID)
	}
	if workspaces[0].Name != "默认分组" {
		t.Fatalf("懒创建分组名称 = %q，期望 %q", workspaces[0].Name, "默认分组")
	}

	// 创建新分组（id 用 ULID）
	created, err := store.CreateWorkspace(ctx, userID, "项目 A", "客户演示")
	if err != nil {
		t.Fatalf("CreateWorkspace() 错误 = %v", err)
	}
	if created.ID == "" {
		t.Fatal("CreateWorkspace() 返回空 id")
	}
	if created.Name != "项目 A" || created.Note != "客户演示" {
		t.Fatalf("CreateWorkspace() 返回 = %+v", created)
	}

	// 列出：default + 新建 = 2 个
	workspaces, err = store.ListWorkspaces(ctx, userID)
	if err != nil {
		t.Fatalf("ListWorkspaces() 错误 = %v", err)
	}
	if len(workspaces) != 2 {
		t.Fatalf("创建后分组数 = %d，期望 2", len(workspaces))
	}

	// 更新分组
	if err := store.UpdateWorkspace(ctx, userID, created.ID, "项目 A-改", "备注已改"); err != nil {
		t.Fatalf("UpdateWorkspace() 错误 = %v", err)
	}
	workspaces, err = store.ListWorkspaces(ctx, userID)
	if err != nil {
		t.Fatalf("ListWorkspaces() 错误 = %v", err)
	}
	var updated *core.Workspace
	for _, ws := range workspaces {
		if ws.ID == created.ID {
			updated = ws
		}
	}
	if updated == nil {
		t.Fatal("更新后找不到新分组")
	}
	if updated.Name != "项目 A-改" || updated.Note != "备注已改" {
		t.Fatalf("更新后分组 = %+v", updated)
	}

	// 更新不存在的分组应报错
	if err := store.UpdateWorkspace(ctx, userID, "no-such-id", "x", ""); err == nil {
		t.Fatal("更新不存在的分组应返回错误")
	}

	// 保存两张画布（默认归入 default）+ 移组
	saveTestCanvas(t, store, userID, "canvas-1", "画布一")
	saveTestCanvas(t, store, userID, "canvas-2", "画布二")

	if err := store.MoveCanvasWorkspace(ctx, userID, "canvas-1", created.ID); !errors.Is(err, core.ErrInvalidInput) {
		t.Fatalf("旧 workspace move 错误 = %v，期望 ErrInvalidInput", err)
	}

	// 验证画布已带 workspace_id（List 与 Get 都读该列）
	canvases, err := store.List(ctx, userID)
	if err != nil {
		t.Fatalf("List() 错误 = %v", err)
	}
	for _, c := range canvases {
		if c.WorkspaceID != core.DefaultWorkspaceID {
			t.Fatalf("旧画布 workspace_id = %q，期望保持 %q", c.WorkspaceID, core.DefaultWorkspaceID)
		}
	}

	got, err := store.Get(ctx, userID, "canvas-1")
	if err != nil {
		t.Fatalf("Get() 错误 = %v", err)
	}
	if got.WorkspaceID != core.DefaultWorkspaceID {
		t.Fatalf("Get() workspace_id = %q，期望 %q", got.WorkspaceID, core.DefaultWorkspaceID)
	}

	// 移组到不存在的分组应报错
	if err := store.MoveCanvasWorkspace(ctx, userID, "canvas-1", "no-such-ws"); err == nil {
		t.Fatal("移动到不存在的分组应返回错误")
	}
	// 移动不存在的画布应报错
	if err := store.MoveCanvasWorkspace(ctx, userID, "no-such-canvas", created.ID); err == nil {
		t.Fatal("移动不存在的画布应返回错误")
	}

	// 删除空旧分组不影响 Scene 归属。
	if err := store.DeleteWorkspace(ctx, userID, created.ID); err != nil {
		t.Fatalf("DeleteWorkspace() 错误 = %v", err)
	}
	got, err = store.Get(ctx, userID, "canvas-1")
	if err != nil {
		t.Fatalf("Get() 错误 = %v", err)
	}
	if got.WorkspaceID != core.DefaultWorkspaceID {
		t.Fatalf("删组后 canvas-1 workspace_id = %q，期望 %q", got.WorkspaceID, core.DefaultWorkspaceID)
	}

	// 删除不存在的分组应报错
	if err := store.DeleteWorkspace(ctx, userID, created.ID); err == nil {
		t.Fatal("删除不存在的分组应返回错误")
	}

	// 不允许删除 default 分组
	if err := store.DeleteWorkspace(ctx, userID, core.DefaultWorkspaceID); !errors.Is(err, core.ErrDeleteDefaultWorkspace) {
		t.Fatalf("删除 default 分组错误 = %v，期望 %v", err, core.ErrDeleteDefaultWorkspace)
	}
}

// TestWorkspaceUserIsolation 验证分组按用户隔离。
func TestWorkspaceUserIsolation(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ws, err := store.CreateWorkspace(ctx, "user-a", "A 的分组", "")
	if err != nil {
		t.Fatalf("CreateWorkspace() 错误 = %v", err)
	}

	// user-b 看不到、也删不掉 user-a 的分组
	listB, err := store.ListWorkspaces(ctx, "user-b")
	if err != nil {
		t.Fatalf("ListWorkspaces() 错误 = %v", err)
	}
	if len(listB) != 1 || listB[0].ID != core.DefaultWorkspaceID {
		t.Fatalf("user-b 的分组 = %+v，期望只有懒创建的 default", listB)
	}
	if err := store.DeleteWorkspace(ctx, "user-b", ws.ID); err == nil {
		t.Fatal("user-b 删除 user-a 的分组应报错")
	}
	if err := store.UpdateWorkspace(ctx, "user-b", ws.ID, "x", ""); err == nil {
		t.Fatal("user-b 更新 user-a 的分组应报错")
	}
}

// TestInitSeedsDefaultWorkspaceForExistingUsers 验证老库升级：
// 已有画布的用户在 NewStore 初始化时自动补 default 分组。
func TestInitSeedsDefaultWorkspaceForExistingUsers(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "excalidraw.db")

	first := NewStore(dsn)
	saveTestCanvas(t, first, "user-seed", "canvas-1", "老画布")
	first.db.Close()

	second := NewStore(dsn)
	defer second.db.Close()

	workspaces, err := second.ListWorkspaces(context.Background(), "user-seed")
	if err != nil {
		t.Fatalf("ListWorkspaces() 错误 = %v", err)
	}
	if len(workspaces) != 1 || workspaces[0].ID != core.DefaultWorkspaceID {
		t.Fatalf("老用户 default 分组未初始化: %+v", workspaces)
	}

	// 无画布的用户不预置（懒创建兜底）
	workspaces, err = second.ListWorkspaces(context.Background(), "user-empty")
	if err != nil {
		t.Fatalf("ListWorkspaces() 错误 = %v", err)
	}
	if len(workspaces) != 1 || workspaces[0].ID != core.DefaultWorkspaceID {
		t.Fatalf("空用户懒创建失败: %+v", workspaces)
	}
}

// TestMigrateLegacyDBAddsWorkspaceColumn 验证幂等迁移：
// 旧 schema（无 workspace_id 列）的库在 NewStore 时自动 ALTER TABLE 升级。
func TestMigrateLegacyDBAddsWorkspaceColumn(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "legacy.db")

	// 手工构造旧 schema 库（模拟迁移前的老库）
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("打开旧库失败: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE canvases (
		id TEXT NOT NULL,
		user_id TEXT NOT NULL,
		name TEXT,
		thumbnail TEXT,
		data BLOB,
		created_at DATETIME,
		updated_at DATETIME,
		PRIMARY KEY (user_id, id)
	)`); err != nil {
		t.Fatalf("创建旧表失败: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO canvases (id, user_id, name, thumbnail, data, created_at, updated_at) VALUES ('legacy-1', 'user-legacy', '旧画布', '', '{}', datetime('now'), datetime('now'))`); err != nil {
		t.Fatalf("插入旧画布失败: %v", err)
	}
	db.Close()

	// NewStore 应完成迁移：加列 + 补 default 分组
	store := NewStore(dsn)
	defer store.db.Close()

	if !columnExists(store.db, "canvases", "workspace_id") {
		t.Fatal("迁移后 canvases 表仍缺少 workspace_id 列")
	}

	// 旧画布自动归入个人工作区默认 Collection，冗余 workspace_id 必须同步。
	canvas, err := store.Get(context.Background(), "user-legacy", "legacy-1")
	if err != nil {
		t.Fatalf("Get() 错误 = %v", err)
	}
	var collectionWorkspaceID string
	if err := store.db.QueryRow(`SELECT c.workspace_id FROM shell_collections c JOIN canvases cv ON cv.collection_id = c.id WHERE cv.id = ?`, "legacy-1").Scan(&collectionWorkspaceID); err != nil {
		t.Fatalf("读取迁移后 Collection 工作区失败: %v", err)
	}
	if canvas.WorkspaceID != collectionWorkspaceID {
		t.Fatalf("旧画布 workspace_id = %q，Collection workspace_id = %q", canvas.WorkspaceID, collectionWorkspaceID)
	}

	// 旧用户自动补 default 分组
	workspaces, err := store.ListWorkspaces(context.Background(), "user-legacy")
	if err != nil {
		t.Fatalf("ListWorkspaces() 错误 = %v", err)
	}
	if len(workspaces) != 1 || workspaces[0].ID != core.DefaultWorkspaceID {
		t.Fatalf("旧用户 default 分组未初始化: %+v", workspaces)
	}

	// 二次打开幂等（不报重复列错误）
	reopen := NewStore(dsn)
	defer reopen.db.Close()
	if !columnExists(reopen.db, "canvases", "workspace_id") {
		t.Fatal("二次打开后 workspace_id 列丢失")
	}
}

func TestSaveRejectsUnknownCanvasID(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	const userID = "user-idb"
	const uuid = "a1b2c3d4-e5f6-7890-abcd-ef1234567890"

	for _, id := range []string{uuid, "server-shaped-but-missing"} {
		err := store.Save(ctx, &core.Canvas{
			ID:     id,
			UserID: userID,
			Name:   "from-indexeddb",
			Data:   []byte(`{"elements":[{"id":"x"}]}`),
		})
		if !errors.Is(err, core.ErrNotFound) {
			t.Fatalf("Save 不存在的 ID %q 错误 = %v，期望 ErrNotFound", id, err)
		}
	}

	list, err := store.List(ctx, userID)
	if err != nil {
		t.Fatalf("List() 错误 = %v", err)
	}
	for _, c := range list {
		if c.ID == uuid || c.ID == "server-shaped-but-missing" {
			t.Fatal("SQLite 不应留下 KV PUT 直接创建的画布")
		}
	}
}
