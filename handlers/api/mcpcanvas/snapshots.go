package mcpcanvas

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// SnapshotRoutes implements the mcp-excalidraw snapshot API. Snapshots are
// stored in their own SQLite table (mcp_snapshots) inside the same DB file.
// The CLI's restore flow is: GET snapshot -> DELETE clear -> POST batch, so
// these endpoints only need to save and list/read — the CLI drives the
// restore writes itself.
func SnapshotRoutes(r chi.Router, s *Store) {
	r.Post("/", s.handleSnapshotSave)
	r.Get("/", s.handleSnapshotList)
	r.Get("/{name}", s.handleSnapshotGet)
}

func (s *Store) handleSnapshotSave(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Name == "" {
		writeErr(w, http.StatusBadRequest, "snapshot name is required")
		return
	}
	canvasID := r.URL.Query().Get("canvasId")
	if canvasID == "" {
		writeErr(w, http.StatusBadRequest, "canvasId query param is required")
		return
	}
	if err := s.authorizeCanvas(r, canvasID, true); err != nil {
		writeAccessErr(w, err)
		return
	}
	s.mu.RLock()
	_, exists := s.targets[canvasID]
	all := s.getAll(canvasID)
	// 深拷贝，锁外安全序列化（避免并发 map 读写 panic）
	all = cloneElementsForRead(all)
	s.mu.RUnlock()
	if !exists {
		writeErr(w, http.StatusNotFound, "Canvas "+canvasID+" not found")
		return
	}
	data, err := json.Marshal(all)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to marshal canvas")
		return
	}
	createdAt := time.Now().Format(time.RFC3339)
	if _, err := s.db.Exec(`INSERT INTO mcp_canvas_snapshots (canvas_id, name, elements, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(canvas_id, name) DO UPDATE SET elements = excluded.elements, created_at = excluded.created_at`,
		canvasID, body.Name, data, createdAt); err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to save snapshot: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":      true,
		"name":         body.Name,
		"canvasId":     canvasID,
		"elementCount": len(all),
		"createdAt":    createdAt,
	})
}

func (s *Store) handleSnapshotList(w http.ResponseWriter, r *http.Request) {
	canvasID := r.URL.Query().Get("canvasId")
	if canvasID == "" {
		writeErr(w, http.StatusBadRequest, "canvasId query param is required")
		return
	}
	if err := s.authorizeCanvas(r, canvasID, false); err != nil {
		writeAccessErr(w, err)
		return
	}
	s.mu.RLock()
	_, exists := s.targets[canvasID]
	s.mu.RUnlock()
	if !exists {
		writeErr(w, http.StatusNotFound, "Canvas "+canvasID+" not found")
		return
	}
	rows, err := s.db.Query(`SELECT name, elements, created_at FROM mcp_canvas_snapshots WHERE canvas_id = ? ORDER BY created_at DESC`, canvasID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to list snapshots: "+err.Error())
		return
	}
	defer rows.Close()
	type snapMeta struct {
		Name      string `json:"name"`
		Count     int    `json:"count"`
		CreatedAt string `json:"createdAt"`
	}
	snaps := []snapMeta{}
	for rows.Next() {
		var name, createdAt string
		var data []byte
		if err := rows.Scan(&name, &data, &createdAt); err != nil {
			writeErr(w, http.StatusInternalServerError, "failed to scan snapshot")
			return
		}
		var elems []map[string]interface{}
		_ = json.Unmarshal(data, &elems)
		snaps = append(snaps, snapMeta{Name: name, Count: len(elems), CreatedAt: createdAt})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "canvasId": canvasID, "snapshots": snaps, "count": len(snaps)})
}

func (s *Store) handleSnapshotGet(w http.ResponseWriter, r *http.Request) {
	canvasID := r.URL.Query().Get("canvasId")
	if canvasID == "" {
		writeErr(w, http.StatusBadRequest, "canvasId query param is required")
		return
	}
	if err := s.authorizeCanvas(r, canvasID, false); err != nil {
		writeAccessErr(w, err)
		return
	}
	name := chi.URLParam(r, "name")
	var data []byte
	var createdAt string
	err := s.db.QueryRow(`SELECT elements, created_at FROM mcp_canvas_snapshots WHERE canvas_id = ? AND name = ?`, canvasID, name).Scan(&data, &createdAt)
	if err != nil {
		writeErr(w, http.StatusNotFound, "Snapshot \""+name+"\" not found")
		return
	}
	var elems []map[string]interface{}
	if err := json.Unmarshal(data, &elems); err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to parse snapshot")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":  true,
		"canvasId": canvasID,
		"snapshot": map[string]interface{}{
			"name":      name,
			"elements":  elems,
			"createdAt": createdAt,
		},
	})
}
