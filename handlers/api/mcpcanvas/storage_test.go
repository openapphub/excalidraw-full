package mcpcanvas

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"excalidraw-complete/core"
	"excalidraw-complete/handlers/auth"
	sqlitestore "excalidraw-complete/stores/sqlite"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
)

const storageTestSecret = "storage-test-secret-at-least-32-bytes"

func storageTestToken(t *testing.T, userID string) string {
	t.Helper()
	t.Setenv("JWT_SECRET", storageTestSecret)
	auth.Init()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, auth.AppClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	})
	signed, err := token.SignedString([]byte(storageTestSecret))
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func openStorageTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "storage.db"))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func uploadStorageObject(t *testing.T, router http.Handler, bucket, path, content string, token ...string) *httptest.ResponseRecorder {
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
	if len(token) > 0 && token[0] != "" {
		request.Header.Set("Authorization", "Bearer "+token[0])
	}
	if len(token) > 1 && token[1] != "" {
		request.Header.Set("X-Scene-Client-ID", token[1])
	}
	router.ServeHTTP(recorder, request)
	return recorder
}

func downloadStorageObject(t *testing.T, router http.Handler, bucket, path string, token ...string) *httptest.ResponseRecorder {
	t.Helper()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v0/b/"+bucket+"/o/"+path+"?alt=media", nil)
	if len(token) > 0 && token[0] != "" {
		request.Header.Set("Authorization", "Bearer "+token[0])
	}
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestWorkspaceSceneFilesRequireACL(t *testing.T) {
	host := sqlitestore.NewStore(t.TempDir() + "/host.db")
	ctx := context.Background()
	const ownerID = "owner"
	const viewerID = "viewer"
	const outsiderID = "outsider"

	workspace, err := host.CreateShellWorkspace(ctx, ownerID, "团队", "", core.WorkspaceShared)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := host.CreateCollection(ctx, ownerID, workspace.ID, "项目", nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	scene, err := host.CreateScene(ctx, ownerID, "画布", nil, []byte(`{"elements":[]}`), &collection.ID)
	if err != nil {
		t.Fatal(err)
	}
	invite, err := host.CreateInviteLink(ctx, ownerID, workspace.ID, core.RoleViewer, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.JoinViaInviteLink(ctx, viewerID, invite.Code, core.MemberUser{ID: viewerID}); err != nil {
		t.Fatal(err)
	}

	db := openStorageTestDB(t)
	store := &Store{db: db, host: host}
	if err := store.initFilesTable(); err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Route("/v0/b/{bucket}", func(r chi.Router) {
		StorageRoutes(r, store)
	})
	path := "files/rooms/" + scene.ID + "/image"

	if response := uploadStorageObject(t, router, "bucket", path, "secret"); response.Code != http.StatusForbidden {
		t.Fatalf("未认证上传 Workspace 文件状态码 = %d", response.Code)
	}
	if response := uploadStorageObject(t, router, "bucket", path, "secret", storageTestToken(t, viewerID)); response.Code != http.StatusForbidden {
		t.Fatalf("VIEWER 上传 Workspace 文件状态码 = %d", response.Code)
	}
	ownerToken := storageTestToken(t, ownerID)
	if response := uploadStorageObject(t, router, "bucket", path, "secret", ownerToken); response.Code != http.StatusConflict {
		t.Fatalf("所有者未持锁上传 Workspace 文件状态码 = %d，期望 %d", response.Code, http.StatusConflict)
	}
	if _, err := host.AcquireSceneLock(ctx, ownerID, scene.ID, "tab-a", "Owner"); err != nil {
		t.Fatal(err)
	}
	if response := uploadStorageObject(t, router, "bucket", path, "secret", ownerToken, "tab-a"); response.Code != http.StatusOK {
		t.Fatalf("所有者上传 Workspace 文件状态码 = %d，响应 = %s", response.Code, response.Body.String())
	}
	if response := downloadStorageObject(t, router, "bucket", path, storageTestToken(t, outsiderID)); response.Code != http.StatusForbidden {
		t.Fatalf("非成员下载 Workspace 文件状态码 = %d", response.Code)
	}
	response := downloadStorageObject(t, router, "bucket", path, storageTestToken(t, viewerID))
	if response.Code != http.StatusOK || response.Body.String() != "secret" {
		t.Fatalf("VIEWER 下载 Workspace 文件失败: %d %q", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("Workspace 文件缓存策略 = %q", response.Header().Get("Cache-Control"))
	}
}

func TestAnonymousRoomFilesStayOutOfSQLite(t *testing.T) {
	host := sqlitestore.NewStore(t.TempDir() + "/host.db")
	db := openStorageTestDB(t)
	store := &Store{db: db, host: host}
	if err := store.initFilesTable(); err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Route("/v0/b/{bucket}", func(r chi.Router) {
		StorageRoutes(r, store)
	})

	const bucket = "test-bucket"
	const path = "files/rooms/0123456789abcdefabcd/image"
	if response := uploadStorageObject(t, router, bucket, path, "encrypted"); response.Code != http.StatusOK {
		t.Fatalf("匿名房间附件上传失败: %d %s", response.Code, response.Body.String())
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM files WHERE bucket = ? AND path = ?`, bucket, path).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("匿名房间附件写入了 SQLite: %d", count)
	}

	download := downloadStorageObject(t, router, bucket, path)
	if download.Code != http.StatusOK || download.Body.String() != "encrypted" {
		t.Fatalf("匿名房间附件内存读取失败: %d %q", download.Code, download.Body.String())
	}
}

func TestStorageUploadRejectsOversizedBody(t *testing.T) {
	db := openStorageTestDB(t)
	store := &Store{db: db}
	if err := store.initFilesTable(); err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Route("/v0/b/{bucket}", func(r chi.Router) {
		StorageRoutes(r, store)
	})

	oversized := strings.Repeat("x", maxStorageUploadRequestBytes)
	response := uploadStorageObject(
		t,
		router,
		"test-bucket",
		"files/rooms/0123456789abcdefabcd/image",
		oversized,
	)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超大附件状态码 = %d，期望 %d", response.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestStorageNonWorkspaceUploadsRequireAuthenticationAndAreImmutable(t *testing.T) {
	db := openStorageTestDB(t)
	store := &Store{db: db}
	if err := store.initFilesTable(); err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Route("/v0/b/{bucket}", func(r chi.Router) {
		StorageRoutes(r, store)
	})
	const bucket = "test-bucket"
	const path = "files/shareLinks/share-id/file-id"

	if response := uploadStorageObject(t, router, bucket, path, "anonymous"); response.Code != http.StatusForbidden {
		t.Fatalf("匿名上传分享文件状态码 = %d，期望 %d", response.Code, http.StatusForbidden)
	}
	token := storageTestToken(t, "user-alice")
	if response := uploadStorageObject(t, router, bucket, path, "original", token); response.Code != http.StatusOK {
		t.Fatalf("登录上传分享文件状态码 = %d，响应 = %s", response.Code, response.Body.String())
	}
	if response := uploadStorageObject(t, router, bucket, path, "replacement", storageTestToken(t, "user-bob")); response.Code != http.StatusOK {
		t.Fatalf("重复上传分享文件状态码 = %d，响应 = %s", response.Code, response.Body.String())
	}
	download := downloadStorageObject(t, router, bucket, path)
	if download.Code != http.StatusOK || download.Body.String() != "original" {
		t.Fatalf("分享文件被覆盖：状态码 = %d，内容 = %q", download.Code, download.Body.String())
	}
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

	token := storageTestToken(t, "user-alice")
	firstUpload := uploadStorageObject(t, router, "first", "shared.txt", "first-data", token)
	if firstUpload.Code != http.StatusOK {
		t.Fatalf("第一个 bucket 上传失败: %d %s", firstUpload.Code, firstUpload.Body.String())
	}
	secondUpload := uploadStorageObject(t, router, "second", "shared.txt", "second-data", token)
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
