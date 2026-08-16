package mcpcanvas

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"excalidraw-complete/core"
	"excalidraw-complete/handlers/auth"
	appmiddleware "excalidraw-complete/middleware"
	"excalidraw-complete/stores"
	"fmt"
	"math"
	"net/http"
	"os"
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

type canvasTarget struct {
	CanvasID     string
	OwnerUserID  string
	WorkspaceID  string
	CollectionID string
}

type atomicCanvasCreator interface {
	CreateMCPScene(ctx context.Context, userID, sceneID, collectionID, title string, data []byte) (*core.WorkspaceScene, error)
}

// canvasState is the in-memory element list for one AI canvas.
type canvasState struct {
	elements map[string]map[string]interface{} // id -> element (agent format)
	order    []string                          // insertion order
}

// Store keeps multiple AI canvases in memory (each as canvasState) and
// persists every mutation to the host app's `canvases` table (native
// Excalidraw format). Every request names its target canvas explicitly;
// there is deliberately no process-global "current canvas" shared by users.
type Store struct {
	mu                 sync.RWMutex
	canvases           map[string]*canvasState // canvasID -> state
	targets            map[string]canvasTarget // canvasID -> durable Workspace binding
	storageMu          sync.Mutex
	anonymousFiles     map[storageObjectKey]anonymousStorageObject
	anonymousFileBytes int64
	db                 *sql.DB
	hub                *Hub
	host               stores.Store
}

