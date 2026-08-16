package mcpcanvas

import (
	"bytes"
	"context"
	"encoding/json"
	"excalidraw-complete/core"
	"excalidraw-complete/handlers/auth"
	appmiddleware "excalidraw-complete/middleware"
	"excalidraw-complete/stores"
	sqlitestore "excalidraw-complete/stores/sqlite"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "excalidraw.db"))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	if _, err := store.db.Exec(`CREATE TABLE shell_collections (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, user_id TEXT NOT NULL)`); err != nil {
		t.Fatalf("create shell_collections: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO shell_collections (id, workspace_id, user_id) VALUES ('collection-a', 'workspace-a', 'test-user')`); err != nil {
		t.Fatalf("insert collection: %v", err)
	}
	if _, err := store.CreateCanvas("collection-a"); err != nil {
		t.Fatalf("CreateCanvas() error = %v", err)
	}
	return store
}

func testCanvasID(t *testing.T, store *Store) string {
	t.Helper()
	store.mu.RLock()
	defer store.mu.RUnlock()
	for id := range store.targets {
		return id
	}
	t.Fatal("test store has no Workspace-bound canvas")
	return ""
}

func newHostedCanvasStore(t *testing.T) (*Store, stores.Store, *core.Collection) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "hosted-mcp.db")
	host := sqlitestore.NewStore(dsn)
	workspace, err := host.CreateShellWorkspace(context.Background(), "mcp-owner", "MCP 团队", "", core.WorkspaceShared)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := host.CreateCollection(context.Background(), "mcp-owner", workspace.ID, "AI 画布", nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(dsn, host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return store, host, collection
}

func requestWithActor(request *http.Request, userID, clientID string) *http.Request {
	claims := &auth.AppClaims{RegisteredClaims: jwt.RegisteredClaims{Subject: userID}}
	ctx := context.WithValue(request.Context(), appmiddleware.ClaimsContextKey, claims)
	request = request.WithContext(ctx)
	request.Header.Set("X-Scene-Client-ID", clientID)
	return request
}

func TestCreateCanvasBindsWorkspaceAndAcceptsFirstElement(t *testing.T) {
	store := newTestStore(t)
	canvasID := testCanvasID(t, store)
	store.mu.RLock()
	state := store.canvases[canvasID]
	target := store.targets[canvasID]
	store.mu.RUnlock()
	if state == nil {
		t.Fatal("新建画布没有 state")
	}
	if target.OwnerUserID != "test-user" || target.WorkspaceID != "workspace-a" || target.CollectionID != "collection-a" {
		t.Fatalf("target = %#v", target)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/elements/?canvasId="+canvasID, strings.NewReader(`{
		"id":"first","type":"rectangle","x":0,"y":0,"width":100,"height":60
	}`))
	store.handleCreate(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("first create status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := store.Count(); got != 1 {
		t.Fatalf("Count() = %d, want 1", got)
	}
}

func TestHostedCreateCanvasRollsBackBindingWhenSceneInsertFails(t *testing.T) {
	store, _, collection := newHostedCanvasStore(t)
	if _, err := store.db.Exec(`CREATE TRIGGER fail_mcp_scene_insert
		BEFORE INSERT ON canvases
		WHEN NEW.id LIKE 'ai-%'
		BEGIN
			SELECT RAISE(ABORT, 'injected MCP scene failure');
		END`); err != nil {
		t.Fatal(err)
	}

	if _, err := store.CreateCanvas("mcp-owner", collection.ID); err == nil {
		t.Fatal("注入 Scene 插入失败后 CreateCanvas() 仍成功")
	}
	var targets, canvases int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM mcp_canvas_targets`).Scan(&targets); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM canvases WHERE id LIKE 'ai-%'`).Scan(&canvases); err != nil {
		t.Fatal(err)
	}
	if targets != 0 || canvases != 0 {
		t.Fatalf("回滚后 targets=%d、canvases=%d，期望均为 0", targets, canvases)
	}
	store.mu.RLock()
	inMemoryTargets := len(store.targets)
	store.mu.RUnlock()
	if inMemoryTargets != 0 {
		t.Fatalf("回滚后内存 target 数=%d，期望 0", inMemoryTargets)
	}
}

func TestMCPWriteLockConflictReturns409AndRollsBackMemory(t *testing.T) {
	store, host, collection := newHostedCanvasStore(t)
	canvasID, err := store.CreateCanvas("mcp-owner", collection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.AcquireSceneLock(context.Background(), "mcp-owner", canvasID, "client-a", "客户端 A"); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/elements/?canvasId="+canvasID, strings.NewReader(`{
		"id":"blocked","type":"rectangle","x":0,"y":0,"width":100,"height":60
	}`))
	store.handleCreate(recorder, requestWithActor(request, "mcp-owner", "client-b"))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("锁冲突状态码 = %d，期望 409；响应 = %s", recorder.Code, recorder.Body.String())
	}
	if got := store.Count(); got != 0 {
		t.Fatalf("锁冲突后内存元素数 = %d，期望 0", got)
	}
}

func TestMCPWebSocketStopsBroadcastAfterMemberRemoval(t *testing.T) {
	store, host, collection := newHostedCanvasStore(t)
	const guestID = "mcp-guest"
	link, err := host.CreateInviteLink(context.Background(), "mcp-owner", collection.WorkspaceID, core.RoleMember, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.JoinViaInviteLink(context.Background(), guestID, link.Code, core.MemberUser{ID: guestID}); err != nil {
		t.Fatal(err)
	}
	canvasID, err := store.CreateCanvas("mcp-owner", collection.ID)
	if err != nil {
		t.Fatal(err)
	}

	router := chi.NewRouter()
	router.Get("/ws", func(w http.ResponseWriter, r *http.Request) {
		claims := &auth.AppClaims{RegisteredClaims: jwt.RegisteredClaims{Subject: r.URL.Query().Get("userId")}}
		ctx := context.WithValue(r.Context(), appmiddleware.ClaimsContextKey, claims)
		store.ServeWS(w, r.WithContext(ctx))
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?canvasId=" + canvasID + "&userId=" + guestID
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	initial := readWSEvent(t, conn)
	if initial.Type != "initial_elements" {
		t.Fatalf("初始事件 = %#v", initial)
	}

	if err := host.RemoveMember(context.Background(), "mcp-owner", collection.WorkspaceID, guestID); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/elements/?canvasId="+canvasID, strings.NewReader(`{
		"id":"after-revoke","type":"rectangle","x":0,"y":0,"width":100,"height":60
	}`))
	store.handleCreate(recorder, requestWithActor(request, "mcp-owner", "owner-client"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("所有者写入状态码 = %d，响应 = %s", recorder.Code, recorder.Body.String())
	}
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var event wsEvent
	if err := conn.ReadJSON(&event); err == nil {
		t.Fatalf("成员移除后旧 WebSocket 仍收到广播：%#v", event)
	}
}

func TestMCPBindingFollowsSceneMoveAndCollectionDelete(t *testing.T) {
	store, host, sourceCollection := newHostedCanvasStore(t)
	ctx := context.Background()
	canvasID, err := store.CreateCanvas("mcp-owner", sourceCollection.ID)
	if err != nil {
		t.Fatal(err)
	}
	targetWorkspace, err := host.CreateShellWorkspace(ctx, "mcp-owner", "目标空间", "", core.WorkspaceShared)
	if err != nil {
		t.Fatal(err)
	}
	targetCollection, err := host.CreateCollection(ctx, "mcp-owner", targetWorkspace.ID, "目标集合", nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO mcp_canvas_snapshots (canvas_id, name, elements, created_at) VALUES (?, 'before-move', '[]', ?)`, canvasID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := host.MoveScene(ctx, "mcp-owner", canvasID, &targetCollection.ID); err != nil {
		t.Fatal(err)
	}

	var workspaceID, collectionID string
	if err := store.db.QueryRow(`SELECT workspace_id, collection_id FROM mcp_canvas_targets WHERE canvas_id = ?`, canvasID).Scan(&workspaceID, &collectionID); err != nil {
		t.Fatal(err)
	}
	if workspaceID != targetWorkspace.ID || collectionID != targetCollection.ID {
		t.Fatalf("移动后 MCP 绑定 workspace=%q、collection=%q", workspaceID, collectionID)
	}
	listed := store.ListCanvases("mcp-owner")
	if len(listed) != 1 || listed[0]["workspaceId"] != targetWorkspace.ID || listed[0]["collectionId"] != targetCollection.ID {
		t.Fatalf("移动后 MCP 列表 = %#v", listed)
	}

	if err := host.DeleteCollection(ctx, "mcp-owner", targetCollection.ID); err != nil {
		t.Fatal(err)
	}
	var targets, snapshots int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM mcp_canvas_targets WHERE canvas_id = ?`, canvasID).Scan(&targets); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM mcp_canvas_snapshots WHERE canvas_id = ?`, canvasID).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if targets != 0 || snapshots != 0 {
		t.Fatalf("删除集合后 targets=%d、snapshots=%d，期望均为 0", targets, snapshots)
	}
	if listed := store.ListCanvases("mcp-owner"); len(listed) != 0 {
		t.Fatalf("删除集合后 MCP 列表仍可见：%#v", listed)
	}
}

