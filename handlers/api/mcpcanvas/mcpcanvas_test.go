package mcpcanvas

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("MCP_CANVAS_ID", "")
	t.Setenv("MCP_CANVAS_USER_ID", "test-user")
	store, err := NewStore(filepath.Join(t.TempDir(), "excalidraw.db"))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return store
}

func TestNewStoreCreatesDefaultCanvasAndAcceptsFirstElement(t *testing.T) {
	store := newTestStore(t)

	if got := store.CurrentCanvasID(); got != DefaultCanvasID {
		t.Fatalf("CurrentCanvasID() = %q, want %q", got, DefaultCanvasID)
	}
	store.mu.RLock()
	state := store.canvases[DefaultCanvasID]
	store.mu.RUnlock()
	if state == nil {
		t.Fatal("全新数据库没有创建默认画布 state")
	}

	var rowCount int
	if err := store.db.QueryRow(
		`SELECT COUNT(*) FROM canvases WHERE user_id = ? AND id = ?`,
		store.userID,
		DefaultCanvasID,
	).Scan(&rowCount); err != nil {
		t.Fatalf("query default canvas: %v", err)
	}
	if rowCount != 1 {
		t.Fatalf("default canvas rows = %d, want 1", rowCount)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/elements/", strings.NewReader(`{
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

func TestCreatePersistenceFailureReleasesLockAndRollsBack(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.db.Exec(`DROP TABLE canvases`); err != nil {
		t.Fatalf("drop canvases table: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/elements/", strings.NewReader(`{
		"id":"will-fail","type":"rectangle","x":0,"y":0,"width":100,"height":60
	}`))
	store.handleCreate(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("create status = %d, want 500; body = %s", recorder.Code, recorder.Body.String())
	}

	done := make(chan struct{})
	go func() {
		_ = store.CurrentCanvasID()
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
	otherCanvasID, err := store.CreateCanvas()
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

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("websocket dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	initial := readWSEvent(t, conn)
	if initial.Type != "initial_elements" || initial.CanvasID != otherCanvasID {
		t.Fatalf("initial event = %#v", initial)
	}

	targetQuery := "?canvasId=" + DefaultCanvasID
	requestJSON(t, server.Client(), http.MethodPost, server.URL+"/api/elements/"+targetQuery, map[string]interface{}{
		"id": "shape", "type": "rectangle", "x": 0, "y": 0, "width": 120, "height": 80,
		"label": map[string]interface{}{"text": "中文标签"},
	})
	created := readWSEvent(t, conn)
	createdIDs := idsOf(created.Elements)
	if created.Type != "element_created" || created.CanvasID != DefaultCanvasID {
		t.Fatalf("created event = %#v", created)
	}
	if !createdIDs["shape"] || !createdIDs["shape-label"] {
		t.Fatalf("created element ids = %#v, want shape and shape-label", createdIDs)
	}
	if created.Element["id"] != "shape" {
		t.Fatalf("legacy element id = %v, want shape", created.Element["id"])
	}

	store.mu.RLock()
	_, inTarget := store.canvases[DefaultCanvasID].elements["shape"]
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
	if deleted.Type != "element_deleted" || deleted.CanvasID != DefaultCanvasID {
		t.Fatalf("deleted event = %#v", deleted)
	}
	if deleted.ElementID != "shape" ||
		!containsString(deleted.ElementIDs, "shape") ||
		!containsString(deleted.ElementIDs, "shape-label") {
		t.Fatalf("deleted ids = %#v, legacy id = %q", deleted.ElementIDs, deleted.ElementID)
	}
}
