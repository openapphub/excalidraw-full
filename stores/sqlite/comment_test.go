package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"excalidraw-complete/core"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// newSceneForComments 建好「个人工作区 + 集合 + 场景」，返回场景 id。
func newSceneForComments(t *testing.T, store *sqliteStore, userID, displayName string) string {
	t.Helper()
	ctx := context.Background()

	ws, err := store.EnsurePersonalWorkspace(ctx, userID, displayName)
	if err != nil {
		t.Fatalf("EnsurePersonalWorkspace() 错误 = %v", err)
	}
	coll, err := store.CreateCollection(ctx, userID, ws.ID, "评论测试", nil, nil, false)
	if err != nil {
		t.Fatalf("CreateCollection() 错误 = %v", err)
	}
	scene, err := store.CreateScene(ctx, userID, "带评论的画布", nil, []byte(`{"elements":[]}`), &coll.ID)
	if err != nil {
		t.Fatalf("CreateScene() 错误 = %v", err)
	}
	return scene.ID
}

// TestCommentThreadLifecycle 覆盖建线程 → 列举 → 回复 → resolve → 删除。
func TestCommentThreadLifecycle(t *testing.T) {
	store := newShellStore(t)
	ctx := context.Background()
	const userID = "user-alice"

	if err := store.UpsertUserProfile(ctx, core.MemberUser{ID: userID, Email: "alice@example.com"}); err != nil {
		t.Fatalf("UpsertUserProfile() 错误 = %v", err)
	}
	sceneID := newSceneForComments(t, store, userID, "Alice")

	thread, err := store.CreateThread(ctx, userID, sceneID, 120.5, -40, "这里的间距不对", nil)
	if err != nil {
		t.Fatalf("CreateThread() 错误 = %v", err)
	}
	if thread.X != 120.5 || thread.Y != -40 {
		t.Fatalf("线程坐标 = (%v, %v)，期望 (120.5, -40)", thread.X, thread.Y)
	}
	if thread.CommentCount != 1 || len(thread.Comments) != 1 {
		t.Fatalf("线程评论数 = %d，期望 1", thread.CommentCount)
	}
	if thread.Comments[0].Content != "这里的间距不对" {
		t.Fatalf("首条评论 = %q", thread.Comments[0].Content)
	}
	// 作者信息应从 user_profiles 拼出（无 name 时退化为邮箱前缀）
	if thread.CreatedBy.Name != "alice" || thread.CreatedBy.Email != "alice@example.com" {
		t.Fatalf("作者信息 = %+v，期望 name=alice", thread.CreatedBy)
	}

	threads, err := store.ListThreads(ctx, userID, sceneID, nil)
	if err != nil {
		t.Fatalf("ListThreads() 错误 = %v", err)
	}
	if len(threads) != 1 || threads[0].ID != thread.ID {
		t.Fatalf("线程列表 = %+v，期望只含 %q", threads, thread.ID)
	}

	comment, err := store.AddComment(ctx, userID, thread.ID, "已改，请再看", nil)
	if err != nil {
		t.Fatalf("AddComment() 错误 = %v", err)
	}
	if comment.ThreadID != thread.ID {
		t.Fatalf("回复所属线程 = %q，期望 %q", comment.ThreadID, thread.ID)
	}

	reloaded, err := store.GetThread(ctx, userID, thread.ID)
	if err != nil {
		t.Fatalf("GetThread() 错误 = %v", err)
	}
	if reloaded.CommentCount != 2 {
		t.Fatalf("回复后评论数 = %d，期望 2", reloaded.CommentCount)
	}

	resolved, err := store.SetThreadResolved(ctx, userID, thread.ID, true)
	if err != nil {
		t.Fatalf("SetThreadResolved(true) 错误 = %v", err)
	}
	if !resolved.Resolved || resolved.ResolvedAt == nil || resolved.ResolvedBy == nil {
		t.Fatalf("resolve 后线程 = %+v，期望带 resolvedAt/resolvedBy", resolved)
	}
	if resolved.ResolvedBy.ID != userID {
		t.Fatalf("resolvedBy = %q，期望 %q", resolved.ResolvedBy.ID, userID)
	}

	// resolved 过滤
	if open, err := store.ListThreads(ctx, userID, sceneID, boolPtr(false)); err != nil {
		t.Fatalf("ListThreads(resolved=false) 错误 = %v", err)
	} else if len(open) != 0 {
		t.Fatalf("未解决线程数 = %d，期望 0", len(open))
	}

	if _, err := store.SetThreadResolved(ctx, userID, thread.ID, false); err != nil {
		t.Fatalf("SetThreadResolved(false) 错误 = %v", err)
	}

	if err := store.DeleteThread(ctx, userID, thread.ID); err != nil {
		t.Fatalf("DeleteThread() 错误 = %v", err)
	}
	if remaining, err := store.ListThreads(ctx, userID, sceneID, nil); err != nil {
		t.Fatalf("删除后 ListThreads() 错误 = %v", err)
	} else if len(remaining) != 0 {
		t.Fatalf("删除后线程数 = %d，期望 0", len(remaining))
	}
}

