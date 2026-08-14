package mcpcanvas

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func openStorageTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "storage.db"))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func uploadStorageObject(t *testing.T, router http.Handler, bucket, path, content string) *httptest.ResponseRecorder {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	metadataHeader := make(textproto.MIMEHeader)
	metadataHeader.Set("Content-Type", "application/json")
	metadataPart, err := writer.CreatePart(metadataHeader)
	if err != nil {
		t.Fatalf("创建元数据分片失败: %v", err)
	}
	if err := json.NewEncoder(metadataPart).Encode(storageMetadata{Name: path, ContentType: "text/plain"}); err != nil {
		t.Fatalf("编码元数据失败: %v", err)
	}
	dataHeader := make(textproto.MIMEHeader)
	dataHeader.Set("Content-Type", "text/plain")
	dataPart, err := writer.CreatePart(dataHeader)
	if err != nil {
		t.Fatalf("创建数据分片失败: %v", err)
	}
	if _, err := io.WriteString(dataPart, content); err != nil {
		t.Fatalf("写入数据分片失败: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("关闭 multipart 写入器失败: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v0/b/"+bucket+"/o", &body)
	request.Header.Set("Content-Type", "multipart/related; boundary="+writer.Boundary())
	router.ServeHTTP(recorder, request)
	return recorder
}

func downloadStorageObject(t *testing.T, router http.Handler, bucket, path string) *httptest.ResponseRecorder {
	t.Helper()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v0/b/"+bucket+"/o/"+path+"?alt=media", nil)
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestInitFilesTableMigratesLegacySchemaWithoutDataLoss(t *testing.T) {
	db := openStorageTestDB(t)
	now := time.Now()
	if _, err := db.Exec(`CREATE TABLE files (
		path TEXT PRIMARY KEY,
		bucket TEXT NOT NULL,
		data BLOB NOT NULL,
		content_type TEXT DEFAULT 'application/octet-stream',
		cache_control TEXT DEFAULT '',
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	)`); err != nil {
		t.Fatalf("创建旧表失败: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO files
		(path, bucket, data, content_type, cache_control, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, "shared.txt", "legacy", []byte("legacy-data"), "text/plain", "max-age=60", now, now); err != nil {
		t.Fatalf("写入旧数据失败: %v", err)
	}

	store := &Store{db: db}
	if err := store.initFilesTable(); err != nil {
		t.Fatalf("迁移旧表失败: %v", err)
	}

	var data []byte
	if err := db.QueryRow(`SELECT data FROM files WHERE bucket = ? AND path = ?`, "legacy", "shared.txt").Scan(&data); err != nil {
		t.Fatalf("读取迁移数据失败: %v", err)
	}
	if string(data) != "legacy-data" {
		t.Fatalf("迁移数据 = %q，期望 legacy-data", data)
	}
	if _, err := db.Exec(`INSERT INTO files
		(path, bucket, data, content_type, cache_control, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, "shared.txt", "second", []byte("second-data"), "text/plain", "", now, now); err != nil {
		t.Fatalf("复合主键未允许同路径写入不同 bucket: %v", err)
	}
}

func TestStorageKeepsSamePathSeparateAcrossBuckets(t *testing.T) {
	db := openStorageTestDB(t)
	store := &Store{db: db}
	if err := store.initFilesTable(); err != nil {
		t.Fatalf("初始化 files 表失败: %v", err)
	}

	router := chi.NewRouter()
	router.Route("/v0/b/{bucket}", func(r chi.Router) {
		StorageRoutes(r, store)
	})

	firstUpload := uploadStorageObject(t, router, "first", "shared.txt", "first-data")
	if firstUpload.Code != http.StatusOK {
		t.Fatalf("第一个 bucket 上传失败: %d %s", firstUpload.Code, firstUpload.Body.String())
	}
	secondUpload := uploadStorageObject(t, router, "second", "shared.txt", "second-data")
	if secondUpload.Code != http.StatusOK {
		t.Fatalf("第二个 bucket 上传失败: %d %s", secondUpload.Code, secondUpload.Body.String())
	}

	firstDownload := downloadStorageObject(t, router, "first", "shared.txt")
	if firstDownload.Code != http.StatusOK || firstDownload.Body.String() != "first-data" {
		t.Fatalf("第一个 bucket 下载结果: %d %q", firstDownload.Code, firstDownload.Body.String())
	}
	secondDownload := downloadStorageObject(t, router, "second", "shared.txt")
	if secondDownload.Code != http.StatusOK || secondDownload.Body.String() != "second-data" {
		t.Fatalf("第二个 bucket 下载结果: %d %q", secondDownload.Code, secondDownload.Body.String())
	}
}
