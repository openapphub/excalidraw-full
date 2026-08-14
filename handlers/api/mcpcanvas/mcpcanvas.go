package mcpcanvas

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
	"github.com/sirupsen/logrus"
	_ "modernc.org/sqlite"
)

// ServiceName must match the identity the mcp-excalidraw CLI checks in /health.
const ServiceName = "mcp-excalidraw-canvas"

// AI canvas name prefix: every canvas the mcp API manages lives in the host
// app's `canvases` table with this id prefix, so they all appear in the UI.
const AICanvasPrefix = "ai-"

// DefaultCanvasID is the initial canvas used when no canvas has been selected
// yet. Override via MCP_CANVAS_ID.
const DefaultCanvasID = AICanvasPrefix + "canvas"

// canvasState is the in-memory element list for one AI canvas.
type canvasState struct {
	elements map[string]map[string]interface{} // id -> element (agent format)
	order    []string                          // insertion order
}

// Store keeps multiple AI canvases in memory (each as canvasState) and
// persists every mutation to the host app's `canvases` table (native
// Excalidraw format). Element endpoints operate on the *current* canvas;
// /api/canvas switches or creates one. WebSocket broadcasts carry a
// canvasId so the frontend can filter.
type Store struct {
	mu       sync.RWMutex
	canvases map[string]*canvasState // canvasID -> state
	current  string                  // current canvas id
	db       *sql.DB
	userID   string
	hub      *Hub
}

