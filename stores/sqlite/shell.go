package sqlite

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"errors"
	"excalidraw-complete/core"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/sirupsen/logrus"
)

// ---------------------------------------------------------------------------
// Schema
// ---------------------------------------------------------------------------

var shellSchemaStmts = []string{
	`CREATE TABLE IF NOT EXISTS user_profiles (
	id TEXT PRIMARY KEY,
	email TEXT,
	name TEXT,
	avatar_url TEXT,
	updated_at DATETIME
);`,
	`CREATE TABLE IF NOT EXISTS shell_workspaces (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	slug TEXT NOT NULL UNIQUE,
	avatar_url TEXT,
	type TEXT NOT NULL DEFAULT 'PERSONAL',
	owner_user_id TEXT NOT NULL,
	created_at DATETIME,
	updated_at DATETIME
);`,
	`CREATE TABLE IF NOT EXISTS shell_members (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL,
	user_id TEXT NOT NULL,
	role TEXT NOT NULL,
	created_at DATETIME,
	UNIQUE(workspace_id, user_id)
);`,
	`CREATE TABLE IF NOT EXISTS shell_collections (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL,
	user_id TEXT NOT NULL,
	name TEXT NOT NULL,
	icon TEXT,
	color TEXT,
	is_private INTEGER NOT NULL DEFAULT 0,
	created_at DATETIME,
	updated_at DATETIME
);`,
	`CREATE TABLE IF NOT EXISTS shell_invite_links (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL,
	code TEXT NOT NULL UNIQUE,
	role TEXT NOT NULL,
	expires_at DATETIME,
	max_uses INTEGER,
	uses INTEGER NOT NULL DEFAULT 0,
	created_at DATETIME
);`,
	// 迁移标记：老分组 → 集合的迁移按用户粒度只跑一次
	`CREATE TABLE IF NOT EXISTS shell_migrated_users (
	user_id TEXT PRIMARY KEY,
	migrated_at DATETIME
);`,
	`CREATE INDEX IF NOT EXISTS idx_shell_members_user ON shell_members(user_id);`,
	`CREATE INDEX IF NOT EXISTS idx_shell_collections_workspace ON shell_collections(workspace_id);`,
	`CREATE INDEX IF NOT EXISTS idx_shell_collections_user ON shell_collections(user_id);`,
}

