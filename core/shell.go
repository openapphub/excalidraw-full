package core

import (
	"context"
	"errors"
	"time"
)

// Workspace shell roles / types (AstraDraw 对齐，2A 精简共享).
type WorkspaceRole string

const (
	RoleAdmin  WorkspaceRole = "ADMIN"
	RoleMember WorkspaceRole = "MEMBER"
	RoleViewer WorkspaceRole = "VIEWER"
)

type WorkspaceType string

const (
	WorkspacePersonal WorkspaceType = "PERSONAL"
	WorkspaceShared   WorkspaceType = "SHARED"
)

// ShellWorkspace is an AstraDraw-shaped workspace (not the stage-1 canvas group).
type ShellWorkspace struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Slug        string        `json:"slug"`
	AvatarURL   *string       `json:"avatarUrl"`
	Role        WorkspaceRole `json:"role"`
	Type        WorkspaceType `json:"type"`
	MemberCount int           `json:"memberCount"`
	CreatedAt   time.Time     `json:"createdAt"`
	UpdatedAt   time.Time     `json:"updatedAt"`
}

type WorkspaceMember struct {
	ID        string        `json:"id"`
	Role      WorkspaceRole `json:"role"`
	UserID    string        `json:"userId"`
	User      MemberUser    `json:"user"`
	CreatedAt time.Time     `json:"createdAt"`
}

type MemberUser struct {
	ID        string  `json:"id"`
	Email     string  `json:"email"`
	Name      *string `json:"name"`
	AvatarURL *string `json:"avatarUrl"`
}