func NewStore(dsn string) (*Store, error) {
	if dsn == "" {
		dsn = os.Getenv("DATA_SOURCE_NAME")
	}
	if dsn == "" {
		dsn = "excalidraw.db"
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	dsn = dsn + sep + "_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	userID := os.Getenv("MCP_CANVAS_USER_ID")
	if userID == "" {
		userID = "github:175949671"
	}
	currentCanvasID := strings.TrimSpace(os.Getenv("MCP_CANVAS_ID"))
	if currentCanvasID == "" {
		currentCanvasID = DefaultCanvasID
	}
	s := &Store{
		canvases: map[string]*canvasState{},
		current:  currentCanvasID,
		db:       db,
		userID:   userID,
		hub:      NewHub(),
	}
	if err := s.initDB(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.load(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) initDB() error {
	// mcpcanvas 可能在主存储使用 memory 时独立打开 SQLite，因此必须自行建表。
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS canvases (
		id TEXT NOT NULL,
		user_id TEXT NOT NULL,
		name TEXT,
		thumbnail TEXT,
		data BLOB,
		created_at DATETIME,
		updated_at DATETIME,
		PRIMARY KEY (user_id, id)
	)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS mcp_snapshots (name TEXT PRIMARY KEY, elements BLOB, created_at DATETIME)`); err != nil {
		return err
	}
	return s.initFilesTable()
}

// load reads every AI-prefixed canvas from the host `canvases` table and
// converts their native elements back to agent format.
func (s *Store) load() error {
	rows, err := s.db.Query(`SELECT id, data FROM canvases WHERE user_id = ? AND id LIKE ?`, s.userID, AICanvasPrefix+"%")
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		var data []byte
		if err := rows.Scan(&id, &data); err != nil {
			continue
		}
		state, err := s.parseCanvas(data)
		if err != nil {
			logrus.WithError(err).WithField("canvas", id).Warn("mcpcanvas: failed to parse canvas, skipping")
			continue
		}
		s.canvases[id] = state
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, ok := s.canvases[s.current]; !ok {
		if len(s.canvases) > 0 && strings.TrimSpace(os.Getenv("MCP_CANVAS_ID")) == "" {
			// 未显式指定画布时，稳定选择已有画布，避免每次启动随机切换。
			ids := make([]string, 0, len(s.canvases))
			for id := range s.canvases {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			s.current = ids[0]
		} else {
			// 全新数据库也必须有可写 state，首次创建元素不能解引用 nil。
			s.canvases[s.current] = newCanvasState()
			if err := s.persistCanvasLocked(s.current, s.canvases[s.current]); err != nil {
				return err
			}
		}
	}
	logrus.WithFields(logrus.Fields{"canvases": len(s.canvases), "current": s.current}).Info("mcpcanvas: loaded")
	return nil
}

func (s *Store) parseCanvas(data []byte) (*canvasState, error) {
	var scene struct {
		Elements []map[string]interface{} `json:"elements"`
	}
	if err := json.Unmarshal(data, &scene); err != nil {
		return nil, err
	}
	return nativeListToAgentState(scene.Elements), nil
}

// nativeListToAgentState converts a native element list back to agent
// format, folding bound text into parent labels.
func nativeListToAgentState(elements []map[string]interface{}) *canvasState {
	state := &canvasState{elements: map[string]map[string]interface{}{}}
	agents := make([]map[string]interface{}, 0, len(elements))
	boundText := map[string]string{} // textElementId -> containerId
	for _, e := range elements {
		typ, _ := e["type"].(string)
		if typ == "text" {
			if cid, ok := e["containerId"].(string); ok && cid != "" {
				if id, ok := e["id"].(string); ok && id != "" {
					boundText[id] = cid
				}
				continue
			}
		}
		agents = append(agents, nativeToAgent(e))
	}
	for textID, containerID := range boundText {
		for _, a := range agents {
			if a["id"] == containerID {
				for _, e := range elements {
					if e["id"] == textID {
						if t, ok := e["text"].(string); ok {
							a["label"] = map[string]interface{}{"text": t}
						}
						break
					}
				}
				break
			}
		}
	}
	for _, a := range agents {
		id, _ := a["id"].(string)
		if id == "" {
			continue
		}
		state.elements[id] = a
		state.order = append(state.order, id)
	}
	fmt.Printf("[mcpcanvas] load: %d elements, %d boundText, agents=%d\n", len(elements), len(boundText), len(agents))
	for _, a := range agents {
		if id, _ := a["id"].(string); id == "rt-note2" || id == "rt-start" {
			fmt.Printf("[mcpcanvas] load %s: text=%v label=%v\n", id, a["text"], a["label"])
		}
	}
	return state
}

// state returns the current canvas state (or nil). Caller must hold s.mu.
func (s *Store) state() *canvasState {
	return s.canvases[s.current]
}

func newCanvasState() *canvasState {
	return &canvasState{elements: map[string]map[string]interface{}{}}
}

// cloneCanvasState 为持久化失败准备内存回滚快照。调用方必须持有 s.mu。
func cloneCanvasState(st *canvasState) *canvasState {
	if st == nil {
		return nil
	}
	cloned := &canvasState{
		elements: make(map[string]map[string]interface{}, len(st.elements)),
		order:    append([]string(nil), st.order...),
	}
	for id, el := range st.elements {
		copyEl := make(map[string]interface{}, len(el))
		for key, value := range el {
			copyEl[key] = value
		}
		cloned.elements[id] = copyEl
	}
	return cloned
}

// persistCanvasLocked writes one canvas back to the host `canvases` table in
// native format. Caller must hold s.mu.
func (s *Store) persistCanvasLocked(canvasID string, st *canvasState) error {
	if st == nil {
		return fmt.Errorf("canvas %s has no state", canvasID)
	}
	native := make([]map[string]interface{}, 0, len(st.order)*2)
	for _, id := range st.order {
		el, textEl := agentToNative(st.elements[id])
		native = append(native, el)
		if textEl != nil {
			native = append(native, textEl)
		}
	}
	resolveArrowBindings(native)

	var existing struct {
		AppState map[string]interface{} `json:"appState"`
		Files    map[string]interface{} `json:"files"`
	}
	var oldData []byte
	if err := s.db.QueryRow(`SELECT data FROM canvases WHERE user_id = ? AND id = ?`, s.userID, canvasID).Scan(&oldData); err == nil {
		_ = json.Unmarshal(oldData, &existing)
	}
	if existing.AppState == nil {
		existing.AppState = map[string]interface{}{"viewBackgroundColor": "#ffffff"}
	}
	if existing.Files == nil {
		existing.Files = map[string]interface{}{}
	}
	scene := map[string]interface{}{
		"elements":  native,
		"appState":  existing.AppState,
		"files":     existing.Files,
		"thumbnail": "",
	}
	data, err := json.Marshal(scene)
	if err != nil {
		return err
	}
	name := canvasID
	if n, ok := existing.AppState["name"].(string); ok && n != "" {
		name = n
	}
	now := time.Now()
	// thumbnail must be '' (not NULL): the host app's List scans it into a
	// string and NULL crashes the whole canvas list.
	_, err = s.db.Exec(`INSERT INTO canvases (id, user_id, name, thumbnail, data, created_at, updated_at) VALUES (?, ?, ?, '', ?, ?, ?)
		ON CONFLICT(user_id, id) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
		canvasID, s.userID, name, data, now, now)
	return err
}

// --- element accessors (operate on current canvas) ---

func (s *Store) getAll() []map[string]interface{} {
	st := s.canvases[s.current]
	if st == nil {
		return []map[string]interface{}{}
	}
	out := make([]map[string]interface{}, 0, len(st.order))
	for _, id := range st.order {
		out = append(out, st.elements[id])
	}
	return out
}

func (s *Store) create(canvasID string, input map[string]interface{}) map[string]interface{} {
	st := s.canvases[canvasID]
	if st == nil {
		st = newCanvasState()
		s.canvases[canvasID] = st
	}
	id, _ := input["id"].(string)
	if id == "" {
		id = ulid.Make().String()
	}
	el := make(map[string]interface{}, len(input)+3)
	for k, v := range input {
		el[k] = v
	}
	el["id"] = id
	if _, ok := el["createdAt"]; !ok {
		el["createdAt"] = nowISO()
	}
	el["updatedAt"] = nowISO()
	if _, ok := el["version"]; !ok {
		el["version"] = float64(1)
	}
	if _, exists := st.elements[id]; !exists {
		st.order = append(st.order, id)
	}
	st.elements[id] = el
	fmt.Printf("[mcpcanvas] create id=%s type=%v text=%v label=%v\n", id, el["type"], el["text"], el["label"])
	return el
}

func (s *Store) update(canvasID, id string, patch map[string]interface{}) (map[string]interface{}, bool) {
	st := s.canvases[canvasID]
	if st == nil {
		return nil, false
	}
	el, ok := st.elements[id]
	if !ok {
		return nil, false
	}
	for k, v := range patch {
		if k == "id" {
			continue
		}
		el[k] = v
	}
	el["updatedAt"] = nowISO()
	ver, _ := el["version"].(float64)
	el["version"] = ver + 1
	fmt.Printf("[mcpcanvas] update id=%s patch_text=%v el_text=%v el_label=%v\n", id, patch["text"], el["text"], el["label"])
	return el, true
}

func (s *Store) remove(canvasID, id string) bool {
	st := s.canvases[canvasID]
	if st == nil {
		return false
	}
	if _, ok := st.elements[id]; !ok {
		return false
	}
	delete(st.elements, id)
	for i, oid := range st.order {
		if oid == id {
			st.order = append(st.order[:i], st.order[i+1:]...)
			break
		}
	}
	return true
}

func (s *Store) clearAll(canvasID string) int {
	st := s.canvases[canvasID]
	if st == nil {
		return 0
	}
	n := len(st.order)
	st.elements = map[string]map[string]interface{}{}
	st.order = nil
	return n
}

// Count returns the number of elements on the current canvas.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := s.canvases[s.current]
	if st == nil {
		return 0
	}
	return len(st.order)
}

// --- canvas management ---

// ListCanvases returns metadata for all AI canvases.
func (s *Store) ListCanvases() []map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]map[string]interface{}, 0, len(s.canvases))
	for id, st := range s.canvases {
		out = append(out, map[string]interface{}{
			"id":       id,
			"name":     id,
			"elements": len(st.order),
			"current":  id == s.current,
		})
	}
	return out
}