// ensureShellSchema 幂等建表，并为 canvases 补 collection_id 列。
func ensureShellSchema(db *sql.DB) {
	for _, stmt := range shellSchemaStmts {
		if _, err := db.Exec(stmt); err != nil {
			log.Fatalf("failed to create shell schema: %v", err)
		}
	}

	if !columnExists(db, "canvases", "collection_id") {
		if _, err := db.Exec(`ALTER TABLE canvases ADD COLUMN collection_id TEXT`); err != nil {
			log.Fatalf("failed to migrate canvases table (add collection_id): %v", err)
		}
		logrus.Info("Migrated canvases table: added collection_id column")
	}

	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_canvases_collection ON canvases(collection_id)`); err != nil {
		log.Fatalf("failed to create canvases collection index: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 通用小工具
// ---------------------------------------------------------------------------

// sqlExecutor 同时被 *sql.DB 与 *sql.Tx 满足，便于事务内外复用同一段逻辑。
type sqlExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// rowScanner 覆盖 *sql.Row 与 *sql.Rows。
type rowScanner interface {
	Scan(dest ...any) error
}

// nullable 把空字符串指针折叠成 SQL NULL。
func nullable(p *string) any {
	if p == nil || *p == "" {
		return nil
	}
	return *p
}

func nsPtr(ns sql.NullString) *string {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	v := ns.String
	return &v
}

func nsString(ns sql.NullString) string {
	if !ns.Valid {
		return ""
	}
	return ns.String
}

func ntTime(nt sql.NullTime) time.Time {
	if !nt.Valid {
		return time.Time{}
	}
	return nt.Time
}

func ntPtr(nt sql.NullTime) *time.Time {
	if !nt.Valid {
		return nil
	}
	v := nt.Time
	return &v
}

// slugify 生成 URL 安全片段。
func slugify(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		case r == '-' || r == '_' || r == ' ':
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// shortID 取 ULID 尾部若干字符作为可读短标识。
func shortID() string {
	id := ulid.Make().String()
	return strings.ToLower(id[len(id)-8:])
}

// uniqueSlug 在 shell_workspaces 中找一个未被占用的 slug。
func uniqueSlug(ctx context.Context, ex sqlExecutor, base string) (string, error) {
	base = slugify(base)
	if base == "" {
		base = "workspace"
	}
	candidate := base
	for i := 1; i <= 50; i++ {
		var one int
		err := ex.QueryRowContext(ctx, `SELECT 1 FROM shell_workspaces WHERE slug = ?`, candidate).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
		candidate = fmt.Sprintf("%s-%d", base, i)
	}
	return base + "-" + shortID(), nil
}

func newInviteCode() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf)
	return strings.ToLower(enc), nil
}

// ---------------------------------------------------------------------------
// 权限
// ---------------------------------------------------------------------------

// memberRole 返回用户在工作区中的角色，非成员返回 core.ErrForbidden。
func (s *sqliteStore) memberRole(ctx context.Context, userID, workspaceID string) (core.WorkspaceRole, error) {
	var role string
	err := s.db.QueryRowContext(ctx,
		`SELECT role FROM shell_members WHERE workspace_id = ? AND user_id = ?`, workspaceID, userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", core.ErrForbidden
	}
	if err != nil {
		return "", err
	}
	return core.WorkspaceRole(role), nil
}

// resolveRole 在非成员时区分“工作区不存在”与“无权限”。
func (s *sqliteStore) resolveRole(ctx context.Context, userID, workspaceID string) (core.WorkspaceRole, error) {
	role, err := s.memberRole(ctx, userID, workspaceID)
	if errors.Is(err, core.ErrForbidden) {
		var one int
		qerr := s.db.QueryRowContext(ctx, `SELECT 1 FROM shell_workspaces WHERE id = ?`, workspaceID).Scan(&one)
		if errors.Is(qerr, sql.ErrNoRows) {
			return "", core.ErrNotFound
		}
		if qerr != nil {
			return "", qerr
		}
	}
	return role, err
}

// roleCanWrite: ADMIN/MEMBER 可写集合与场景，VIEWER 只读。
func roleCanWrite(role core.WorkspaceRole) bool {
	return role == core.RoleAdmin || role == core.RoleMember
}

func requireWrite(role core.WorkspaceRole) error {
	if roleCanWrite(role) {
		return nil
	}
	return core.ErrForbidden
}

func requireAdmin(role core.WorkspaceRole) error {
	if role == core.RoleAdmin {
		return nil
	}
	return core.ErrForbidden
}

// ---------------------------------------------------------------------------
// 工作区
// ---------------------------------------------------------------------------

const shellWorkspaceCols = `w.id, w.name, w.slug, w.avatar_url, w.type, w.created_at, w.updated_at,
	(SELECT COUNT(*) FROM shell_members m2 WHERE m2.workspace_id = w.id)`

func scanShellWorkspace(sc rowScanner, role core.WorkspaceRole) (*core.ShellWorkspace, error) {
	var (
		ws               core.ShellWorkspace
		avatar           sql.NullString
		typ              string
		created, updated sql.NullTime
	)
	if err := sc.Scan(&ws.ID, &ws.Name, &ws.Slug, &avatar, &typ, &created, &updated, &ws.MemberCount); err != nil {
		return nil, err
	}
	ws.AvatarURL = nsPtr(avatar)
	ws.Type = core.WorkspaceType(typ)
	ws.Role = role
	ws.CreatedAt = ntTime(created)
	ws.UpdatedAt = ntTime(updated)
	return &ws, nil
}

// createPersonalWorkspace 建个人工作区 + ADMIN 成员，已存在则复用。
// 返回工作区 id 与其私有集合 id（未创建时为空）。
func createPersonalWorkspace(ctx context.Context, ex sqlExecutor, userID, displayName string, withPrivateCollection bool) (string, string, error) {
	var wsID string
	err := ex.QueryRowContext(ctx,
		`SELECT id FROM shell_workspaces WHERE type = ? AND owner_user_id = ? ORDER BY created_at LIMIT 1`,
		string(core.WorkspacePersonal), userID).Scan(&wsID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", "", err
	}

	now := time.Now().UTC()

	if errors.Is(err, sql.ErrNoRows) {
		name := "My Workspace"
		if strings.TrimSpace(displayName) != "" {
			name = strings.TrimSpace(displayName) + "'s Workspace"
		}
		slug, serr := uniqueSlug(ctx, ex, "personal-"+shortID())
		if serr != nil {
			return "", "", serr
		}
		wsID = ulid.Make().String()
		if _, err := ex.ExecContext(ctx,
			`INSERT INTO shell_workspaces (id, name, slug, avatar_url, type, owner_user_id, created_at, updated_at)
			 VALUES (?, ?, ?, NULL, ?, ?, ?, ?)`,
			wsID, name, slug, string(core.WorkspacePersonal), userID, now, now); err != nil {
			return "", "", err
		}
	}

	if _, err := ex.ExecContext(ctx,
		`INSERT OR IGNORE INTO shell_members (id, workspace_id, user_id, role, created_at) VALUES (?, ?, ?, ?, ?)`,
		ulid.Make().String(), wsID, userID, string(core.RoleAdmin), now); err != nil {
		return "", "", err
	}

	// 默认集合（不再创建 is_private=1 的 Private 类型）
	var privateID string
	perr := ex.QueryRowContext(ctx,
		`SELECT id FROM shell_collections WHERE workspace_id = ? ORDER BY created_at LIMIT 1`,
		wsID).Scan(&privateID)
	if perr != nil && !errors.Is(perr, sql.ErrNoRows) {
		return "", "", perr
	}

	if privateID == "" && withPrivateCollection {
		privateID = ulid.Make().String()
		if _, err := ex.ExecContext(ctx,
			`INSERT INTO shell_collections (id, workspace_id, user_id, name, icon, color, is_private, created_at, updated_at)
			 VALUES (?, ?, ?, ?, NULL, NULL, 0, ?, ?)`,
			privateID, wsID, userID, "默认", now, now); err != nil {
			return "", "", err
		}
	}

	return wsID, privateID, nil
}

func (s *sqliteStore) EnsurePersonalWorkspace(ctx context.Context, userID, displayName string) (*core.ShellWorkspace, error) {
	if userID == "" {
		return nil, core.ErrInvalidInput
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	wsID, _, err := createPersonalWorkspace(ctx, tx, userID, displayName, true)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetShellWorkspace(ctx, userID, wsID)
}

func (s *sqliteStore) ListShellWorkspaces(ctx context.Context, userID string) ([]*core.ShellWorkspace, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+shellWorkspaceCols+`, m.role
		FROM shell_workspaces w
		JOIN shell_members m ON m.workspace_id = w.id
		WHERE m.user_id = ?
		ORDER BY CASE WHEN w.type = 'PERSONAL' THEN 0 ELSE 1 END, w.created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	workspaces := make([]*core.ShellWorkspace, 0)
	for rows.Next() {
		var (
			ws               core.ShellWorkspace
			avatar           sql.NullString
			typ, role        string
			created, updated sql.NullTime
		)
		if err := rows.Scan(&ws.ID, &ws.Name, &ws.Slug, &avatar, &typ, &created, &updated, &ws.MemberCount, &role); err != nil {
			return nil, err
		}
		ws.AvatarURL = nsPtr(avatar)
		ws.Type = core.WorkspaceType(typ)
		ws.Role = core.WorkspaceRole(role)
		ws.CreatedAt = ntTime(created)
		ws.UpdatedAt = ntTime(updated)
		workspaces = append(workspaces, &ws)
	}
	return workspaces, rows.Err()
}

func (s *sqliteStore) GetShellWorkspace(ctx context.Context, userID, workspaceID string) (*core.ShellWorkspace, error) {
	role, err := s.resolveRole(ctx, userID, workspaceID)
	if err != nil {
		return nil, err
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+shellWorkspaceCols+` FROM shell_workspaces w WHERE w.id = ?`, workspaceID)
	ws, err := scanShellWorkspace(row, role)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, core.ErrNotFound
	}
	return ws, err
}

