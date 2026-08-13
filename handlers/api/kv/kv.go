package kv

import (
	"encoding/json"
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

func HandleListCanvases(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := r.Context().Value(middleware.ClaimsContextKey).(*auth.AppClaims)
		if !ok {
			render.Status(r, http.StatusUnauthorized)
			render.JSON(w, r, map[string]string{"error": "User claims not found"})
			return
		}

		canvases, err := store.List(r.Context(), claims.Subject)
		if err != nil {
			logrus.WithFields(logrus.Fields{
				"error":  err,
				"userID": claims.Subject,
			}).Error("Failed to list canvases")
			render.Status(r, http.StatusInternalServerError)
			render.JSON(w, r, map[string]string{"error": "Failed to list canvases"})
			return
		}

		// If canvases is nil (e.g., user has no canvases), return an empty slice instead of null.
		if canvases == nil {
			canvases = []*core.Canvas{}
		}

		render.JSON(w, r, canvases)
	}
}

func HandleGetCanvas(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := r.Context().Value(middleware.ClaimsContextKey).(*auth.AppClaims)
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

		canvas, err := store.Get(r.Context(), claims.Subject, key)
		if err != nil {
			logrus.WithFields(logrus.Fields{
				"error":  err,
				"userID": claims.Subject,
				"key":    key,
			}).Warn("Failed to get canvas")
			// This could be a not found error or a real server error.
			// For simplicity, we'll return 404, but in a real app, you might want to distinguish.
			render.Status(r, http.StatusNotFound)
			render.JSON(w, r, map[string]string{"error": "Canvas not found"})
			return
		}

		// The canvas data is returned as raw bytes.
		w.Header().Set("Content-Type", "application/json")
		w.Write(canvas.Data)
	}
}

func HandleSaveCanvas(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := r.Context().Value(middleware.ClaimsContextKey).(*auth.AppClaims)
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
			logrus.WithFields(logrus.Fields{
				"error": err,
				"key":   key,
			}).Error("Failed to read request body")
			render.Status(r, http.StatusInternalServerError)
			render.JSON(w, r, map[string]string{"error": "Failed to read request body"})
			return
		}
		defer r.Body.Close()

		// For simplicity, we use the key as the name. A more advanced implementation
		// might parse a name from the body or have a separate field.
		var canvasData struct {
			AppState struct {
				Name string `json:"name"`
			} `json:"appState"`
			Thumbnail string `json:"thumbnail"`
		}
		// We make a copy of the body because json.Unmarshal will consume the reader.
		bodyCopy := make([]byte, len(body))
		copy(bodyCopy, body)

		canvasName := key // Default to key
		var canvasThumbnail string
		if err := json.Unmarshal(bodyCopy, &canvasData); err == nil {
			// AI canvases (id prefix "ai-") keep a stable name — ignore the
			// frontend's auto-generated "无标题-<timestamp>" names, otherwise
			// every save renames the canvas.
			if !strings.HasPrefix(key, "ai-") && canvasData.AppState.Name != "" {
				canvasName = canvasData.AppState.Name
			}
			canvasThumbnail = canvasData.Thumbnail
		}

		canvas := &core.Canvas{
			ID:        key,
			UserID:    claims.Subject,
			Name:      canvasName,
			Thumbnail: canvasThumbnail,
			Data:      body,
		}

		if err := store.Save(r.Context(), canvas); err != nil {
			logrus.WithFields(logrus.Fields{
				"error":  err,
				"userID": claims.Subject,
				"key":    key,
			}).Error("Failed to save canvas")
			render.Status(r, http.StatusInternalServerError)
			render.JSON(w, r, map[string]string{"error": "Failed to save canvas"})
			return
		}

		render.Status(r, http.StatusOK)
	}
}

func HandleDeleteCanvas(store stores.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := r.Context().Value(middleware.ClaimsContextKey).(*auth.AppClaims)
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

		if err := store.Delete(r.Context(), claims.Subject, key); err != nil {
			logrus.WithFields(logrus.Fields{
				"error":  err,
				"userID": claims.Subject,
				"key":    key,
			}).Error("Failed to delete canvas")
			render.Status(r, http.StatusInternalServerError)
			render.JSON(w, r, map[string]string{"error": "Failed to delete canvas"})
			return
		}

		render.Status(r, http.StatusOK)
	}
}