// TestCommentEditAndDelete 校验编辑仅限作者，且删完最后一条评论会连线程一起清掉。
func TestCommentEditAndDelete(t *testing.T) {
	store := newShellStore(t)
	ctx := context.Background()
	const userID = "user-alice"
	sceneID := newSceneForComments(t, store, userID, "Alice")

	thread, err := store.CreateThread(ctx, userID, sceneID, 0, 0, "第一条", nil)
	if err != nil {
		t.Fatalf("CreateThread() 错误 = %v", err)
	}
	rootID := thread.Comments[0].ID

	edited, err := store.UpdateComment(ctx, userID, rootID, "第一条（已改）")
	if err != nil {
		t.Fatalf("UpdateComment() 错误 = %v", err)
	}
	if edited.Content != "第一条（已改）" || edited.EditedAt == nil {
		t.Fatalf("编辑结果 = %+v，期望内容更新且带 editedAt", edited)
	}

	// 非作者不能编辑
	if _, err := store.UpdateComment(ctx, "user-bob", rootID, "捣乱"); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("他人编辑错误 = %v，期望 ErrForbidden", err)
	}

	if err := store.DeleteComment(ctx, userID, rootID); err != nil {
		t.Fatalf("DeleteComment() 错误 = %v", err)
	}
	if _, err := store.GetThread(ctx, userID, thread.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("删完最后一条评论后 GetThread 错误 = %v，期望 ErrNotFound", err)
	}
}

// TestCommentPermissions 校验非成员既不能读也不能写，VIEWER 只读。
func TestCommentPermissions(t *testing.T) {
	store := newShellStore(t)
	ctx := context.Background()
	const owner = "user-alice"
	const outsider = "user-carol"

	sceneID := newSceneForComments(t, store, owner, "Alice")
	thread, err := store.CreateThread(ctx, owner, sceneID, 10, 10, "内部讨论", nil)
	if err != nil {
		t.Fatalf("CreateThread() 错误 = %v", err)
	}

	if _, err := store.ListThreads(ctx, outsider, sceneID, nil); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("非成员读取错误 = %v，期望 ErrForbidden", err)
	}
	if _, err := store.AddComment(ctx, outsider, thread.ID, "路过", nil); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("非成员回复错误 = %v，期望 ErrForbidden", err)
	}

	// VIEWER 加入同一工作区后可读不可写
	viewer := core.MemberUser{ID: "user-view", Email: "view@example.com"}
	if err := store.UpsertUserProfile(ctx, viewer); err != nil {
		t.Fatalf("UpsertUserProfile() 错误 = %v", err)
	}
	workspaceID := workspaceIDOfScene(t, store, sceneID)
	if _, err := store.InviteMember(ctx, owner, workspaceID, viewer.Email, core.RoleViewer); err != nil {
		t.Fatalf("InviteMember() 错误 = %v", err)
	}

	if threads, err := store.ListThreads(ctx, viewer.ID, sceneID, nil); err != nil {
		t.Fatalf("VIEWER 读取错误 = %v", err)
	} else if len(threads) != 1 {
		t.Fatalf("VIEWER 可见线程数 = %d，期望 1", len(threads))
	}
	if _, err := store.AddComment(ctx, viewer.ID, thread.ID, "只读不该能写", nil); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("VIEWER 写入错误 = %v，期望 ErrForbidden", err)
	}
}

