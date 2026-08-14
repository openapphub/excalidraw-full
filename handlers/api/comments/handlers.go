// Package comments 提供画布评论线程与站内通知的 HTTP 接口。
//
// 路径/响应形状对齐 AstraDraw 客户端（auth/api/comments.ts、auth/api/notifications.ts）：
// 无 {data:} 包装，错误统一 {"message": "..."}，鉴权走 JWT Bearer。
package comments

import (
	"encoding/json"
	"errors"
	"excalidraw-complete/core"
	"excalidraw-complete/handlers/auth"
	"excalidraw-complete/middleware"
	"excalidraw-complete/stores"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/render"
	"github.com/sirupsen/logrus"
)

func userFromContext(r *http.Request) (*auth.AppClaims, bool) {
	claims, ok := r.Context().Value(middleware.ClaimsContextKey).(*auth.AppClaims)
	if !ok || claims == nil || claims.Subject == "" {
		return nil, false
	}
	return claims, true
}

func writeUnauthorized(w http.ResponseWriter, r *http.Request) {
	render.Status(r, http.StatusUnauthorized)
	render.JSON(w, r, map[string]string{"message": "Not authenticated"})
}

func writeErr(w http.ResponseWriter, r *http.Request, err error, fallback string) {
	status := http.StatusInternalServerError
	msg := fallback
	switch {
	case errors.Is(err, core.ErrNotFound):
		status = http.StatusNotFound
		msg = "Not found"
	case errors.Is(err, core.ErrForbidden):
		status = http.StatusForbidden
		msg = "Access denied"
	case errors.Is(err, core.ErrConflict):
		status = http.StatusConflict
		msg = err.Error()
	case errors.Is(err, core.ErrInvalidInput):
		status = http.StatusBadRequest
		msg = "Invalid input"
	}
	logrus.WithError(err).WithField("status", status).Warn(fallback)
	render.Status(r, status)
	render.JSON(w, r, map[string]string{"message": msg})
}

func readJSON(r *http.Request, dst interface{}) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if len(body) == 0 {
		return nil
	}
	return json.Unmarshal(body, dst)
}

// ensureProfile 让评论作者名/头像在 user_profiles 里保持最新。
func ensureProfile(store stores.Store, r *http.Request, claims *auth.AppClaims) {
	user := core.MemberUser{ID: claims.Subject, Email: claims.Email}
	if claims.Name != "" {
		n := claims.Name
		user.Name = &n
	}
	if claims.AvatarURL != "" {
		a := claims.AvatarURL
		user.AvatarURL = &a
	}
	_ = store.UpsertUserProfile(r.Context(), user)
}

func successJSON(w http.ResponseWriter, r *http.Request) {
	render.JSON(w, r, map[string]bool{"success": true})
}

// ---------------------------------------------------------------------------
// Threads
// ---------------------------------------------------------------------------

// HandleListThreads GET /api/v2/scenes/{sceneId}/threads
func HandleListThreads(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			writeUnauthorized(w, r)
			return
		}
		sceneID := chi.URLParam(r, "sceneId")

		var resolved *bool
		if raw := strings.TrimSpace(r.URL.Query().Get("resolved")); raw != "" {
			value := raw == "true" || raw == "1"
			resolved = &value
		}

		threads, err := store.ListThreads(r.Context(), claims.Subject, sceneID, resolved)
		if err != nil {
			writeErr(w, r, err, "Failed to list comment threads")
			return
		}
		if threads == nil {
			threads = []*core.CommentThread{}
		}
		render.JSON(w, r, threads)
	}
}

type createThreadRequest struct {
	X        float64  `json:"x"`
	Y        float64  `json:"y"`
	Content  string   `json:"content"`
	Mentions []string `json:"mentions"`
}

// HandleCreateThread POST /api/v2/scenes/{sceneId}/threads
func HandleCreateThread(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			writeUnauthorized(w, r)
			return
		}
		ensureProfile(store, r, claims)

		var req createThreadRequest
		if err := readJSON(r, &req); err != nil {
			writeErr(w, r, core.ErrInvalidInput, "Invalid request body")
			return
		}

		thread, err := store.CreateThread(r.Context(), claims.Subject,
			chi.URLParam(r, "sceneId"), req.X, req.Y, req.Content, req.Mentions)
		if err != nil {
			writeErr(w, r, err, "Failed to create comment thread")
			return
		}
		render.Status(r, http.StatusCreated)
		render.JSON(w, r, thread)
	}
}