// CreateCanvas makes a new empty AI canvas and switches to it. Returns the id.
func (s *Store) CreateCanvas() (string, error) {
	id := AICanvasPrefix + ulid.Make().String()
	s.mu.Lock()
	defer s.mu.Unlock()
	previousCurrent := s.current
	s.canvases[id] = newCanvasState()
	s.current = id
	err := s.persistCanvasLocked(id, s.canvases[id])
	if err != nil {
		delete(s.canvases, id)
		s.current = previousCurrent
	}
	return id, err
}

// SwitchCanvas switches the current canvas to an existing one.
func (s *Store) SwitchCanvas(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.canvases[id]; !ok {
		return false
	}
	s.current = id
	return true
}

// CurrentCanvasID returns the current canvas id.
func (s *Store) CurrentCanvasID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current
}

// mutationCanvasLocked 返回本次写请求的稳定目标。调用方必须持有 s.mu。
// canvasId 为可选参数，旧版 CLI 不传时仍操作 current。
func (s *Store) mutationCanvasLocked(r *http.Request) (string, *canvasState, bool) {
	canvasID := strings.TrimSpace(r.URL.Query().Get("canvasId"))
	if canvasID == "" {
		canvasID = s.current
	}
	st, ok := s.canvases[canvasID]
	return canvasID, st, ok
}

