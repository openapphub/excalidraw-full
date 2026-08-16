package roomaccess

import (
	"context"
	"errors"
	"excalidraw-complete/core"
	"excalidraw-complete/handlers/auth"
	"excalidraw-complete/stores/sqlite"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const roomAccessTestSecret = "room-access-test-secret-at-least-32-bytes"

func signedTestToken(t *testing.T, userID string) string {
	t.Helper()
	t.Setenv("JWT_SECRET", roomAccessTestSecret)
	auth.Init()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, auth.AppClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	})
	signed, err := token.SignedString([]byte(roomAccessTestSecret))
	if err != nil {
		t.Fatalf("签发测试 JWT 失败: %v", err)
	}
	return signed
}

func TestAuthorizeSeparatesWorkspaceScenesFromAnonymousRooms(t *testing.T) {
	store := sqlite.NewStore(t.TempDir() + "/room-access.db")
	ctx := context.Background()
	const ownerID = "owner"
	const viewerID = "viewer"
	const outsiderID = "outsider"

	workspace, err := store.CreateShellWorkspace(ctx, ownerID, "团队", "", core.WorkspaceShared)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := store.CreateCollection(ctx, ownerID, workspace.ID, "项目", nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	scene, err := store.CreateScene(ctx, ownerID, "受保护画布", nil, []byte(`{"elements":[]}`), &collection.ID)
	if err != nil {
		t.Fatal(err)
	}
	invite, err := store.CreateInviteLink(ctx, ownerID, workspace.ID, core.RoleViewer, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.JoinViaInviteLink(ctx, viewerID, invite.Code, core.MemberUser{ID: viewerID}); err != nil {
		t.Fatal(err)
	}

	if access, err := Authorize(ctx, store, "0123456789abcdefabcd", ""); err != nil || access.WorkspaceScene {
		t.Fatalf("随机匿名房间应放行: access=%+v err=%v", access, err)
	}
	for _, roomID := range []string{
		"random-anonymous-room",
		"0123456789ABCDEFABCD",
		"01J5M8D2N7E6P4Q3R2S1T0V9WX",
		"ai-01J5M8D2N7E6P4Q3R2S1T0V9WX",
	} {
		if _, err := Authorize(ctx, store, roomID, ""); !errors.Is(err, core.ErrForbidden) {
			t.Fatalf("非官方匿名 roomID %q 错误 = %v，期望 ErrForbidden", roomID, err)
		}
	}
	if _, err := Authorize(ctx, store, scene.ID, ""); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("未认证加入 Workspace Scene 错误 = %v，期望 ErrForbidden", err)
	}
	if _, err := Authorize(ctx, store, scene.ID, signedTestToken(t, outsiderID)); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("非成员加入 Workspace Scene 错误 = %v，期望 ErrForbidden", err)
	}

	viewerAccess, err := Authorize(ctx, store, scene.ID, signedTestToken(t, viewerID))
	if err != nil {
		t.Fatalf("VIEWER 应可读取协作房间: %v", err)
	}
	if !viewerAccess.WorkspaceScene || viewerAccess.CanEdit {
		t.Fatalf("VIEWER 权限错误: %+v", viewerAccess)
	}

	ownerAccess, err := Authorize(ctx, store, scene.ID, signedTestToken(t, ownerID))
	if err != nil {
		t.Fatalf("所有者应可加入协作房间: %v", err)
	}
	if !ownerAccess.WorkspaceScene || !ownerAccess.CanEdit {
		t.Fatalf("所有者权限错误: %+v", ownerAccess)
	}
}

func TestAuthorizeDoesNotDowngradeDeletedWorkspaceSceneToAnonymousRoom(t *testing.T) {
	store := sqlite.NewStore(t.TempDir() + "/deleted-room-access.db")
	ctx := context.Background()
	const ownerID = "owner"

	workspace, err := store.CreateShellWorkspace(ctx, ownerID, "团队", "", core.WorkspaceShared)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := store.CreateCollection(ctx, ownerID, workspace.ID, "项目", nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	scene, err := store.CreateScene(ctx, ownerID, "待删除画布", nil, []byte(`{"elements":[]}`), &collection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteScene(ctx, ownerID, scene.ID); err != nil {
		t.Fatal(err)
	}

	for _, token := range []string{"", signedTestToken(t, ownerID)} {
		if _, err := Authorize(ctx, store, scene.ID, token); !errors.Is(err, core.ErrForbidden) {
			t.Fatalf("已删除 Workspace Scene 不得降级匿名: token=%t err=%v", token != "", err)
		}
	}
}

func TestAuthorizeWithoutStoreOnlyAllowsOfficialAnonymousRoomIDs(t *testing.T) {
	if _, err := Authorize(context.Background(), nil, "0123456789abcdefabcd", ""); err != nil {
		t.Fatalf("无 Store 时合法匿名房间应放行: %v", err)
	}
	if _, err := Authorize(context.Background(), nil, "deleted-scene-id", ""); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("无 Store 时任意 roomID 错误 = %v，期望 ErrForbidden", err)
	}
}
