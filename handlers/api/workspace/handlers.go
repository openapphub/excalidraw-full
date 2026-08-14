package workspace

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"excalidraw-complete/core"
	"excalidraw-complete/handlers/auth"
	"excalidraw-complete/middleware"
	"excalidraw-complete/stores"
	"io"
	"net/http"
	"strings"
	"time"

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

func memberUserFromClaims(c *auth.AppClaims) core.MemberUser {
	u := core.MemberUser{ID: c.Subject, Email: c.Email}
	if c.Name != "" {
		n := c.Name
		u.Name = &n
	}
	if c.AvatarURL != "" {
		a := c.AvatarURL
		u.AvatarURL = &a
	}
	return u
}

func writeErr(w http.ResponseWriter, r *http.Request, err error, fallback string) {
	status := http.StatusInternalServerError
	msg := fallback
	var lockErr *core.SceneLockError
	switch {
	case errors.As(err, &lockErr):
		status = http.StatusConflict
		render.Status(r, status)
		render.JSON(w, r, map[string]any{
			"message": lockErr.Error(),
			"editor":  lockErr.Editor,
		})
		return
	case errors.Is(err, core.ErrNotFound):
		status = http.StatusNotFound
		msg = err.Error()
	case errors.Is(err, core.ErrForbidden):
		status = http.StatusForbidden
		msg = "Access denied"
	case errors.Is(err, core.ErrConflict):
		status = http.StatusConflict
		msg = err.Error()
	case errors.Is(err, core.ErrInvalidInput), errors.Is(err, core.ErrDeletePersonal):
		status = http.StatusBadRequest
		msg = err.Error()
	}
	logrus.WithError(err).WithField("status", status).Warn(fallback)
	render.Status(r, status)
	render.JSON(w, r, map[string]string{"message": msg})
}

func ensureProfile(store stores.Store, r *http.Request, claims *auth.AppClaims) {
	_ = store.UpsertUserProfile(r.Context(), memberUserFromClaims(claims))
	_, _ = store.EnsurePersonalWorkspace(r.Context(), claims.Subject, claims.Name)
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

// ---- Workspaces ----

func HandleListWorkspaces(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"message": "Not authenticated"})
			return
		}
		ensureProfile(store, r, claims)
		list, err := store.ListShellWorkspaces(r.Context(), claims.Subject)
		if err != nil {
			writeErr(w, r, err, "Failed to list workspaces")
			return
		}
		if list == nil {
			list = []*core.ShellWorkspace{}
		}
		render.JSON(w, r, list)
	}
}

func HandleCreateWorkspace(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"message": "Not authenticated"})
			return
		}
		ensureProfile(store, r, claims)
		var req struct {
			Name string `json:"name"`
			Slug string `json:"slug"`
			Type string `json:"type"`
		}
		if err := readJSON(r, &req); err != nil || req.Name == "" {
			render.Status(r, http.StatusBadRequest)
			render.JSON(w, r, map[string]string{"message": "name is required"})
			return
		}
		typ := core.WorkspaceShared
		if strings.EqualFold(req.Type, string(core.WorkspacePersonal)) {
			typ = core.WorkspacePersonal
		}
		ws, err := store.CreateShellWorkspace(r.Context(), claims.Subject, req.Name, req.Slug, typ)
		if err != nil {
			writeErr(w, r, err, "Failed to create workspace")
			return
		}
		render.Status(r, http.StatusCreated)
		render.JSON(w, r, ws)
	}
}

func HandleGetWorkspace(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"message": "Not authenticated"})
			return
		}
		ws, err := store.GetShellWorkspace(r.Context(), claims.Subject, chi.URLParam(r, "id"))
		if err != nil {
			writeErr(w, r, err, "Failed to get workspace")
			return
		}
		render.JSON(w, r, ws)
	}
}

func HandleUpdateWorkspace(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"message": "Not authenticated"})
			return
		}
		var req struct {
			Name *string `json:"name"`
			Slug *string `json:"slug"`
		}
		if err := readJSON(r, &req); err != nil {
			render.Status(r, http.StatusBadRequest)
			render.JSON(w, r, map[string]string{"message": "Invalid request body"})
			return
		}
		ws, err := store.UpdateShellWorkspace(r.Context(), claims.Subject, chi.URLParam(r, "id"), req.Name, req.Slug)
		if err != nil {
			writeErr(w, r, err, "Failed to update workspace")
			return
		}
		render.JSON(w, r, ws)
	}
}