func nativeElementIDs(el map[string]interface{}) []string {
	native, textEl := agentToNative(el)
	ids := make([]string, 0, 2)
	if id, ok := native["id"].(string); ok && id != "" {
		ids = append(ids, id)
	}
	if textEl != nil {
		if id, ok := textEl["id"].(string); ok && id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func removedElementIDs(before, after []string) []string {
	remaining := make(map[string]struct{}, len(after))
	for _, id := range after {
		remaining[id] = struct{}{}
	}
	removed := make([]string, 0, len(before))
	for _, id := range before {
		if _, ok := remaining[id]; !ok {
			removed = append(removed, id)
		}
	}
	return removed
}

// nativeBroadcastMessageLocked 在锁内生成完整、稳定的广播快照。
// 单元素事件同时保留 element 旧字段，并通过 elements 同步绑定文字。
func (s *Store) nativeBroadcastMessageLocked(msgType string, agentElements []map[string]interface{}, canvasID string, removedIDs []string) map[string]interface{} {
	st := s.canvases[canvasID]
	var all []map[string]interface{}
	if st != nil {
		all = make([]map[string]interface{}, 0, len(st.order))
		for _, id := range st.order {
			all = append(all, st.elements[id])
		}
	}
	full := make([]map[string]interface{}, 0, len(all)*2)
	for _, el := range all {
		n, textEl := agentToNative(el)
		full = append(full, n)
		if textEl != nil {
			full = append(full, textEl)
		}
	}
	resolveArrowBindings(full)
	wantedIDs := make([]string, 0, len(agentElements)*2)
	for _, el := range agentElements {
		wantedIDs = append(wantedIDs, nativeElementIDs(el)...)
	}
	resolved := make([]map[string]interface{}, 0, len(wantedIDs))
	for _, id := range wantedIDs {
		for _, f := range full {
			if f["id"] == id {
				resolved = append(resolved, f)
				break
			}
		}
	}
	msg := map[string]interface{}{"type": msgType, "canvasId": canvasID}
	if len(removedIDs) > 0 {
		msg["removedElementIds"] = removedIDs
	}
	switch msgType {
	case "element_created", "element_updated":
		if len(resolved) > 0 {
			msg["element"] = resolved[0]
			msg["elements"] = resolved
		}
	case "elements_batch_created":
		msg["elements"] = resolved
	case "canvas_cleared":
		msg["timestamp"] = nowISO()
	}
	return msg
}

// --- HTTP helpers ---

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logrus.WithError(err).Warn("mcpcanvas: encode response failed")
	}
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]interface{}{"success": false, "error": msg})
}

func nowISO() string { return time.Now().Format(time.RFC3339) }