// TestMentionNotifications 校验 @提及写 MENTION、被回复的线程作者写 COMMENT，且不给自己发。
func TestMentionNotifications(t *testing.T) {
	store := newShellStore(t)
	ctx := context.Background()
	const owner = "user-alice"

	sceneID := newSceneForComments(t, store, owner, "Alice")
	workspaceID := workspaceIDOfScene(t, store, sceneID)

	bob := core.MemberUser{ID: "user-bob", Email: "bob@example.com"}
	if err := store.UpsertUserProfile(ctx, bob); err != nil {
		t.Fatalf("UpsertUserProfile() 错误 = %v", err)
	}
	if _, err := store.InviteMember(ctx, owner, workspaceID, bob.Email, core.RoleMember); err != nil {
		t.Fatalf("InviteMember() 错误 = %v", err)
	}

	// alice 建线程并 @bob → bob 收到 MENTION，alice 自己没有
	thread, err := store.CreateThread(ctx, owner, sceneID, 5, 5, "@[Bob](user-bob) 看下这里",
		[]string{bob.ID, owner, "user-outsider"})
	if err != nil {
		t.Fatalf("CreateThread() 错误 = %v", err)
	}

	if count, err := store.CountUnreadNotifications(ctx, bob.ID); err != nil {
		t.Fatalf("CountUnreadNotifications(bob) 错误 = %v", err)
	} else if count != 1 {
		t.Fatalf("bob 未读数 = %d，期望 1", count)
	}
	if count, err := store.CountUnreadNotifications(ctx, owner); err != nil {
		t.Fatalf("CountUnreadNotifications(alice) 错误 = %v", err)
	} else if count != 0 {
		t.Fatalf("alice 未读数 = %d，期望 0（不给自己发）", count)
	}
	// 非成员不该收到通知
	if count, err := store.CountUnreadNotifications(ctx, "user-outsider"); err != nil {
		t.Fatalf("CountUnreadNotifications(outsider) 错误 = %v", err)
	} else if count != 0 {
		t.Fatalf("非成员未读数 = %d，期望 0", count)
	}

	resp, err := store.ListNotifications(ctx, bob.ID, "", 0, false)
	if err != nil {
		t.Fatalf("ListNotifications() 错误 = %v", err)
	}
	if len(resp.Notifications) != 1 {
		t.Fatalf("bob 通知数 = %d，期望 1", len(resp.Notifications))
	}
	notification := resp.Notifications[0]
	if notification.Type != core.NotificationMention {
		t.Fatalf("通知类型 = %q，期望 MENTION", notification.Type)
	}
	if notification.Thread == nil || notification.Thread.ID != thread.ID {
		t.Fatalf("通知线程 = %+v，期望 %q", notification.Thread, thread.ID)
	}
	if notification.Scene.ID != sceneID || notification.Scene.Name != "带评论的画布" {
		t.Fatalf("通知场景 = %+v，期望带场景名", notification.Scene)
	}
	if notification.Actor.ID != owner || notification.Read {
		t.Fatalf("通知 actor/已读 = %+v / %v", notification.Actor, notification.Read)
	}

	// bob 回复 → 线程作者 alice 收到 COMMENT
	if _, err := store.AddComment(ctx, bob.ID, thread.ID, "收到", nil); err != nil {
		t.Fatalf("AddComment() 错误 = %v", err)
	}
	aliceResp, err := store.ListNotifications(ctx, owner, "", 0, true)
	if err != nil {
		t.Fatalf("ListNotifications(alice, unread) 错误 = %v", err)
	}
	if len(aliceResp.Notifications) != 1 || aliceResp.Notifications[0].Type != core.NotificationComment {
		t.Fatalf("alice 未读通知 = %+v，期望 1 条 COMMENT", aliceResp.Notifications)
	}

	// 标记已读
	if err := store.MarkNotificationRead(ctx, bob.ID, notification.ID); err != nil {
		t.Fatalf("MarkNotificationRead() 错误 = %v", err)
	}
	if count, err := store.CountUnreadNotifications(ctx, bob.ID); err != nil {
		t.Fatalf("CountUnreadNotifications() 错误 = %v", err)
	} else if count != 0 {
		t.Fatalf("标记已读后未读数 = %d，期望 0", count)
	}

	if err := store.MarkAllNotificationsRead(ctx, owner); err != nil {
		t.Fatalf("MarkAllNotificationsRead() 错误 = %v", err)
	}
	if count, err := store.CountUnreadNotifications(ctx, owner); err != nil {
		t.Fatalf("CountUnreadNotifications() 错误 = %v", err)
	} else if count != 0 {
		t.Fatalf("全部已读后未读数 = %d，期望 0", count)
	}
}

func boolPtr(v bool) *bool {
	return &v
}

// workspaceIDOfScene 由场景反查所属工作区（经集合）。
func workspaceIDOfScene(t *testing.T, store *sqliteStore, sceneID string) string {
	t.Helper()
	var workspaceID string
	err := store.db.QueryRow(
		`SELECT c.workspace_id FROM shell_collections c
		 JOIN canvases s ON s.collection_id = c.id WHERE s.id = ? LIMIT 1`, sceneID).Scan(&workspaceID)
	if err != nil {
		t.Fatalf("查询场景所属工作区失败: %v", err)
	}
	return workspaceID
}

// TestEnsureCommentSchemaMigratesLegacyTable 生产库可能已有旧 element comments 表。
func TestEnsureCommentSchemaMigratesLegacyTable(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "legacy-comments.db")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("打开临时库失败: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE comments (
		id TEXT PRIMARY KEY,
		canvas_id TEXT NOT NULL,
		element_id TEXT NOT NULL,
		user_id TEXT NOT NULL,
		text TEXT NOT NULL,
		parent_id TEXT DEFAULT '',
		resolved INTEGER DEFAULT 0,
		created_at DATETIME NOT NULL
	)`); err != nil {
		t.Fatalf("创建旧 comments 表失败: %v", err)
	}
	db.Close()

	store := NewStore(dsn)
	t.Cleanup(func() { store.db.Close() })

	if !columnExists(store.db, "comments", "thread_id") {
		t.Fatal("迁移后 comments 应有 thread_id")
	}
	var name string
	if err := store.db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='comments_legacy'`).Scan(&name); err != nil {
		t.Fatalf("应保留 comments_legacy: %v", err)
	}
}
