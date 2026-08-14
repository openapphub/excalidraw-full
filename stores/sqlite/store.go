package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"excalidraw-complete/core"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/sirupsen/logrus"
	_ "modernc.org/sqlite"
)

func sqliteDSN(dataSourceName string) string {
	if strings.Contains(dataSourceName, "busy_timeout") {
		return dataSourceName
	}
	sep := "?"
	if strings.Contains(dataSourceName, "?") {
		sep = "&"
	}
	return dataSourceName + sep + "_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
}

type sqliteStore struct {
	db *sql.DB
}

// NewStore creates a new SQLite-based store.
func NewStore(dataSourceName string) *sqliteStore {
	db, err := sql.Open("sqlite", sqliteDSN(dataSourceName))
	if err != nil {
		log.Fatalf("failed to open sqlite database: %v", err)
	}

	// Initialize table for anonymous documents
	docTableStmt := `CREATE TABLE IF NOT EXISTS documents (id TEXT PRIMARY KEY, data BLOB);`
	if _, err = db.Exec(docTableStmt); err != nil {
		log.Fatalf("failed to create documents table: %v", err)
	}

	// Initialize table for user-owned canvases
	canvasTableStmt := `
CREATE TABLE IF NOT EXISTS canvases (
	id TEXT NOT NULL,
	user_id TEXT NOT NULL,
	name TEXT,
	thumbnail TEXT,
	data BLOB,
	workspace_id TEXT NOT NULL DEFAULT 'default',
	created_at DATETIME,
	updated_at DATETIME,
	PRIMARY KEY (user_id, id)
);`
	if _, err = db.Exec(canvasTableStmt); err != nil {
		log.Fatalf("failed to create canvases table: %v", err)
	}

	// 幂等迁移：老库（无 workspace_id 列）直接 ALTER TABLE 升级
	if !columnExists(db, "canvases", "workspace_id") {
		if _, err = db.Exec(`ALTER TABLE canvases ADD COLUMN workspace_id TEXT NOT NULL DEFAULT 'default'`); err != nil {
			log.Fatalf("failed to migrate canvases table (add workspace_id): %v", err)
		}
		logrus.Info("Migrated canvases table: added workspace_id column")
	}

	ensureCanvasEditLockColumns(db)

	// Initialize table for workspaces (canvas groups)
	// 注意：主键是 (user_id, id) 而非单列 id —— default 分组每用户各一行，
	// 若 id 全局唯一则第二个用户的 default 会被 INSERT OR IGNORE 吞掉（懒创建失效）。
	workspaceTableStmt := `
CREATE TABLE IF NOT EXISTS workspaces (
	id TEXT NOT NULL,
	user_id TEXT NOT NULL,
	name TEXT NOT NULL,
	note TEXT,
	created_at DATETIME,
	updated_at DATETIME,
	PRIMARY KEY (user_id, id)
);`
	if _, err = db.Exec(workspaceTableStmt); err != nil {
		log.Fatalf("failed to create workspaces table: %v", err)
	}

	// 初始化默认分组：为每个已有画布的用户补 default workspace（老库直接升级）。
	// 无任何画布的用户跳过，懒创建由 ListWorkspaces 兜底。
	rows, err := db.Query(`SELECT DISTINCT user_id FROM canvases`)
	if err != nil {
		log.Fatalf("failed to query distinct canvas users: %v", err)
	}
	var users []string
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			rows.Close()
			log.Fatalf("failed to scan distinct canvas user: %v", err)
		}
		users = append(users, userID)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		log.Fatalf("failed to iterate distinct canvas users: %v", err)
	}

	for _, userID := range users {
		if _, err = db.Exec(`INSERT OR IGNORE INTO workspaces (id, user_id, name, note, created_at, updated_at) VALUES (?, ?, ?, '', ?, ?)`,
			core.DefaultWorkspaceID, userID, "默认分组", time.Now(), time.Now()); err != nil {
			log.Fatalf("failed to seed default workspace for user %s: %v", userID, err)
		}
	}
	if len(users) > 0 {
		logrus.WithField("users", len(users)).Info("Seeded default workspaces for existing users")
	}

	// Workspace Shell（阶段 1.5）：建表 + 老分组迁移为集合
	ensureShellSchema(db)

	// 画布评论 + 通知（阶段 4）
	ensureCommentSchema(db)
	ensureLocalAuthSchema(db)

	store := &sqliteStore{db}
	if err = store.MigrateLegacyGroupsToShell(context.Background()); err != nil {
		log.Fatalf("failed to migrate legacy groups to workspace shell: %v", err)
	}

	return store
}