func HandleUploadWorkspaceAvatar(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"message": "Not authenticated"})
			return
		}
		if err := r.ParseMultipartForm(2 << 20); err != nil {
			render.Status(r, http.StatusBadRequest)
			render.JSON(w, r, map[string]string{"message": "Invalid file"})
			return
		}
		file, header, err := r.FormFile("avatar")
		if err != nil {
			render.Status(r, http.StatusBadRequest)
			render.JSON(w, r, map[string]string{"message": "avatar field required"})
			return
		}
		defer file.Close()
		if header.Size > 2<<20 {
			render.Status(r, http.StatusRequestEntityTooLarge)
			render.JSON(w, r, map[string]string{"message": "Image file is too large"})
			return
		}
		data, err := io.ReadAll(file)
		if err != nil || len(data) == 0 {
			render.Status(r, http.StatusBadRequest)
			render.JSON(w, r, map[string]string{"message": "Failed to read file"})
			return
		}
		mime := http.DetectContentType(data)
		if !strings.HasPrefix(mime, "image/") {
			mime = "image/png"
		}
		dataURL := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
		ws, err := store.UpdateShellWorkspaceAvatar(r.Context(), claims.Subject, chi.URLParam(r, "id"), dataURL)
		if err != nil {
			writeErr(w, r, err, "Failed to save workspace avatar")
			return
		}
		render.JSON(w, r, ws)
	}
}

func HandleDeleteWorkspace(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"message": "Not authenticated"})
			return
		}
		if err := store.DeleteShellWorkspace(r.Context(), claims.Subject, chi.URLParam(r, "id")); err != nil {
			writeErr(w, r, err, "Failed to delete workspace")
			return
		}
		render.JSON(w, r, map[string]bool{"success": true})
	}
}

// ---- Members ----

func HandleListMembers(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"message": "Not authenticated"})
			return
		}
		list, err := store.ListMembers(r.Context(), claims.Subject, chi.URLParam(r, "id"))
		if err != nil {
			writeErr(w, r, err, "Failed to list members")
			return
		}
		if list == nil {
			list = []*core.WorkspaceMember{}
		}
		render.JSON(w, r, list)
	}
}

func HandleInviteMember(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"message": "Not authenticated"})
			return
		}
		var req struct {
			Email string `json:"email"`
			Role  string `json:"role"`
		}
		if err := readJSON(r, &req); err != nil || req.Email == "" {
			render.Status(r, http.StatusBadRequest)
			render.JSON(w, r, map[string]string{"message": "email is required"})
			return
		}
		role := core.RoleMember
		if req.Role != "" {
			role = core.WorkspaceRole(strings.ToUpper(req.Role))
		}
		m, err := store.InviteMember(r.Context(), claims.Subject, chi.URLParam(r, "id"), req.Email, role)
		if err != nil {
			writeErr(w, r, err, "Failed to invite member")
			return
		}
		render.Status(r, http.StatusCreated)
		render.JSON(w, r, m)
	}
}

func HandleUpdateMember(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"message": "Not authenticated"})
			return
		}
		var req struct {
			Role string `json:"role"`
		}
		if err := readJSON(r, &req); err != nil || req.Role == "" {
			render.Status(r, http.StatusBadRequest)
			render.JSON(w, r, map[string]string{"message": "role is required"})
			return
		}
		m, err := store.UpdateMemberRole(r.Context(), claims.Subject, chi.URLParam(r, "id"), chi.URLParam(r, "memberId"), core.WorkspaceRole(strings.ToUpper(req.Role)))
		if err != nil {
			writeErr(w, r, err, "Failed to update member")
			return
		}
		render.JSON(w, r, m)
	}
}

func HandleRemoveMember(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"message": "Not authenticated"})
			return
		}
		if err := store.RemoveMember(r.Context(), claims.Subject, chi.URLParam(r, "id"), chi.URLParam(r, "memberId")); err != nil {
			writeErr(w, r, err, "Failed to remove member")
			return
		}
		render.JSON(w, r, map[string]bool{"success": true})
	}
}

// ---- Invite links ----

func HandleListInviteLinks(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"message": "Not authenticated"})
			return
		}
		list, err := store.ListInviteLinks(r.Context(), claims.Subject, chi.URLParam(r, "id"))
		if err != nil {
			writeErr(w, r, err, "Failed to list invite links")
			return
		}
		if list == nil {
			list = []*core.InviteLink{}
		}
		render.JSON(w, r, list)
	}
}

