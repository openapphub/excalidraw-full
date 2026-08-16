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

const sceneWriteAccessSQL = ` AND (
	((collection_id IS NULL OR collection_id = '') AND user_id = ?)
	OR EXISTS (
		SELECT 1 FROM shell_collections c
		JOIN shell_members m ON m.workspace_id = c.workspace_id
		WHERE c.id = canvases.collection_id AND m.user_id = ?
		AND m.role IN ('ADMIN', 'MEMBER')
	)
)`

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
	return st.leaseLive()
}

// leaseLive 同时覆盖独占锁和协作模式的持久化主写者租约。
func (st *canvasEditState) leaseLive() bool {
	if st == nil {
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

func (st *canvasEditState) leaseEditor(viewerUserID string) *core.SceneEditor {
	if st == nil || !st.leaseLive() {
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
	if !st.leaseLive() {
		return nil
	}
	clientID := strings.TrimSpace(core.SceneClientIDFromContext(ctx))
	if st.lockUserID == userID && clientID != "" && st.lockClientID == clientID {
		return nil
	}
	return &core.SceneLockError{Editor: st.leaseEditor(userID)}
}

// execSceneMetadataWrite 允许无锁时更新标题等元数据；一旦存在有效独占锁或
// 协作写者租约，则只有对应 clientId 能写。校验与 UPDATE 位于同一 SQL，避免
// “先读锁、后更新”之间被另一客户端抢锁。
func (s *sqliteStore) execSceneMetadataWrite(
	ctx context.Context,
	userID, ownerID, sceneID, setClause string,
	setArgs ...any,
) error {
	clientID := strings.TrimSpace(core.SceneClientIDFromContext(ctx))
	now := time.Now().UTC()
	query := `UPDATE canvases SET ` + setClause + ` WHERE user_id = ? AND id = ? AND (
		edit_lock_user_id IS NULL OR edit_lock_client_id IS NULL OR edit_lock_until IS NULL OR edit_lock_until <= ?
		OR (edit_lock_user_id = ? AND edit_lock_client_id = ?)
	)` + sceneWriteAccessSQL
	args := append([]any{}, setArgs...)
	args = append(args, ownerID, sceneID, now, userID, clientID, userID, userID)
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected > 0 {
		return nil
	}
	if _, _, err := s.sceneAccess(ctx, userID, sceneID); err != nil {
		return err
	}
	st, err := s.readCanvasEditState(ctx, s.db, sceneID)
	if err != nil {
		return err
	}
	return &core.SceneLockError{Editor: st.leaseEditor(userID)}
}

// execSceneContentWrite 原子校验 ACL 之外的写者身份并完成内容更新：
// 独占模式必须持有当前有效锁；协作模式由首个写者取得数据库租约，后续只有
// 同一 clientId 能续租写入，租约过期后其他协作客户端才可接管。
func (s *sqliteStore) execSceneContentWrite(
	ctx context.Context,
	userID, ownerID, sceneID, setClause string,
	setArgs ...any,
) error {
	clientID := strings.TrimSpace(core.SceneClientIDFromContext(ctx))
	if clientID == "" {
		return &core.SceneLockError{Message: "scene write requires a client id"}
	}

	now := time.Now().UTC()
	until := now.Add(sceneEditLockTTL)
	query := `UPDATE canvases SET ` + setClause + `,
		edit_lock_user_id = CASE WHEN collab_enabled = 1 THEN ? ELSE edit_lock_user_id END,
		edit_lock_client_id = CASE WHEN collab_enabled = 1 THEN ? ELSE edit_lock_client_id END,
		edit_lock_until = CASE WHEN collab_enabled = 1 THEN ? ELSE edit_lock_until END
		WHERE user_id = ? AND id = ? AND (
			(collab_enabled = 0 AND edit_lock_user_id = ? AND edit_lock_client_id = ? AND edit_lock_until > ?)
			OR
			(collab_enabled = 1 AND (
				edit_lock_user_id IS NULL OR edit_lock_client_id IS NULL OR edit_lock_until IS NULL OR edit_lock_until <= ?
				OR (edit_lock_user_id = ? AND edit_lock_client_id = ?)
			))
		)` + sceneWriteAccessSQL
	args := append([]any{}, setArgs...)
	args = append(args,
		userID, clientID, until,
		ownerID, sceneID,
		userID, clientID, now,
		now, userID, clientID,
		userID, userID,
	)
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected > 0 {
		return nil
	}
	if _, _, err := s.sceneAccess(ctx, userID, sceneID); err != nil {
		return err
	}

	st, err := s.readCanvasEditState(ctx, s.db, sceneID)
	if err != nil {
		return err
	}
	if st.leaseLive() {
		return &core.SceneLockError{Editor: st.leaseEditor(userID)}
	}
	return &core.SceneLockError{Message: "scene write requires an active edit lock"}
}

// CheckSceneContentWrite 供附件等旁路内容写入复用 Scene 的 ACL 与锁协议。
// 无值更新仍会在协作模式下原子取得/续租主写者，独占模式则要求当前 client
// 已持锁，避免文件覆盖绕过 Scene JSON 的写者约束。
func (s *sqliteStore) CheckSceneContentWrite(ctx context.Context, userID, sceneID string) error {
	r, canEdit, err := s.sceneAccess(ctx, userID, sceneID)
	if err != nil {
		return err
	}
	if !canEdit {
		return core.ErrForbidden
	}
	return s.execSceneContentWrite(ctx, userID, r.ownerID, r.id, `updated_at = updated_at`)
}

// CheckSceneRealtimeWrite 校验可靠 WebSocket 元素广播。协作模式允许所有
// 可编辑成员发送实时增量；独占模式仍要求当前 clientId 持有有效编辑锁。
// 这里只校验广播资格，不取得或续租协作模式的 SQLite 主写者租约。
func (s *sqliteStore) CheckSceneRealtimeWrite(ctx context.Context, userID, sceneID string) error {
	if _, canEdit, err := s.sceneAccess(ctx, userID, sceneID); err != nil {
		return err
	} else if !canEdit {
		return core.ErrForbidden
	}

	state, err := s.readCanvasEditState(ctx, s.db, sceneID)
	if err != nil {
		return err
	}
	if state.collabEnabled {
		return nil
	}

	clientID := strings.TrimSpace(core.SceneClientIDFromContext(ctx))
	if state.leaseLive() && state.lockUserID == userID && clientID != "" && state.lockClientID == clientID {
		return nil
	}
	if state.leaseLive() {
		return &core.SceneLockError{Editor: state.leaseEditor(userID)}
	}
	return &core.SceneLockError{Message: "scene write requires an active edit lock"}
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

	name := strings.TrimSpace(displayName)
	now := time.Now().UTC()
	until := time.Now().UTC().Add(sceneEditLockTTL)
	res, err := s.db.ExecContext(ctx, `
UPDATE canvases SET
	edit_lock_user_id = ?,
	edit_lock_client_id = ?,
	edit_lock_until = ?,
	edit_lock_name = ?
WHERE user_id = ? AND id = ? AND collab_enabled = 0 AND (
	edit_lock_user_id IS NULL OR edit_lock_client_id IS NULL OR edit_lock_until IS NULL OR edit_lock_until <= ?
	OR (edit_lock_user_id = ? AND edit_lock_client_id = ?)
)`+sceneWriteAccessSQL, userID, clientID, until, name, r.ownerID, r.id, now, userID, clientID, userID, userID)
	if err != nil {
		return nil, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		if _, _, err := s.sceneAccess(ctx, userID, sceneID); err != nil {
			return nil, err
		}
		st, err := s.readCanvasEditState(ctx, s.db, r.id)
		if err != nil {
			return nil, err
		}
		if !st.collabEnabled {
			return nil, &core.SceneLockError{Editor: st.leaseEditor(userID)}
		}
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
	clientID := strings.TrimSpace(core.SceneClientIDFromContext(ctx))
	if clientID == "" {
		return nil, &core.SceneLockError{Message: "scene collaboration change requires a client id"}
	}
	now := time.Now().UTC()
	var res sql.Result
	if enabled {
		res, err = s.db.ExecContext(ctx, `
UPDATE canvases SET
	collab_enabled = 1,
	edit_lock_user_id = NULL,
	edit_lock_client_id = NULL,
	edit_lock_until = NULL,
	edit_lock_name = NULL
WHERE user_id = ? AND id = ? AND collab_enabled = 0
	AND edit_lock_user_id = ? AND edit_lock_client_id = ? AND edit_lock_until > ?`+sceneWriteAccessSQL,
			r.ownerID, r.id, userID, clientID, now, userID, userID)
	} else {
		res, err = s.db.ExecContext(ctx, `
UPDATE canvases SET
	collab_enabled = 0,
	edit_lock_user_id = ?,
	edit_lock_client_id = ?,
	edit_lock_until = ?,
	edit_lock_name = NULL
WHERE user_id = ? AND id = ? AND collab_enabled = 1 AND (
	edit_lock_user_id IS NULL OR edit_lock_client_id IS NULL OR edit_lock_until IS NULL OR edit_lock_until <= ?
	OR (edit_lock_user_id = ? AND edit_lock_client_id = ?)
)`+sceneWriteAccessSQL, userID, clientID, now.Add(sceneEditLockTTL), r.ownerID, r.id, now, userID, clientID, userID, userID)
	}
	if err != nil {
		return nil, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		if _, _, err := s.sceneAccess(ctx, userID, sceneID); err != nil {
			return nil, err
		}
		st, err := s.readCanvasEditState(ctx, s.db, sceneID)
		if err != nil {
			return nil, err
		}
		if enabled && st.collabEnabled {
			return s.GetScene(ctx, userID, sceneID)
		}
		if !enabled && !st.collabEnabled && st.leaseLive() && st.lockUserID == userID && st.lockClientID == clientID {
			return s.GetScene(ctx, userID, sceneID)
		}
		if st.leaseLive() {
			return nil, &core.SceneLockError{Editor: st.leaseEditor(userID)}
		}
		if enabled {
			return nil, &core.SceneLockError{Message: "acquire the scene lock before enabling collaboration"}
		}
		return nil, &core.SceneLockError{Message: "scene collaboration state changed concurrently"}
	}
	return s.GetScene(ctx, userID, sceneID)
}
