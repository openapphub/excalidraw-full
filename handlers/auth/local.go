package auth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"excalidraw-complete/core"
	"excalidraw-complete/stores"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/go-chi/render"
	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/bcrypt"
)

type authUserJSON struct {
	ID        string  `json:"id"`
	Email     string  `json:"email"`
	Name      *string `json:"name"`
	AvatarURL *string `json:"avatarUrl"`
}

func localAuthEnabled() bool {
	v := strings.ToLower(os.Getenv("LOCAL_AUTH_ENABLED"))
	return v == "" || v == "1" || v == "true" || v == "yes"
}

func registrationEnabled() bool {
	v := strings.ToLower(os.Getenv("LOCAL_AUTH_REGISTRATION"))
	return v == "" || v == "1" || v == "true" || v == "yes"
}

func oidcOrGitHubConfigured() bool {
	return (os.Getenv("OIDC_ISSUER_URL") != "" && os.Getenv("OIDC_CLIENT_ID") != "") ||
		(os.Getenv("GITHUB_CLIENT_ID") != "" && os.Getenv("GITHUB_CLIENT_SECRET") != "")
}

func HandleAuthStatus(w http.ResponseWriter, r *http.Request) {
	render.JSON(w, r, map[string]bool{
		"oidcConfigured":      oidcOrGitHubConfigured(),
		"localAuthEnabled":    localAuthEnabled(),
		"registrationEnabled": localAuthEnabled() && registrationEnabled(),
	})
}

func claimsFromRequest(r *http.Request) (*AppClaims, error) {
	h := r.Header.Get("Authorization")
	parts := strings.Split(h, " ")
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return nil, errors.New("missing bearer token")
	}
	return ParseJWT(parts[1])
}

func memberToJSON(u *core.MemberUser) authUserJSON {
	return authUserJSON{ID: u.ID, Email: u.Email, Name: u.Name, AvatarURL: u.AvatarURL}
}

func issueTokenForLocal(u *core.LocalUser) (string, error) {
	return createJWT(&core.User{
		Subject:   u.ID,
		Login:     u.Email,
		Email:     u.Email,
		Name:      u.Name,
		AvatarURL: u.AvatarURL,
	})
}

func HandleLocalRegister(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !localAuthEnabled() || !registrationEnabled() {
			render.Status(r, http.StatusForbidden)
			render.JSON(w, r, map[string]string{"message": "Registration disabled"})
			return
		}
		var req struct {
			Email    string `json:"email"`
			Password string `json:"password"`
			Name     string `json:"name"`
		}
		body, _ := io.ReadAll(r.Body)
		defer r.Body.Close()
		if err := json.Unmarshal(body, &req); err != nil || strings.TrimSpace(req.Email) == "" || len(req.Password) < 6 {
			render.Status(r, http.StatusBadRequest)
			render.JSON(w, r, map[string]string{"message": "email and password (min 6) required"})
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		if err != nil {
			render.Status(r, http.StatusInternalServerError)
			render.JSON(w, r, map[string]string{"message": "Failed to hash password"})
			return
		}
		u, err := store.CreateLocalUser(r.Context(), req.Email, string(hash), req.Name)
		if err != nil {
			if errors.Is(err, core.ErrConflict) {
				render.Status(r, http.StatusConflict)
				render.JSON(w, r, map[string]string{"message": "Email already registered"})
				return
			}
			logrus.WithError(err).Error("register failed")
			render.Status(r, http.StatusInternalServerError)
			render.JSON(w, r, map[string]string{"message": "Registration failed"})
			return
		}
		token, err := issueTokenForLocal(u)
		if err != nil {
			render.Status(r, http.StatusInternalServerError)
			render.JSON(w, r, map[string]string{"message": "Failed to create session"})
			return
		}
		name := u.Name
		render.JSON(w, r, map[string]interface{}{
			"success": true,
			"token":   token,
			"user": authUserJSON{
				ID:    u.ID,
				Email: u.Email,
				Name:  &name,
			},
		})
	}
}

