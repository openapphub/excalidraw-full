package core

import "context"

// LocalUser 是账密登录账号（与 GitHub/OIDC 用户并存，id 形如 local:{ulid}）。
type LocalUser struct {
	ID           string
	Email        string
	PasswordHash string
	Name         string
	AvatarURL    string
}

type LocalAuthStore interface {
	CreateLocalUser(ctx context.Context, email, passwordHash, name string) (*LocalUser, error)
	GetLocalUserByEmail(ctx context.Context, email string) (*LocalUser, error)
	GetLocalUserByID(ctx context.Context, id string) (*LocalUser, error)
}
