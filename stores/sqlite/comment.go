package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"excalidraw-complete/core"
	"log"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

// ---------------------------------------------------------------------------
// Schema
// ---------------------------------------------------------------------------

var commentSchemaStmts = []string{
	`CREATE TABLE IF NOT EXISTS comment_threads (
	id TEXT PRIMARY KEY,
	scene_id TEXT NOT NULL,
	x REAL NOT NULL DEFAULT 0,
	y REAL NOT NULL DEFAULT 0,
	resolved INTEGER NOT NULL DEFAULT 0,
	resolved_at DATETIME,
	resolved_by TEXT,
	created_by TEXT NOT NULL,
	created_at DATETIME,
	updated_at DATETIME
);`,
	`CREATE TABLE IF NOT EXISTS comments (
	id TEXT PRIMARY KEY,
	thread_id TEXT NOT NULL,
	content TEXT NOT NULL,
	mentions TEXT,
	created_by TEXT NOT NULL,
	edited_at DATETIME,
	created_at DATETIME
);`,
	`CREATE TABLE IF NOT EXISTS notifications (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL,
	type TEXT NOT NULL,
	scene_id TEXT,
	thread_id TEXT,
	comment_id TEXT,
	actor_user_id TEXT,
	read_at DATETIME,
	created_at DATETIME
);`,
	`CREATE INDEX IF NOT EXISTS idx_comment_threads_scene ON comment_threads(scene_id);`,
	`CREATE INDEX IF NOT EXISTS idx_comments_thread ON comments(thread_id);`,
	`CREATE INDEX IF NOT EXISTS idx_notifications_user ON notifications(user_id, id DESC);`,
	`CREATE INDEX IF NOT EXISTS idx_notifications_unread ON notifications(user_id, read_at);`,
}

func ensureCommentSchema(db *sql.DB) {
	// 生产库里可能已有旧版 element comments 表（canvas_id/element_id/text，无 thread_id）。
	// CREATE TABLE IF NOT EXISTS 不会改结构，随后建 thread_id 索引会直接 Fatal。
	if commentTableExists(db) && !columnExists(db, "comments", "thread_id") {
		if _, err := db.Exec(`ALTER TABLE comments RENAME TO comments_legacy`); err != nil {
			log.Fatalf("failed to rename legacy comments table: %v", err)
		}
	}
	for _, stmt := range commentSchemaStmts {
		if _, err := db.Exec(stmt); err != nil {
			log.Fatalf("failed to create comment schema: %v", err)
		}
	}
}

