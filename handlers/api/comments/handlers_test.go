package comments

import (
	"bytes"
	"context"
	"encoding/json"
	"excalidraw-complete/core"
	"excalidraw-complete/handlers/auth"
	"excalidraw-complete/middleware"
	"excalidraw-complete/stores"
	"excalidraw-complete/stores/sqlite"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	_ "modernc.org/sqlite"
)

// newTestRouter 挂载评论路由，并用固定 claims 冒充已登录用户。
func newTestRouter(t *testing.T, store stores.Store, userID, email string) *chi.Mux {
	t.Helper()
	r := chi.NewRouter()
	r.Route("/api/v2", func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if userID == "" {
					next.ServeHTTP(w, req)
					return
				}
				claims := &auth.AppClaims{
					RegisteredClaims: jwt.RegisteredClaims{Subject: userID},
					Email:            email,
					Name:             "",
				}
				ctx := context.WithValue(req.Context(), middleware.ClaimsContextKey, claims)
				next.ServeHTTP(w, req.WithContext(ctx))
			})
		})
		Routes(r, store)
	})
	return r
}

func newTestStore(t *testing.T) stores.Store {
	t.Helper()
	return sqlite.NewStore(filepath.Join(t.TempDir(), "comments-http-test.db"))
}

// newScene 建好工作区/集合/场景，返回场景 id 与工作区 id。
func newScene(t *testing.T, store stores.Store, userID string) (string, string) {
	t.Helper()
	ctx := context.Background()
	ws, err := store.EnsurePersonalWorkspace(ctx, userID, "Alice")
	if err != nil {
		t.Fatalf("EnsurePersonalWorkspace() 错误 = %v", err)
	}
	coll, err := store.CreateCollection(ctx, userID, ws.ID, "评论", nil, nil, false)
	if err != nil {
		t.Fatalf("CreateCollection() 错误 = %v", err)
	}
	scene, err := store.CreateScene(ctx, userID, "画布", nil, []byte("{}"), &coll.ID)
	if err != nil {
		t.Fatalf("CreateScene() 错误 = %v", err)
	}
	return scene.ID, ws.ID
}

func do(t *testing.T, router http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化请求体失败: %v", err)
		}
		reader = bytes.NewReader(buf)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析响应失败: %v (body=%s)", err, rec.Body.String())
	}
	return out
}