type Collection struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Icon        *string   `json:"icon"`
	Color       *string   `json:"color"`
	IsPrivate   bool      `json:"isPrivate"`
	UserID      string    `json:"userId"`
	WorkspaceID string    `json:"workspaceId"`
	SceneCount  int       `json:"sceneCount"`
	CanWrite    bool      `json:"canWrite"`
	IsOwner     bool      `json:"isOwner"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type InviteLink struct {
	ID          string        `json:"id"`
	Code        string        `json:"code"`
	Role        WorkspaceRole `json:"role"`
	WorkspaceID string        `json:"workspaceId"`
	ExpiresAt   *time.Time    `json:"expiresAt"`
	MaxUses     *int          `json:"maxUses"`
	Uses        int           `json:"uses"`
	CreatedAt   time.Time     `json:"createdAt"`
}

// WorkspaceScene aligns with AstraDraw WorkspaceScene; id == canvas id.
type WorkspaceScene struct {
	ID            string       `json:"id"`
	Title         string       `json:"title"`
	ThumbnailURL  *string      `json:"thumbnailUrl"`
	StorageKey    string       `json:"storageKey"`
	RoomID        *string      `json:"roomId"`
	CollectionID  *string      `json:"collectionId"`
	IsPublic      bool         `json:"isPublic"`
	LastOpenedAt  *time.Time   `json:"lastOpenedAt"`
	CreatedAt     time.Time    `json:"createdAt"`
	UpdatedAt     time.Time    `json:"updatedAt"`
	CanEdit       bool         `json:"canEdit,omitempty"`
	CollabEnabled bool         `json:"collabEnabled"`
	Editor        *SceneEditor `json:"editor,omitempty"`
}

// SceneEditor is the exclusive-lock holder (empty when collabEnabled).
type SceneEditor struct {
	UserID string `json:"userId"`
	Name   string `json:"name"`
	IsSelf bool   `json:"isSelf"`
}

// SceneLockError is returned when another tab/user holds the exclusive edit lock.
type SceneLockError struct {
	Editor *SceneEditor
}

func (e *SceneLockError) Error() string {
	if e != nil && e.Editor != nil && e.Editor.Name != "" {
		return e.Editor.Name + " 正在编辑"
	}
	return "scene is being edited"
}

func (e *SceneLockError) Unwrap() error {
	return ErrConflict
}

// ShellStore is the Workspace Shell persistence API (stage 1.5).
type ShellStore interface {
	// EnsurePersonalWorkspace creates PERSONAL workspace + Private collection if missing.
	EnsurePersonalWorkspace(ctx context.Context, userID, displayName string) (*ShellWorkspace, error)

	ListShellWorkspaces(ctx context.Context, userID string) ([]*ShellWorkspace, error)
	GetShellWorkspace(ctx context.Context, userID, workspaceID string) (*ShellWorkspace, error)
	CreateShellWorkspace(ctx context.Context, userID, name, slug string, typ WorkspaceType) (*ShellWorkspace, error)
	UpdateShellWorkspace(ctx context.Context, userID, workspaceID string, name, slug *string) (*ShellWorkspace, error)
	UpdateShellWorkspaceAvatar(ctx context.Context, userID, workspaceID, avatarURL string) (*ShellWorkspace, error)
	DeleteShellWorkspace(ctx context.Context, userID, workspaceID string) error

	ListMembers(ctx context.Context, userID, workspaceID string) ([]*WorkspaceMember, error)
	InviteMember(ctx context.Context, actorID, workspaceID, email string, role WorkspaceRole) (*WorkspaceMember, error)
	UpdateMemberRole(ctx context.Context, actorID, workspaceID, memberID string, role WorkspaceRole) (*WorkspaceMember, error)
	RemoveMember(ctx context.Context, actorID, workspaceID, memberID string) error

	ListInviteLinks(ctx context.Context, userID, workspaceID string) ([]*InviteLink, error)
	CreateInviteLink(ctx context.Context, userID, workspaceID string, role WorkspaceRole, expiresAt *time.Time, maxUses *int) (*InviteLink, error)
	DeleteInviteLink(ctx context.Context, userID, workspaceID, linkID string) error
	JoinViaInviteLink(ctx context.Context, userID, code string, user MemberUser) (*ShellWorkspace, error)

	ListCollections(ctx context.Context, userID, workspaceID string) ([]*Collection, error)
	CreateCollection(ctx context.Context, userID, workspaceID, name string, icon, color *string, isPrivate bool) (*Collection, error)
	GetCollection(ctx context.Context, userID, collectionID string) (*Collection, error)
	UpdateCollection(ctx context.Context, userID, collectionID string, name, icon, color *string, isPrivate *bool) (*Collection, error)
	DeleteCollection(ctx context.Context, userID, collectionID string) error
	CopyCollectionToWorkspace(ctx context.Context, userID, collectionID, targetWorkspaceID string) (*Collection, error)
	MoveCollectionToWorkspace(ctx context.Context, userID, collectionID, targetWorkspaceID string) (*Collection, error)

	ListScenes(ctx context.Context, userID string, workspaceID, collectionID *string) ([]*WorkspaceScene, error)
	GetScene(ctx context.Context, userID, sceneID string) (*WorkspaceScene, error)
	GetSceneData(ctx context.Context, userID, sceneID string) ([]byte, error)
	CreateScene(ctx context.Context, userID string, title string, thumbnail *string, data []byte, collectionID *string) (*WorkspaceScene, error)
	UpdateScene(ctx context.Context, userID, sceneID string, title, thumbnail *string, data []byte) (*WorkspaceScene, error)
	UpdateSceneData(ctx context.Context, userID, sceneID string, data []byte) error
	UploadSceneThumbnail(ctx context.Context, userID, sceneID string, thumbnail string) (string, error)
	DeleteScene(ctx context.Context, userID, sceneID string) error
	DuplicateScene(ctx context.Context, userID, sceneID string) (*WorkspaceScene, error)
	MoveScene(ctx context.Context, userID, sceneID string, collectionID *string) (*WorkspaceScene, error)

	AcquireSceneLock(ctx context.Context, userID, sceneID, clientID, displayName string) (*WorkspaceScene, error)
	ReleaseSceneLock(ctx context.Context, userID, sceneID, clientID string) error
	SetSceneCollab(ctx context.Context, userID, sceneID string, enabled bool) (*WorkspaceScene, error)

	// UpsertUserProfile caches profile fields for member listings.
	UpsertUserProfile(ctx context.Context, user MemberUser) error
	GetUserProfile(ctx context.Context, userID string) (*MemberUser, error)
	UpdateUserProfile(ctx context.Context, userID string, name *string, avatarURL *string, clearAvatar bool) (*MemberUser, error)

	// MigrateLegacyGroupsToShell converts stage-1 canvas groups → collections (idempotent).
	MigrateLegacyGroupsToShell(ctx context.Context) error
}

var (
	ErrNotFound       = errors.New("not found")
	ErrForbidden      = errors.New("forbidden")
	ErrConflict       = errors.New("conflict")
	ErrInvalidInput   = errors.New("invalid input")
	ErrDeletePersonal = errors.New("cannot delete personal workspace")
)
