package comments

import (
	"excalidraw-complete/stores"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/render"
)

// 站内通知（COMMENT / MENTION）。与评论同包，避免为 4 个端点再复制一份
// claims/错误处理样板。

// HandleListNotifications GET /api/v2/notifications?cursor=&limit=&unread=
func HandleListNotifications(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			writeUnauthorized(w, r)
			return
		}

		query := r.URL.Query()
		limit := 0
		if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil {
				limit = parsed
			}
		}
		unread := query.Get("unread") == "true" || query.Get("unread") == "1"

		resp, err := store.ListNotifications(r.Context(), claims.Subject,
			strings.TrimSpace(query.Get("cursor")), limit, unread)
		if err != nil {
			writeErr(w, r, err, "Failed to list notifications")
			return
		}
		render.JSON(w, r, resp)
	}
}

// HandleUnreadCount GET /api/v2/notifications/unread-count
func HandleUnreadCount(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			writeUnauthorized(w, r)
			return
		}
		count, err := store.CountUnreadNotifications(r.Context(), claims.Subject)
		if err != nil {
			writeErr(w, r, err, "Failed to get unread count")
			return
		}
		render.JSON(w, r, map[string]int{"count": count})
	}
}

// HandleMarkNotificationRead POST /api/v2/notifications/{id}/read
func HandleMarkNotificationRead(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			writeUnauthorized(w, r)
			return
		}
		if err := store.MarkNotificationRead(r.Context(), claims.Subject, chi.URLParam(r, "id")); err != nil {
			writeErr(w, r, err, "Failed to mark notification as read")
			return
		}
		successJSON(w, r)
	}
}

// HandleMarkAllNotificationsRead POST /api/v2/notifications/read-all
func HandleMarkAllNotificationsRead(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			writeUnauthorized(w, r)
			return
		}
		if err := store.MarkAllNotificationsRead(r.Context(), claims.Subject); err != nil {
			writeErr(w, r, err, "Failed to mark all notifications as read")
			return
		}
		successJSON(w, r)
	}
}
