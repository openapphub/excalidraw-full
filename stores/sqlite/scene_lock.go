package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"excalidraw-complete/core"
	"log"
	"strings"
	"time"
)

const sceneEditLockTTL = 30 * time.Second

func ensureCanvasEditLockColumns(db *sql.DB) {
	cols := []struct {
		name string
		ddl  string
	}{
		{"collab_enabled", `ALTER TABLE canvases ADD COLUMN collab_enabled INTEGER NOT NULL DEFAULT 0`},
		{"edit_lock_user_id", `ALTER TABLE canvases ADD COLUMN edit_lock_user_id TEXT`},
		{"edit_lock_client_id", `ALTER TABLE canvases ADD COLUMN edit_lock_client_id TEXT`},
		{"edit_lock_until", `ALTER TABLE canvases ADD COLUMN edit_lock_until DATETIME`},
		{"edit_lock_name", `ALTER TABLE canvases ADD COLUMN edit_lock_name TEXT`},
	}
	for _, col := range cols {
		if columnExists(db, "canvases", col.name) {
			continue
		}
		if _, err := db.Exec(col.ddl); err != nil {
			log.Fatalf("failed to migrate canvases table (add %s): %v", col.name, err)
		}
	}
}

type canvasEditState struct {
	collabEnabled bool
	lockUserID    string
	lockClientID  string
	lockUntil     time.Time
	lockName      string
}

func (s *sqliteStore) readCanvasEditState(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, sceneID string) (*canvasEditState, error) {
	var collab int
	var userID, clientID, name sql.NullString
	var until sql.NullTime
	err := q.QueryRowContext(ctx, `
SELECT collab_enabled, edit_lock_user_id, edit_lock_client_id, edit_lock_until, edit_lock_name
FROM canvases WHERE id = ? LIMIT 1`, sceneID).Scan(&collab, &userID, &clientID, &until, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, core.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	st := &canvasEditState{collabEnabled: collab != 0}
	if userID.Valid {
		st.lockUserID = userID.String
	}
	if clientID.Valid {
		st.lockClientID = clientID.String
	}
	if until.Valid {
		st.lockUntil = until.Time
	}
	if name.Valid {
		st.lockName = name.String
	}
	return st, nil
}

func (st *canvasEditState) lockLive() bool {
	if st == nil || st.collabEnabled {
		return false
	}
	if st.lockUserID == "" {
		return false
	}
	return st.lockUntil.After(time.Now())
}

func (st *canvasEditState) editor(viewerUserID string) *core.SceneEditor {
	if st == nil || !st.lockLive() {
		return nil
	}
	name := strings.TrimSpace(st.lockName)
	if name == "" {
		name = "同事"
	}
	return &core.SceneEditor{
		UserID: st.lockUserID,
		Name:   name,
		IsSelf: st.lockUserID == viewerUserID,
	}
}

func (s *sqliteStore) attachSceneEditState(ctx context.Context, userID string, scene *core.WorkspaceScene) error {
	st, err := s.readCanvasEditState(ctx, s.db, scene.ID)
	if err != nil {
		return err
	}
	scene.CollabEnabled = st.collabEnabled
	scene.Editor = st.editor(userID)
	return nil
}

func (s *sqliteStore) rejectIfExclusiveLockHeld(ctx context.Context, userID, sceneID string) error {
	st, err := s.readCanvasEditState(ctx, s.db, sceneID)
	if errors.Is(err, core.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if st.collabEnabled || !st.lockLive() {
		return nil
	}
	if st.lockUserID == userID {
		return nil
	}
	return &core.SceneLockError{Editor: st.editor(userID)}
}

func (s *sqliteStore) AcquireSceneLock(ctx context.Context, userID, sceneID, clientID, displayName string) (*core.WorkspaceScene, error) {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return nil, core.ErrInvalidInput
	}
	r, canEdit, err := s.sceneAccess(ctx, userID, sceneID)
	if err != nil {
		return nil, err
	}
	if !canEdit {
		return nil, core.ErrForbidden
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	st, err := s.readCanvasEditState(ctx, tx, r.id)
	if err != nil {
		return nil, err
	}
	if !st.collabEnabled && st.lockLive() &&
		(st.lockUserID != userID || st.lockClientID != clientID) {
		return nil, &core.SceneLockError{Editor: st.editor(userID)}
	}

	name := strings.TrimSpace(displayName)
	until := time.Now().UTC().Add(sceneEditLockTTL)
	if st.collabEnabled {
		// 已解锁一起编辑：不占独占锁，只返回当前状态。
	} else if _, err = tx.ExecContext(ctx, `
UPDATE canvases SET
	edit_lock_user_id = ?,
	edit_lock_client_id = ?,
	edit_lock_until = ?,
	edit_lock_name = ?
WHERE user_id = ? AND id = ?`, userID, clientID, until, name, r.ownerID, r.id); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetScene(ctx, userID, sceneID)
}

func (s *sqliteStore) ReleaseSceneLock(ctx context.Context, userID, sceneID, clientID string) error {
	r, canEdit, err := s.sceneAccess(ctx, userID, sceneID)
	if err != nil {
		return err
	}
	if !canEdit {
		return core.ErrForbidden
	}
	_, err = s.db.ExecContext(ctx, `
UPDATE canvases SET
	edit_lock_user_id = NULL,
	edit_lock_client_id = NULL,
	edit_lock_until = NULL,
	edit_lock_name = NULL
WHERE user_id = ? AND id = ? AND edit_lock_user_id = ? AND edit_lock_client_id = ?`,
		r.ownerID, r.id, userID, strings.TrimSpace(clientID))
	return err
}

func (s *sqliteStore) SetSceneCollab(ctx context.Context, userID, sceneID string, enabled bool) (*core.WorkspaceScene, error) {
	r, canEdit, err := s.sceneAccess(ctx, userID, sceneID)
	if err != nil {
		return nil, err
	}
	if !canEdit {
		return nil, core.ErrForbidden
	}
	flag := 0
	if enabled {
		flag = 1
	}
	if _, err = s.db.ExecContext(ctx, `
UPDATE canvases SET
	collab_enabled = ?,
	edit_lock_user_id = NULL,
	edit_lock_client_id = NULL,
	edit_lock_until = NULL,
	edit_lock_name = NULL
WHERE user_id = ? AND id = ?`, flag, r.ownerID, r.id); err != nil {
		return nil, err
	}
	return s.GetScene(ctx, userID, sceneID)
}
