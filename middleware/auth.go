package middleware

import (
	"context"
	"excalidraw-complete/handlers/auth"
	"net/http"
	"strings"

	"github.com/go-chi/render"
)

type contextKey string

const ClaimsContextKey = contextKey("claims")

func AuthJWT(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"error": "Authorization header is required"})
			return
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"error": "Authorization header format must be Bearer {token}"})
			return
		}

		tokenString := parts[1]
		claims, err := auth.ParseJWT(tokenString)
		if err != nil {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"error": "Invalid token"})
			return
		}

		ctx := context.WithValue(r.Context(), ClaimsContextKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// AuthWebSocketJWT 从 WebSocket 子协议读取 JWT。浏览器 WebSocket API 不能
// 自定义 Authorization 头；把 token 放在查询串又会泄露到访问日志，因此使用
// `Sec-WebSocket-Protocol: excalidraw-auth, <jwt>`。
func AuthWebSocketJWT(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protocols := strings.Split(r.Header.Get("Sec-WebSocket-Protocol"), ",")
		if len(protocols) < 2 || strings.TrimSpace(protocols[0]) != "excalidraw-auth" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		claims, err := auth.ParseJWT(strings.TrimSpace(protocols[1]))
		if err != nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), ClaimsContextKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