func NewStore(dsn string, host ...stores.Store) (*Store, error) {
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
	s := &Store{
		canvases: map[string]*canvasState{},
		targets:  map[string]canvasTarget{},
		db:       db,
		hub:      NewHub(),
	}
	if len(host) > 0 {
		s.host = host[0]
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
		workspace_id TEXT NOT NULL DEFAULT 'default',
		collection_id TEXT,
		created_at DATETIME,
		updated_at DATETIME,
		PRIMARY KEY (user_id, id)
	)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS mcp_snapshots (name TEXT PRIMARY KEY, elements BLOB, created_at DATETIME)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS mcp_canvas_targets (
		canvas_id TEXT PRIMARY KEY,
		owner_user_id TEXT NOT NULL,
		workspace_id TEXT NOT NULL,
		collection_id TEXT NOT NULL,
		created_at DATETIME NOT NULL
	)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS mcp_canvas_snapshots (
		canvas_id TEXT NOT NULL,
		name TEXT NOT NULL,
		elements BLOB NOT NULL,
		created_at DATETIME NOT NULL,
		PRIMARY KEY (canvas_id, name)
	)`); err != nil {
		return err
	}
	return s.initFilesTable()
}

// load reads only explicitly bound AI canvases. Legacy ai-* rows without a
// binding are intentionally not selected: their Workspace ownership is
// ambiguous and they must be migrated explicitly.
func (s *Store) load() error {
	rows, err := s.db.Query(`SELECT t.canvas_id, t.owner_user_id, t.workspace_id, t.collection_id, c.data
		FROM mcp_canvas_targets t
		JOIN canvases c ON c.id = t.canvas_id AND c.user_id = t.owner_user_id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, ownerID, workspaceID, collectionID string
		var data []byte
		if err := rows.Scan(&id, &ownerID, &workspaceID, &collectionID, &data); err != nil {
			continue
		}
		state, err := s.parseCanvas(data)
		if err != nil {
			logrus.WithError(err).WithField("canvas", id).Warn("mcpcanvas: failed to parse canvas, skipping")
			continue
		}
		s.canvases[id] = state
		s.targets[id] = canvasTarget{CanvasID: id, OwnerUserID: ownerID, WorkspaceID: workspaceID, CollectionID: collectionID}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	logrus.WithField("canvases", len(s.canvases)).Info("mcpcanvas: loaded")
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
	logrus.WithFields(logrus.Fields{
		"elements":   len(elements),
		"boundText":  len(boundText),
		"agentCount": len(agents),
	}).Debug("mcpcanvas: load")
	return state
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
func (s *Store) persistCanvasLocked(ctx context.Context, actorUserID, canvasID string, st *canvasState) error {
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
	target, ok := s.targets[canvasID]
	if !ok {
		return fmt.Errorf("canvas %s has no Workspace binding", canvasID)
	}
	if err := s.db.QueryRow(`SELECT data FROM canvases WHERE user_id = ? AND id = ?`, target.OwnerUserID, canvasID).Scan(&oldData); err == nil {
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
	if s.host != nil {
		if actorUserID == "" {
			return core.ErrForbidden
		}
		clientID := strings.TrimSpace(core.SceneClientIDFromContext(ctx))
		if clientID == "" {
			return &core.SceneLockError{Message: "scene write requires a client id"}
		}
		if _, err := s.host.AcquireSceneLock(ctx, actorUserID, canvasID, clientID, "MCP"); err != nil {
			return err
		}
		return s.host.UpdateSceneData(ctx, actorUserID, canvasID, data)
	}
	_, err = s.db.Exec(`INSERT INTO canvases (id, user_id, name, thumbnail, data, workspace_id, collection_id, created_at, updated_at) VALUES (?, ?, ?, '', ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, id) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
		canvasID, target.OwnerUserID, name, data, target.WorkspaceID, target.CollectionID, now, now)
	return err
}

func requestActor(r *http.Request) (string, bool) {
	claims, ok := r.Context().Value(appmiddleware.ClaimsContextKey).(*auth.AppClaims)
	if !ok || claims == nil || claims.Subject == "" {
		return "", false
	}
	return claims.Subject, true
}

func sceneWriteContext(r *http.Request) context.Context {
	return core.WithSceneClientID(r.Context(), strings.TrimSpace(r.Header.Get("X-Scene-Client-ID")))
}

func (s *Store) authorizeCanvas(r *http.Request, canvasID string, write bool) error {
	if s.host == nil {
		return nil
	}
	userID, ok := requestActor(r)
	if !ok {
		return core.ErrForbidden
	}
	scene, err := s.host.GetScene(r.Context(), userID, canvasID)
	if err != nil {
		return err
	}
	if write && !scene.CanEdit {
		return core.ErrForbidden
	}
	return nil
}

func (s *Store) broadcastToCanvas(canvasID string, msg map[string]interface{}) {
	s.hub.BroadcastToCanvas(canvasID, msg, func(userID string) bool {
		if s.host == nil {
			return true
		}
		if userID == "" {
			return false
		}
		_, err := s.host.GetScene(context.Background(), userID, canvasID)
		return err == nil
	})
}

func writeAccessErr(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, core.ErrInvalidInput):
		status = http.StatusBadRequest
	case errors.Is(err, core.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, core.ErrForbidden):
		status = http.StatusForbidden
	case errors.Is(err, core.ErrConflict):
		status = http.StatusConflict
	default:
		var lockErr *core.SceneLockError
		if errors.As(err, &lockErr) {
			status = http.StatusConflict
		}
	}
	writeErr(w, status, err.Error())
}

// --- element accessors ---

func (s *Store) getAll(canvasID string) []map[string]interface{} {
	st := s.canvases[canvasID]
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
	logrus.WithFields(logrus.Fields{"id": id, "type": el["type"], "text": el["text"], "label": el["label"]}).Debug("mcpcanvas: create")
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
	logrus.WithFields(logrus.Fields{"id": id, "text": el["text"], "label": el["label"]}).Debug("mcpcanvas: update")
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

// Count returns the total number of elements in bound AI canvases.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, st := range s.canvases {
		count += len(st.order)
	}
	return count
}

// --- canvas management ---

// ListCanvases returns metadata for all AI canvases.
func (s *Store) ListCanvases(userID string) []map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]map[string]interface{}, 0, len(s.canvases))
	for id, st := range s.canvases {
		target := s.targets[id]
		if s.host != nil {
			scene, err := s.host.GetScene(context.Background(), userID, id)
			if err != nil {
				continue
			}
			target.WorkspaceID = scene.WorkspaceID
			if scene.CollectionID != nil {
				target.CollectionID = *scene.CollectionID
			}
		}
		out = append(out, map[string]interface{}{
			"id":           id,
			"name":         id,
			"elements":     len(st.order),
			"workspaceId":  target.WorkspaceID,
			"collectionId": target.CollectionID,
		})
	}
	return out
}

// CreateCanvas makes a new Workspace-bound AI canvas. The collection is the
// explicit binding supplied by the direct drawing CLI; its owner and
// Workspace are derived from the database instead of a process environment.
func (s *Store) CreateCanvas(args ...string) (string, error) {
	return s.createCanvas(context.Background(), args...)
}

func (s *Store) createCanvas(ctx context.Context, args ...string) (string, error) {
	var userID, collectionID string
	switch len(args) {
	case 1:
		collectionID = args[0]
	case 2:
		userID, collectionID = args[0], args[1]
	default:
		return "", core.ErrInvalidInput
	}
	if strings.TrimSpace(collectionID) == "" {
		return "", core.ErrInvalidInput
	}
	id := AICanvasPrefix + ulid.Make().String()
	s.mu.Lock()
	defer s.mu.Unlock()
	var target canvasTarget
	if s.host != nil {
		if userID == "" {
			return "", core.ErrInvalidInput
		}
		creator, ok := s.host.(atomicCanvasCreator)
		if !ok {
			return "", fmt.Errorf("host store does not support atomic MCP canvas creation")
		}
		scene, err := creator.CreateMCPScene(ctx, userID, id, collectionID, id,
			[]byte(`{"elements":[],"appState":{},"files":{}}`))
		if err != nil {
			return "", err
		}
		target.OwnerUserID = userID
		target.WorkspaceID = scene.WorkspaceID
		target.CanvasID = id
		target.CollectionID = collectionID
		s.canvases[id] = newCanvasState()
		s.targets[id] = target
		return id, nil
	} else if err := s.db.QueryRow(`SELECT user_id, workspace_id FROM shell_collections WHERE id = ?`, collectionID).Scan(&target.OwnerUserID, &target.WorkspaceID); err != nil {
		if err == sql.ErrNoRows {
			return "", core.ErrNotFound
		}
		return "", err
	}
	target.CanvasID = id
	target.CollectionID = collectionID
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	if _, err := tx.Exec(`INSERT INTO mcp_canvas_targets (canvas_id, owner_user_id, workspace_id, collection_id, created_at) VALUES (?, ?, ?, ?, ?)`,
		target.CanvasID, target.OwnerUserID, target.WorkspaceID, target.CollectionID, now); err != nil {
		return "", err
	}
	// 新建空 AI Scene 尚无锁，先与 target 绑定原子插入；后续内容 mutation
	// 一律通过 host ACL/锁。
	if _, err := tx.Exec(`INSERT INTO canvases (id, user_id, name, thumbnail, data, workspace_id, collection_id, created_at, updated_at)
		VALUES (?, ?, ?, '', ?, ?, ?, ?, ?)`, id, target.OwnerUserID, id,
		[]byte(`{"elements":[],"appState":{},"files":{}}`), target.WorkspaceID, target.CollectionID, now, now); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	s.canvases[id] = newCanvasState()
	s.targets[id] = target
	return id, nil
}

// mutationCanvasLocked returns the explicit request target. Callers must hold
// s.mu. A missing canvasId is rejected rather than falling back to shared
// process state.
func (s *Store) mutationCanvasLocked(r *http.Request) (string, *canvasState, bool) {
	canvasID := strings.TrimSpace(r.URL.Query().Get("canvasId"))
	if canvasID == "" {
		return "", nil, false
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

// --- /api/elements routes (every request requires ?canvasId=...) ---

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
	if err := s.authorizeCanvas(r, target, true); err != nil {
		writeAccessErr(w, err)
		return
	}
	actorUserID, _ := requestActor(r)
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
		if persistErr = s.persistCanvasLocked(sceneWriteContext(r), actorUserID, target, s.canvases[target]); persistErr != nil {
			s.canvases[target] = before
		}
	}()
	if !canvasOK {
		writeErr(w, http.StatusNotFound, "Canvas "+target+" not found")
		return
	}
	if persistErr != nil {
		writeAccessErr(w, persistErr)
		return
	}
	// Broadcast the full native list so other connected viewers update
	native := body.Elements
	s.broadcastToCanvas(target, map[string]interface{}{"type": "elements_synced", "canvasId": target, "elements": native, "count": len(native)})
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "count": len(native), "canvasId": target})
}

// --- /api/canvas routes (multi-canvas management) ---

func CanvasRoutes(r chi.Router, s *Store) {
	r.Get("/", s.handleCanvasList)
	r.Post("/", s.handleCanvasCreate)
	r.Put("/{id}", s.handleCanvasSwitch)
}

func (s *Store) handleCanvasList(w http.ResponseWriter, r *http.Request) {
	userID, ok := requestActor(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "authentication required")
		return
	}
	canvases := s.ListCanvases(userID)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":  true,
		"canvases": canvases,
		"count":    len(canvases),
	})
}

func (s *Store) handleCanvasCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CollectionID string `json:"collectionId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	userID, ok := requestActor(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "authentication required")
		return
	}
	id, err := s.createCanvas(r.Context(), userID, body.CollectionID)
	if err != nil {
		writeAccessErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{"success": true, "canvasId": id})
}

func (s *Store) handleCanvasSwitch(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.authorizeCanvas(r, id, false); err != nil {
		writeAccessErr(w, err)
		return
	}
	s.mu.RLock()
	_, ok := s.targets[id]
	s.mu.RUnlock()
	if !ok {
		writeErr(w, http.StatusNotFound, "Canvas "+id+" not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "canvasId": id})
}

// cloneElementsForRead 深拷贝一组元素 map，供只读 handler 在释放锁后安全序列化。
// 写者（update 原地改 top-level 值、delete/clear 删 key）可能并发修改内存 map，
// 锁外序列化"活"元素会触发 concurrent map iteration and map write panic。
func cloneElementsForRead(elements []map[string]interface{}) []map[string]interface{} {
	out := make([]map[string]interface{}, len(elements))
	for i, el := range elements {
		out[i] = cloneElementForRead(el)
	}
	return out
}

func cloneElementForRead(element map[string]interface{}) map[string]interface{} {
	if element == nil {
		return nil
	}
	clone := make(map[string]interface{}, len(element))
	for key, value := range element {
		clone[key] = cloneValueForRead(value)
	}
	return clone
}

// cloneValueForRead 递归复制 JSON 解码后的容器值。元素的绑定、points 和
// customData 都可能是嵌套 map/slice；只复制顶层 map 会让后续的嵌套写入再次
// 与锁外 JSON 编码共享内存。
func cloneValueForRead(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		clone := make(map[string]interface{}, len(typed))
		for key, nested := range typed {
			clone[key] = cloneValueForRead(nested)
		}
		return clone
	case []interface{}:
		clone := make([]interface{}, len(typed))
		for i, nested := range typed {
			clone[i] = cloneValueForRead(nested)
		}
		return clone
	case []map[string]interface{}:
		clone := make([]map[string]interface{}, len(typed))
		for i, nested := range typed {
			clone[i] = cloneElementForRead(nested)
		}
		return clone
	case []float64:
		return append([]float64(nil), typed...)
	case [][]float64:
		clone := make([][]float64, len(typed))
		for i, nested := range typed {
			clone[i] = append([]float64(nil), nested...)
		}
		return clone
	case []string:
		return append([]string(nil), typed...)
	default:
		return value
	}
}

// handleList lists all elements of the current canvas.
func (s *Store) handleList(w http.ResponseWriter, r *http.Request) {
	canvasID := strings.TrimSpace(r.URL.Query().Get("canvasId"))
	if err := s.authorizeCanvas(r, canvasID, false); err != nil {
		writeAccessErr(w, err)
		return
	}
	s.mu.RLock()
	cur, st, canvasOK := s.mutationCanvasLocked(r)
	var all []map[string]interface{}
	if canvasOK {
		all = make([]map[string]interface{}, 0, len(st.order))
		for _, id := range st.order {
			all = append(all, st.elements[id])
		}
	}
	// 在锁内深拷贝，锁外序列化拷贝（避免并发 map 读写 panic）
	all = cloneElementsForRead(all)
	s.mu.RUnlock()
	if !canvasOK {
		writeErr(w, http.StatusBadRequest, "canvasId query param is required and must reference a Workspace-bound AI canvas")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "elements": all, "count": len(all), "canvasId": cur})
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
	canvasID := strings.TrimSpace(r.URL.Query().Get("canvasId"))
	if err := s.authorizeCanvas(r, canvasID, true); err != nil {
		writeAccessErr(w, err)
		return
	}
	actorUserID, _ := requestActor(r)
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
		if persistErr = s.persistCanvasLocked(sceneWriteContext(r), actorUserID, cur, s.canvases[cur]); persistErr != nil {
			s.canvases[cur] = before
			return
		}
		message = s.nativeBroadcastMessageLocked("element_created", []map[string]interface{}{el}, cur, nil)
		// HTTP 响应会在解锁后编码，不能引用可被后续写请求原地更新的 map。
		el = cloneElementForRead(el)
	}()
	if !canvasOK {
		writeErr(w, http.StatusNotFound, "Canvas "+cur+" not found")
		return
	}
	if persistErr != nil {
		writeAccessErr(w, persistErr)
		return
	}
	s.broadcastToCanvas(cur, message)
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "element": el, "canvasId": cur})
}

