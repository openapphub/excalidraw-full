package firebase

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"excalidraw-complete/core"
	"excalidraw-complete/handlers/auth"
	"excalidraw-complete/stores/sqlite"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
)

const firebaseTestSecret = "firebase-test-secret-at-least-32-bytes"

func firebaseTestToken(t *testing.T, userID string) string {
	t.Helper()
	t.Setenv("JWT_SECRET", firebaseTestSecret)
	auth.Init()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, auth.AppClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	})
	signed, err := token.SignedString([]byte(firebaseTestSecret))
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func resetSavedItems() {
	savedItemsMu.Lock()
	savedItems = make(map[string]interface{})
	roomSnapshots = make(map[string]roomSnapshotEntry)
	roomSnapshotBytes = 0
	savedItemsMu.Unlock()
}

func performJSONRequest(t *testing.T, handler http.HandlerFunc, body interface{}) *httptest.ResponseRecorder {
	t.Helper()

	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("编码请求失败: %v", err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(encoded))
	handler.ServeHTTP(recorder, request)
	return recorder
}

func performAuthenticatedJSONRequest(t *testing.T, handler http.HandlerFunc, body interface{}, token string, clientID ...string) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(encoded))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if len(clientID) > 0 && clientID[0] != "" {
		request.Header.Set("X-Scene-Client-ID", clientID[0])
	}
	handler.ServeHTTP(recorder, request)
	return recorder
}

func performRoomSnapshotRequest(
	t *testing.T,
	handler http.HandlerFunc,
	method string,
	roomID string,
	body interface{},
	token string,
	clientID string,
) *httptest.ResponseRecorder {
	t.Helper()

	var requestBody = bytes.NewReader(nil)
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		requestBody = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, "/api/v2/collab/rooms/"+roomID, requestBody)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("room_id", roomID)
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if clientID != "" {
		request.Header.Set("X-Scene-Client-ID", clientID)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func validRoomSnapshotWrite(expectedRevision uint64, sceneVersion int64) RoomSnapshotWriteRequest {
	return RoomSnapshotWriteRequest{
		ExpectedRevision: expectedRevision,
		SceneVersion:     sceneVersion,
		Ciphertext:       base64.StdEncoding.EncodeToString([]byte("encrypted-scene")),
		IV:               base64.StdEncoding.EncodeToString([]byte("123456789012")),
	}
}

func TestAnonymousRoomSnapshotUsesAtomicRevision(t *testing.T) {
	resetSavedItems()
	store := sqlite.NewStore(t.TempDir() + "/room-snapshot.db")
	const roomID = "0123456789abcdefabcd"

	if response := performRoomSnapshotRequest(t, HandleRoomSnapshotGet(store), http.MethodGet, roomID, nil, "", ""); response.Code != http.StatusNotFound {
		t.Fatalf("空房间读取状态码 = %d，期望 404", response.Code)
	}

	first := performRoomSnapshotRequest(
		t,
		HandleRoomSnapshotPut(store),
		http.MethodPut,
		roomID,
		validRoomSnapshotWrite(0, 1),
		"",
		"",
	)
	if first.Code != http.StatusOK {
		t.Fatalf("首次写入状态码 = %d，响应 = %s", first.Code, first.Body.String())
	}
	if first.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("房间快照写入缓存策略 = %q", first.Header().Get("Cache-Control"))
	}
	var firstSnapshot RoomSnapshot
	if err := json.Unmarshal(first.Body.Bytes(), &firstSnapshot); err != nil {
		t.Fatal(err)
	}
	if firstSnapshot.Revision != 1 {
		t.Fatalf("首次 revision = %d，期望 1", firstSnapshot.Revision)
	}

	stale := performRoomSnapshotRequest(
		t,
		HandleRoomSnapshotPut(store),
		http.MethodPut,
		roomID,
		validRoomSnapshotWrite(0, 2),
		"",
		"",
	)
	if stale.Code != http.StatusConflict {
		t.Fatalf("过期 revision 写入状态码 = %d，期望 409", stale.Code)
	}

	second := performRoomSnapshotRequest(
		t,
		HandleRoomSnapshotPut(store),
		http.MethodPut,
		roomID,
		validRoomSnapshotWrite(1, 2),
		"",
		"",
	)
	if second.Code != http.StatusOK {
		t.Fatalf("第二次写入状态码 = %d，响应 = %s", second.Code, second.Body.String())
	}

	read := performRoomSnapshotRequest(t, HandleRoomSnapshotGet(store), http.MethodGet, roomID, nil, "", "")
	if read.Code != http.StatusOK {
		t.Fatalf("读取状态码 = %d，响应 = %s", read.Code, read.Body.String())
	}
	if read.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("房间快照读取缓存策略 = %q", read.Header().Get("Cache-Control"))
	}
	var snapshot RoomSnapshot
	if err := json.Unmarshal(read.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision != 2 || snapshot.SceneVersion != 2 {
		t.Fatalf("房间快照 = %+v，期望 revision=2 sceneVersion=2", snapshot)
	}

	invalidRoom := performRoomSnapshotRequest(t, HandleRoomSnapshotPut(store), http.MethodPut, "not-a-room", validRoomSnapshotWrite(0, 1), "", "")
	if invalidRoom.Code != http.StatusForbidden {
		t.Fatalf("非法匿名房间状态码 = %d，期望 403", invalidRoom.Code)
	}
}