func HandleCreateInviteLink(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"message": "Not authenticated"})
			return
		}
		var req struct {
			Role      string  `json:"role"`
			ExpiresAt *string `json:"expiresAt"`
			MaxUses   *int    `json:"maxUses"`
		}
		_ = readJSON(r, &req)
		role := core.RoleMember
		if req.Role != "" {
			role = core.WorkspaceRole(strings.ToUpper(req.Role))
		}
		var expires *time.Time
		if req.ExpiresAt != nil && *req.ExpiresAt != "" {
			t, err := time.Parse(time.RFC3339, *req.ExpiresAt)
			if err != nil {
				render.Status(r, http.StatusBadRequest)
				render.JSON(w, r, map[string]string{"message": "invalid expiresAt"})
				return
			}
			expires = &t
		}
		link, err := store.CreateInviteLink(r.Context(), claims.Subject, chi.URLParam(r, "id"), role, expires, req.MaxUses)
		if err != nil {
			writeErr(w, r, err, "Failed to create invite link")
			return
		}
		render.Status(r, http.StatusCreated)
		render.JSON(w, r, link)
	}
}

func HandleDeleteInviteLink(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"message": "Not authenticated"})
			return
		}
		if err := store.DeleteInviteLink(r.Context(), claims.Subject, chi.URLParam(r, "id"), chi.URLParam(r, "linkId")); err != nil {
			writeErr(w, r, err, "Failed to delete invite link")
			return
		}
		render.JSON(w, r, map[string]bool{"success": true})
	}
}

func HandleJoinWorkspace(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"message": "Not authenticated"})
			return
		}
		ensureProfile(store, r, claims)
		var req struct {
			Code string `json:"code"`
		}
		if err := readJSON(r, &req); err != nil || req.Code == "" {
			render.Status(r, http.StatusBadRequest)
			render.JSON(w, r, map[string]string{"message": "code is required"})
			return
		}
		ws, err := store.JoinViaInviteLink(r.Context(), claims.Subject, req.Code, memberUserFromClaims(claims))
		if err != nil {
			writeErr(w, r, err, "Failed to join workspace")
			return
		}
		render.JSON(w, r, ws)
	}
}

// ---- Collections ----

func HandleListCollections(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"message": "Not authenticated"})
			return
		}
		list, err := store.ListCollections(r.Context(), claims.Subject, chi.URLParam(r, "id"))
		if err != nil {
			writeErr(w, r, err, "Failed to list collections")
			return
		}
		if list == nil {
			list = []*core.Collection{}
		}
		render.JSON(w, r, list)
	}
}

func HandleCreateCollection(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"message": "Not authenticated"})
			return
		}
		var req struct {
			Name      string  `json:"name"`
			Icon      *string `json:"icon"`
			Color     *string `json:"color"`
			IsPrivate *bool   `json:"isPrivate"`
		}
		if err := readJSON(r, &req); err != nil || req.Name == "" {
			render.Status(r, http.StatusBadRequest)
			render.JSON(w, r, map[string]string{"message": "name is required"})
			return
		}
		isPrivate := false
		_ = req.IsPrivate
		c, err := store.CreateCollection(r.Context(), claims.Subject, chi.URLParam(r, "id"), req.Name, req.Icon, req.Color, isPrivate)
		if err != nil {
			writeErr(w, r, err, "Failed to create collection")
			return
		}
		render.Status(r, http.StatusCreated)
		render.JSON(w, r, c)
	}
}

func HandleGetCollection(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"message": "Not authenticated"})
			return
		}
		c, err := store.GetCollection(r.Context(), claims.Subject, chi.URLParam(r, "id"))
		if err != nil {
			writeErr(w, r, err, "Failed to get collection")
			return
		}
		render.JSON(w, r, c)
	}
}

func HandleUpdateCollection(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"message": "Not authenticated"})
			return
		}
		var req struct {
			Name      *string `json:"name"`
			Icon      *string `json:"icon"`
			Color     *string `json:"color"`
			IsPrivate *bool   `json:"isPrivate"`
		}
		if err := readJSON(r, &req); err != nil {
			render.Status(r, http.StatusBadRequest)
			render.JSON(w, r, map[string]string{"message": "Invalid request body"})
			return
		}
		c, err := store.UpdateCollection(r.Context(), claims.Subject, chi.URLParam(r, "id"), req.Name, req.Icon, req.Color, req.IsPrivate)
		if err != nil {
			writeErr(w, r, err, "Failed to update collection")
			return
		}
		render.JSON(w, r, c)
	}
}

func HandleDeleteCollection(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"message": "Not authenticated"})
			return
		}
		if err := store.DeleteCollection(r.Context(), claims.Subject, chi.URLParam(r, "id")); err != nil {
			writeErr(w, r, err, "Failed to delete collection")
			return
		}
		render.JSON(w, r, map[string]bool{"success": true})
	}
}

