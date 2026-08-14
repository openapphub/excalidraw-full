package core

import (
	"context"
	"time"
)

// 画布评论（阶段 4）：对齐 AstraDraw 客户端 auth/api/types.ts 的
// CommentThread / Comment / Notification 形状，JSON 不加 {data:} 包装。

// UserSummary 是评论/通知里展示作者所需的最小用户信息。
type UserSummary struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Email  string `json:"email"`
	Avatar string `json:"avatar,omitempty"`
}

// Comment 是线程内的一条评论。
type Comment struct {
	ID        string      `json:"id"`
	ThreadID  string      `json:"threadId"`
	Content   string      `json:"content"`
	Mentions  []string    `json:"mentions"`
	CreatedBy UserSummary `json:"createdBy"`
	EditedAt  *time.Time  `json:"editedAt,omitempty"`
	CreatedAt time.Time   `json:"createdAt"`
}

// CommentThread 是锚定在画布坐标上的图钉线程。
type CommentThread struct {
	ID           string       `json:"id"`
	SceneID      string       `json:"sceneId"`
	X            float64      `json:"x"`
	Y            float64      `json:"y"`
	Resolved     bool         `json:"resolved"`
	ResolvedAt   *time.Time   `json:"resolvedAt,omitempty"`
	ResolvedBy   *UserSummary `json:"resolvedBy,omitempty"`
	CreatedBy    UserSummary  `json:"createdBy"`
	Comments     []Comment    `json:"comments"`
	CommentCount int          `json:"commentCount"`
	CreatedAt    time.Time    `json:"createdAt"`
	UpdatedAt    time.Time    `json:"updatedAt"`
}

// NotificationType 目前只有评论回复与 @提及两种。
type NotificationType string

const (
	NotificationComment NotificationType = "COMMENT"
	NotificationMention NotificationType = "MENTION"
)

// NotificationRef 是客户端期望的 { id } 引用形状。
type NotificationRef struct {
	ID string `json:"id"`
}

// NotificationScene 携带场景名，便于通知列表直接展示。
type NotificationScene struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Notification struct {
	ID        string            `json:"id"`
	Type      NotificationType  `json:"type"`
	Actor     UserSummary       `json:"actor"`
	Thread    *NotificationRef  `json:"thread,omitempty"`
	Comment   *NotificationRef  `json:"comment,omitempty"`
	Scene     NotificationScene `json:"scene"`
	Read      bool              `json:"read"`
	ReadAt    *time.Time        `json:"readAt,omitempty"`
	CreatedAt time.Time         `json:"createdAt"`
}

// NotificationsResponse 是游标分页响应。
type NotificationsResponse struct {
	Notifications []*Notification `json:"notifications"`
	NextCursor    string          `json:"nextCursor,omitempty"`
	HasMore       bool            `json:"hasMore"`
}

// CommentStore 是评论 + 通知的持久化 API。
//
// 权限：能读该 scene 的用户即可读评论；能写该 scene（非 VIEWER）才能写评论，
// 具体判定复用 ShellStore 的场景/集合成员逻辑。
type CommentStore interface {
	ListThreads(ctx context.Context, userID, sceneID string, resolved *bool) ([]*CommentThread, error)
	GetThread(ctx context.Context, userID, threadID string) (*CommentThread, error)
	CreateThread(ctx context.Context, userID, sceneID string, x, y float64, content string, mentions []string) (*CommentThread, error)
	UpdateThreadPosition(ctx context.Context, userID, threadID string, x, y *float64) (*CommentThread, error)
	SetThreadResolved(ctx context.Context, userID, threadID string, resolved bool) (*CommentThread, error)
	DeleteThread(ctx context.Context, userID, threadID string) error

	AddComment(ctx context.Context, userID, threadID, content string, mentions []string) (*Comment, error)
	UpdateComment(ctx context.Context, userID, commentID, content string) (*Comment, error)
	DeleteComment(ctx context.Context, userID, commentID string) error

	ListNotifications(ctx context.Context, userID, cursor string, limit int, unreadOnly bool) (*NotificationsResponse, error)
	CountUnreadNotifications(ctx context.Context, userID string) (int, error)
	MarkNotificationRead(ctx context.Context, userID, notificationID string) error
	MarkAllNotificationsRead(ctx context.Context, userID string) error
}