func commentTableExists(db *sql.DB) bool {
	var name string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='comments'`).Scan(&name)
	return err == nil && name == "comments"
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

// normalizeMentions 去空白、去重，保持前端传入顺序。
func normalizeMentions(mentions []string) []string {
	cleaned := make([]string, 0, len(mentions))
	seen := map[string]bool{}
	for _, m := range mentions {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		cleaned = append(cleaned, m)
	}
	return cleaned
}

func encodeMentions(mentions []string) any {
	cleaned := normalizeMentions(mentions)
	if len(cleaned) == 0 {
		return nil
	}
	buf, err := json.Marshal(cleaned)
	if err != nil {
		return nil
	}
	return string(buf)
}

func decodeMentions(raw sql.NullString) []string {
	if !raw.Valid || raw.String == "" {
		return []string{}
	}
	var out []string
	if err := json.Unmarshal([]byte(raw.String), &out); err != nil {
		return []string{}
	}
	if out == nil {
		return []string{}
	}
	return out
}

// fallbackSummary 在 user_profiles 尚无该用户时给出可展示的兜底信息。
func fallbackSummary(userID string) core.UserSummary {
	return core.UserSummary{ID: userID, Name: userID}
}

func summaryFromProfile(id string, email, name, avatar sql.NullString) core.UserSummary {
	s := core.UserSummary{
		ID:     id,
		Email:  nsString(email),
		Avatar: nsString(avatar),
	}
	s.Name = nsString(name)
	if s.Name == "" {
		if at := strings.Index(s.Email, "@"); at > 0 {
			s.Name = s.Email[:at]
		} else if s.Email != "" {
			s.Name = s.Email
		} else {
			s.Name = id
		}
	}
	return s
}

// userSummaries 批量取用户展示信息，缺档的用兜底值补齐。
func (s *sqliteStore) userSummaries(ctx context.Context, ids []string) (map[string]core.UserSummary, error) {
	out := map[string]core.UserSummary{}
	unique := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := out[id]; ok {
			continue
		}
		out[id] = fallbackSummary(id)
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return out, nil
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(unique)), ",")
	args := make([]any, len(unique))
	for i, id := range unique {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, email, name, avatar_url FROM user_profiles WHERE id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var email, name, avatar sql.NullString
		if err := rows.Scan(&id, &email, &name, &avatar); err != nil {
			return nil, err
		}
		out[id] = summaryFromProfile(id, email, name, avatar)
	}
	return out, rows.Err()
}

func (s *sqliteStore) userSummary(ctx context.Context, userID string) core.UserSummary {
	m, err := s.userSummaries(ctx, []string{userID})
	if err != nil {
		return fallbackSummary(userID)
	}
	if u, ok := m[userID]; ok {
		return u
	}
	return fallbackSummary(userID)
}

// ---------------------------------------------------------------------------
// 权限：评论读写跟随场景
// ---------------------------------------------------------------------------

// commentSceneAccess 返回 (canWrite, error)：能读该场景才不报错，VIEWER 只读。
func (s *sqliteStore) commentSceneAccess(ctx context.Context, userID, sceneID string) (bool, error) {
	_, canWrite, err := s.sceneAccess(ctx, userID, sceneID)
	if err != nil {
		return false, err
	}
	return canWrite, nil
}

func (s *sqliteStore) requireSceneWrite(ctx context.Context, userID, sceneID string) error {
	canWrite, err := s.commentSceneAccess(ctx, userID, sceneID)
	if err != nil {
		return err
	}
	if !canWrite {
		return core.ErrForbidden
	}
	return nil
}

// sceneOwner 返回场景所属用户（canvases.user_id）。
func (s *sqliteStore) sceneOwner(ctx context.Context, sceneID string) (string, error) {
	var owner string
	err := s.db.QueryRowContext(ctx, `SELECT user_id FROM canvases WHERE id = ? LIMIT 1`, sceneID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", core.ErrNotFound
	}
	return owner, err
}

// notifiableUsers 是「能收到该场景通知」的用户集合：场景所有者 + 所属集合工作区成员。
func (s *sqliteStore) notifiableUsers(ctx context.Context, sceneID string) (map[string]bool, error) {
	allowed := map[string]bool{}

	var owner string
	var collectionID sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT user_id, collection_id FROM canvases WHERE id = ? LIMIT 1`, sceneID).Scan(&owner, &collectionID)
	if errors.Is(err, sql.ErrNoRows) {
		return allowed, core.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	allowed[owner] = true

	if !collectionID.Valid || collectionID.String == "" {
		return allowed, nil
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT m.user_id FROM shell_members m
		 JOIN shell_collections c ON c.workspace_id = m.workspace_id
		 WHERE c.id = ?`, collectionID.String)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		allowed[uid] = true
	}
	return allowed, rows.Err()
}

// ---------------------------------------------------------------------------
// 线程读取
// ---------------------------------------------------------------------------

const commentThreadCols = `id, scene_id, x, y, resolved, resolved_at, resolved_by, created_by, created_at, updated_at`

type threadRow struct {
	id         string
	sceneID    string
	x, y       float64
	resolved   bool
	resolvedAt sql.NullTime
	resolvedBy sql.NullString
	createdBy  string
	createdAt  sql.NullTime
	updatedAt  sql.NullTime
}

func scanThreadRow(sc rowScanner) (*threadRow, error) {
	var r threadRow
	var resolved int
	if err := sc.Scan(&r.id, &r.sceneID, &r.x, &r.y, &resolved,
		&r.resolvedAt, &r.resolvedBy, &r.createdBy, &r.createdAt, &r.updatedAt); err != nil {
		return nil, err
	}
	r.resolved = resolved != 0
	return &r, nil
}

func (r *threadRow) toThread(profiles map[string]core.UserSummary) *core.CommentThread {
	thread := &core.CommentThread{
		ID:        r.id,
		SceneID:   r.sceneID,
		X:         r.x,
		Y:         r.y,
		Resolved:  r.resolved,
		CreatedAt: ntTime(r.createdAt),
		UpdatedAt: ntTime(r.updatedAt),
		Comments:  []core.Comment{},
	}
	if summary, ok := profiles[r.createdBy]; ok {
		thread.CreatedBy = summary
	} else {
		thread.CreatedBy = fallbackSummary(r.createdBy)
	}
	thread.ResolvedAt = ntPtr(r.resolvedAt)
	if by := nsString(r.resolvedBy); by != "" {
		summary, ok := profiles[by]
		if !ok {
			summary = fallbackSummary(by)
		}
		thread.ResolvedBy = &summary
	}
	return thread
}

// loadThreadRow 只读线程行，不做权限判断。
func (s *sqliteStore) loadThreadRow(ctx context.Context, threadID string) (*threadRow, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+commentThreadCols+` FROM comment_threads WHERE id = ?`, threadID)
	r, err := scanThreadRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, core.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

// hydrateThreads 给线程批量挂上评论与作者信息。
func (s *sqliteStore) hydrateThreads(ctx context.Context, rows []*threadRow) ([]*core.CommentThread, error) {
	if len(rows) == 0 {
		return []*core.CommentThread{}, nil
	}

	threadIDs := make([]string, 0, len(rows))
	userIDs := make([]string, 0, len(rows)*2)
	for _, r := range rows {
		threadIDs = append(threadIDs, r.id)
		userIDs = append(userIDs, r.createdBy)
		if by := nsString(r.resolvedBy); by != "" {
			userIDs = append(userIDs, by)
		}
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(threadIDs)), ",")
	args := make([]any, len(threadIDs))
	for i, id := range threadIDs {
		args[i] = id
	}
	commentRows, err := s.db.QueryContext(ctx,
		`SELECT id, thread_id, content, mentions, created_by, edited_at, created_at
		 FROM comments WHERE thread_id IN (`+placeholders+`) ORDER BY created_at ASC, id ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer commentRows.Close()

	type rawComment struct {
		comment  core.Comment
		threadID string
	}
	var raws []rawComment
	for commentRows.Next() {
		var (
			c        core.Comment
			mentions sql.NullString
			editedAt sql.NullTime
			created  sql.NullTime
			threadID string
			author   string
		)
		if err := commentRows.Scan(&c.ID, &threadID, &c.Content, &mentions, &author, &editedAt, &created); err != nil {
			return nil, err
		}
		c.ThreadID = threadID
		c.Mentions = decodeMentions(mentions)
		c.EditedAt = ntPtr(editedAt)
		c.CreatedAt = ntTime(created)
		c.CreatedBy = core.UserSummary{ID: author}
		userIDs = append(userIDs, author)
		raws = append(raws, rawComment{comment: c, threadID: threadID})
	}
	if err := commentRows.Err(); err != nil {
		return nil, err
	}

	profiles, err := s.userSummaries(ctx, userIDs)
	if err != nil {
		return nil, err
	}

	byThread := map[string][]core.Comment{}
	for _, raw := range raws {
		c := raw.comment
		if summary, ok := profiles[c.CreatedBy.ID]; ok {
			c.CreatedBy = summary
		} else {
			c.CreatedBy = fallbackSummary(c.CreatedBy.ID)
		}
		byThread[raw.threadID] = append(byThread[raw.threadID], c)
	}

	threads := make([]*core.CommentThread, 0, len(rows))
	for _, r := range rows {
		thread := r.toThread(profiles)
		if comments := byThread[r.id]; comments != nil {
			thread.Comments = comments
		}
		thread.CommentCount = len(thread.Comments)
		threads = append(threads, thread)
	}
	return threads, nil
}

func (s *sqliteStore) ListThreads(ctx context.Context, userID, sceneID string, resolved *bool) ([]*core.CommentThread, error) {
	if _, err := s.commentSceneAccess(ctx, userID, sceneID); err != nil {
		return nil, err
	}

	query := `SELECT ` + commentThreadCols + ` FROM comment_threads WHERE scene_id = ?`
	args := []any{sceneID}
	if resolved != nil {
		query += ` AND resolved = ?`
		if *resolved {
			args = append(args, 1)
		} else {
			args = append(args, 0)
		}
	}
	query += ` ORDER BY created_at ASC, id ASC`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var threadRows []*threadRow
	for rows.Next() {
		r, err := scanThreadRow(rows)
		if err != nil {
			return nil, err
		}
		threadRows = append(threadRows, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return s.hydrateThreads(ctx, threadRows)
}

func (s *sqliteStore) GetThread(ctx context.Context, userID, threadID string) (*core.CommentThread, error) {
	r, err := s.loadThreadRow(ctx, threadID)
	if err != nil {
		return nil, err
	}
	if _, err := s.commentSceneAccess(ctx, userID, r.sceneID); err != nil {
		return nil, err
	}
	threads, err := s.hydrateThreads(ctx, []*threadRow{r})
	if err != nil {
		return nil, err
	}
	return threads[0], nil
}

// ---------------------------------------------------------------------------
// 线程写入
// ---------------------------------------------------------------------------

func (s *sqliteStore) CreateThread(ctx context.Context, userID, sceneID string, x, y float64, content string, mentions []string) (*core.CommentThread, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, core.ErrInvalidInput
	}
	if err := s.requireSceneWrite(ctx, userID, sceneID); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	threadID := ulid.Make().String()
	commentID := ulid.Make().String()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO comment_threads (id, scene_id, x, y, resolved, created_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 0, ?, ?, ?)`,
		threadID, sceneID, x, y, userID, now, now); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO comments (id, thread_id, content, mentions, created_by, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		commentID, threadID, content, encodeMentions(mentions), userID, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	// 通知失败不影响评论本身
	s.notifyForComment(ctx, notifyParams{
		actorID:        userID,
		sceneID:        sceneID,
		threadID:       threadID,
		commentID:      commentID,
		threadAuthorID: userID,
		mentions:       mentions,
	})

	return s.GetThread(ctx, userID, threadID)
}

func (s *sqliteStore) UpdateThreadPosition(ctx context.Context, userID, threadID string, x, y *float64) (*core.CommentThread, error) {
	r, err := s.loadThreadRow(ctx, threadID)
	if err != nil {
		return nil, err
	}
	if err := s.requireSceneWrite(ctx, userID, r.sceneID); err != nil {
		return nil, err
	}
	if x == nil && y == nil {
		return s.GetThread(ctx, userID, threadID)
	}

	sets := []string{"updated_at = ?"}
	args := []any{time.Now().UTC()}
	if x != nil {
		sets = append(sets, "x = ?")
		args = append(args, *x)
	}
	if y != nil {
		sets = append(sets, "y = ?")
		args = append(args, *y)
	}
	args = append(args, threadID)

	if _, err := s.db.ExecContext(ctx,
		`UPDATE comment_threads SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...); err != nil {
		return nil, err
	}
	return s.GetThread(ctx, userID, threadID)
}

func (s *sqliteStore) SetThreadResolved(ctx context.Context, userID, threadID string, resolved bool) (*core.CommentThread, error) {
	r, err := s.loadThreadRow(ctx, threadID)
	if err != nil {
		return nil, err
	}
	if err := s.requireSceneWrite(ctx, userID, r.sceneID); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	if resolved {
		_, err = s.db.ExecContext(ctx,
			`UPDATE comment_threads SET resolved = 1, resolved_at = ?, resolved_by = ?, updated_at = ? WHERE id = ?`,
			now, userID, now, threadID)
	} else {
		_, err = s.db.ExecContext(ctx,
			`UPDATE comment_threads SET resolved = 0, resolved_at = NULL, resolved_by = NULL, updated_at = ? WHERE id = ?`,
			now, threadID)
	}
	if err != nil {
		return nil, err
	}
	return s.GetThread(ctx, userID, threadID)
}

func (s *sqliteStore) DeleteThread(ctx context.Context, userID, threadID string) error {
	r, err := s.loadThreadRow(ctx, threadID)
	if err != nil {
		return err
	}
	if err := s.requireSceneWrite(ctx, userID, r.sceneID); err != nil {
		return err
	}
	// 线程作者或场景所有者可删整条线程
	if r.createdBy != userID {
		owner, err := s.sceneOwner(ctx, r.sceneID)
		if err != nil {
			return err
		}
		if owner != userID {
			return core.ErrForbidden
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM comments WHERE thread_id = ?`, threadID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM comment_threads WHERE id = ?`, threadID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM notifications WHERE thread_id = ?`, threadID); err != nil {
		return err
	}
	return tx.Commit()
}

// ---------------------------------------------------------------------------
// 评论写入
// ---------------------------------------------------------------------------

func (s *sqliteStore) AddComment(ctx context.Context, userID, threadID, content string, mentions []string) (*core.Comment, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, core.ErrInvalidInput
	}
	r, err := s.loadThreadRow(ctx, threadID)
	if err != nil {
		return nil, err
	}
	if err := s.requireSceneWrite(ctx, userID, r.sceneID); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	commentID := ulid.Make().String()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO comments (id, thread_id, content, mentions, created_by, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		commentID, threadID, content, encodeMentions(mentions), userID, now); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE comment_threads SET updated_at = ? WHERE id = ?`, now, threadID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	s.notifyForComment(ctx, notifyParams{
		actorID:        userID,
		sceneID:        r.sceneID,
		threadID:       threadID,
		commentID:      commentID,
		threadAuthorID: r.createdBy,
		mentions:       mentions,
	})

	return &core.Comment{
		ID:        commentID,
		ThreadID:  threadID,
		Content:   content,
		Mentions:  normalizeMentions(mentions),
		CreatedBy: s.userSummary(ctx, userID),
		CreatedAt: now,
	}, nil
}

func (s *sqliteStore) UpdateComment(ctx context.Context, userID, commentID, content string) (*core.Comment, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, core.ErrInvalidInput
	}

	var (
		threadID  string
		author    string
		mentions  sql.NullString
		createdAt sql.NullTime
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT thread_id, created_by, mentions, created_at FROM comments WHERE id = ?`, commentID).
		Scan(&threadID, &author, &mentions, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, core.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if author != userID {
		return nil, core.ErrForbidden
	}

	r, err := s.loadThreadRow(ctx, threadID)
	if err != nil {
		return nil, err
	}
	if err := s.requireSceneWrite(ctx, userID, r.sceneID); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	if _, err := s.db.ExecContext(ctx,
		`UPDATE comments SET content = ?, edited_at = ? WHERE id = ?`, content, now, commentID); err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE comment_threads SET updated_at = ? WHERE id = ?`, now, threadID); err != nil {
		return nil, err
	}

	edited := now
	return &core.Comment{
		ID:        commentID,
		ThreadID:  threadID,
		Content:   content,
		Mentions:  decodeMentions(mentions),
		CreatedBy: s.userSummary(ctx, userID),
		EditedAt:  &edited,
		CreatedAt: ntTime(createdAt),
	}, nil
}

// DeleteComment 删除单条评论；若该线程已无评论，连线程一起删除（避免留下空图钉）。
func (s *sqliteStore) DeleteComment(ctx context.Context, userID, commentID string) error {
	var threadID, author string
	err := s.db.QueryRowContext(ctx,
		`SELECT thread_id, created_by FROM comments WHERE id = ?`, commentID).Scan(&threadID, &author)
	if errors.Is(err, sql.ErrNoRows) {
		return core.ErrNotFound
	}
	if err != nil {
		return err
	}

	r, err := s.loadThreadRow(ctx, threadID)
	if err != nil {
		return err
	}
	if err := s.requireSceneWrite(ctx, userID, r.sceneID); err != nil {
		return err
	}
	if author != userID {
		owner, err := s.sceneOwner(ctx, r.sceneID)
		if err != nil {
			return err
		}
		if owner != userID {
			return core.ErrForbidden
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM comments WHERE id = ?`, commentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM notifications WHERE comment_id = ?`, commentID); err != nil {
		return err
	}

	var remaining int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM comments WHERE thread_id = ?`, threadID).Scan(&remaining); err != nil {
		return err
	}
	if remaining == 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM comment_threads WHERE id = ?`, threadID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM notifications WHERE thread_id = ?`, threadID); err != nil {
			return err
		}
	} else if _, err := tx.ExecContext(ctx,
		`UPDATE comment_threads SET updated_at = ? WHERE id = ?`, time.Now().UTC(), threadID); err != nil {
		return err
	}

	return tx.Commit()
}

// ---------------------------------------------------------------------------
// 通知
// ---------------------------------------------------------------------------

type notifyParams struct {
	actorID        string
	sceneID        string
	threadID       string
	commentID      string
	threadAuthorID string
	mentions       []string
}

// notifyForComment 为 @提及写 MENTION、为被回复的线程作者写 COMMENT。
// 只给「能访问该场景」的用户发，且从不给自己发。
func (s *sqliteStore) notifyForComment(ctx context.Context, p notifyParams) {
	allowed, err := s.notifiableUsers(ctx, p.sceneID)
	if err != nil {
		return
	}

	now := time.Now().UTC()
	notified := map[string]bool{p.actorID: true}

	insert := func(userID string, typ core.NotificationType) {
		if userID == "" || notified[userID] || !allowed[userID] {
			return
		}
		notified[userID] = true
		_, _ = s.db.ExecContext(ctx,
			`INSERT INTO notifications (id, user_id, type, scene_id, thread_id, comment_id, actor_user_id, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			ulid.Make().String(), userID, string(typ), p.sceneID, p.threadID, p.commentID, p.actorID, now)
	}

	for _, mentioned := range p.mentions {
		insert(strings.TrimSpace(mentioned), core.NotificationMention)
	}
	insert(p.threadAuthorID, core.NotificationComment)
}

const notificationCols = `n.id, n.type, n.scene_id, n.thread_id, n.comment_id, n.actor_user_id, n.read_at, n.created_at,
	(SELECT c.name FROM canvases c WHERE c.id = n.scene_id LIMIT 1)`

func (s *sqliteStore) ListNotifications(ctx context.Context, userID, cursor string, limit int, unreadOnly bool) (*core.NotificationsResponse, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	query := `SELECT ` + notificationCols + ` FROM notifications n WHERE n.user_id = ?`
	args := []any{userID}
	if unreadOnly {
		query += ` AND n.read_at IS NULL`
	}
	if cursor != "" {
		query += ` AND n.id < ?`
		args = append(args, cursor)
	}
	query += ` ORDER BY n.id DESC LIMIT ?`
	args = append(args, limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var (
		list    []*core.Notification
		actors  []string
		hasMore bool
	)
	for rows.Next() {
		var (
			n         core.Notification
			typ       string
			sceneID   sql.NullString
			threadID  sql.NullString
			commentID sql.NullString
			actorID   sql.NullString
			readAt    sql.NullTime
			createdAt sql.NullTime
			sceneName sql.NullString
		)
		if err := rows.Scan(&n.ID, &typ, &sceneID, &threadID, &commentID, &actorID, &readAt, &createdAt, &sceneName); err != nil {
			return nil, err
		}
		if len(list) == limit {
			hasMore = true
			break
		}
		n.Type = core.NotificationType(typ)
		n.Scene = core.NotificationScene{ID: nsString(sceneID), Name: nsString(sceneName)}
		if n.Scene.Name == "" {
			n.Scene.Name = "Untitled"
		}
		if id := nsString(threadID); id != "" {
			n.Thread = &core.NotificationRef{ID: id}
		}
		if id := nsString(commentID); id != "" {
			n.Comment = &core.NotificationRef{ID: id}
		}
		n.Read = readAt.Valid
		n.ReadAt = ntPtr(readAt)
		n.CreatedAt = ntTime(createdAt)
		n.Actor = core.UserSummary{ID: nsString(actorID)}
		actors = append(actors, n.Actor.ID)
		list = append(list, &n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	profiles, err := s.userSummaries(ctx, actors)
	if err != nil {
		return nil, err
	}
	for _, n := range list {
		if summary, ok := profiles[n.Actor.ID]; ok {
			n.Actor = summary
		} else {
			n.Actor = fallbackSummary(n.Actor.ID)
		}
	}

	resp := &core.NotificationsResponse{Notifications: list, HasMore: hasMore}
	if resp.Notifications == nil {
		resp.Notifications = []*core.Notification{}
	}
	if hasMore && len(list) > 0 {
		resp.NextCursor = list[len(list)-1].ID
	}
	return resp, nil
}

func (s *sqliteStore) CountUnreadNotifications(ctx context.Context, userID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM notifications WHERE user_id = ? AND read_at IS NULL`, userID).Scan(&count)
	return count, err
}

func (s *sqliteStore) MarkNotificationRead(ctx context.Context, userID, notificationID string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE notifications SET read_at = ? WHERE id = ? AND user_id = ? AND read_at IS NULL`,
		time.Now().UTC(), notificationID, userID)
	if err != nil {
		return err
	}
	if affected, err := res.RowsAffected(); err == nil && affected == 0 {
		// 已读或不属于该用户：确认存在性以区分 404
		var one int
		qerr := s.db.QueryRowContext(ctx,
			`SELECT 1 FROM notifications WHERE id = ? AND user_id = ?`, notificationID, userID).Scan(&one)
		if errors.Is(qerr, sql.ErrNoRows) {
			return core.ErrNotFound
		}
		return qerr
	}
	return nil
}

func (s *sqliteStore) MarkAllNotificationsRead(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE notifications SET read_at = ? WHERE user_id = ? AND read_at IS NULL`,
		time.Now().UTC(), userID)
	return err
}

var _ core.CommentStore = (*sqliteStore)(nil)