func TestCreatePersistenceFailureReleasesLockAndRollsBack(t *testing.T) {
	store := newTestStore(t)
	canvasID := testCanvasID(t, store)
	if _, err := store.db.Exec(`DROP TABLE canvases`); err != nil {
		t.Fatalf("drop canvases table: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/elements/?canvasId="+canvasID, strings.NewReader(`{
		"id":"will-fail","type":"rectangle","x":0,"y":0,"width":100,"height":60
	}`))
	store.handleCreate(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("create status = %d, want 500; body = %s", recorder.Code, recorder.Body.String())
	}

	done := make(chan struct{})
	go func() {
		_ = store.Count()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("持久化失败后 Store 锁未释放")
	}
	if got := store.Count(); got != 0 {
		t.Fatalf("Count() after rollback = %d, want 0", got)
	}
}

type wsEvent struct {
	Type              string                   `json:"type"`
	CanvasID          string                   `json:"canvasId"`
	Element           map[string]interface{}   `json:"element"`
	Elements          []map[string]interface{} `json:"elements"`
	ElementID         string                   `json:"elementId"`
	ElementIDs        []string                 `json:"elementIds"`
	RemovedElementIDs []string                 `json:"removedElementIds"`
}

func readWSEvent(t *testing.T, conn *websocket.Conn) wsEvent {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}
	var event wsEvent
	if err := conn.ReadJSON(&event); err != nil {
		t.Fatalf("ReadJSON() error = %v", err)
	}
	return event
}

