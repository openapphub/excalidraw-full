// Package shellstub 提供 core.ShellStore 的“未支持”占位实现。
//
// Workspace Shell（阶段 1.5）目前只在 sqlite 后端落地，memory/filesystem/aws
// 后端通过嵌入 Unsupported 满足统一的 stores.Store 接口，读操作返回空集合以便
// 前端可降级渲染，写操作返回 ErrUnsupported。
package shellstub

import (
	"context"
	"errors"
	"excalidraw-complete/core"
	"time"
)

// ErrUnsupported 表示当前存储后端未实现 Workspace Shell。
var ErrUnsupported = errors.New("shell store not supported on this backend")

// Unsupported 是零大小占位类型，嵌入后即满足 core.ShellStore。
type Unsupported struct{}

func (Unsupported) EnsurePersonalWorkspace(ctx context.Context, userID, displayName string) (*core.ShellWorkspace, error) {
	return nil, ErrUnsupported
}

func (Unsupported) ListShellWorkspaces(ctx context.Context, userID string) ([]*core.ShellWorkspace, error) {
	return []*core.ShellWorkspace{}, nil
}

func (Unsupported) GetShellWorkspace(ctx context.Context, userID, workspaceID string) (*core.ShellWorkspace, error) {
	return nil, core.ErrNotFound
}

func (Unsupported) CreateShellWorkspace(ctx context.Context, userID, name, slug string, typ core.WorkspaceType) (*core.ShellWorkspace, error) {
	return nil, ErrUnsupported
}

func (Unsupported) UpdateShellWorkspace(ctx context.Context, userID, workspaceID string, name, slug *string) (*core.ShellWorkspace, error) {
	return nil, ErrUnsupported
}

func (Unsupported) UpdateShellWorkspaceAvatar(ctx context.Context, userID, workspaceID, avatarURL string) (*core.ShellWorkspace, error) {
	return nil, ErrUnsupported
}

func (Unsupported) DeleteShellWorkspace(ctx context.Context, userID, workspaceID string) error {
	return ErrUnsupported
}

func (Unsupported) ListMembers(ctx context.Context, userID, workspaceID string) ([]*core.WorkspaceMember, error) {
	return []*core.WorkspaceMember{}, nil
}

func (Unsupported) InviteMember(ctx context.Context, actorID, workspaceID, email string, role core.WorkspaceRole) (*core.WorkspaceMember, error) {
	return nil, ErrUnsupported
}

func (Unsupported) UpdateMemberRole(ctx context.Context, actorID, workspaceID, memberID string, role core.WorkspaceRole) (*core.WorkspaceMember, error) {
	return nil, ErrUnsupported
}

func (Unsupported) RemoveMember(ctx context.Context, actorID, workspaceID, memberID string) error {
	return ErrUnsupported
}

func (Unsupported) ListInviteLinks(ctx context.Context, userID, workspaceID string) ([]*core.InviteLink, error) {
	return []*core.InviteLink{}, nil
}

func (Unsupported) CreateInviteLink(ctx context.Context, userID, workspaceID string, role core.WorkspaceRole, expiresAt *time.Time, maxUses *int) (*core.InviteLink, error) {
	return nil, ErrUnsupported
}

func (Unsupported) DeleteInviteLink(ctx context.Context, userID, workspaceID, linkID string) error {
	return ErrUnsupported
}

func (Unsupported) JoinViaInviteLink(ctx context.Context, userID, code string, user core.MemberUser) (*core.ShellWorkspace, error) {
	return nil, ErrUnsupported
}

func (Unsupported) ListCollections(ctx context.Context, userID, workspaceID string) ([]*core.Collection, error) {
	return []*core.Collection{}, nil
}

func (Unsupported) CreateCollection(ctx context.Context, userID, workspaceID, name string, icon, color *string, isPrivate bool) (*core.Collection, error) {
	return nil, ErrUnsupported
}

func (Unsupported) GetCollection(ctx context.Context, userID, collectionID string) (*core.Collection, error) {
	return nil, core.ErrNotFound
}