func HandleCopyCollection(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"message": "Not authenticated"})
			return
		}
		var req struct {
			TargetWorkspaceID string `json:"targetWorkspaceId"`
		}
		if err := readJSON(r, &req); err != nil || req.TargetWorkspaceID == "" {
			render.Status(r, http.StatusBadRequest)
			render.JSON(w, r, map[string]string{"message": "targetWorkspaceId is required"})
			return
		}
		c, err := store.CopyCollectionToWorkspace(r.Context(), claims.Subject, chi.URLParam(r, "id"), req.TargetWorkspaceID)
		if err != nil {
			writeErr(w, r, err, "Failed to copy collection")
			return
		}
		render.JSON(w, r, c)
	}
}

func HandleMoveCollection(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"message": "Not authenticated"})
			return
		}
		var req struct {
			TargetWorkspaceID string `json:"targetWorkspaceId"`
		}
		if err := readJSON(r, &req); err != nil || req.TargetWorkspaceID == "" {
			render.Status(r, http.StatusBadRequest)
			render.JSON(w, r, map[string]string{"message": "targetWorkspaceId is required"})
			return
		}
		c, err := store.MoveCollectionToWorkspace(r.Context(), claims.Subject, chi.URLParam(r, "id"), req.TargetWorkspaceID)
		if err != nil {
			writeErr(w, r, err, "Failed to move collection")
			return
		}
		render.JSON(w, r, c)
	}
}

// ---- Scenes ----

func HandleListScenes(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"message": "Not authenticated"})
			return
		}
		ensureProfile(store, r, claims)
		var wsID, colID *string
		if v := r.URL.Query().Get("workspaceId"); v != "" {
			wsID = &v
		}
		if v := r.URL.Query().Get("collectionId"); v != "" {
			colID = &v
		}
		list, err := store.ListScenes(r.Context(), claims.Subject, wsID, colID)
		if err != nil {
			writeErr(w, r, err, "Failed to list scenes")
			return
		}
		if list == nil {
			list = []*core.WorkspaceScene{}
		}
		render.JSON(w, r, list)
	}
}

func HandleGetScene(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"message": "Not authenticated"})
			return
		}
		s, err := store.GetScene(r.Context(), claims.Subject, chi.URLParam(r, "id"))
		if err != nil {
			writeErr(w, r, err, "Failed to get scene")
			return
		}
		render.JSON(w, r, s)
	}
}

func HandleGetSceneData(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"message": "Not authenticated"})
			return
		}
		data, err := store.GetSceneData(r.Context(), claims.Subject, chi.URLParam(r, "id"))
		if err != nil {
			writeErr(w, r, err, "Failed to get scene data")
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}
}

func HandleCreateScene(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"message": "Not authenticated"})
			return
		}
		ensureProfile(store, r, claims)
		var req struct {
			Title        string  `json:"title"`
			Thumbnail    *string `json:"thumbnail"`
			Data         string  `json:"data"`
			CollectionID *string `json:"collectionId"`
		}
		if err := readJSON(r, &req); err != nil {
			render.Status(r, http.StatusBadRequest)
			render.JSON(w, r, map[string]string{"message": "Invalid request body"})
			return
		}
		var data []byte
		if req.Data != "" {
			data = []byte(req.Data)
		}
		s, err := store.CreateScene(r.Context(), claims.Subject, req.Title, req.Thumbnail, data, req.CollectionID)
		if err != nil {
			writeErr(w, r, err, "Failed to create scene")
			return
		}
		render.Status(r, http.StatusCreated)
		render.JSON(w, r, s)
	}
}

func HandleUpdateScene(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"message": "Not authenticated"})
			return
		}
		var req struct {
			Title     *string `json:"title"`
			Thumbnail *string `json:"thumbnail"`
			Data      *string `json:"data"`
		}
		if err := readJSON(r, &req); err != nil {
			render.Status(r, http.StatusBadRequest)
			render.JSON(w, r, map[string]string{"message": "Invalid request body"})
			return
		}
		var data []byte
		if req.Data != nil {
			data = []byte(*req.Data)
		}
		s, err := store.UpdateScene(r.Context(), claims.Subject, chi.URLParam(r, "id"), req.Title, req.Thumbnail, data)
		if err != nil {
			writeErr(w, r, err, "Failed to update scene")
			return
		}
		render.JSON(w, r, s)
	}
}

