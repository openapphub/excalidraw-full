package mcpcanvas

import (
	"context"
	"encoding/json"
	"errors"
	"excalidraw-complete/core"
	"excalidraw-complete/handlers/api/roomaccess"
	"excalidraw-complete/handlers/auth"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sirupsen/logrus"
)

const (
	maxStorageUploadRequestBytes = 4 << 20
	anonymousStorageTTL          = 24 * time.Hour
	maxAnonymousStorageBytes     = 128 << 20
)

type storageObjectKey struct {
	bucket string
	path   string
}

type anonymousStorageObject struct {
	data         []byte
	contentType  string
	cacheControl string
	updatedAt    time.Time
}

func (s *Store) pruneAnonymousFilesLocked(now time.Time) {
	for key, object := range s.anonymousFiles {
		if now.Sub(object.updatedAt) < anonymousStorageTTL {
			continue
		}
		delete(s.anonymousFiles, key)
		s.anonymousFileBytes -= int64(len(object.data))
	}
}

func (s *Store) storeAnonymousFile(key storageObjectKey, object anonymousStorageObject) {
	s.storageMu.Lock()
	defer s.storageMu.Unlock()

	if s.anonymousFiles == nil {
		s.anonymousFiles = make(map[storageObjectKey]anonymousStorageObject)
	}
	s.pruneAnonymousFilesLocked(object.updatedAt)
	if previous, ok := s.anonymousFiles[key]; ok {
		s.anonymousFileBytes -= int64(len(previous.data))
		delete(s.anonymousFiles, key)
	}

	for s.anonymousFileBytes+int64(len(object.data)) > maxAnonymousStorageBytes {
		var oldestKey storageObjectKey
		var oldest anonymousStorageObject
		found := false
		for candidateKey, candidate := range s.anonymousFiles {
			if !found || candidate.updatedAt.Before(oldest.updatedAt) {
				oldestKey = candidateKey
				oldest = candidate
				found = true
			}
		}
		if !found {
			break
		}
		delete(s.anonymousFiles, oldestKey)
		s.anonymousFileBytes -= int64(len(oldest.data))
	}

	object.data = append([]byte(nil), object.data...)
	s.anonymousFiles[key] = object
	s.anonymousFileBytes += int64(len(object.data))
}

func (s *Store) loadAnonymousFile(key storageObjectKey, now time.Time) (anonymousStorageObject, bool) {
	s.storageMu.Lock()
	defer s.storageMu.Unlock()

	s.pruneAnonymousFilesLocked(now)
	object, ok := s.anonymousFiles[key]
	if !ok {
		return anonymousStorageObject{}, false
	}
	object.data = append([]byte(nil), object.data...)
	return object, true
}

func sceneIDFromStoragePath(path string) (string, bool) {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) < 4 || parts[0] != "files" || parts[1] != "rooms" || parts[2] == "" {
		return "", false
	}
	return parts[2], true
}

func (s *Store) authorizeStorageObject(r *http.Request, path string, requireWrite bool) (roomaccess.Access, error) {
	sceneID, ok := sceneIDFromStoragePath(path)
	if !ok {
		if !requireWrite {
			return roomaccess.Access{}, nil
		}
		claims, err := auth.ParseJWT(roomaccess.BearerToken(r))
		if err != nil {
			return roomaccess.Access{}, core.ErrForbidden
		}
		return roomaccess.Access{UserID: claims.Subject, CanEdit: true}, nil
	}
	access, err := roomaccess.Authorize(r.Context(), s.host, sceneID, roomaccess.BearerToken(r))
	if err != nil {
		return roomaccess.Access{}, err
	}
	if access.WorkspaceScene && requireWrite && !access.CanEdit {
		return roomaccess.Access{}, core.ErrForbidden
	}
	if access.WorkspaceScene && requireWrite {
		checker, ok := s.host.(interface {
			CheckSceneContentWrite(ctx context.Context, userID, sceneID string) error
		})
		if !ok {
			return roomaccess.Access{}, core.ErrForbidden
		}
		ctx := core.WithSceneClientID(
			r.Context(),
			strings.TrimSpace(r.Header.Get("X-Scene-Client-ID")),
		)
		if err := checker.CheckSceneContentWrite(ctx, access.UserID, sceneID); err != nil {
			return roomaccess.Access{}, err
		}
	}
	return access, nil
}

// StoredFile is a row in the files table (Firebase Storage emulator).
type StoredFile struct {
	Path         string    `json:"name"`
	Bucket       string    `json:"bucket"`
	ContentType  string    `json:"contentType"`
	Size         int64     `json:"size"`
	CacheControl string    `json:"cacheControl,omitempty"`
	CreatedAt    time.Time `json:"timeCreated"`
	UpdatedAt    time.Time `json:"updated"`
}