func TestAnonymousRoomSnapshotExpiresFromMemory(t *testing.T) {
	resetSavedItems()
	store := sqlite.NewStore(t.TempDir() + "/room-snapshot-expiry.db")
	const roomID = "0123456789abcdefabcd"
	snapshot := RoomSnapshot{
		Revision:     1,
		SceneVersion: 1,
		Ciphertext:   base64.StdEncoding.EncodeToString([]byte("encrypted-scene")),
		IV:           base64.StdEncoding.EncodeToString([]byte("123456789012")),
		UpdatedAt:    time.Now().Add(-roomSnapshotTTL).UTC().Format(time.RFC3339Nano),
	}
	savedItemsMu.Lock()
	roomSnapshots[roomID] = roomSnapshotEntry{
		snapshot:  snapshot,
		storedAt:  time.Now().Add(-roomSnapshotTTL),
		byteCount: int64(len(snapshot.Ciphertext) + len(snapshot.IV)),
	}
	roomSnapshotBytes = roomSnapshots[roomID].byteCount
	savedItemsMu.Unlock()

	response := performRoomSnapshotRequest(
		t,
		HandleRoomSnapshotGet(store),
		http.MethodGet,
		roomID,
		nil,
		"",
		"",
	)
	if response.Code != http.StatusNotFound {
		t.Fatalf("过期房间快照状态码 = %d，期望 %d", response.Code, http.StatusNotFound)
	}
	savedItemsMu.RLock()
	defer savedItemsMu.RUnlock()
	if len(roomSnapshots) != 0 || roomSnapshotBytes != 0 {
		t.Fatalf("过期房间快照未释放: rooms=%d bytes=%d", len(roomSnapshots), roomSnapshotBytes)
	}
}