func (Unsupported) UpdateCollection(ctx context.Context, userID, collectionID string, name, icon, color *string, isPrivate *bool) (*core.Collection, error) {
	return nil, ErrUnsupported
}

func (Unsupported) DeleteCollection(ctx context.Context, userID, collectionID string) error {
	return ErrUnsupported
}

func (Unsupported) CopyCollectionToWorkspace(ctx context.Context, userID, collectionID, targetWorkspaceID string) (*core.Collection, error) {
	return nil, ErrUnsupported
}

func (Unsupported) MoveCollectionToWorkspace(ctx context.Context, userID, collectionID, targetWorkspaceID string) (*core.Collection, error) {
	return nil, ErrUnsupported
}

func (Unsupported) ListScenes(ctx context.Context, userID string, workspaceID, collectionID *string) ([]*core.WorkspaceScene, error) {
	return []*core.WorkspaceScene{}, nil
}

func (Unsupported) GetScene(ctx context.Context, userID, sceneID string) (*core.WorkspaceScene, error) {
	return nil, core.ErrNotFound
}

func (Unsupported) GetSceneData(ctx context.Context, userID, sceneID string) ([]byte, error) {
	return nil, core.ErrNotFound
}

func (Unsupported) CreateScene(ctx context.Context, userID string, title string, thumbnail *string, data []byte, collectionID *string) (*core.WorkspaceScene, error) {
	return nil, ErrUnsupported
}

func (Unsupported) UpdateScene(ctx context.Context, userID, sceneID string, title, thumbnail *string, data []byte) (*core.WorkspaceScene, error) {
	return nil, ErrUnsupported
}

func (Unsupported) UpdateSceneData(ctx context.Context, userID, sceneID string, data []byte) error {
	return ErrUnsupported
}

func (Unsupported) UploadSceneThumbnail(ctx context.Context, userID, sceneID string, thumbnail string) (string, error) {
	return "", ErrUnsupported
}

func (Unsupported) DeleteScene(ctx context.Context, userID, sceneID string) error {
	return ErrUnsupported
}

func (Unsupported) DuplicateScene(ctx context.Context, userID, sceneID string) (*core.WorkspaceScene, error) {
	return nil, ErrUnsupported
}

func (Unsupported) MoveScene(ctx context.Context, userID, sceneID string, collectionID *string) (*core.WorkspaceScene, error) {
	return nil, ErrUnsupported
}

func (Unsupported) AcquireSceneLock(ctx context.Context, userID, sceneID, clientID, displayName string) (*core.WorkspaceScene, error) {
	return nil, ErrUnsupported
}

func (Unsupported) ReleaseSceneLock(ctx context.Context, userID, sceneID, clientID string) error {
	return ErrUnsupported
}

func (Unsupported) SetSceneCollab(ctx context.Context, userID, sceneID string, enabled bool) (*core.WorkspaceScene, error) {
	return nil, ErrUnsupported
}

func (Unsupported) UpsertUserProfile(ctx context.Context, user core.MemberUser) error {
	return nil
}

func (Unsupported) GetUserProfile(ctx context.Context, userID string) (*core.MemberUser, error) {
	return &core.MemberUser{ID: userID}, nil
}

func (Unsupported) UpdateUserProfile(ctx context.Context, userID string, name *string, avatarURL *string, clearAvatar bool) (*core.MemberUser, error) {
	return nil, ErrUnsupported
}

func (Unsupported) CreateLocalUser(ctx context.Context, email, passwordHash, name string) (*core.LocalUser, error) {
	return nil, ErrUnsupported
}

func (Unsupported) GetLocalUserByEmail(ctx context.Context, email string) (*core.LocalUser, error) {
	return nil, core.ErrNotFound
}

func (Unsupported) GetLocalUserByID(ctx context.Context, id string) (*core.LocalUser, error) {
	return nil, core.ErrNotFound
}

// MigrateLegacyGroupsToShell 无老数据可迁移，直接成功以免拖垮启动流程。
func (Unsupported) MigrateLegacyGroupsToShell(ctx context.Context) error {
	return nil
}

var _ core.ShellStore = Unsupported{}