func HandleLocalLogin(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !localAuthEnabled() {
			render.Status(r, http.StatusForbidden)
			render.JSON(w, r, map[string]string{"message": "Local auth disabled"})
			return
		}
		var req struct {
			Username string `json:"username"`
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		body, _ := io.ReadAll(r.Body)
		defer r.Body.Close()
		_ = json.Unmarshal(body, &req)
		login := req.Username
		if login == "" {
			login = req.Email
		}
		if login == "" || req.Password == "" {
			render.Status(r, http.StatusBadRequest)
			render.JSON(w, r, map[string]string{"message": "username and password required"})
			return
		}
		u, err := store.GetLocalUserByEmail(r.Context(), login)
		if err != nil {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"message": "Invalid credentials"})
			return
		}
		if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)) != nil {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"message": "Invalid credentials"})
			return
		}
		token, err := issueTokenForLocal(u)
		if err != nil {
			render.Status(r, http.StatusInternalServerError)
			render.JSON(w, r, map[string]string{"message": "Failed to create session"})
			return
		}
		name := u.Name
		var avatar *string
		if u.AvatarURL != "" {
			avatar = &u.AvatarURL
		}
		render.JSON(w, r, map[string]interface{}{
			"success": true,
			"token":   token,
			"user": authUserJSON{
				ID:        u.ID,
				Email:     u.Email,
				Name:      &name,
				AvatarURL: avatar,
			},
		})
	}
}

func profileFromClaims(store stores.Store, r *http.Request, claims *AppClaims) *core.MemberUser {
	p, err := store.GetUserProfile(r.Context(), claims.Subject)
	if err == nil && p != nil && (p.Email != "" || (p.Name != nil && *p.Name != "")) {
		if p.Email == "" {
			p.Email = claims.Email
		}
		return p
	}
	name, avatar := claims.Name, claims.AvatarURL
	var namePtr, avatarPtr *string
	if name != "" {
		namePtr = &name
	}
	if avatar != "" {
		avatarPtr = &avatar
	}
	p = &core.MemberUser{ID: claims.Subject, Email: claims.Email, Name: namePtr, AvatarURL: avatarPtr}
	_ = store.UpsertUserProfile(r.Context(), *p)
	return p
}

func HandleAuthMe(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, err := claimsFromRequest(r)
		if err != nil {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"message": "Not authenticated"})
			return
		}
		render.JSON(w, r, memberToJSON(profileFromClaims(store, r, claims)))
	}
}

func HandleUpdateProfile(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, err := claimsFromRequest(r)
		if err != nil {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"message": "Not authenticated"})
			return
		}
		var req struct {
			Name *string `json:"name"`
		}
		body, _ := io.ReadAll(r.Body)
		defer r.Body.Close()
		if err := json.Unmarshal(body, &req); err != nil {
			render.Status(r, http.StatusBadRequest)
			render.JSON(w, r, map[string]string{"message": "Invalid body"})
			return
		}
		p, err := store.UpdateUserProfile(r.Context(), claims.Subject, req.Name, nil, false)
		if err != nil {
			render.Status(r, http.StatusInternalServerError)
			render.JSON(w, r, map[string]string{"message": "Failed to update profile"})
			return
		}
		render.JSON(w, r, memberToJSON(p))
	}
}

func HandleUploadAvatar(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, err := claimsFromRequest(r)
		if err != nil {
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
		p, err := store.UpdateUserProfile(r.Context(), claims.Subject, nil, &dataURL, false)
		if err != nil {
			render.Status(r, http.StatusInternalServerError)
			render.JSON(w, r, map[string]string{"message": "Failed to save avatar"})
			return
		}
		render.JSON(w, r, memberToJSON(p))
	}
}

func HandleDeleteAvatar(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, err := claimsFromRequest(r)
		if err != nil {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"message": "Not authenticated"})
			return
		}
		p, err := store.UpdateUserProfile(r.Context(), claims.Subject, nil, nil, true)
		if err != nil {
			render.Status(r, http.StatusInternalServerError)
			render.JSON(w, r, map[string]string{"message": "Failed to remove avatar"})
			return
		}
		render.JSON(w, r, memberToJSON(p))
	}
}