func requestJSON(t *testing.T, client *http.Client, method, url string, body interface{}) {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	request, err := http.NewRequest(method, url, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("client.Do() error = %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("%s %s status = %d", method, url, response.StatusCode)
	}
}

func idsOf(elements []map[string]interface{}) map[string]bool {
	ids := make(map[string]bool, len(elements))
	for _, element := range elements {
		if id, ok := element["id"].(string); ok {
			ids[id] = true
		}
	}
	return ids
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestElementEventsIncludeBoundTextAndStableCanvasID(t *testing.T) {
	store := newTestStore(t)
	canvasID := testCanvasID(t, store)
	otherCanvasID, err := store.CreateCanvas("collection-a")
	if err != nil {
		t.Fatalf("CreateCanvas() error = %v", err)
	}

	router := chi.NewRouter()
	router.Route("/api/elements", func(r chi.Router) {
		Routes(r, store)
	})
	router.Get("/ws", store.ServeWS)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?canvasId=" + canvasID
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("websocket dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	initial := readWSEvent(t, conn)
	if initial.Type != "initial_elements" || initial.CanvasID != canvasID {
		t.Fatalf("initial event = %#v", initial)
	}

	targetQuery := "?canvasId=" + canvasID
	requestJSON(t, server.Client(), http.MethodPost, server.URL+"/api/elements/"+targetQuery, map[string]interface{}{
		"id": "shape", "type": "rectangle", "x": 0, "y": 0, "width": 120, "height": 80,
		"label": map[string]interface{}{"text": "中文标签"},
	})
	created := readWSEvent(t, conn)
	createdIDs := idsOf(created.Elements)
	if created.Type != "element_created" || created.CanvasID != canvasID {
		t.Fatalf("created event = %#v", created)
	}
	if !createdIDs["shape"] || !createdIDs["shape-label"] {
		t.Fatalf("created element ids = %#v, want shape and shape-label", createdIDs)
	}
	if created.Element["id"] != "shape" {
		t.Fatalf("legacy element id = %v, want shape", created.Element["id"])
	}

	store.mu.RLock()
	_, inTarget := store.canvases[canvasID].elements["shape"]
	_, inCurrent := store.canvases[otherCanvasID].elements["shape"]
	store.mu.RUnlock()
	if !inTarget || inCurrent {
		t.Fatalf("显式 canvasId 写入错误：target=%v current=%v", inTarget, inCurrent)
	}

	requestJSON(t, server.Client(), http.MethodPut, server.URL+"/api/elements/shape"+targetQuery, map[string]interface{}{
		"label": map[string]interface{}{"text": "更新标签"},
	})
	updated := readWSEvent(t, conn)
	updatedIDs := idsOf(updated.Elements)
	if updated.Type != "element_updated" || !updatedIDs["shape"] || !updatedIDs["shape-label"] {
		t.Fatalf("updated event = %#v", updated)
	}

	requestJSON(t, server.Client(), http.MethodPut, server.URL+"/api/elements/shape"+targetQuery, map[string]interface{}{
		"label": map[string]interface{}{"text": ""},
	})
	removedLabel := readWSEvent(t, conn)
	if !containsString(removedLabel.RemovedElementIDs, "shape-label") {
		t.Fatalf("removedElementIds = %#v, want shape-label", removedLabel.RemovedElementIDs)
	}

	requestJSON(t, server.Client(), http.MethodPut, server.URL+"/api/elements/shape"+targetQuery, map[string]interface{}{
		"label": map[string]interface{}{"text": "再次添加"},
	})
	_ = readWSEvent(t, conn)

	requestJSON(t, server.Client(), http.MethodDelete, server.URL+"/api/elements/shape"+targetQuery, nil)
	deleted := readWSEvent(t, conn)
	if deleted.Type != "element_deleted" || deleted.CanvasID != canvasID {
		t.Fatalf("deleted event = %#v", deleted)
	}
	if deleted.ElementID != "shape" ||
		!containsString(deleted.ElementIDs, "shape") ||
		!containsString(deleted.ElementIDs, "shape-label") {
		t.Fatalf("deleted ids = %#v, legacy id = %q", deleted.ElementIDs, deleted.ElementID)
	}
}