func HandleUpdateSceneData(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"message": "Not authenticated"})
			return
		}
		data, err := io.ReadAll(r.Body)
		if err != nil {
			render.Status(r, http.StatusBadRequest)
			render.JSON(w, r, map[string]string{"message": "Failed to read body"})
			return
		}
		defer r.Body.Close()
		if err := store.UpdateSceneData(r.Context(), claims.Subject, chi.URLParam(r, "id"), data); err != nil {
			writeErr(w, r, err, "Failed to update scene data")
			return
		}
		render.JSON(w, r, map[string]bool{"success": true})
	}
}

func HandleUploadSceneThumbnail(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"message": "Not authenticated"})
			return
		}
		data, err := io.ReadAll(r.Body)
		if err != nil {
			render.Status(r, http.StatusBadRequest)
			render.JSON(w, r, map[string]string{"message": "Failed to read body"})
			return
		}
		defer r.Body.Close()
		// Store as data-url-ish base64 prefix so frontend can use as img src if needed;
		// for binary PNG we store raw as latin1 string via base64 in handler? Keep simple: store as data URI length marker.
		thumb := string(data)
		url, err := store.UploadSceneThumbnail(r.Context(), claims.Subject, chi.URLParam(r, "id"), thumb)
		if err != nil {
			writeErr(w, r, err, "Failed to upload thumbnail")
			return
		}
		render.JSON(w, r, map[string]string{"thumbnailUrl": url})
	}
}

func HandleDeleteScene(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"message": "Not authenticated"})
			return
		}
		if err := store.DeleteScene(r.Context(), claims.Subject, chi.URLParam(r, "id")); err != nil {
			writeErr(w, r, err, "Failed to delete scene")
			return
		}
		render.JSON(w, r, map[string]bool{"success": true})
	}
}

func HandleDuplicateScene(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"message": "Not authenticated"})
			return
		}
		s, err := store.DuplicateScene(r.Context(), claims.Subject, chi.URLParam(r, "id"))
		if err != nil {
			writeErr(w, r, err, "Failed to duplicate scene")
			return
		}
		render.Status(r, http.StatusCreated)
		render.JSON(w, r, s)
	}
}

func HandleMoveScene(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"message": "Not authenticated"})
			return
		}
		var req struct {
			CollectionID *string `json:"collectionId"`
		}
		if err := readJSON(r, &req); err != nil {
			render.Status(r, http.StatusBadRequest)
			render.JSON(w, r, map[string]string{"message": "Invalid request body"})
			return
		}
		s, err := store.MoveScene(r.Context(), claims.Subject, chi.URLParam(r, "id"), req.CollectionID)
		if err != nil {
			writeErr(w, r, err, "Failed to move scene")
			return
		}
		render.JSON(w, r, s)
	}
}

func HandleAcquireSceneLock(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"message": "Not authenticated"})
			return
		}
		var req struct {
			ClientID    string `json:"clientId"`
			DisplayName string `json:"displayName"`
		}
		if err := readJSON(r, &req); err != nil {
			render.Status(r, http.StatusBadRequest)
			render.JSON(w, r, map[string]string{"message": "Invalid request body"})
			return
		}
		name := req.DisplayName
		if name == "" {
			name = claims.Name
		}
		s, err := store.AcquireSceneLock(r.Context(), claims.Subject, chi.URLParam(r, "id"), req.ClientID, name)
		if err != nil {
			writeErr(w, r, err, "Failed to acquire scene lock")
			return
		}
		render.JSON(w, r, s)
	}
}

func HandleReleaseSceneLock(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := userFromContext(r)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"message": "Not authenticated"})
			return
		}
		var req struct {
			ClientID string `json:"clientId"`
		}
		_ = readJSON(r, &req)
		if req.ClientID == "" {
			req.ClientID = r.URL.Query().Get("clientId")
		}
		if err := store.ReleaseSceneLock(r.Context(), claims.Subject, chi.URLParam(r, "id"), req.ClientID); err != nil {
			writeErr(w, r, err, "Failed to release scene lock")
			return
		}
		render.JSON(w, r, map[string]bool{"success": true})
	}
}

func HandleEnableSceneCollab(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		handleSetSceneCollab(store, w, r, true)
	}
}

func HandleDisableSceneCollab(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		handleSetSceneCollab(store, w, r, false)
	}
}

func handleSetSceneCollab(store stores.Store, w http.ResponseWriter, r *http.Request, enabled bool) {
	claims, ok := userFromContext(r)
	if !ok {
		render.Status(r, http.StatusUnauthorized)
		render.JSON(w, r, map[string]string{"message": "Not authenticated"})
		return
	}
	s, err := store.SetSceneCollab(r.Context(), claims.Subject, chi.URLParam(r, "id"), enabled)
	if err != nil {
		writeErr(w, r, err, "Failed to update scene collaboration")
		return
	}
	render.JSON(w, r, s)
}