func (s *Store) handleUpdate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	canvasID := strings.TrimSpace(r.URL.Query().Get("canvasId"))
	if err := s.authorizeCanvas(r, canvasID, true); err != nil {
		writeAccessErr(w, err)
		return
	}
	actorUserID, _ := requestActor(r)
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
		if persistErr = s.persistCanvasLocked(sceneWriteContext(r), actorUserID, cur, s.canvases[cur]); persistErr != nil {
			s.canvases[cur] = before
			return
		}
		removedIDs := removedElementIDs(beforeIDs, nativeElementIDs(el))
		message = s.nativeBroadcastMessageLocked("element_updated", []map[string]interface{}{el}, cur, removedIDs)
		// HTTP 响应会在解锁后编码，不能引用可被后续写请求原地更新的 map。
		el = cloneElementForRead(el)
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
		writeAccessErr(w, persistErr)
		return
	}
	s.broadcastToCanvas(cur, message)
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "element": el, "canvasId": cur})
}

func (s *Store) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	canvasID := strings.TrimSpace(r.URL.Query().Get("canvasId"))
	if err := s.authorizeCanvas(r, canvasID, true); err != nil {
		writeAccessErr(w, err)
		return
	}
	actorUserID, _ := requestActor(r)
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
		if persistErr = s.persistCanvasLocked(sceneWriteContext(r), actorUserID, cur, s.canvases[cur]); persistErr != nil {
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
		writeAccessErr(w, persistErr)
		return
	}
	s.broadcastToCanvas(cur, map[string]interface{}{
		"type":       "element_deleted",
		"canvasId":   cur,
		"elementId":  id,
		"elementIds": deletedIDs,
	})
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "message": "Element " + id + " deleted successfully", "canvasId": cur})
}