// initFilesTable creates the files table if it doesn't exist.
func (s *Store) initFilesTable() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS files (
		path TEXT NOT NULL,
		bucket TEXT NOT NULL,
		data BLOB NOT NULL,
		content_type TEXT DEFAULT 'application/octet-stream',
		cache_control TEXT DEFAULT '',
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		PRIMARY KEY (bucket, path)
	)`)
	if err != nil {
		return err
	}

	primaryKey, err := s.filesPrimaryKey()
	if err != nil {
		return err
	}
	if len(primaryKey) == 2 && primaryKey[0] == "bucket" && primaryKey[1] == "path" {
		return nil
	}
	if len(primaryKey) != 1 || primaryKey[0] != "path" {
		return fmt.Errorf("unsupported files table primary key: %v", primaryKey)
	}

	return s.migrateFilesTable()
}

func (s *Store) filesPrimaryKey() ([]string, error) {
	rows, err := s.db.Query(`PRAGMA table_info(files)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	primaryKeyByPosition := make(map[int]string)
	maxPosition := 0
	for rows.Next() {
		var cid, notNull, primaryKeyPosition int
		var name, columnType string
		var defaultValue interface{}
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKeyPosition); err != nil {
			return nil, err
		}
		if primaryKeyPosition > 0 {
			primaryKeyByPosition[primaryKeyPosition] = name
			if primaryKeyPosition > maxPosition {
				maxPosition = primaryKeyPosition
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	primaryKey := make([]string, maxPosition)
	for position, name := range primaryKeyByPosition {
		primaryKey[position-1] = name
	}
	return primaryKey, nil
}

func (s *Store) migrateFilesTable() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 在同一事务内复制旧数据并替换表，迁移失败时保留原表。
	if _, err := tx.Exec(`DROP TABLE IF EXISTS files_v2_migration`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE TABLE files_v2_migration (
		path TEXT NOT NULL,
		bucket TEXT NOT NULL,
		data BLOB NOT NULL,
		content_type TEXT DEFAULT 'application/octet-stream',
		cache_control TEXT DEFAULT '',
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		PRIMARY KEY (bucket, path)
	)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO files_v2_migration
		(path, bucket, data, content_type, cache_control, created_at, updated_at)
		SELECT path, bucket, data, content_type, cache_control, created_at, updated_at FROM files`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE files`); err != nil {
		return err
	}
	if _, err := tx.Exec(`ALTER TABLE files_v2_migration RENAME TO files`); err != nil {
		return err
	}
	return tx.Commit()
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
	r.Body = http.MaxBytesReader(w, r.Body, maxStorageUploadRequestBytes)

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
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				http.Error(w, "storage object too large", http.StatusRequestEntityTooLarge)
				return
			}
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
	access, err := s.authorizeStorageObject(r, path, true)
	if err != nil {
		writeAccessErr(w, err)
		return
	}
	contentType := meta.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	now := time.Now()
	if _, isRoomFile := sceneIDFromStoragePath(path); isRoomFile && !access.WorkspaceScene {
		s.storeAnonymousFile(storageObjectKey{bucket: bucket, path: path}, anonymousStorageObject{
			data:         data,
			contentType:  contentType,
			cacheControl: meta.CacheControl,
			updatedAt:    now,
		})
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
		return
	}
	query := `INSERT INTO files (path, bucket, data, content_type, cache_control, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(bucket, path) DO UPDATE SET data=excluded.data, content_type=excluded.content_type,
			cache_control=excluded.cache_control, updated_at=excluded.updated_at`
	if strings.HasPrefix(path, "files/shareLinks/") {
		// 分享链接没有单独的 ACL 归属表。文件 ID 一旦创建即不可变，避免知道公开
		// 分享 ID 的其他登录用户覆盖原始附件；重复上传保持幂等成功。
		query = `INSERT INTO files (path, bucket, data, content_type, cache_control, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(bucket, path) DO NOTHING`
	}
	_, err = s.db.Exec(query, path, bucket, data, contentType, meta.CacheControl, now, now)
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
	logrus.WithFields(logrus.Fields{"bucket": bucket, "path": path}).Debug("storage: download")
	access, err := s.authorizeStorageObject(r, path, false)
	if err != nil {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	if _, isRoomFile := sceneIDFromStoragePath(path); isRoomFile && !access.WorkspaceScene {
		object, ok := s.loadAnonymousFile(storageObjectKey{bucket: bucket, path: path}, time.Now())
		if !ok {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		if object.contentType == "" {
			object.contentType = "application/octet-stream"
		}
		if object.cacheControl != "" {
			w.Header().Set("Cache-Control", object.cacheControl)
		}
		w.Header().Set("Content-Type", object.contentType)
		_, _ = w.Write(object.data)
		return
	}

	var data []byte
	var contentType, cacheControl string
	err = s.db.QueryRow(`SELECT data, content_type, cache_control FROM files WHERE path = ? AND bucket = ?`,
		path, bucket).Scan(&data, &contentType, &cacheControl)
	if err != nil {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if access.WorkspaceScene {
		w.Header().Set("Cache-Control", "private, no-store")
	} else if cacheControl != "" {
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