// HandleGetThread GET /api/v2/threads/{threadId}
func HandleGetThread(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			writeUnauthorized(w, r)
			return
		}
		thread, err := store.GetThread(r.Context(), claims.Subject, chi.URLParam(r, "threadId"))
		if err != nil {
			writeErr(w, r, err, "Failed to get comment thread")
			return
		}
		render.JSON(w, r, thread)
	}
}

type updateThreadRequest struct {
	X *float64 `json:"x"`
	Y *float64 `json:"y"`
}

// HandleUpdateThread PUT|PATCH /api/v2/threads/{threadId}
func HandleUpdateThread(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			writeUnauthorized(w, r)
			return
		}
		var req updateThreadRequest
		if err := readJSON(r, &req); err != nil {
			writeErr(w, r, core.ErrInvalidInput, "Invalid request body")
			return
		}
		thread, err := store.UpdateThreadPosition(r.Context(), claims.Subject,
			chi.URLParam(r, "threadId"), req.X, req.Y)
		if err != nil {
			writeErr(w, r, err, "Failed to update thread position")
			return
		}
		render.JSON(w, r, thread)
	}
}

// handleResolve 共享 resolve/reopen 两个端点。
func handleResolve(store stores.Store, resolved bool, fallback string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			writeUnauthorized(w, r)
			return
		}
		ensureProfile(store, r, claims)
		thread, err := store.SetThreadResolved(r.Context(), claims.Subject,
			chi.URLParam(r, "threadId"), resolved)
		if err != nil {
			writeErr(w, r, err, fallback)
			return
		}
		render.JSON(w, r, thread)
	}
}

// HandleResolveThread POST /api/v2/threads/{threadId}/resolve
func HandleResolveThread(store stores.Store) http.HandlerFunc {
	return handleResolve(store, true, "Failed to resolve thread")
}

// HandleReopenThread POST /api/v2/threads/{threadId}/reopen
func HandleReopenThread(store stores.Store) http.HandlerFunc {
	return handleResolve(store, false, "Failed to reopen thread")
}

// HandleDeleteThread DELETE /api/v2/threads/{threadId}
func HandleDeleteThread(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			writeUnauthorized(w, r)
			return
		}
		if err := store.DeleteThread(r.Context(), claims.Subject, chi.URLParam(r, "threadId")); err != nil {
			writeErr(w, r, err, "Failed to delete comment thread")
			return
		}
		successJSON(w, r)
	}
}

// ---------------------------------------------------------------------------
// Comments
// ---------------------------------------------------------------------------

type createCommentRequest struct {
	Content  string   `json:"content"`
	Mentions []string `json:"mentions"`
}

// HandleAddComment POST /api/v2/threads/{threadId}/comments
func HandleAddComment(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			writeUnauthorized(w, r)
			return
		}
		ensureProfile(store, r, claims)

		var req createCommentRequest
		if err := readJSON(r, &req); err != nil {
			writeErr(w, r, core.ErrInvalidInput, "Invalid request body")
			return
		}
		comment, err := store.AddComment(r.Context(), claims.Subject,
			chi.URLParam(r, "threadId"), req.Content, req.Mentions)
		if err != nil {
			writeErr(w, r, err, "Failed to add comment")
			return
		}
		render.Status(r, http.StatusCreated)
		render.JSON(w, r, comment)
	}
}

type updateCommentRequest struct {
	Content string `json:"content"`
}

// HandleUpdateComment PUT|PATCH /api/v2/comments/{id}
func HandleUpdateComment(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			writeUnauthorized(w, r)
			return
		}
		var req updateCommentRequest
		if err := readJSON(r, &req); err != nil {
			writeErr(w, r, core.ErrInvalidInput, "Invalid request body")
			return
		}
		comment, err := store.UpdateComment(r.Context(), claims.Subject,
			chi.URLParam(r, "id"), req.Content)
		if err != nil {
			writeErr(w, r, err, "Failed to update comment")
			return
		}
		render.JSON(w, r, comment)
	}
}

// HandleDeleteComment DELETE /api/v2/comments/{id}
func HandleDeleteComment(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			writeUnauthorized(w, r)
			return
		}
		if err := store.DeleteComment(r.Context(), claims.Subject, chi.URLParam(r, "id")); err != nil {
			writeErr(w, r, err, "Failed to delete comment")
			return
		}
		successJSON(w, r)
	}
}
