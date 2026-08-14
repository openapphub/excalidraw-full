package sqlite

import (
	"context"
	"database/sql"
	"excalidraw-complete/core"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

func ensureLocalAuthSchema(db *sql.DB) {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS local_users (
	id TEXT PRIMARY KEY,
	email TEXT NOT NULL UNIQUE,
	password_hash TEXT NOT NULL,
	name TEXT,
	avatar_url TEXT,
	created_at DATETIME
);`)
	if err != nil {
		panic("failed to create local_users: " + err.Error())
	}
}

func (s *sqliteStore) CreateLocalUser(ctx context.Context, email, passwordHash, name string) (*core.LocalUser, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || passwordHash == "" {
		return nil, core.ErrInvalidInput
	}
	id := "local:" + ulid.Make().String()
	if name == "" {
		name = strings.Split(email, "@")[0]
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO local_users (id, email, password_hash, name, created_at) VALUES (?, ?, ?, ?, datetime('now'))`,
		id, email, passwordHash, name)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, core.ErrConflict
		}
		return nil, err
	}
	u := &core.LocalUser{ID: id, Email: email, PasswordHash: passwordHash, Name: name}
	_ = s.UpsertUserProfile(ctx, core.MemberUser{ID: id, Email: email, Name: &name})
	return u, nil
}

func (s *sqliteStore) GetLocalUserByEmail(ctx context.Context, email string) (*core.LocalUser, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	row := s.db.QueryRowContext(ctx,
		`SELECT id, email, password_hash, COALESCE(name,''), COALESCE(avatar_url,'') FROM local_users WHERE lower(email) = ?`,
		email)
	return scanLocalUser(row)
}

func (s *sqliteStore) GetLocalUserByID(ctx context.Context, id string) (*core.LocalUser, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, email, password_hash, COALESCE(name,''), COALESCE(avatar_url,'') FROM local_users WHERE id = ?`,
		id)
	return scanLocalUser(row)
}

func scanLocalUser(row *sql.Row) (*core.LocalUser, error) {
	var u core.LocalUser
	if err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Name, &u.AvatarURL); err != nil {
		if err == sql.ErrNoRows {
			return nil, core.ErrNotFound
		}
		return nil, err
	}
	return &u, nil
}

func (s *sqliteStore) GetUserProfile(ctx context.Context, userID string) (*core.MemberUser, error) {
	var email, name, avatar sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT email, name, avatar_url FROM user_profiles WHERE id = ?`, userID).
		Scan(&email, &name, &avatar)
	if err == sql.ErrNoRows {
		return &core.MemberUser{ID: userID}, nil
	}
	if err != nil {
		return nil, err
	}
	u := &core.MemberUser{ID: userID, Email: email.String}
	if name.Valid && name.String != "" {
		n := name.String
		u.Name = &n
	}
	if avatar.Valid && avatar.String != "" {
		a := avatar.String
		u.AvatarURL = &a
	}
	return u, nil
}

func (s *sqliteStore) UpdateUserProfile(ctx context.Context, userID string, name *string, avatarURL *string, clearAvatar bool) (*core.MemberUser, error) {
	cur, err := s.GetUserProfile(ctx, userID)
	if err != nil {
		return nil, err
	}
	if cur.ID == "" {
		cur.ID = userID
	}
	if name != nil {
		cur.Name = name
	}
	if clearAvatar {
		cur.AvatarURL = nil
	} else if avatarURL != nil {
		cur.AvatarURL = avatarURL
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO user_profiles (id, email, name, avatar_url, updated_at) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   name = COALESCE(excluded.name, user_profiles.name),
		   avatar_url = excluded.avatar_url,
		   updated_at = excluded.updated_at`,
		userID, cur.Email, nullable(cur.Name), nullable(cur.AvatarURL), time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(userID, "local:") {
		_, _ = s.db.ExecContext(ctx,
			`UPDATE local_users SET name = COALESCE(?, name), avatar_url = ? WHERE id = ?`,
			nullable(cur.Name), nullable(cur.AvatarURL), userID)
	}
	return s.GetUserProfile(ctx, userID)
}