func (s *Store) handleClear(w http.ResponseWriter, r *http.Request) {
	canvasID := strings.TrimSpace(r.URL.Query().Get("canvasId"))
	if err := s.authorizeCanvas(r, canvasID, true); err != nil {
		writeAccessErr(w, err)
		return
	}
	actorUserID, _ := requestActor(r)
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
		if persistErr = s.persistCanvasLocked(sceneWriteContext(r), actorUserID, cur, s.canvases[cur]); persistErr != nil {
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
		writeAccessErr(w, persistErr)
		return
	}
	s.broadcastToCanvas(cur, message)
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
	canvasID := strings.TrimSpace(r.URL.Query().Get("canvasId"))
	if err := s.authorizeCanvas(r, canvasID, true); err != nil {
		writeAccessErr(w, err)
		return
	}
	actorUserID, _ := requestActor(r)
	var (
		cur        string
		created    []map[string]interface{}
		response   []map[string]interface{}
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
		if persistErr = s.persistCanvasLocked(sceneWriteContext(r), actorUserID, cur, s.canvases[cur]); persistErr != nil {
			s.canvases[cur] = before
			return
		}
		message = s.nativeBroadcastMessageLocked("elements_batch_created", created, cur, nil)
		response = cloneElementsForRead(created)
	}()
	if !canvasOK {
		writeErr(w, http.StatusNotFound, "Canvas "+cur+" not found")
		return
	}
	if persistErr != nil {
		writeAccessErr(w, persistErr)
		return
	}
	s.broadcastToCanvas(cur, message)
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "elements": response, "count": len(response), "canvasId": cur})
}