// TestThreadEndpoints 走一遍建线程 → 列举 → 回复 → resolve → 删除的 HTTP 路径。
func TestThreadEndpoints(t *testing.T) {
	store := newTestStore(t)
	const userID = "user-alice"
	sceneID, _ := newScene(t, store, userID)
	router := newTestRouter(t, store, userID, "alice@example.com")

	rec := do(t, router, http.MethodPost, "/api/v2/scenes/"+sceneID+"/threads",
		map[string]any{"x": 12, "y": 34, "content": "这里对齐有问题"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("创建线程状态码 = %d，期望 201（body=%s）", rec.Code, rec.Body.String())
	}
	thread := decode[core.CommentThread](t, rec)
	if thread.ID == "" || thread.CommentCount != 1 {
		t.Fatalf("创建结果 = %+v，期望带 id 且 commentCount=1", thread)
	}
	if thread.X != 12 || thread.Y != 34 {
		t.Fatalf("线程坐标 = (%v, %v)，期望 (12, 34)", thread.X, thread.Y)
	}

	rec = do(t, router, http.MethodGet, "/api/v2/scenes/"+sceneID+"/threads", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("列举状态码 = %d，期望 200", rec.Code)
	}
	list := decode[[]core.CommentThread](t, rec)
	if len(list) != 1 || list[0].ID != thread.ID {
		t.Fatalf("线程列表 = %+v，期望 1 条", list)
	}

	rec = do(t, router, http.MethodPost, "/api/v2/threads/"+thread.ID+"/comments",
		map[string]any{"content": "已修"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("回复状态码 = %d，期望 201（body=%s）", rec.Code, rec.Body.String())
	}

	rec = do(t, router, http.MethodPut, "/api/v2/threads/"+thread.ID,
		map[string]any{"x": 99.5, "y": 100})
	if rec.Code != http.StatusOK {
		t.Fatalf("移动线程状态码 = %d，期望 200（body=%s）", rec.Code, rec.Body.String())
	}
	if moved := decode[core.CommentThread](t, rec); moved.X != 99.5 || moved.Y != 100 {
		t.Fatalf("移动后坐标 = (%v, %v)，期望 (99.5, 100)", moved.X, moved.Y)
	}

	rec = do(t, router, http.MethodPost, "/api/v2/threads/"+thread.ID+"/resolve", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("resolve 状态码 = %d，期望 200（body=%s）", rec.Code, rec.Body.String())
	}
	if resolved := decode[core.CommentThread](t, rec); !resolved.Resolved {
		t.Fatalf("resolve 后 resolved = false，期望 true")
	}

	rec = do(t, router, http.MethodPost, "/api/v2/threads/"+thread.ID+"/reopen", nil)
	if resolved := decode[core.CommentThread](t, rec); resolved.Resolved {
		t.Fatalf("reopen 后 resolved = true，期望 false")
	}

	rec = do(t, router, http.MethodDelete, "/api/v2/threads/"+thread.ID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("删除状态码 = %d，期望 200", rec.Code)
	}
	if rec.Body.String() == "" || !bytes.Contains(rec.Body.Bytes(), []byte(`"success":true`)) {
		t.Fatalf("删除响应 = %s，期望 {\"success\":true}", rec.Body.String())
	}
}

// TestNotificationEndpoints 校验 @提及后被提及者能通过通知接口取到未读。
func TestNotificationEndpoints(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	const alice = "user-alice"
	sceneID, workspaceID := newScene(t, store, alice)

	bob := core.MemberUser{ID: "user-bob", Email: "bob@example.com"}
	if err := store.UpsertUserProfile(ctx, bob); err != nil {
		t.Fatalf("UpsertUserProfile() 错误 = %v", err)
	}
	if _, err := store.InviteMember(ctx, alice, workspaceID, bob.Email, core.RoleMember); err != nil {
		t.Fatalf("InviteMember() 错误 = %v", err)
	}

	aliceRouter := newTestRouter(t, store, alice, "alice@example.com")
	rec := do(t, aliceRouter, http.MethodPost, "/api/v2/scenes/"+sceneID+"/threads",
		map[string]any{"x": 1, "y": 2, "content": "@[Bob](user-bob) 看下", "mentions": []string{bob.ID}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("创建线程状态码 = %d（body=%s）", rec.Code, rec.Body.String())
	}

	bobRouter := newTestRouter(t, store, bob.ID, bob.Email)
	rec = do(t, bobRouter, http.MethodGet, "/api/v2/notifications/unread-count", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("未读数状态码 = %d，期望 200", rec.Code)
	}
	if count := decode[map[string]int](t, rec); count["count"] != 1 {
		t.Fatalf("未读数 = %v，期望 1", count)
	}

	rec = do(t, bobRouter, http.MethodGet, "/api/v2/notifications", nil)
	resp := decode[core.NotificationsResponse](t, rec)
	if len(resp.Notifications) != 1 {
		t.Fatalf("通知数 = %d，期望 1（body=%s）", len(resp.Notifications), rec.Body.String())
	}
	if resp.HasMore {
		t.Fatal("hasMore = true，期望 false")
	}
	notification := resp.Notifications[0]
	if notification.Type != core.NotificationMention || notification.Scene.ID != sceneID {
		t.Fatalf("通知 = %+v，期望 MENTION 且指向该场景", notification)
	}

	rec = do(t, bobRouter, http.MethodPost, "/api/v2/notifications/"+notification.ID+"/read", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("标记已读状态码 = %d，期望 200（body=%s）", rec.Code, rec.Body.String())
	}
	rec = do(t, bobRouter, http.MethodGet, "/api/v2/notifications/unread-count", nil)
	if count := decode[map[string]int](t, rec); count["count"] != 0 {
		t.Fatalf("标记已读后未读数 = %v，期望 0", count)
	}

	rec = do(t, bobRouter, http.MethodPost, "/api/v2/notifications/read-all", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("全部已读状态码 = %d，期望 200", rec.Code)
	}
}

// TestUnauthenticatedRejected 无 JWT claims 时评论接口应返回 401。
func TestUnauthenticatedRejected(t *testing.T) {
	store := newTestStore(t)
	router := newTestRouter(t, store, "", "")

	rec := do(t, router, http.MethodGet, "/api/v2/scenes/whatever/threads", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("未登录列举线程状态码 = %d，期望 401", rec.Code)
	}
	rec = do(t, router, http.MethodGet, "/api/v2/notifications/unread-count", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("未登录取未读数状态码 = %d，期望 401", rec.Code)
	}
}

// TestForbiddenForOutsider 非成员访问他人场景评论应 403。
func TestForbiddenForOutsider(t *testing.T) {
	store := newTestStore(t)
	sceneID, _ := newScene(t, store, "user-alice")
	router := newTestRouter(t, store, "user-carol", "carol@example.com")

	rec := do(t, router, http.MethodGet, "/api/v2/scenes/"+sceneID+"/threads", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("非成员列举状态码 = %d，期望 403（body=%s）", rec.Code, rec.Body.String())
	}
}