// num extracts a float from a JSON-decoded numeric field.
func num(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// --- /api/elements routes (operate on current canvas) ---

func Routes(r chi.Router, s *Store) {
	r.Get("/", s.handleList)
	r.Post("/", s.handleCreate)
	r.Delete("/clear", s.handleClear) // static segment beats {id} in chi
	r.Post("/batch", s.handleBatchCreate)
	r.Post("/sync", s.handleSync)
	r.Get("/search", s.handleSearch)
	r.Put("/{id}", s.handleUpdate)
	r.Delete("/{id}", s.handleDelete)
	r.Get("/{id}", s.handleGet)
}

// handleSync receives the full canvas in native Excalidraw format (sent by
// the frontend after user edits) and replaces the target canvas, then
// broadcasts so other viewers stay in sync. This is the frontend→server
// direction of two-way sync.
//
// The frontend MUST pass its own canvasId (query param `canvasId`): without
// it the sync would overwrite whatever canvas is currently selected server-
// side, which is wrong when the user's open canvas differs from the AI's
// current canvas. A missing/unknown canvasId is rejected (400/404).
func (s *Store) handleSync(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Elements []map[string]interface{} `json:"elements"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Elements == nil {
		writeErr(w, http.StatusBadRequest, "Expected elements array")
		return
	}
	target := r.URL.Query().Get("canvasId")
	if target == "" {
		writeErr(w, http.StatusBadRequest, "canvasId query param is required (protects other canvases from accidental overwrite)")
		return
	}
	var (
		canvasOK   bool
		persistErr error
	)
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		previous, ok := s.canvases[target]
		canvasOK = ok
		if !ok {
			return
		}
		before := cloneCanvasState(previous)
		// 将 native 元素转换为 agent 格式，并整体替换指定画布。
		s.canvases[target] = nativeListToAgentState(body.Elements)
		if persistErr = s.persistCanvasLocked(target, s.canvases[target]); persistErr != nil {
			s.canvases[target] = before
		}
	}()
	if !canvasOK {
		writeErr(w, http.StatusNotFound, "Canvas "+target+" not found")
		return
	}
	if persistErr != nil {
		writeErr(w, http.StatusInternalServerError, "failed to persist: "+persistErr.Error())
		return
	}
	// Broadcast the full native list so other connected viewers update
	native := body.Elements
	s.hub.Broadcast(map[string]interface{}{"type": "elements_synced", "canvasId": target, "elements": native, "count": len(native)})
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "count": len(native), "canvasId": target})
}

// --- /api/canvas routes (multi-canvas management) ---

func CanvasRoutes(r chi.Router, s *Store) {
	r.Get("/", s.handleCanvasList)
	r.Post("/", s.handleCanvasCreate)
	r.Put("/{id}", s.handleCanvasSwitch)
}

func (s *Store) handleCanvasList(w http.ResponseWriter, r *http.Request) {
	canvases := s.ListCanvases()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":  true,
		"canvases": canvases,
		"current":  s.CurrentCanvasID(),
		"count":    len(canvases),
	})
}

func (s *Store) handleCanvasCreate(w http.ResponseWriter, r *http.Request) {
	id, err := s.CreateCanvas()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to create canvas: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "canvasId": id, "current": id})
}

func (s *Store) handleCanvasSwitch(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.SwitchCanvas(id) {
		writeErr(w, http.StatusNotFound, "Canvas "+id+" not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "current": id})
}

func (s *Store) handleList(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	all := s.getAll()
	cur := s.current
	s.mu.RUnlock()
	for _, e := range all {
		if id, _ := e["id"].(string); id == "rt-note2" {
			fmt.Printf("[mcpcanvas] list rt-note2 keys=%v text=%v label=%v\n", sortedKeys(e), e["text"], e["label"])
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "elements": all, "count": len(all), "canvasId": cur})
}

func sortedKeys(m map[string]interface{}) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func (s *Store) handleCreate(w http.ResponseWriter, r *http.Request) {
	var body map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	typ, _ := body["type"].(string)
	if typ == "" {
		writeErr(w, http.StatusBadRequest, "element type is required")
		return
	}
	var (
		cur        string
		el         map[string]interface{}
		message    map[string]interface{}
		canvasOK   bool
		persistErr error
	)
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		var st *canvasState
		cur, st, canvasOK = s.mutationCanvasLocked(r)
		if !canvasOK {
			return
		}
		before := cloneCanvasState(st)
		el = s.create(cur, body)
		if persistErr = s.persistCanvasLocked(cur, s.canvases[cur]); persistErr != nil {
			s.canvases[cur] = before
			return
		}
		message = s.nativeBroadcastMessageLocked("element_created", []map[string]interface{}{el}, cur, nil)
	}()
	if !canvasOK {
		writeErr(w, http.StatusNotFound, "Canvas "+cur+" not found")
		return
	}
	if persistErr != nil {
		writeErr(w, http.StatusInternalServerError, "failed to persist: "+persistErr.Error())
		return
	}
	s.hub.Broadcast(message)
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "element": el, "canvasId": cur})
}

func (s *Store) handleUpdate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	var (
		cur        string
		el         map[string]interface{}
		message    map[string]interface{}
		canvasOK   bool
		ok         bool
		persistErr error
	)
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		var st *canvasState
		cur, st, canvasOK = s.mutationCanvasLocked(r)
		if !canvasOK {
			return
		}
		original, exists := st.elements[id]
		if !exists {
			return
		}
		beforeIDs := nativeElementIDs(original)
		before := cloneCanvasState(st)
		el, ok = s.update(cur, id, body)
		if !ok {
			return
		}
		if persistErr = s.persistCanvasLocked(cur, s.canvases[cur]); persistErr != nil {
			s.canvases[cur] = before
			return
		}
		removedIDs := removedElementIDs(beforeIDs, nativeElementIDs(el))
		message = s.nativeBroadcastMessageLocked("element_updated", []map[string]interface{}{el}, cur, removedIDs)
	}()
	if !canvasOK {
		writeErr(w, http.StatusNotFound, "Canvas "+cur+" not found")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "Element with ID "+id+" not found")
		return
	}
	if persistErr != nil {
		writeErr(w, http.StatusInternalServerError, "failed to persist: "+persistErr.Error())
		return
	}
	s.hub.Broadcast(message)
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "element": el, "canvasId": cur})
}

func (s *Store) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var (
		cur        string
		deletedIDs []string
		canvasOK   bool
		ok         bool
		persistErr error
	)
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		var st *canvasState
		cur, st, canvasOK = s.mutationCanvasLocked(r)
		if !canvasOK {
			return
		}
		original, exists := st.elements[id]
		if !exists {
			return
		}
		deletedIDs = nativeElementIDs(original)
		before := cloneCanvasState(st)
		ok = s.remove(cur, id)
		if !ok {
			return
		}
		if persistErr = s.persistCanvasLocked(cur, s.canvases[cur]); persistErr != nil {
			s.canvases[cur] = before
		}
	}()
	if !canvasOK {
		writeErr(w, http.StatusNotFound, "Canvas "+cur+" not found")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "Element with ID "+id+" not found")
		return
	}
	if persistErr != nil {
		writeErr(w, http.StatusInternalServerError, "failed to persist: "+persistErr.Error())
		return
	}
	s.hub.Broadcast(map[string]interface{}{
		"type":       "element_deleted",
		"canvasId":   cur,
		"elementId":  id,
		"elementIds": deletedIDs,
	})
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "message": "Element " + id + " deleted successfully", "canvasId": cur})
}

func (s *Store) handleClear(w http.ResponseWriter, r *http.Request) {
	var (
		cur        string
		n          int
		message    map[string]interface{}
		canvasOK   bool
		persistErr error
	)
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		var st *canvasState
		cur, st, canvasOK = s.mutationCanvasLocked(r)
		if !canvasOK {
			return
		}
		before := cloneCanvasState(st)
		n = s.clearAll(cur)
		if persistErr = s.persistCanvasLocked(cur, s.canvases[cur]); persistErr != nil {
			s.canvases[cur] = before
			return
		}
		message = s.nativeBroadcastMessageLocked("canvas_cleared", nil, cur, nil)
	}()
	if !canvasOK {
		writeErr(w, http.StatusNotFound, "Canvas "+cur+" not found")
		return
	}
	if persistErr != nil {
		writeErr(w, http.StatusInternalServerError, "failed to persist: "+persistErr.Error())
		return
	}
	s.hub.Broadcast(message)
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "message": fmt.Sprintf("Cleared %d elements", n), "count": n})
}

func (s *Store) handleBatchCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Elements []map[string]interface{} `json:"elements"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Elements == nil {
		writeErr(w, http.StatusBadRequest, "Expected an array of elements")
		return
	}
	for _, e := range body.Elements {
		typ, _ := e["type"].(string)
		if typ == "" {
			writeErr(w, http.StatusBadRequest, "element type is required")
			return
		}
	}
	var (
		cur        string
		created    []map[string]interface{}
		message    map[string]interface{}
		canvasOK   bool
		persistErr error
	)
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		var st *canvasState
		cur, st, canvasOK = s.mutationCanvasLocked(r)
		if !canvasOK {
			return
		}
		before := cloneCanvasState(st)
		created = make([]map[string]interface{}, 0, len(body.Elements))
		for _, e := range body.Elements {
			created = append(created, s.create(cur, e))
		}
		if persistErr = s.persistCanvasLocked(cur, s.canvases[cur]); persistErr != nil {
			s.canvases[cur] = before
			return
		}
		message = s.nativeBroadcastMessageLocked("elements_batch_created", created, cur, nil)
	}()
	if !canvasOK {
		writeErr(w, http.StatusNotFound, "Canvas "+cur+" not found")
		return
	}
	if persistErr != nil {
		writeErr(w, http.StatusInternalServerError, "failed to persist: "+persistErr.Error())
		return
	}
	s.hub.Broadcast(message)
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "elements": created, "count": len(created), "canvasId": cur})
}