func TestWorkspaceRoomSnapshotRequiresACLAndLock(t *testing.T) {
	resetSavedItems()
	store := sqlite.NewStore(t.TempDir() + "/workspace-room-snapshot.db")
	ctx := context.Background()
	const ownerID = "owner"
	const viewerID = "viewer"
	const clientID = "owner-tab"

	workspace, err := store.CreateShellWorkspace(ctx, ownerID, "团队", "", core.WorkspaceShared)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := store.CreateCollection(ctx, ownerID, workspace.ID, "项目", nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	scene, err := store.CreateScene(ctx, ownerID, "画布", nil, []byte(`{"elements":[]}`), &collection.ID)
	if err != nil {
		t.Fatal(err)
	}
	invite, err := store.CreateInviteLink(ctx, ownerID, workspace.ID, core.RoleViewer, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.JoinViaInviteLink(ctx, viewerID, invite.Code, core.MemberUser{ID: viewerID}); err != nil {
		t.Fatal(err)
	}

	viewerToken := firebaseTestToken(t, viewerID)
	if response := performRoomSnapshotRequest(t, HandleRoomSnapshotPut(store), http.MethodPut, scene.ID, validRoomSnapshotWrite(0, 1), viewerToken, "viewer-tab"); response.Code != http.StatusForbidden {
		t.Fatalf("VIEWER 写 Workspace 房间状态码 = %d，期望 403", response.Code)
	}

	ownerToken := firebaseTestToken(t, ownerID)
	if response := performRoomSnapshotRequest(t, HandleRoomSnapshotPut(store), http.MethodPut, scene.ID, validRoomSnapshotWrite(0, 1), ownerToken, clientID); response.Code != http.StatusConflict {
		t.Fatalf("所有者无锁写入状态码 = %d，期望 409", response.Code)
	}
	if _, err := store.AcquireSceneLock(ctx, ownerID, scene.ID, clientID, "Owner"); err != nil {
		t.Fatal(err)
	}
	if response := performRoomSnapshotRequest(t, HandleRoomSnapshotPut(store), http.MethodPut, scene.ID, validRoomSnapshotWrite(0, 1), ownerToken, clientID); response.Code != http.StatusOK {
		t.Fatalf("持锁所有者写入状态码 = %d，响应 = %s", response.Code, response.Body.String())
	}
	if response := performRoomSnapshotRequest(t, HandleRoomSnapshotGet(store), http.MethodGet, scene.ID, nil, viewerToken, ""); response.Code != http.StatusOK {
		t.Fatalf("VIEWER 读取状态码 = %d，响应 = %s", response.Code, response.Body.String())
	}
}

func TestWorkspaceSceneDocumentsRequireACL(t *testing.T) {
	resetSavedItems()
	store := sqlite.NewStore(t.TempDir() + "/firebase-acl.db")
	ctx := context.Background()
	const ownerID = "owner"
	const viewerID = "viewer"
	const outsiderID = "outsider"

	workspace, err := store.CreateShellWorkspace(ctx, ownerID, "团队", "", core.WorkspaceShared)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := store.CreateCollection(ctx, ownerID, workspace.ID, "项目", nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	scene, err := store.CreateScene(ctx, ownerID, "画布", nil, []byte(`{"elements":[]}`), &collection.ID)
	if err != nil {
		t.Fatal(err)
	}
	invite, err := store.CreateInviteLink(ctx, ownerID, workspace.ID, core.RoleViewer, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.JoinViaInviteLink(ctx, viewerID, invite.Code, core.MemberUser{ID: viewerID}); err != nil {
		t.Fatal(err)
	}

	document := "projects/test/databases/(default)/documents/scenes/" + scene.ID
	write := BatchCommitRequest{Writes: []WriteRequest{{
		Update: UpdateRequest{Name: document, Fields: map[string]interface{}{"ciphertext": "secret"}},
	}}}
	if response := performAuthenticatedJSONRequest(t, HandleBatchCommit(store), write, ""); response.Code != http.StatusForbidden {
		t.Fatalf("未认证写 Workspace Scene 状态码 = %d", response.Code)
	}
	if response := performAuthenticatedJSONRequest(t, HandleBatchCommit(store), write, firebaseTestToken(t, viewerID)); response.Code != http.StatusForbidden {
		t.Fatalf("VIEWER 写 Workspace Scene 状态码 = %d", response.Code)
	}
	ownerToken := firebaseTestToken(t, ownerID)
	if response := performAuthenticatedJSONRequest(t, HandleBatchCommit(store), write, ownerToken); response.Code != http.StatusConflict {
		t.Fatalf("所有者无锁写 Workspace Scene 状态码 = %d，期望 409", response.Code)
	}
	if _, err := store.AcquireSceneLock(ctx, ownerID, scene.ID, "tab-a", "Owner"); err != nil {
		t.Fatal(err)
	}
	if response := performAuthenticatedJSONRequest(t, HandleBatchCommit(store), write, ownerToken, "tab-b"); response.Code != http.StatusConflict {
		t.Fatalf("非持锁客户端写 Workspace Scene 状态码 = %d，期望 409", response.Code)
	}
	if response := performAuthenticatedJSONRequest(t, HandleBatchCommit(store), write, ownerToken, "tab-a"); response.Code != http.StatusOK {
		t.Fatalf("持锁客户端写 Workspace Scene 状态码 = %d，响应 = %s", response.Code, response.Body.String())
	}

	read := BatchGetRequest{Documents: []string{document}}
	if response := performAuthenticatedJSONRequest(t, HandleBatchGet(store), read, firebaseTestToken(t, outsiderID)); response.Code != http.StatusForbidden {
		t.Fatalf("非成员读 Workspace Scene 状态码 = %d", response.Code)
	}
	if response := performAuthenticatedJSONRequest(t, HandleBatchGet(store), read, firebaseTestToken(t, viewerID)); response.Code != http.StatusOK {
		t.Fatalf("VIEWER 读 Workspace Scene 状态码 = %d，响应 = %s", response.Code, response.Body.String())
	}
}

func TestDeletedWorkspaceSceneCannotReadOrRewriteStaleDocument(t *testing.T) {
	resetSavedItems()
	store := sqlite.NewStore(t.TempDir() + "/firebase-deleted-scene.db")
	ctx := context.Background()
	const ownerID = "owner"
	const clientID = "tab-a"

	workspace, err := store.CreateShellWorkspace(ctx, ownerID, "团队", "", core.WorkspaceShared)
	if err != nil {
		t.Fatal(err)
	}
	collection, err := store.CreateCollection(ctx, ownerID, workspace.ID, "项目", nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	scene, err := store.CreateScene(ctx, ownerID, "画布", nil, []byte(`{"elements":[]}`), &collection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireSceneLock(ctx, ownerID, scene.ID, clientID, "Owner"); err != nil {
		t.Fatal(err)
	}

	document := "projects/test/databases/(default)/documents/scenes/" + scene.ID
	write := BatchCommitRequest{Writes: []WriteRequest{{
		Update: UpdateRequest{Name: document, Fields: map[string]interface{}{"ciphertext": "stale-secret"}},
	}}}
	ownerToken := firebaseTestToken(t, ownerID)
	if response := performAuthenticatedJSONRequest(t, HandleBatchCommit(store), write, ownerToken, clientID); response.Code != http.StatusOK {
		t.Fatalf("删除前写入状态码 = %d，响应 = %s", response.Code, response.Body.String())
	}
	if err := store.DeleteScene(core.WithSceneClientID(ctx, clientID), ownerID, scene.ID); err != nil {
		t.Fatal(err)
	}

	read := BatchGetRequest{Documents: []string{document}}
	if response := performAuthenticatedJSONRequest(t, HandleBatchGet(store), read, ownerToken); response.Code != http.StatusForbidden {
		t.Fatalf("删除后旧文档读取状态码 = %d，期望 403；响应 = %s", response.Code, response.Body.String())
	}
	if response := performAuthenticatedJSONRequest(t, HandleBatchCommit(store), write, ownerToken, clientID); response.Code != http.StatusForbidden {
		t.Fatalf("删除后旧文档重写状态码 = %d，期望 403；响应 = %s", response.Code, response.Body.String())
	}
}

func TestBatchHandlersRejectEmptyArrays(t *testing.T) {
	resetSavedItems()

	commit := performJSONRequest(t, HandleBatchCommit(), BatchCommitRequest{})
	if commit.Code != http.StatusBadRequest {
		t.Fatalf("空 writes 状态码 = %d，期望 %d", commit.Code, http.StatusBadRequest)
	}

	get := performJSONRequest(t, HandleBatchGet(), BatchGetRequest{})
	if get.Code != http.StatusBadRequest {
		t.Fatalf("空 documents 状态码 = %d，期望 %d", get.Code, http.StatusBadRequest)
	}
}

func TestProductionHandlersRejectNonSceneDocuments(t *testing.T) {
	resetSavedItems()
	store := sqlite.NewStore(t.TempDir() + "/firebase-document-scope.db")
	document := "projects/test/databases/(default)/documents/users/private"

	write := BatchCommitRequest{Writes: []WriteRequest{{
		Update: UpdateRequest{Name: document, Fields: map[string]interface{}{"secret": "value"}},
	}}}
	if response := performAuthenticatedJSONRequest(t, HandleBatchCommit(store), write, ""); response.Code != http.StatusForbidden {
		t.Fatalf("非 Scene 文档匿名写入状态码 = %d，期望 403", response.Code)
	}

	read := BatchGetRequest{Documents: []string{document}}
	if response := performAuthenticatedJSONRequest(t, HandleBatchGet(store), read, ""); response.Code != http.StatusForbidden {
		t.Fatalf("非 Scene 文档匿名读取状态码 = %d，期望 403", response.Code)
	}

	savedItemsMu.RLock()
	_, exists := savedItems[document]
	savedItemsMu.RUnlock()
	if exists {
		t.Fatal("被拒绝的非 Scene 文档不应写入全局缓存")
	}
}

func TestBatchHandlersProcessAllEntries(t *testing.T) {
	resetSavedItems()

	writes := BatchCommitRequest{Writes: []WriteRequest{
		{Update: UpdateRequest{Name: "documents/first", Fields: map[string]interface{}{"value": "one"}}},
		{Update: UpdateRequest{Name: "documents/second", Fields: map[string]interface{}{"value": "two"}}},
	}}
	commit := performJSONRequest(t, HandleBatchCommit(), writes)
	if commit.Code != http.StatusOK {
		t.Fatalf("批量提交状态码 = %d，响应 = %s", commit.Code, commit.Body.String())
	}
	var commitResponse BatchCommitResponse
	if err := json.Unmarshal(commit.Body.Bytes(), &commitResponse); err != nil {
		t.Fatalf("解析批量提交响应失败: %v", err)
	}
	if len(commitResponse.WriteResults) != len(writes.Writes) {
		t.Fatalf("writeResults 数量 = %d，期望 %d", len(commitResponse.WriteResults), len(writes.Writes))
	}

	get := performJSONRequest(t, HandleBatchGet(), BatchGetRequest{Documents: []string{
		"documents/first",
		"documents/missing",
		"documents/second",
	}})
	if get.Code != http.StatusOK {
		t.Fatalf("批量读取状态码 = %d，响应 = %s", get.Code, get.Body.String())
	}
	var responses []struct {
		Found   *FoundInfoResponse `json:"found"`
		Missing string             `json:"missing"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &responses); err != nil {
		t.Fatalf("解析批量读取响应失败: %v", err)
	}
	if len(responses) != 3 {
		t.Fatalf("批量读取响应数量 = %d，期望 3", len(responses))
	}
	if responses[0].Found == nil || responses[0].Found.Name != "documents/first" {
		t.Fatalf("第一条响应不正确: %+v", responses[0])
	}
	if responses[1].Missing != "documents/missing" {
		t.Fatalf("第二条响应不正确: %+v", responses[1])
	}
	if responses[2].Found == nil || responses[2].Found.Name != "documents/second" {
		t.Fatalf("第三条响应不正确: %+v", responses[2])
	}
}

func TestBatchHandlersConcurrentAccess(t *testing.T) {
	resetSavedItems()

	const workers = 64
	var waitGroup sync.WaitGroup
	errors := make(chan error, workers)
	for i := 0; i < workers; i++ {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			name := fmt.Sprintf("documents/%d", index)

			commit := performJSONRequest(t, HandleBatchCommit(), BatchCommitRequest{Writes: []WriteRequest{
				{Update: UpdateRequest{Name: name, Fields: map[string]interface{}{"value": index}}},
			}})
			if commit.Code != http.StatusOK {
				errors <- fmt.Errorf("提交 %s 返回 %d", name, commit.Code)
				return
			}

			get := performJSONRequest(t, HandleBatchGet(), BatchGetRequest{Documents: []string{name}})
			if get.Code != http.StatusOK {
				errors <- fmt.Errorf("读取 %s 返回 %d", name, get.Code)
			}
		}(i)
	}
	waitGroup.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}

	savedItemsMu.RLock()
	itemCount := len(savedItems)
	savedItemsMu.RUnlock()
	if itemCount != workers {
		t.Fatalf("保存条目数量 = %d，期望 %d", itemCount, workers)
	}
}
