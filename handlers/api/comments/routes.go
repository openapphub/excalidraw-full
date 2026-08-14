package comments

import (
	"excalidraw-complete/stores"

	"github.com/go-chi/chi/v5"
)

// Routes 在已带 JWT 中间件的 /api/v2 组上挂载评论 + 通知路由。
// 集中在这里注册，main.go 只需一行接线。
//
// 客户端（AstraDraw auth/api/comments.ts）用 PUT 更新，这里同时接受 PATCH，
// 便于直接复用未改动的上游客户端代码。
func Routes(r chi.Router, store stores.Store) {
	r.Route("/scenes/{sceneId}/threads", func(r chi.Router) {
		r.Get("/", HandleListThreads(store))
		r.Post("/", HandleCreateThread(store))
	})

	r.Route("/threads/{threadId}", func(r chi.Router) {
		r.Get("/", HandleGetThread(store))
		r.Put("/", HandleUpdateThread(store))
		r.Patch("/", HandleUpdateThread(store))
		r.Delete("/", HandleDeleteThread(store))
		r.Post("/resolve", HandleResolveThread(store))
		r.Post("/reopen", HandleReopenThread(store))
		r.Post("/comments", HandleAddComment(store))
	})

	r.Route("/comments/{id}", func(r chi.Router) {
		r.Put("/", HandleUpdateComment(store))
		r.Patch("/", HandleUpdateComment(store))
		r.Delete("/", HandleDeleteComment(store))
	})

	r.Route("/notifications", func(r chi.Router) {
		r.Get("/", HandleListNotifications(store))
		r.Get("/unread-count", HandleUnreadCount(store))
		r.Post("/read-all", HandleMarkAllNotificationsRead(store))
		r.Post("/{id}/read", HandleMarkNotificationRead(store))
	})
}