func (s *Store) handleGet(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.mu.RLock()
	st := s.canvases[s.current]
	var el map[string]interface{}
	if st != nil {
		el = st.elements[id]
	}
	s.mu.RUnlock()
	if el == nil {
		writeErr(w, http.StatusNotFound, "Element with ID "+id+" not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "element": el})
}

func (s *Store) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	typeFilter := q.Get("type")
	xMin := parseFloatQ(q, "x_min", math.Inf(-1))
	xMax := parseFloatQ(q, "x_max", math.Inf(1))
	yMin := parseFloatQ(q, "y_min", math.Inf(-1))
	yMax := parseFloatQ(q, "y_max", math.Inf(1))
	filters := map[string]string{}
	for k, vs := range q {
		if k == "type" || k == "x_min" || k == "x_max" || k == "y_min" || k == "y_max" {
			continue
		}
		filters[k] = vs[0]
	}
	s.mu.RLock()
	st := s.canvases[s.current]
	results := []map[string]interface{}{}
	if st != nil {
		results = make([]map[string]interface{}, 0, len(st.order))
		for _, id := range st.order {
			el := st.elements[id]
			typ, _ := el["type"].(string)
			if typeFilter != "" && typ != typeFilter {
				continue
			}
			x, _ := num(el["x"])
			y, _ := num(el["y"])
			if x < xMin || x > xMax || y < yMin || y > yMax {
				continue
			}
			match := true
			for k, v := range filters {
				ev, exists := el[k]
				if !exists || fmtSprint(ev) != v {
					match = false
					break
				}
			}
			if match {
				results = append(results, el)
			}
		}
	}
	s.mu.RUnlock()
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "elements": results, "count": len(results)})
}

func parseFloatQ(q map[string][]string, key string, def float64) float64 {
	vs := q[key]
	if len(vs) == 0 {
		return def
	}
	f, err := strconv.ParseFloat(vs[0], 64)
	if err != nil {
		return def
	}
	return f
}

// fmtSprint mirrors JS String() coercion used by the reference server's exact-match filter.
func fmtSprint(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case string:
		return t
	case float64:
		if t == math.Trunc(t) {
			return strconv.FormatFloat(t, 'f', -1, 64)
		}
		return strconv.FormatFloat(t, 'g', -1, 64)
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprintf("%v", v)
	}
}
