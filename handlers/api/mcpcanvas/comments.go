package mcpcanvas

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
)

// Comment is a single comment or reply in a thread.
// Threads are flat: parent_id == "" means top-level (anchored to an element),
// otherwise it's a reply to the top-level comment.
type Comment struct {
	ID        string    `json:"id"`
	CanvasID  string    `json:"canvasId"`
	ElementID string    `json:"elementId"`
	UserID    string    `json:"userId"`
	Text      string    `json:"text"`
	ParentID  string    `json:"parentId,omitempty"`
	Resolved  bool      `json:"resolved"`
	CreatedAt time.Time `json:"createdAt"`
}

// initCommentsTable creates the comments table if it doesn't exist.
func (s *Store) initCommentsTable() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS comments (
		id TEXT PRIMARY KEY,
		canvas_id TEXT NOT NULL,
		element_id TEXT NOT NULL,
		user_id TEXT NOT NULL,
		text TEXT NOT NULL,
		parent_id TEXT DEFAULT '',
		resolved INTEGER DEFAULT 0,
		created_at DATETIME NOT NULL
	)`)
	return err
}

// listComments returns all comments for a canvas, optionally filtered by element.
func (s *Store) listComments(canvasID, elementID string) ([]Comment, error) {
	query := `SELECT id, canvas_id, element_id, user_id, text, parent_id, resolved, created_at
		FROM comments WHERE canvas_id = ?`
	args := []interface{}{canvasID}
	if elementID != "" {
		query += ` AND element_id = ?`
		args = append(args, elementID)
	}
	query += ` ORDER BY created_at ASC`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	comments := []Comment{}
	for rows.Next() {
		var c Comment
		var resolved int
		if err := rows.Scan(&c.ID, &c.CanvasID, &c.ElementID, &c.UserID, &c.Text, &c.ParentID, &resolved, &c.CreatedAt); err != nil {
			return nil, err
		}
		c.Resolved = resolved == 1
		comments = append(comments, c)
	}
	return comments, rows.Err()
}

// createComment inserts a new comment and broadcasts it over WS.
func (s *Store) createComment(c Comment) (Comment, error) {
	if c.ID == "" {
		c.ID = ulid.Make().String()
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now()
	}
	_, err := s.db.Exec(`INSERT INTO comments (id, canvas_id, element_id, user_id, text, parent_id, resolved, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.CanvasID, c.ElementID, c.UserID, c.Text, c.ParentID, boolToInt(c.Resolved), c.CreatedAt)
	if err != nil {
		return Comment{}, err
	}
	s.hub.Broadcast(map[string]interface{}{
		"type":     "comment_created",
		"canvasId": c.CanvasID,
		"comment":  c,
	})
	return c, nil
}

// updateComment updates text/resolved and broadcasts the change.
func (s *Store) updateComment(id string, patch map[string]interface{}) (Comment, bool) {
	// fetch existing
	var c Comment
	var resolved int
	err := s.db.QueryRow(`SELECT id, canvas_id, element_id, user_id, text, parent_id, resolved, created_at
		FROM comments WHERE id = ?`, id).Scan(&c.ID, &c.CanvasID, &c.ElementID, &c.UserID, &c.Text, &c.ParentID, &resolved, &c.CreatedAt)
	if err != nil {
		return Comment{}, false
	}
	c.Resolved = resolved == 1

	if text, ok := patch["text"].(string); ok && text != "" {
		c.Text = text
	}
	if resolved, ok := patch["resolved"].(bool); ok {
		c.Resolved = resolved
	}
	_, err = s.db.Exec(`UPDATE comments SET text = ?, resolved = ? WHERE id = ?`, c.Text, boolToInt(c.Resolved), id)
	if err != nil {
		return Comment{}, false
	}
	s.hub.Broadcast(map[string]interface{}{
		"type":     "comment_updated",
		"canvasId": c.CanvasID,
		"comment":  c,
	})
	return c, true
}

// deleteComment removes a comment (and its replies) and broadcasts the deletion.
func (s *Store) deleteComment(id string) (string, bool) {
	var canvasID string
	err := s.db.QueryRow(`SELECT canvas_id FROM comments WHERE id = ?`, id).Scan(&canvasID)
	if err != nil {
		return "", false
	}
	// delete the comment and any replies to it
	_, err = s.db.Exec(`DELETE FROM comments WHERE id = ? OR parent_id = ?`, id, id)
	if err != nil {
		return "", false
	}
	s.hub.Broadcast(map[string]interface{}{
		"type":     "comment_deleted",
		"canvasId": canvasID,
		"id":       id,
	})
	return canvasID, true
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// CommentRoutes registers the comments CRUD endpoints.
func CommentRoutes(r chi.Router, s *Store) {
	r.Get("/", s.handleListComments)
	r.Post("/", s.handleCreateComment)
	r.Route("/{id}", func(r chi.Router) {
		r.Put("/", s.handleUpdateComment)
		r.Delete("/", s.handleDeleteComment)
	})
}

// --- HTTP handlers ---

func (s *Store) handleListComments(w http.ResponseWriter, r *http.Request) {
	canvasID := r.URL.Query().Get("canvasId")
	if canvasID == "" {
		canvasID = s.current
	}
	elementID := r.URL.Query().Get("elementId")
	comments, err := s.listComments(canvasID, elementID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "canvasId": canvasID, "comments": comments})
}

func (s *Store) handleCreateComment(w http.ResponseWriter, r *http.Request) {
	var c Comment
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	if c.CanvasID == "" {
		c.CanvasID = s.current
	}
	if c.ElementID == "" || c.Text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "elementId and text are required"})
		return
	}
	c.UserID = s.userID
	created, err := s.createComment(c)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "comment": created})
}

func (s *Store) handleUpdateComment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var patch map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	updated, ok := s.updateComment(id, patch)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"success": false, "error": "comment not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "comment": updated})
}

func (s *Store) handleDeleteComment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	canvasID, ok := s.deleteComment(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"success": false, "error": "comment not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "canvasId": canvasID})
}

// logComments is a debug helper (kept minimal, see Pitfall 17: log to locate).
func logComments(msg string, args ...interface{}) {
	fmt.Printf("[comments] "+msg+"\n", args...)
}
