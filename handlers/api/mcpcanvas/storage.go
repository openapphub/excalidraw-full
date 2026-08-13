package mcpcanvas

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// StoredFile is a row in the files table (Firebase Storage emulator).
type StoredFile struct {
	Path        string    `json:"name"`
	Bucket      string    `json:"bucket"`
	ContentType string    `json:"contentType"`
	Size        int64     `json:"size"`
	CacheControl string   `json:"cacheControl,omitempty"`
	CreatedAt   time.Time `json:"timeCreated"`
	UpdatedAt   time.Time `json:"updated"`
}

// initFilesTable creates the files table if it doesn't exist.
func (s *Store) initFilesTable() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS files (
		path TEXT PRIMARY KEY,
		bucket TEXT NOT NULL,
		data BLOB NOT NULL,
		content_type TEXT DEFAULT 'application/octet-stream',
		cache_control TEXT DEFAULT '',
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	)`)
	return err
}

// storageMetadata is the JSON metadata part of the multipart upload body.
// Only writable fields are sent by the SDK (see toResourceString).
type storageMetadata struct {
	Name           string            `json:"name"`
	Md5Hash        string            `json:"md5Hash"`
	CacheControl   string            `json:"cacheControl"`
	ContentType    string            `json:"contentType"`
	CustomMetadata map[string]string `json:"metadata"`
}

// storageObjectResponse is the metadata response the SDK parses after upload.
// Field names follow the Firebase Storage REST API (see mappings in SDK).
type storageObjectResponse struct {
	Bucket         string            `json:"bucket"`
	Generation     string            `json:"generation"`
	Metageneration string            `json:"metageneration"`
	Name           string            `json:"name"`
	Size           string            `json:"size"`
	TimeCreated    string            `json:"timeCreated"`
	Updated        string            `json:"updated"`
	Md5Hash        string            `json:"md5Hash"`
	CacheControl   string            `json:"cacheControl"`
	ContentType    string            `json:"contentType"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

// handleStorageUpload handles POST /v0/b/{bucket}/o — the Firebase Storage
// multipart upload protocol (X-Goog-Upload-Protocol: multipart).
// Body: multipart/related with part 1 = JSON metadata, part 2 = file bytes.
func (s *Store) handleStorageUpload(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")

	// Parse multipart/related body: part 1 = JSON metadata, part 2 = bytes.
	// Note: r.MultipartReader() only accepts multipart/form-data, but the
	// Firebase SDK sends multipart/related — parse manually.
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "bad content-type: " + err.Error()})
		return
	}
	boundary := params["boundary"]
	if boundary == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "missing boundary"})
		return
	}
	reader := multipart.NewReader(r.Body, boundary)

	var meta storageMetadata
	var data []byte
	partIndex := 0
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": err.Error()})
			return
		}
		body, err := io.ReadAll(part)
		part.Close()
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": err.Error()})
			return
		}
		if partIndex == 0 {
			if err := json.Unmarshal(body, &meta); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "bad metadata: " + err.Error()})
				return
			}
		} else {
			data = body
		}
		partIndex++
	}

	path := strings.TrimPrefix(meta.Name, "/")
	if path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "name is required"})
		return
	}
	contentType := meta.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	now := time.Now()
	_, err = s.db.Exec(`INSERT INTO files (path, bucket, data, content_type, cache_control, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET data=excluded.data, content_type=excluded.content_type,
			cache_control=excluded.cache_control, updated_at=excluded.updated_at`,
		path, bucket, data, contentType, meta.CacheControl, now, now)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, storageObjectResponse{
		Bucket:         bucket,
		Generation:     fmt.Sprintf("%d", now.UnixNano()),
		Metageneration: "1",
		Name:           path,
		Size:           fmt.Sprintf("%d", len(data)),
		TimeCreated:    now.UTC().Format(time.RFC3339),
		Updated:        now.UTC().Format(time.RFC3339),
		Md5Hash:        meta.Md5Hash,
		CacheControl:   meta.CacheControl,
		ContentType:    contentType,
		Metadata:       meta.CustomMetadata,
	})
}

// handleStorageDownload handles GET /v0/b/{bucket}/o/{path}?alt=media
func (s *Store) handleStorageDownload(w http.ResponseWriter, r *http.Request) {
	bucket := chi.URLParam(r, "bucket")
	path := chi.URLParam(r, "*")
	// chi returns the raw (still %-encoded) wildcard; the SDK encodes the
	// object path with %2F separators, so decode before looking up.
	if decoded, err := url.PathUnescape(path); err == nil {
		path = decoded
	}
	fmt.Printf("[storage] download bucket=%s path=%q\n", bucket, path)

	var data []byte
	var contentType, cacheControl string
	err := s.db.QueryRow(`SELECT data, content_type, cache_control FROM files WHERE path = ? AND bucket = ?`,
		path, bucket).Scan(&data, &contentType, &cacheControl)
	if err != nil {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if cacheControl != "" {
		w.Header().Set("Cache-Control", cacheControl)
	}
	w.Header().Set("Content-Type", contentType)
	w.Write(data)
}

// StorageRoutes registers the Firebase Storage emulator endpoints.
func StorageRoutes(r chi.Router, s *Store) {
	r.Post("/o", s.handleStorageUpload)
	r.Get("/o/*", s.handleStorageDownload)
}