func (s *Store) handleGet(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	canvasID := strings.TrimSpace(r.URL.Query().Get("canvasId"))
	if err := s.authorizeCanvas(r, canvasID, false); err != nil {
		writeAccessErr(w, err)
		return
	}
	s.mu.RLock()
	_, st, canvasOK := s.mutationCanvasLocked(r)
	var el map[string]interface{}
	if canvasOK {
		if orig, ok := st.elements[id]; ok {
			// 深拷贝，锁外安全序列化
			el = cloneElementForRead(orig)
		}
	}
	s.mu.RUnlock()
	if !canvasOK {
		writeErr(w, http.StatusBadRequest, "canvasId query param is required and must reference a Workspace-bound AI canvas")
		return
	}
	if el == nil {
		writeErr(w, http.StatusNotFound, "Element with ID "+id+" not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "element": el})
}

func (s *Store) handleSearch(w http.ResponseWriter, r *http.Request) {
	canvasID := strings.TrimSpace(r.URL.Query().Get("canvasId"))
	if err := s.authorizeCanvas(r, canvasID, false); err != nil {
		writeAccessErr(w, err)
		return
	}
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
	canvasID, st, canvasOK := s.mutationCanvasLocked(r)
	results := []map[string]interface{}{}
	if canvasOK {
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
				// 深拷贝，锁外安全序列化
				results = append(results, cloneElementForRead(el))
			}
		}
	}
	s.mu.RUnlock()
	if !canvasOK {
		writeErr(w, http.StatusBadRequest, "canvasId query param is required and must reference a Workspace-bound AI canvas")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "elements": results, "count": len(results), "canvasId": canvasID})
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