func (s *sqliteStore) CreateShellWorkspace(ctx context.Context, userID, name, slug string, typ core.WorkspaceType) (*core.ShellWorkspace, error) {
	if userID == "" || strings.TrimSpace(name) == "" {
		return nil, core.ErrInvalidInput
	}
	if typ == "" {
		typ = core.WorkspaceShared
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if slug == "" {
		slug = name
	}
	finalSlug, err := uniqueSlug(ctx, tx, slug)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	wsID := ulid.Make().String()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO shell_workspaces (id, name, slug, avatar_url, type, owner_user_id, created_at, updated_at)
		 VALUES (?, ?, ?, NULL, ?, ?, ?, ?)`,
		wsID, strings.TrimSpace(name), finalSlug, string(typ), userID, now, now); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO shell_members (id, workspace_id, user_id, role, created_at) VALUES (?, ?, ?, ?, ?)`,
		ulid.Make().String(), wsID, userID, string(core.RoleAdmin), now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetShellWorkspace(ctx, userID, wsID)
}

func (s *sqliteStore) UpdateShellWorkspace(ctx context.Context, userID, workspaceID string, name, slug *string) (*core.ShellWorkspace, error) {
	role, err := s.resolveRole(ctx, userID, workspaceID)
	if err != nil {
		return nil, err
	}
	if err := requireAdmin(role); err != nil {
		return nil, err
	}

	if name != nil && strings.TrimSpace(*name) != "" {
		if _, err := s.db.ExecContext(ctx, `UPDATE shell_workspaces SET name = ?, updated_at = ? WHERE id = ?`,
			strings.TrimSpace(*name), time.Now().UTC(), workspaceID); err != nil {
			return nil, err
		}
	}
	if slug != nil && slugify(*slug) != "" {
		desired := slugify(*slug)
		var one int
		err := s.db.QueryRowContext(ctx, `SELECT 1 FROM shell_workspaces WHERE slug = ? AND id <> ?`, desired, workspaceID).Scan(&one)
		if err == nil {
			return nil, core.ErrConflict
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE shell_workspaces SET slug = ?, updated_at = ? WHERE id = ?`,
			desired, time.Now().UTC(), workspaceID); err != nil {
			return nil, err
		}
	}
	return s.GetShellWorkspace(ctx, userID, workspaceID)
}

func (s *sqliteStore) UpdateShellWorkspaceAvatar(ctx context.Context, userID, workspaceID, avatarURL string) (*core.ShellWorkspace, error) {
	role, err := s.resolveRole(ctx, userID, workspaceID)
	if err != nil {
		return nil, err
	}
	if err := requireAdmin(role); err != nil {
		return nil, err
	}
	if strings.TrimSpace(avatarURL) == "" {
		return nil, core.ErrInvalidInput
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE shell_workspaces SET avatar_url = ?, updated_at = ? WHERE id = ?`,
		avatarURL, time.Now().UTC(), workspaceID); err != nil {
		return nil, err
	}
	return s.GetShellWorkspace(ctx, userID, workspaceID)
}

func (s *sqliteStore) DeleteShellWorkspace(ctx context.Context, userID, workspaceID string) error {
	role, err := s.resolveRole(ctx, userID, workspaceID)
	if err != nil {
		return err
	}
	if err := requireAdmin(role); err != nil {
		return err
	}

	var typ string
	if err := s.db.QueryRowContext(ctx, `SELECT type FROM shell_workspaces WHERE id = ?`, workspaceID).Scan(&typ); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.ErrNotFound
		}
		return err
	}
	if core.WorkspaceType(typ) == core.WorkspacePersonal {
		return core.ErrDeletePersonal
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 场景本体保留（仍归属各自 owner），仅解除集合归属
	if _, err := tx.ExecContext(ctx,
		`UPDATE canvases SET collection_id = NULL WHERE collection_id IN (SELECT id FROM shell_collections WHERE workspace_id = ?)`,
		workspaceID); err != nil {
		return err
	}
	for _, stmt := range []string{
		`DELETE FROM shell_collections WHERE workspace_id = ?`,
		`DELETE FROM shell_invite_links WHERE workspace_id = ?`,
		`DELETE FROM shell_members WHERE workspace_id = ?`,
		`DELETE FROM shell_workspaces WHERE id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, stmt, workspaceID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ---------------------------------------------------------------------------
// 成员
// ---------------------------------------------------------------------------

func (s *sqliteStore) ListMembers(ctx context.Context, userID, workspaceID string) ([]*core.WorkspaceMember, error) {
	if _, err := s.resolveRole(ctx, userID, workspaceID); err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT m.id, m.role, m.user_id, m.created_at, p.email, p.name, p.avatar_url
		 FROM shell_members m
		 LEFT JOIN user_profiles p ON p.id = m.user_id
		 WHERE m.workspace_id = ?
		 ORDER BY m.created_at`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	members := make([]*core.WorkspaceMember, 0)
	for rows.Next() {
		m, err := scanMember(rows)
		if err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

func scanMember(sc rowScanner) (*core.WorkspaceMember, error) {
	var (
		m                   core.WorkspaceMember
		role                string
		created             sql.NullTime
		email, name, avatar sql.NullString
	)
	if err := sc.Scan(&m.ID, &role, &m.UserID, &created, &email, &name, &avatar); err != nil {
		return nil, err
	}
	m.Role = core.WorkspaceRole(role)
	m.CreatedAt = ntTime(created)
	m.User = core.MemberUser{
		ID:        m.UserID,
		Email:     nsString(email),
		Name:      nsPtr(name),
		AvatarURL: nsPtr(avatar),
	}
	return &m, nil
}

func (s *sqliteStore) getMember(ctx context.Context, workspaceID, memberID string) (*core.WorkspaceMember, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT m.id, m.role, m.user_id, m.created_at, p.email, p.name, p.avatar_url
		 FROM shell_members m
		 LEFT JOIN user_profiles p ON p.id = m.user_id
		 WHERE m.workspace_id = ? AND (m.id = ? OR m.user_id = ?)`, workspaceID, memberID, memberID)
	m, err := scanMember(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, core.ErrNotFound
	}
	return m, err
}

func (s *sqliteStore) InviteMember(ctx context.Context, actorID, workspaceID, email string, role core.WorkspaceRole) (*core.WorkspaceMember, error) {
	actorRole, err := s.resolveRole(ctx, actorID, workspaceID)
	if err != nil {
		return nil, err
	}
	if err := requireAdmin(actorRole); err != nil {
		return nil, err
	}
	if strings.TrimSpace(email) == "" {
		return nil, core.ErrInvalidInput
	}
	if role == "" {
		role = core.RoleMember
	}

	var targetID string
	err = s.db.QueryRowContext(ctx, `SELECT id FROM user_profiles WHERE lower(email) = lower(?) LIMIT 1`,
		strings.TrimSpace(email)).Scan(&targetID)
	if errors.Is(err, sql.ErrNoRows) {
		// 该邮箱尚未登录过本实例，handler 可提示改用邀请链接
		return nil, fmt.Errorf("%w: no user with email %s", core.ErrNotFound, email)
	}
	if err != nil {
		return nil, err
	}

	var one int
	err = s.db.QueryRowContext(ctx, `SELECT 1 FROM shell_members WHERE workspace_id = ? AND user_id = ?`, workspaceID, targetID).Scan(&one)
	if err == nil {
		return nil, core.ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	memberID := ulid.Make().String()
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO shell_members (id, workspace_id, user_id, role, created_at) VALUES (?, ?, ?, ?, ?)`,
		memberID, workspaceID, targetID, string(role), time.Now().UTC()); err != nil {
		return nil, err
	}
	return s.getMember(ctx, workspaceID, memberID)
}

func (s *sqliteStore) UpdateMemberRole(ctx context.Context, actorID, workspaceID, memberID string, role core.WorkspaceRole) (*core.WorkspaceMember, error) {
	actorRole, err := s.resolveRole(ctx, actorID, workspaceID)
	if err != nil {
		return nil, err
	}
	if err := requireAdmin(actorRole); err != nil {
		return nil, err
	}
	switch role {
	case core.RoleAdmin, core.RoleMember, core.RoleViewer:
	default:
		return nil, core.ErrInvalidInput
	}

	member, err := s.getMember(ctx, workspaceID, memberID)
	if err != nil {
		return nil, err
	}
	owner, err := s.workspaceOwner(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	if member.UserID == owner {
		return nil, core.ErrForbidden
	}

	if _, err := s.db.ExecContext(ctx, `UPDATE shell_members SET role = ? WHERE id = ?`, string(role), member.ID); err != nil {
		return nil, err
	}
	return s.getMember(ctx, workspaceID, member.ID)
}

func (s *sqliteStore) RemoveMember(ctx context.Context, actorID, workspaceID, memberID string) error {
	actorRole, err := s.resolveRole(ctx, actorID, workspaceID)
	if err != nil {
		return err
	}
	member, err := s.getMember(ctx, workspaceID, memberID)
	if err != nil {
		return err
	}
	// 管理员可移除他人，普通成员只能退出自己
	if member.UserID != actorID {
		if err := requireAdmin(actorRole); err != nil {
			return err
		}
	}
	owner, err := s.workspaceOwner(ctx, workspaceID)
	if err != nil {
		return err
	}
	if member.UserID == owner {
		return core.ErrForbidden
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM shell_members WHERE id = ?`, member.ID)
	return err
}

func (s *sqliteStore) workspaceOwner(ctx context.Context, workspaceID string) (string, error) {
	var owner string
	err := s.db.QueryRowContext(ctx, `SELECT owner_user_id FROM shell_workspaces WHERE id = ?`, workspaceID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", core.ErrNotFound
	}
	return owner, err
}

// ---------------------------------------------------------------------------
// 邀请链接
// ---------------------------------------------------------------------------

func scanInviteLink(sc rowScanner) (*core.InviteLink, error) {
	var (
		l       core.InviteLink
		role    string
		expires sql.NullTime
		maxUses sql.NullInt64
		created sql.NullTime
	)
	if err := sc.Scan(&l.ID, &l.WorkspaceID, &l.Code, &role, &expires, &maxUses, &l.Uses, &created); err != nil {
		return nil, err
	}
	l.Role = core.WorkspaceRole(role)
	l.ExpiresAt = ntPtr(expires)
	if maxUses.Valid {
		v := int(maxUses.Int64)
		l.MaxUses = &v
	}
	l.CreatedAt = ntTime(created)
	return &l, nil
}

const inviteLinkCols = `id, workspace_id, code, role, expires_at, max_uses, uses, created_at`

func (s *sqliteStore) ListInviteLinks(ctx context.Context, userID, workspaceID string) ([]*core.InviteLink, error) {
	role, err := s.resolveRole(ctx, userID, workspaceID)
	if err != nil {
		return nil, err
	}
	if err := requireAdmin(role); err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT `+inviteLinkCols+` FROM shell_invite_links WHERE workspace_id = ? ORDER BY created_at DESC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	links := make([]*core.InviteLink, 0)
	for rows.Next() {
		l, err := scanInviteLink(rows)
		if err != nil {
			return nil, err
		}
		links = append(links, l)
	}
	return links, rows.Err()
}

func (s *sqliteStore) CreateInviteLink(ctx context.Context, userID, workspaceID string, role core.WorkspaceRole, expiresAt *time.Time, maxUses *int) (*core.InviteLink, error) {
	actorRole, err := s.resolveRole(ctx, userID, workspaceID)
	if err != nil {
		return nil, err
	}
	if err := requireAdmin(actorRole); err != nil {
		return nil, err
	}
	if role == "" {
		role = core.RoleMember
	}

	code, err := newInviteCode()
	if err != nil {
		return nil, err
	}
	var maxUsesArg any
	if maxUses != nil {
		maxUsesArg = *maxUses
	}
	var expiresArg any
	if expiresAt != nil {
		expiresArg = expiresAt.UTC()
	}

	id := ulid.Make().String()
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO shell_invite_links (id, workspace_id, code, role, expires_at, max_uses, uses, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, 0, ?)`,
		id, workspaceID, code, string(role), expiresArg, maxUsesArg, time.Now().UTC()); err != nil {
		return nil, err
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+inviteLinkCols+` FROM shell_invite_links WHERE id = ?`, id)
	return scanInviteLink(row)
}

func (s *sqliteStore) DeleteInviteLink(ctx context.Context, userID, workspaceID, linkID string) error {
	role, err := s.resolveRole(ctx, userID, workspaceID)
	if err != nil {
		return err
	}
	if err := requireAdmin(role); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM shell_invite_links WHERE id = ? AND workspace_id = ?`, linkID, workspaceID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return core.ErrNotFound
	}
	return nil
}

func (s *sqliteStore) JoinViaInviteLink(ctx context.Context, userID, code string, user core.MemberUser) (*core.ShellWorkspace, error) {
	if userID == "" || strings.TrimSpace(code) == "" {
		return nil, core.ErrInvalidInput
	}
	if user.ID == "" {
		user.ID = userID
	}
	if err := s.UpsertUserProfile(ctx, user); err != nil {
		return nil, err
	}

	row := s.db.QueryRowContext(ctx, `SELECT `+inviteLinkCols+` FROM shell_invite_links WHERE code = ?`, strings.TrimSpace(code))
	link, err := scanInviteLink(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, core.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	// 已是成员：直接返回，不消耗次数，也不受过期/次数限制影响
	if _, err := s.memberRole(ctx, userID, link.WorkspaceID); err == nil {
		return s.GetShellWorkspace(ctx, userID, link.WorkspaceID)
	} else if !errors.Is(err, core.ErrForbidden) {
		return nil, err
	}

	if link.ExpiresAt != nil && time.Now().After(*link.ExpiresAt) {
		return nil, fmt.Errorf("%w: invite link expired", core.ErrForbidden)
	}
	if link.MaxUses != nil && link.Uses >= *link.MaxUses {
		return nil, fmt.Errorf("%w: invite link exhausted", core.ErrForbidden)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO shell_members (id, workspace_id, user_id, role, created_at) VALUES (?, ?, ?, ?, ?)`,
		ulid.Make().String(), link.WorkspaceID, userID, string(link.Role), time.Now().UTC()); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE shell_invite_links SET uses = uses + 1 WHERE id = ?`, link.ID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE shell_workspaces SET type = 'SHARED', updated_at = ? WHERE id = ? AND type = 'PERSONAL'`,
		time.Now().UTC(), link.WorkspaceID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetShellWorkspace(ctx, userID, link.WorkspaceID)
}

// ---------------------------------------------------------------------------
// 集合
// ---------------------------------------------------------------------------

const shellCollectionCols = `c.id, c.workspace_id, c.user_id, c.name, c.icon, c.color, c.is_private, c.created_at, c.updated_at,
	(SELECT COUNT(*) FROM canvases cv WHERE cv.collection_id = c.id)`

func scanCollection(sc rowScanner) (*core.Collection, error) {
	var (
		c                core.Collection
		icon, color      sql.NullString
		isPrivate        int
		created, updated sql.NullTime
	)
	if err := sc.Scan(&c.ID, &c.WorkspaceID, &c.UserID, &c.Name, &icon, &color, &isPrivate, &created, &updated, &c.SceneCount); err != nil {
		return nil, err
	}
	c.Icon = nsPtr(icon)
	c.Color = nsPtr(color)
	c.IsPrivate = isPrivate != 0
	c.CreatedAt = ntTime(created)
	c.UpdatedAt = ntTime(updated)
	return &c, nil
}

// collectionAccess 载入集合并校验访问权限，同时回填 CanWrite/IsOwner。
func (s *sqliteStore) collectionAccess(ctx context.Context, userID, collectionID string) (*core.Collection, core.WorkspaceRole, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+shellCollectionCols+` FROM shell_collections c WHERE c.id = ?`, collectionID)
	coll, err := scanCollection(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", core.ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}

	role, err := s.memberRole(ctx, userID, coll.WorkspaceID)
	if err != nil {
		return nil, "", err
	}
	coll.IsOwner = coll.UserID == userID
	coll.CanWrite = roleCanWrite(role)
	return coll, role, nil
}

func (s *sqliteStore) ListCollections(ctx context.Context, userID, workspaceID string) ([]*core.Collection, error) {
	role, err := s.resolveRole(ctx, userID, workspaceID)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx, `SELECT `+shellCollectionCols+`
		FROM shell_collections c
		WHERE c.workspace_id = ?
		ORDER BY c.created_at`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	collections := make([]*core.Collection, 0)
	for rows.Next() {
		coll, err := scanCollection(rows)
		if err != nil {
			return nil, err
		}
		coll.IsOwner = coll.UserID == userID
		coll.CanWrite = roleCanWrite(role)
		collections = append(collections, coll)
	}
	return collections, rows.Err()
}

func (s *sqliteStore) CreateCollection(ctx context.Context, userID, workspaceID, name string, icon, color *string, isPrivate bool) (*core.Collection, error) {
	role, err := s.resolveRole(ctx, userID, workspaceID)
	if err != nil {
		return nil, err
	}
	if err := requireWrite(role); err != nil {
		return nil, err
	}
	if strings.TrimSpace(name) == "" {
		return nil, core.ErrInvalidInput
	}

	now := time.Now().UTC()
	id := ulid.Make().String()
	// 2A：集合不再区分 Private/共享，一律工作区内可见
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO shell_collections (id, workspace_id, user_id, name, icon, color, is_private, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?)`,
		id, workspaceID, userID, strings.TrimSpace(name), nullable(icon), nullable(color), now, now); err != nil {
		return nil, err
	}
	return s.GetCollection(ctx, userID, id)
}

func (s *sqliteStore) GetCollection(ctx context.Context, userID, collectionID string) (*core.Collection, error) {
	coll, _, err := s.collectionAccess(ctx, userID, collectionID)
	return coll, err
}

func (s *sqliteStore) UpdateCollection(ctx context.Context, userID, collectionID string, name, icon, color *string, isPrivate *bool) (*core.Collection, error) {
	_, role, err := s.collectionAccess(ctx, userID, collectionID)
	if err != nil {
		return nil, err
	}
	if err := requireWrite(role); err != nil {
		return nil, err
	}

	sets := []string{}
	args := []any{}
	if name != nil && strings.TrimSpace(*name) != "" {
		sets = append(sets, "name = ?")
		args = append(args, strings.TrimSpace(*name))
	}
	if icon != nil {
		sets = append(sets, "icon = ?")
		args = append(args, nullable(icon))
	}
	if color != nil {
		sets = append(sets, "color = ?")
		args = append(args, nullable(color))
	}
	_ = isPrivate
	if len(sets) > 0 {
		sets = append(sets, "updated_at = ?")
		args = append(args, time.Now().UTC(), collectionID)
		if _, err := s.db.ExecContext(ctx,
			`UPDATE shell_collections SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...); err != nil {
			return nil, err
		}
	}
	return s.GetCollection(ctx, userID, collectionID)
}

func (s *sqliteStore) DeleteCollection(ctx context.Context, userID, collectionID string) error {
	coll, role, err := s.collectionAccess(ctx, userID, collectionID)
	if err != nil {
		return err
	}
	if err := requireWrite(role); err != nil {
		return err
	}
	// 非所有者需管理员权限
	if !coll.IsOwner {
		if err := requireAdmin(role); err != nil {
			return err
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 场景不随集合删除，先落回“未归类”
	if _, err := tx.ExecContext(ctx, `UPDATE canvases SET collection_id = NULL WHERE collection_id = ?`, collectionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM shell_collections WHERE id = ?`, collectionID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *sqliteStore) CopyCollectionToWorkspace(ctx context.Context, userID, collectionID, targetWorkspaceID string) (*core.Collection, error) {
	source, _, err := s.collectionAccess(ctx, userID, collectionID)
	if err != nil {
		return nil, err
	}
	targetRole, err := s.resolveRole(ctx, userID, targetWorkspaceID)
	if err != nil {
		return nil, err
	}
	if err := requireWrite(targetRole); err != nil {
		return nil, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	newID := ulid.Make().String()
	private := 0
	if source.IsPrivate {
		private = 1
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO shell_collections (id, workspace_id, user_id, name, icon, color, is_private, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		newID, targetWorkspaceID, userID, source.Name, nullable(source.Icon), nullable(source.Color), private, now, now); err != nil {
		return nil, err
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT name, thumbnail, data FROM canvases WHERE collection_id = ?`, collectionID)
	if err != nil {
		return nil, err
	}
	type copyRow struct {
		name      sql.NullString
		thumbnail sql.NullString
		data      []byte
	}
	var copies []copyRow
	for rows.Next() {
		var cr copyRow
		if err := rows.Scan(&cr.name, &cr.thumbnail, &cr.data); err != nil {
			rows.Close()
			return nil, err
		}
		copies = append(copies, cr)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, cr := range copies {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO canvases (id, user_id, name, thumbnail, data, workspace_id, collection_id, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			ulid.Make().String(), userID, nsString(cr.name), nsString(cr.thumbnail), cr.data,
			core.DefaultWorkspaceID, newID, now, now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetCollection(ctx, userID, newID)
}

func (s *sqliteStore) MoveCollectionToWorkspace(ctx context.Context, userID, collectionID, targetWorkspaceID string) (*core.Collection, error) {
	source, sourceRole, err := s.collectionAccess(ctx, userID, collectionID)
	if err != nil {
		return nil, err
	}
	if !source.IsOwner {
		if err := requireAdmin(sourceRole); err != nil {
			return nil, err
		}
	}
	targetRole, err := s.resolveRole(ctx, userID, targetWorkspaceID)
	if err != nil {
		return nil, err
	}
	if err := requireWrite(targetRole); err != nil {
		return nil, err
	}

	if _, err := s.db.ExecContext(ctx, `UPDATE shell_collections SET workspace_id = ?, updated_at = ? WHERE id = ?`,
		targetWorkspaceID, time.Now().UTC(), collectionID); err != nil {
		return nil, err
	}
	return s.GetCollection(ctx, userID, collectionID)
}

// ---------------------------------------------------------------------------
// 场景（scene id == canvas id）
// ---------------------------------------------------------------------------

const sceneCols = `id, user_id, name, thumbnail, collection_id, created_at, updated_at`

type sceneRow struct {
	id           string
	ownerID      string
	name         sql.NullString
	thumbnail    sql.NullString
	collectionID sql.NullString
	createdAt    sql.NullTime
	updatedAt    sql.NullTime
}

func scanSceneRow(sc rowScanner) (*sceneRow, error) {
	var r sceneRow
	if err := sc.Scan(&r.id, &r.ownerID, &r.name, &r.thumbnail, &r.collectionID, &r.createdAt, &r.updatedAt); err != nil {
		return nil, err
	}
	return &r, nil
}

func (r *sceneRow) toScene(canEdit bool) *core.WorkspaceScene {
	updated := ntTime(r.updatedAt)
	scene := &core.WorkspaceScene{
		ID:           r.id,
		Title:        nsString(r.name),
		ThumbnailURL: nsPtr(r.thumbnail),
		StorageKey:   r.id,
		CollectionID: nsPtr(r.collectionID),
		CreatedAt:    ntTime(r.createdAt),
		UpdatedAt:    updated,
		CanEdit:      canEdit,
	}
	// canvases 无独立 last_opened_at 列，用 updated_at 近似供“最近”视图排序
	if !updated.IsZero() {
		scene.LastOpenedAt = &updated
	}
	return scene
}

// sceneAccess 定位场景并计算是否可编辑。
func (s *sqliteStore) sceneAccess(ctx context.Context, userID, sceneID string) (*sceneRow, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+sceneCols+` FROM canvases WHERE id = ? LIMIT 1`, sceneID)
	r, err := scanSceneRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, core.ErrNotFound
	}
	if err != nil {
		return nil, false, err
	}
	if r.ownerID == userID {
		return r, true, nil
	}
	// 他人场景：必须通过所在集合的工作区成员身份访问
	if !r.collectionID.Valid || r.collectionID.String == "" {
		return nil, false, core.ErrForbidden
	}
	_, role, err := s.collectionAccess(ctx, userID, r.collectionID.String)
	if err != nil {
		return nil, false, err
	}
	return r, roleCanWrite(role), nil
}

func (s *sqliteStore) ListScenes(ctx context.Context, userID string, workspaceID, collectionID *string) ([]*core.WorkspaceScene, error) {
	switch {
	case collectionID != nil && *collectionID != "":
		_, role, err := s.collectionAccess(ctx, userID, *collectionID)
		if err != nil {
			return nil, err
		}
		return s.queryScenes(ctx, userID, role,
			`SELECT `+sceneCols+` FROM canvases WHERE collection_id = ?`+workspaceListedSceneSQL("id")+` ORDER BY updated_at DESC`, *collectionID)

	case workspaceID != nil && *workspaceID != "":
		role, err := s.resolveRole(ctx, userID, *workspaceID)
		if err != nil {
			return nil, err
		}
		return s.queryScenes(ctx, userID, role,
			`SELECT cv.id, cv.user_id, cv.name, cv.thumbnail, cv.collection_id, cv.created_at, cv.updated_at
			 FROM canvases cv
			 JOIN shell_collections c ON c.id = cv.collection_id
			 WHERE c.workspace_id = ?`+workspaceListedSceneSQL("cv.id")+`
			 ORDER BY cv.updated_at DESC`, *workspaceID)

	default:
		// 无范围限定：返回用户自己的工作区场景（排除 IndexedDB UUID）
		return s.queryScenes(ctx, userID, core.RoleAdmin,
			`SELECT `+sceneCols+` FROM canvases WHERE user_id = ?`+workspaceListedSceneSQL("id")+` ORDER BY updated_at DESC`, userID)
	}
}

func (s *sqliteStore) queryScenes(ctx context.Context, userID string, role core.WorkspaceRole, query string, args ...any) ([]*core.WorkspaceScene, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	scenes := make([]*core.WorkspaceScene, 0)
	for rows.Next() {
		r, err := scanSceneRow(rows)
		if err != nil {
			return nil, err
		}
		scenes = append(scenes, r.toScene(r.ownerID == userID || roleCanWrite(role)))
	}
	return scenes, rows.Err()
}

func (s *sqliteStore) GetScene(ctx context.Context, userID, sceneID string) (*core.WorkspaceScene, error) {
	r, canEdit, err := s.sceneAccess(ctx, userID, sceneID)
	if err != nil {
		return nil, err
	}
	scene := r.toScene(canEdit)
	if err := s.attachSceneEditState(ctx, userID, scene); err != nil {
		return nil, err
	}
	return scene, nil
}

func (s *sqliteStore) GetSceneData(ctx context.Context, userID, sceneID string) ([]byte, error) {
	r, _, err := s.sceneAccess(ctx, userID, sceneID)
	if err != nil {
		return nil, err
	}
	var data []byte
	if err := s.db.QueryRowContext(ctx, `SELECT data FROM canvases WHERE user_id = ? AND id = ?`, r.ownerID, r.id).Scan(&data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, core.ErrNotFound
		}
		return nil, err
	}
	return data, nil
}

func (s *sqliteStore) CreateScene(ctx context.Context, userID string, title string, thumbnail *string, data []byte, collectionID *string) (*core.WorkspaceScene, error) {
	if userID == "" {
		return nil, core.ErrInvalidInput
	}
	var collArg any
	if collectionID != nil && *collectionID != "" {
		_, role, err := s.collectionAccess(ctx, userID, *collectionID)
		if err != nil {
			return nil, err
		}
		if err := requireWrite(role); err != nil {
			return nil, err
		}
		collArg = *collectionID
	} else {
		// 未指定集合时落入个人工作区默认集合，保证 Workspace 列表能看到。
		if _, defaultCollID, err := createPersonalWorkspace(ctx, s.db, userID, "", true); err != nil {
			return nil, err
		} else if defaultCollID != "" {
			collArg = defaultCollID
		}
	}
	if strings.TrimSpace(title) == "" {
		title = "Untitled"
	}
	if len(bytes.TrimSpace(data)) == 0 {
		data = []byte(`{"elements":[],"appState":{},"files":{}}`)
	}

	now := time.Now().UTC()
	id := ulid.Make().String()
	// workspace_id 保留 default 以兼容阶段一的分组视图
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO canvases (id, user_id, name, thumbnail, data, workspace_id, collection_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, userID, title, derefOrEmpty(thumbnail), data, core.DefaultWorkspaceID, collArg, now, now); err != nil {
		return nil, err
	}
	return s.GetScene(ctx, userID, id)
}

func derefOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (s *sqliteStore) UpdateScene(ctx context.Context, userID, sceneID string, title, thumbnail *string, data []byte) (*core.WorkspaceScene, error) {
	r, canEdit, err := s.sceneAccess(ctx, userID, sceneID)
	if err != nil {
		return nil, err
	}
	if !canEdit {
		return nil, core.ErrForbidden
	}

	sets := []string{}
	args := []any{}
	if title != nil && strings.TrimSpace(*title) != "" {
		sets = append(sets, "name = ?")
		args = append(args, strings.TrimSpace(*title))
	}
	if thumbnail != nil {
		sets = append(sets, "thumbnail = ?")
		args = append(args, *thumbnail)
	}
	if data != nil {
		sets = append(sets, "data = ?")
		args = append(args, data)
	}
	if len(sets) > 0 {
		sets = append(sets, "updated_at = ?")
		args = append(args, time.Now().UTC(), r.ownerID, r.id)
		if _, err := s.db.ExecContext(ctx,
			`UPDATE canvases SET `+strings.Join(sets, ", ")+` WHERE user_id = ? AND id = ?`, args...); err != nil {
			return nil, err
		}
	}
	return s.GetScene(ctx, userID, sceneID)
}

func (s *sqliteStore) UpdateSceneData(ctx context.Context, userID, sceneID string, data []byte) error {
	r, canEdit, err := s.sceneAccess(ctx, userID, sceneID)
	if err != nil {
		return err
	}
	if !canEdit {
		return core.ErrForbidden
	}
	if err := s.rejectIfExclusiveLockHeld(ctx, userID, sceneID); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE canvases SET data = ?, updated_at = ? WHERE user_id = ? AND id = ?`,
		data, time.Now().UTC(), r.ownerID, r.id)
	return err
}

func (s *sqliteStore) UploadSceneThumbnail(ctx context.Context, userID, sceneID string, thumbnail string) (string, error) {
	r, canEdit, err := s.sceneAccess(ctx, userID, sceneID)
	if err != nil {
		return "", err
	}
	if !canEdit {
		return "", core.ErrForbidden
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE canvases SET thumbnail = ?, updated_at = ? WHERE user_id = ? AND id = ?`,
		thumbnail, time.Now().UTC(), r.ownerID, r.id); err != nil {
		return "", err
	}
	return thumbnail, nil
}

func (s *sqliteStore) DeleteScene(ctx context.Context, userID, sceneID string) error {
	r, _, err := s.sceneAccess(ctx, userID, sceneID)
	if err != nil {
		return err
	}
	// 只有拥有者或工作区管理员能删除
	if r.ownerID != userID {
		_, role, err := s.collectionAccess(ctx, userID, r.collectionID.String)
		if err != nil {
			return err
		}
		if err := requireAdmin(role); err != nil {
			return err
		}
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM canvases WHERE user_id = ? AND id = ?`, r.ownerID, r.id)
	return err
}

func (s *sqliteStore) DuplicateScene(ctx context.Context, userID, sceneID string) (*core.WorkspaceScene, error) {
	r, _, err := s.sceneAccess(ctx, userID, sceneID)
	if err != nil {
		return nil, err
	}
	data, err := s.GetSceneData(ctx, userID, sceneID)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	newID := ulid.Make().String()
	var collArg any
	if r.collectionID.Valid && r.collectionID.String != "" {
		collArg = r.collectionID.String
	}
	title := nsString(r.name)
	if title == "" {
		title = "Untitled"
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO canvases (id, user_id, name, thumbnail, data, workspace_id, collection_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		newID, userID, title+" (Copy)", nsString(r.thumbnail), data, core.DefaultWorkspaceID, collArg, now, now); err != nil {
		return nil, err
	}
	return s.GetScene(ctx, userID, newID)
}

func (s *sqliteStore) MoveScene(ctx context.Context, userID, sceneID string, collectionID *string) (*core.WorkspaceScene, error) {
	r, canEdit, err := s.sceneAccess(ctx, userID, sceneID)
	if err != nil {
		return nil, err
	}
	if !canEdit {
		return nil, core.ErrForbidden
	}

	var collArg any
	if collectionID != nil && *collectionID != "" {
		_, role, err := s.collectionAccess(ctx, userID, *collectionID)
		if err != nil {
			return nil, err
		}
		if err := requireWrite(role); err != nil {
			return nil, err
		}
		collArg = *collectionID
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE canvases SET collection_id = ?, updated_at = ? WHERE user_id = ? AND id = ?`,
		collArg, time.Now().UTC(), r.ownerID, r.id); err != nil {
		return nil, err
	}
	return s.GetScene(ctx, userID, sceneID)
}

// ---------------------------------------------------------------------------
// 用户资料
// ---------------------------------------------------------------------------

func (s *sqliteStore) UpsertUserProfile(ctx context.Context, user core.MemberUser) error {
	if user.ID == "" {
		return core.ErrInvalidInput
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO user_profiles (id, email, name, avatar_url, updated_at) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   email = COALESCE(NULLIF(excluded.email, ''), user_profiles.email),
		   name = COALESCE(excluded.name, user_profiles.name),
		   avatar_url = COALESCE(excluded.avatar_url, user_profiles.avatar_url),
		   updated_at = excluded.updated_at`,
		user.ID, user.Email, nullable(user.Name), nullable(user.AvatarURL), time.Now().UTC())
	return err
}

// ---------------------------------------------------------------------------
// 老分组 → 集合迁移
// ---------------------------------------------------------------------------

// MigrateLegacyGroupsToShell 把阶段一的 workspaces（画布分组）转成个人工作区下的
// 集合。按用户粒度写 shell_migrated_users 标记，可重复调用。
func (s *sqliteStore) MigrateLegacyGroupsToShell(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx,
		`SELECT user_id FROM canvases UNION SELECT user_id FROM workspaces`)
	if err != nil {
		return err
	}
	var users []string
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			rows.Close()
			return err
		}
		if userID != "" {
			users = append(users, userID)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	migrated := 0
	for _, userID := range users {
		var one int
		err := s.db.QueryRowContext(ctx, `SELECT 1 FROM shell_migrated_users WHERE user_id = ?`, userID).Scan(&one)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := s.migrateLegacyUser(ctx, userID); err != nil {
			return fmt.Errorf("migrate legacy groups for user %s: %w", userID, err)
		}
		migrated++
	}
	if migrated > 0 {
		logrus.WithField("users", migrated).Info("Migrated legacy canvas groups to workspace shell collections")
	}
	return nil
}

func (s *sqliteStore) migrateLegacyUser(ctx context.Context, userID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	type legacyGroup struct {
		id   string
		name string
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, name FROM workspaces WHERE user_id = ? ORDER BY created_at`, userID)
	if err != nil {
		return err
	}
	var groups []legacyGroup
	for rows.Next() {
		var g legacyGroup
		if err := rows.Scan(&g.id, &g.name); err != nil {
			rows.Close()
			return err
		}
		groups = append(groups, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	hasDefault := false
	for _, g := range groups {
		if g.id == core.DefaultWorkspaceID {
			hasDefault = true
			break
		}
	}

	// 老 default 分组变成默认可分享集合，无需再造一个 Private
	wsID, privateID, err := createPersonalWorkspace(ctx, tx, userID, "", !hasDefault)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	for _, g := range groups {
		collID := ulid.Make().String()
		name := g.name
		if strings.TrimSpace(name) == "" {
			name = "Untitled"
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO shell_collections (id, workspace_id, user_id, name, icon, color, is_private, created_at, updated_at)
			 VALUES (?, ?, ?, ?, NULL, NULL, 0, ?, ?)`,
			collID, wsID, userID, name, now, now); err != nil {
			return err
		}
		if g.id == core.DefaultWorkspaceID || privateID == "" {
			privateID = collID
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE canvases SET collection_id = ?
			 WHERE user_id = ? AND workspace_id = ? AND (collection_id IS NULL OR collection_id = '')`,
			collID, userID, g.id); err != nil {
			return err
		}
	}

	// 兜底：分组已丢失的画布归入私有集合
	if privateID != "" {
		if _, err := tx.ExecContext(ctx,
			`UPDATE canvases SET collection_id = ? WHERE user_id = ? AND (collection_id IS NULL OR collection_id = '')`,
			privateID, userID); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO shell_migrated_users (user_id, migrated_at) VALUES (?, ?)`, userID, now); err != nil {
		return err
	}
	return tx.Commit()
}

var _ core.ShellStore = (*sqliteStore)(nil)
