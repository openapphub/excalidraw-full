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

func TestOrdinaryWorkspaceSceneSupportsIncrementalCRUDAndWebSocket(t *testing.T) {
	store, host, collection := newHostedCanvasStore(t)
	collectionID := collection.ID
	scene, err := host.CreateScene(context.Background(), "mcp-owner", "普通画布", nil,
		[]byte(`{"elements":[],"appState":{"viewBackgroundColor":"#ffffff"},"files":{}}`), &collectionID)
	if err != nil {
		t.Fatal(err)
	}
	const clientID = "browser-tab"
	if _, err := host.AcquireSceneLock(context.Background(), "mcp-owner", scene.ID, clientID, "浏览器"); err != nil {
		t.Fatal(err)
	}

	store.mu.RLock()
	_, hasDurableTarget := store.targets[scene.ID]
	store.mu.RUnlock()
	if hasDurableTarget {
		t.Fatal("普通 Scene 不应写入 mcp_canvas_targets")
	}

	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := &auth.AppClaims{RegisteredClaims: jwt.RegisteredClaims{Subject: "mcp-owner"}}
			ctx := context.WithValue(r.Context(), appmiddleware.ClaimsContextKey, claims)
			r = r.WithContext(ctx)
			if r.Header.Get("X-Scene-Client-ID") == "" {
				r.Header.Set("X-Scene-Client-ID", clientID)
			}
			next.ServeHTTP(w, r)
		})
	})
	router.Route("/api/elements", func(r chi.Router) {
		Routes(r, store)
	})
	router.Get("/ws", store.ServeWS)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?canvasId=" + scene.ID
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("普通 Scene websocket dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	initial := readWSEvent(t, conn)
	if initial.Type != "initial_elements" || initial.CanvasID != scene.ID || len(initial.Elements) != 0 {
		t.Fatalf("普通 Scene initial event = %#v", initial)
	}

	targetQuery := "?canvasId=" + scene.ID
	requestJSON(t, server.Client(), http.MethodPost, server.URL+"/api/elements/"+targetQuery, map[string]interface{}{
		"id": "shape", "type": "rectangle", "x": 20, "y": 30, "width": 160, "height": 80,
		"label": map[string]interface{}{"text": "普通 Scene"},
	})
	created := readWSEvent(t, conn)
	if created.Type != "element_created" || created.CanvasID != scene.ID || !idsOf(created.Elements)["shape"] {
		t.Fatalf("普通 Scene created event = %#v", created)
	}

	requestJSON(t, server.Client(), http.MethodPut, server.URL+"/api/elements/shape"+targetQuery, map[string]interface{}{
		"backgroundColor": "#a5d8ff",
	})
	updated := readWSEvent(t, conn)
	if updated.Type != "element_updated" || updated.Element["backgroundColor"] != "#a5d8ff" {
		t.Fatalf("普通 Scene updated event = %#v", updated)
	}

	data, err := host.GetSceneData(context.Background(), "mcp-owner", scene.ID)
	if err != nil {
		t.Fatal(err)
	}
	var persisted struct {
		Elements []map[string]interface{} `json:"elements"`
		AppState map[string]interface{}   `json:"appState"`
	}
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if !idsOf(persisted.Elements)["shape"] || persisted.AppState["viewBackgroundColor"] != "#ffffff" {
		t.Fatalf("普通 Scene 持久化内容 = %#v", persisted)
	}

	requestJSON(t, server.Client(), http.MethodDelete, server.URL+"/api/elements/shape"+targetQuery, nil)
	deleted := readWSEvent(t, conn)
	if deleted.Type != "element_deleted" || deleted.CanvasID != scene.ID || !containsString(deleted.ElementIDs, "shape") {
		t.Fatalf("普通 Scene deleted event = %#v", deleted)
	}
	data, err = host.GetSceneData(context.Background(), "mcp-owner", scene.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if len(persisted.Elements) != 0 {
		t.Fatalf("普通 Scene 删除后 elements = %#v", persisted.Elements)
	}
}

func TestOrdinaryWorkspaceSceneIncrementalWritePreservesNativeElements(t *testing.T) {
	store, host, collection := newHostedCanvasStore(t)
	collectionID := collection.ID
	initial := map[string]interface{}{
		"elements": []map[string]interface{}{
			{
				"id": "existing-shape", "type": "rectangle", "x": 10, "y": 20, "width": 220, "height": 120,
				"angle": 0.25, "strokeColor": "#c92a2a", "backgroundColor": "#fff3bf", "fillStyle": "hachure",
				"strokeWidth": 4, "strokeStyle": "dashed", "roughness": 2, "opacity": 73,
				"groupIds": []interface{}{"group-a"}, "frameId": "frame-a", "roundness": map[string]interface{}{"type": 2},
				"seed": 123, "version": 7, "versionNonce": 456, "isDeleted": false,
				"boundElements": []interface{}{map[string]interface{}{"type": "text", "id": "custom-label"}},
				"updated":       111, "link": "https://example.test", "locked": true,
				"customData": map[string]interface{}{"owner": "human", "nested": map[string]interface{}{"keep": true}},
			},
			{
				"id": "custom-label", "type": "text", "x": 55, "y": 62, "width": 130, "height": 35,
				"angle": 0.25, "strokeColor": "#862e9c", "backgroundColor": "transparent", "fillStyle": "solid",
				"strokeWidth": 1, "strokeStyle": "solid", "roughness": 0, "opacity": 81,
				"groupIds": []interface{}{"group-a"}, "frameId": "frame-a", "roundness": nil,
				"seed": 789, "version": 9, "versionNonce": 987, "isDeleted": false,
				"boundElements": nil, "updated": 222, "link": nil, "locked": true,
				"text": "人工标签", "originalText": "人工标签", "fontSize": 28, "fontFamily": 5,
				"textAlign": "center", "verticalAlign": "middle", "autoResize": true, "lineHeight": 1.25,
				"containerId": "existing-shape",
			},
			{
				"id": "existing-arrow", "type": "arrow", "x": 300, "y": 80, "width": 120, "height": 40,
				"points":       []interface{}{[]interface{}{0, 0}, []interface{}{50, 40}, []interface{}{120, 0}},
				"startBinding": map[string]interface{}{"elementId": "existing-shape", "focus": 0.35, "gap": 11},
				"endBinding":   nil, "startArrowhead": nil, "endArrowhead": "triangle",
				"strokeColor": "#1971c2", "backgroundColor": "transparent", "fillStyle": "solid",
				"strokeWidth": 3, "strokeStyle": "dotted", "roughness": 1, "opacity": 66,
				"groupIds": []interface{}{}, "frameId": nil, "roundness": map[string]interface{}{"type": 2},
				"seed": 321, "version": 5, "versionNonce": 654, "isDeleted": false,
				"boundElements": nil, "updated": 333, "link": nil, "locked": false,
			},
		},
		"appState": map[string]interface{}{"viewBackgroundColor": "#f8f9fa"},
		"files":    map[string]interface{}{},
		"type":     "excalidraw",
		"version":  2,
		"source":   "https://excalidraw.com",
	}
	initialData, err := json.Marshal(initial)
	if err != nil {
		t.Fatal(err)
	}
	scene, err := host.CreateScene(context.Background(), "mcp-owner", "保真画布", nil, initialData, &collectionID)
	if err != nil {
		t.Fatal(err)
	}
	const clientID = "browser-tab"
	if _, err := host.AcquireSceneLock(context.Background(), "mcp-owner", scene.ID, clientID, "浏览器"); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/elements/?canvasId="+scene.ID, strings.NewReader(`{
		"id":"mcp-added","type":"ellipse","x":500,"y":100,"width":120,"height":80
	}`))
	store.handleCreate(recorder, requestWithActor(request, "mcp-owner", clientID))
	if recorder.Code != http.StatusOK {
		t.Fatalf("增量创建状态码 = %d，响应 = %s", recorder.Code, recorder.Body.String())
	}

	persistedData, err := host.GetSceneData(context.Background(), "mcp-owner", scene.ID)
	if err != nil {
		t.Fatal(err)
	}
	var persisted struct {
		Elements []map[string]interface{} `json:"elements"`
		Type     string                   `json:"type"`
		Version  int                      `json:"version"`
		Source   string                   `json:"source"`
	}
	if err := json.Unmarshal(persistedData, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Type != "excalidraw" || persisted.Version != 2 || persisted.Source != "https://excalidraw.com" {
		t.Fatalf("Scene 顶层元数据被重置：%#v", persisted)
	}
	byID := make(map[string]map[string]interface{}, len(persisted.Elements))
	for _, element := range persisted.Elements {
		id, _ := element["id"].(string)
		byID[id] = element
	}
	shape := byID["existing-shape"]
	if shape == nil || shape["fillStyle"] != "hachure" || shape["locked"] != true || shape["version"] != float64(7) {
		t.Fatalf("原生形状字段被重置：%#v", shape)
	}
	for field, want := range map[string]string{
		"groupIds":   `["group-a"]`,
		"roundness":  `{"type":2}`,
		"customData": `{"nested":{"keep":true},"owner":"human"}`,
	} {
		got, err := json.Marshal(shape[field])
		if err != nil || string(got) != want {
			t.Fatalf("形状字段 %s = %s，期望 %s", field, got, want)
		}
	}
	label := byID["custom-label"]
	if label == nil || label["fontSize"] != float64(28) || label["fontFamily"] != float64(5) || label["strokeColor"] != "#862e9c" || label["locked"] != true {
		t.Fatalf("绑定文字字段被重置：%#v", label)
	}
	arrow := byID["existing-arrow"]
	points, err := json.Marshal(arrow["points"])
	if err != nil || string(points) != `[[0,0],[50,40],[120,0]]` {
		t.Fatalf("已有箭头几何被重算：%s", points)
	}
	startBinding, err := json.Marshal(arrow["startBinding"])
	if err != nil || string(startBinding) != `{"elementId":"existing-shape","focus":0.35,"gap":11}` {
		t.Fatalf("已有箭头绑定被重置：%s", startBinding)
	}
	if byID["mcp-added"] == nil {
		t.Fatal("新增元素未持久化")
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