// columnExists checks whether a column exists in a table via PRAGMA table_info.
func columnExists(db *sql.DB, table, column string) bool {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		log.Fatalf("failed to query PRAGMA table_info(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull int
		var dfltValue interface{}
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dfltValue, &pk); err != nil {
			log.Fatalf("failed to scan PRAGMA table_info(%s): %v", table, err)
		}
		if name == column {
			return true
		}
	}
	return false
}

// DocumentStore implementation
func (s *sqliteStore) FindID(ctx context.Context, id string) (*core.Document, error) {
	log := logrus.WithField("document_id", id)
	log.Debug("Retrieving document by ID")
	var data []byte
	err := s.db.QueryRowContext(ctx, "SELECT data FROM documents WHERE id = ?", id).Scan(&data)
	if err != nil {
		if err == sql.ErrNoRows {
			log.WithField("error", "document not found").Warn("Document with specified ID not found")
			return nil, fmt.Errorf("document with id %s not found", id)
		}
		log.WithError(err).Error("Failed to retrieve document")
		return nil, err
	}
	document := core.Document{
		Data: *bytes.NewBuffer(data),
	}
	log.Info("Document retrieved successfully")
	return &document, nil
}

func (s *sqliteStore) Create(ctx context.Context, document *core.Document) (string, error) {
	id := ulid.Make().String()
	data := document.Data.Bytes()
	log := logrus.WithFields(logrus.Fields{
		"document_id": id,
		"data_length": len(data),
	})

	_, err := s.db.ExecContext(ctx, "INSERT INTO documents (id, data) VALUES (?, ?)", id, data)
	if err != nil {
		log.WithError(err).Error("Failed to create document")
		return "", err
	}
	log.Info("Document created successfully")
	return id, nil
}

// CanvasStore implementation
func (s *sqliteStore) List(ctx context.Context, userID string) ([]*core.Canvas, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, name, updated_at, thumbnail, workspace_id FROM canvases WHERE user_id = ?", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var canvases []*core.Canvas
	for rows.Next() {
		var canvas core.Canvas
		canvas.UserID = userID
		if err := rows.Scan(&canvas.ID, &canvas.Name, &canvas.UpdatedAt, &canvas.Thumbnail, &canvas.WorkspaceID); err != nil {
			return nil, err
		}
		canvases = append(canvases, &canvas)
	}
	return canvases, nil
}

func (s *sqliteStore) Get(ctx context.Context, userID, id string) (*core.Canvas, error) {
	r, _, err := s.sceneAccess(ctx, userID, id)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) || errors.Is(err, core.ErrForbidden) {
			return nil, fmt.Errorf("canvas not found")
		}
		return nil, err
	}
	var canvas core.Canvas
	canvas.UserID = r.ownerID
	canvas.ID = id
	err = s.db.QueryRowContext(ctx, "SELECT name, data, created_at, updated_at, thumbnail, workspace_id FROM canvases WHERE user_id = ? AND id = ?", r.ownerID, id).Scan(&canvas.Name, &canvas.Data, &canvas.CreatedAt, &canvas.UpdatedAt, &canvas.Thumbnail, &canvas.WorkspaceID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("canvas not found")
		}
		return nil, err
	}
	return &canvas, nil
}

