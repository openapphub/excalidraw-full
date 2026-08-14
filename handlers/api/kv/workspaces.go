package kv

import (
	"encoding/json"
	"excalidraw-complete/core"
	"excalidraw-complete/handlers/auth"
	"excalidraw-complete/middleware"
	"excalidraw-complete/stores"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/render"
	"github.com/sirupsen/logrus"
)

// userIDFromContext 从 JWT claims 中取出 user_id（与 kv.go 现有风格一致）。
func userIDFromContext(r *http.Request) (string, bool) {
	claims, ok := r.Context().Value(middleware.ClaimsContextKey).(*auth.AppClaims)
	if !ok {
		return "", false
	}
	return claims.Subject, true
}

// HandleListWorkspaces 列出当前用户的所有分组（首次访问懒创建 default 分组）。
func HandleListWorkspaces(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := userIDFromContext(r)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"error": "User claims not found"})
			return
		}

		workspaces, err := store.ListWorkspaces(r.Context(), userID)
		if err != nil {
			logrus.WithFields(logrus.Fields{
				"error":  err,
				"userID": userID,
			}).Error("Failed to list workspaces")
			render.Status(r, http.StatusInternalServerError)
			render.JSON(w, r, map[string]string{"error": "Failed to list workspaces"})
			return
		}

		if workspaces == nil {
			workspaces = []*core.Workspace{}
		}

		render.JSON(w, r, workspaces)
	}
}

// HandleCreateWorkspace 创建新分组（id 由 store 用 ULID 生成）。
func HandleCreateWorkspace(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := userIDFromContext(r)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"error": "User claims not found"})
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			logrus.WithError(err).Error("Failed to read request body")
			render.Status(r, http.StatusInternalServerError)
			render.JSON(w, r, map[string]string{"error": "Failed to read request body"})
			return
		}
		defer r.Body.Close()

		var req struct {
			Name string `json:"name"`
			Note string `json:"note"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			render.Status(r, http.StatusBadRequest)
			render.JSON(w, r, map[string]string{"error": "Invalid request body"})
			return
		}
		if req.Name == "" {
			render.Status(r, http.StatusBadRequest)
			render.JSON(w, r, map[string]string{"error": "Workspace name is required"})
			return
		}

		workspace, err := store.CreateWorkspace(r.Context(), userID, req.Name, req.Note)
		if err != nil {
			logrus.WithFields(logrus.Fields{
				"error":  err,
				"userID": userID,
			}).Error("Failed to create workspace")
			render.Status(r, http.StatusInternalServerError)
			render.JSON(w, r, map[string]string{"error": "Failed to create workspace"})
			return
		}

		render.Status(r, http.StatusCreated)
		render.JSON(w, r, workspace)
	}
}

// HandleUpdateWorkspace 更新分组名称/备注。
func HandleUpdateWorkspace(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := userIDFromContext(r)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"error": "User claims not found"})
			return
		}

		id := chi.URLParam(r, "id")
		if id == "" {
			render.Status(r, http.StatusBadRequest)
			render.JSON(w, r, map[string]string{"error": "Workspace id is required"})
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			logrus.WithError(err).Error("Failed to read request body")
			render.Status(r, http.StatusInternalServerError)
			render.JSON(w, r, map[string]string{"error": "Failed to read request body"})
			return
		}
		defer r.Body.Close()

		var req struct {
			Name *string `json:"name"`
			Note *string `json:"note"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			render.Status(r, http.StatusBadRequest)
			render.JSON(w, r, map[string]string{"error": "Invalid request body"})
			return
		}
		if req.Name == nil || *req.Name == "" {
			render.Status(r, http.StatusBadRequest)
			render.JSON(w, r, map[string]string{"error": "Workspace name is required"})
			return
		}
		note := ""
		if req.Note != nil {
			note = *req.Note
		}

		if err := store.UpdateWorkspace(r.Context(), userID, id, *req.Name, note); err != nil {
			logrus.WithFields(logrus.Fields{
				"error":  err,
				"userID": userID,
				"id":     id,
			}).Error("Failed to update workspace")
			render.Status(r, http.StatusNotFound)
			render.JSON(w, r, map[string]string{"error": "Workspace not found"})
			return
		}

		render.Status(r, http.StatusOK)
	}
}

// HandleDeleteWorkspace 删除分组（组内画布自动迁回 default）。
func HandleDeleteWorkspace(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := userIDFromContext(r)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"error": "User claims not found"})
			return
		}

		id := chi.URLParam(r, "id")
		if id == "" {
			render.Status(r, http.StatusBadRequest)
			render.JSON(w, r, map[string]string{"error": "Workspace id is required"})
			return
		}

		if err := store.DeleteWorkspace(r.Context(), userID, id); err != nil {
			logrus.WithFields(logrus.Fields{
				"error":  err,
				"userID": userID,
				"id":     id,
			}).Error("Failed to delete workspace")
			if err == core.ErrDeleteDefaultWorkspace {
				render.Status(r, http.StatusBadRequest)
				render.JSON(w, r, map[string]string{"error": "Cannot delete the default workspace"})
				return
			}
			render.Status(r, http.StatusNotFound)
			render.JSON(w, r, map[string]string{"error": "Workspace not found"})
			return
		}

		render.Status(r, http.StatusOK)
	}
}

// HandleMoveCanvasWorkspace 把画布移动到指定分组。
// 注册在 /api/v2/kv/{key}/workspace 子路由上（JWT 保护组内）。
func HandleMoveCanvasWorkspace(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := userIDFromContext(r)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"error": "User claims not found"})
			return
		}

		key := chi.URLParam(r, "key")
		if key == "" {
			render.Status(r, http.StatusBadRequest)
			render.JSON(w, r, map[string]string{"error": "Canvas key is required"})
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			logrus.WithError(err).Error("Failed to read request body")
			render.Status(r, http.StatusInternalServerError)
			render.JSON(w, r, map[string]string{"error": "Failed to read request body"})
			return
		}
		defer r.Body.Close()

		var req struct {
			WorkspaceID string `json:"workspaceId"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			render.Status(r, http.StatusBadRequest)
			render.JSON(w, r, map[string]string{"error": "Invalid request body"})
			return
		}
		if req.WorkspaceID == "" {
			render.Status(r, http.StatusBadRequest)
			render.JSON(w, r, map[string]string{"error": "workspaceId is required"})
			return
		}

		if err := store.MoveCanvasWorkspace(r.Context(), userID, key, req.WorkspaceID); err != nil {
			logrus.WithFields(logrus.Fields{
				"error":       err,
				"userID":      userID,
				"key":         key,
				"workspaceID": req.WorkspaceID,
			}).Error("Failed to move canvas workspace")
			render.Status(r, http.StatusNotFound)
			render.JSON(w, r, map[string]string{"error": "Canvas or workspace not found"})
			return
		}

		render.Status(r, http.StatusOK)
	}
}
