package shellstub

import (
	"context"
	"excalidraw-complete/core"
)

// 画布评论 + 通知只在 sqlite 后端落地；其余后端读操作返回空集合，写操作报
// ErrUnsupported，让前端可以降级渲染而不是白屏。

func (Unsupported) ListThreads(ctx context.Context, userID, sceneID string, resolved *bool) ([]*core.CommentThread, error) {
	return []*core.CommentThread{}, nil
}

func (Unsupported) GetThread(ctx context.Context, userID, threadID string) (*core.CommentThread, error) {
	return nil, core.ErrNotFound
}

func (Unsupported) CreateThread(ctx context.Context, userID, sceneID string, x, y float64, content string, mentions []string) (*core.CommentThread, error) {
	return nil, ErrUnsupported
}

func (Unsupported) UpdateThreadPosition(ctx context.Context, userID, threadID string, x, y *float64) (*core.CommentThread, error) {
	return nil, ErrUnsupported
}

func (Unsupported) SetThreadResolved(ctx context.Context, userID, threadID string, resolved bool) (*core.CommentThread, error) {
	return nil, ErrUnsupported
}

func (Unsupported) DeleteThread(ctx context.Context, userID, threadID string) error {
	return ErrUnsupported
}

func (Unsupported) AddComment(ctx context.Context, userID, threadID, content string, mentions []string) (*core.Comment, error) {
	return nil, ErrUnsupported
}

func (Unsupported) UpdateComment(ctx context.Context, userID, commentID, content string) (*core.Comment, error) {
	return nil, ErrUnsupported
}

func (Unsupported) DeleteComment(ctx context.Context, userID, commentID string) error {
	return ErrUnsupported
}

func (Unsupported) ListNotifications(ctx context.Context, userID, cursor string, limit int, unreadOnly bool) (*core.NotificationsResponse, error) {
	return &core.NotificationsResponse{Notifications: []*core.Notification{}, HasMore: false}, nil
}

func (Unsupported) CountUnreadNotifications(ctx context.Context, userID string) (int, error) {
	return 0, nil
}

func (Unsupported) MarkNotificationRead(ctx context.Context, userID, notificationID string) error {
	return ErrUnsupported
}

func (Unsupported) MarkAllNotificationsRead(ctx context.Context, userID string) error {
	return ErrUnsupported
}

var _ core.CommentStore = Unsupported{}