func (s *sqliteStore) Save(ctx context.Context, canvas *core.Canvas) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() // Rollback on any error

	// 画布未指定分组时默认归入 default
	if canvas.WorkspaceID == "" {
		canvas.WorkspaceID = core.DefaultWorkspaceID
	}

	now := time.Now()
	r, canEdit, accessErr := s.sceneAccess(ctx, canvas.UserID, canvas.ID)
	if accessErr == nil {
		if !canEdit {
			return core.ErrForbidden
		}
		if err := s.rejectIfExclusiveLockHeld(ctx, canvas.UserID, canvas.ID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE canvases SET name = ?, data = ?, updated_at = ?, thumbnail = ?, workspace_id = ? WHERE user_id = ? AND id = ?", canvas.Name, canvas.Data, now, canvas.Thumbnail, canvas.WorkspaceID, r.ownerID, r.id)
	} else if errors.Is(accessErr, core.ErrNotFound) {
		// 禁止把浏览器 IndexedDB UUID 插入 SQLite，否则 Workspace 会混入本地脏数据。
		if isIndexedDBCanvasID(canvas.ID) {
			return fmt.Errorf("refusing to persist indexeddb canvas id")
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO canvases (id, user_id, name, data, created_at, updated_at, thumbnail, workspace_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)", canvas.ID, canvas.UserID, canvas.Name, canvas.Data, now, now, canvas.Thumbnail, canvas.WorkspaceID)
	} else {
		return accessErr
	}

	if err != nil {
		return err
	}

	return tx.Commit()
}

func (s *sqliteStore) Delete(ctx context.Context, userID, id string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM canvases WHERE user_id = ? AND id = ?", userID, id)
	return err
}

// WorkspaceStore implementation
func (s *sqliteStore) ListWorkspaces(ctx context.Context, userID string) ([]*core.Workspace, error) {
	// 懒创建：首次访问（无任何 workspace 行）时补 default 分组
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM workspaces WHERE user_id = ?", userID).Scan(&count); err != nil {
		return nil, err
	}
	if count == 0 {
		now := time.Now()
		if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO workspaces (id, user_id, name, note, created_at, updated_at) VALUES (?, ?, ?, '', ?, ?)`,
			core.DefaultWorkspaceID, userID, "默认分组", now, now); err != nil {
			return nil, err
		}
	}

	rows, err := s.db.QueryContext(ctx, "SELECT id, name, note, created_at, updated_at FROM workspaces WHERE user_id = ? ORDER BY created_at", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var workspaces []*core.Workspace
	for rows.Next() {
		var ws core.Workspace
		if err := rows.Scan(&ws.ID, &ws.Name, &ws.Note, &ws.CreatedAt, &ws.UpdatedAt); err != nil {
			return nil, err
		}
		workspaces = append(workspaces, &ws)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return workspaces, nil
}

func (s *sqliteStore) CreateWorkspace(ctx context.Context, userID, name, note string) (*core.Workspace, error) {
	ws := &core.Workspace{
		ID:   ulid.Make().String(),
		Name: name,
		Note: note,
	}
	now := time.Now()
	ws.CreatedAt = now
	ws.UpdatedAt = now
	if _, err := s.db.ExecContext(ctx, `INSERT INTO workspaces (id, user_id, name, note, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		ws.ID, userID, ws.Name, ws.Note, ws.CreatedAt, ws.UpdatedAt); err != nil {
		return nil, err
	}
	return ws, nil
}

func (s *sqliteStore) UpdateWorkspace(ctx context.Context, userID, id, name, note string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE workspaces SET name = ?, note = ?, updated_at = ? WHERE id = ? AND user_id = ?`,
		name, note, time.Now(), id, userID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("workspace not found")
	}
	return nil
}

func (s *sqliteStore) DeleteWorkspace(ctx context.Context, userID, id string) error {
	if id == core.DefaultWorkspaceID {
		return core.ErrDeleteDefaultWorkspace
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 删除前先把该组画布迁回 default
	if _, err := tx.ExecContext(ctx, `UPDATE canvases SET workspace_id = ? WHERE user_id = ? AND workspace_id = ?`,
		core.DefaultWorkspaceID, userID, id); err != nil {
		return err
	}

	res, err := tx.ExecContext(ctx, `DELETE FROM workspaces WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("workspace not found")
	}
	return tx.Commit()
}

func (s *sqliteStore) MoveCanvasWorkspace(ctx context.Context, userID, canvasID, workspaceID string) error {
	// 目标分组必须存在（default 恒存在）
	var exists int
	if err := s.db.QueryRowContext(ctx, "SELECT 1 FROM workspaces WHERE id = ? AND user_id = ?", workspaceID, userID).Scan(&exists); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("workspace not found")
		}
		return err
	}

	res, err := s.db.ExecContext(ctx, `UPDATE canvases SET workspace_id = ? WHERE user_id = ? AND id = ?`, workspaceID, userID, canvasID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("canvas not found")
	}
	return nil
}
