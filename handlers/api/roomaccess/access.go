package roomaccess

import (
	"context"
	"errors"
	"excalidraw-complete/core"
	"excalidraw-complete/handlers/auth"
	"excalidraw-complete/stores"
	"net/http"
	"regexp"
	"strings"
)

var anonymousRoomIDPattern = regexp.MustCompile(`^[0-9a-f]{20}$`)

// Access 描述协作房间是否绑定持久化 Scene，以及当前用户是否可写。
// 不存在于 Workspace 存储中的随机房间保留官方匿名协作语义。
type Access struct {
	UserID         string
	WorkspaceScene bool
	CanEdit        bool
}

// BearerToken 读取标准 Authorization 头；无效格式按未提供处理。
func BearerToken(r *http.Request) string {
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return parts[1]
}

// Authorize 先把 roomID 当作持久化 Scene 查找。若 Scene 存在，则必须有
// 有效 JWT 且通过服务端 Workspace ACL；只有符合官方生成格式且确实不存在的
// roomID 才按匿名房间放行。持久化 Scene 的 ULID 删除后不能降级成匿名房间。
func Authorize(ctx context.Context, store stores.Store, roomID, token string) (Access, error) {
	if store == nil {
		if anonymousRoomIDPattern.MatchString(roomID) {
			return Access{}, nil
		}
		return Access{}, core.ErrForbidden
	}

	if token != "" {
		if claims, err := auth.ParseJWT(token); err == nil {
			scene, sceneErr := store.GetScene(ctx, claims.Subject, roomID)
			switch {
			case sceneErr == nil:
				return Access{
					UserID:         claims.Subject,
					WorkspaceScene: true,
					CanEdit:        scene.CanEdit,
				}, nil
			case !errors.Is(sceneErr, core.ErrNotFound):
				return Access{}, sceneErr
			}
		}
	}

	// 无有效身份时仍查询一次：已存在 Scene 会返回 Forbidden。查询不到时也只允许
	// 官方客户端生成的 10 字节十六进制 ID，不能让已删除 ULID 或任意旧 ID 降级匿名。
	if _, err := store.GetScene(ctx, "", roomID); err == nil {
		return Access{}, core.ErrForbidden
	} else if errors.Is(err, core.ErrNotFound) {
		if anonymousRoomIDPattern.MatchString(roomID) {
			return Access{}, nil
		}
		return Access{}, core.ErrForbidden
	} else {
		return Access{}, err
	}
}
