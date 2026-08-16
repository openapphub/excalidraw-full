package main

import (
	"excalidraw-complete/stores/memory"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMCPCanvasRoutesRequireAuthentication(t *testing.T) {
	t.Setenv("DATA_SOURCE_NAME", t.TempDir()+"/mcp-auth.db")
	router, mcStore := setupRouter(memory.NewStore())
	t.Cleanup(func() { _ = mcStore.Close() })

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/canvas", nil)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("未认证 MCP 请求状态码 = %d，期望 %d", recorder.Code, http.StatusUnauthorized)
	}
}

func TestShareLinkCreationRequiresAuthentication(t *testing.T) {
	t.Setenv("DATA_SOURCE_NAME", t.TempDir()+"/share-auth.db")
	router, mcStore := setupRouter(memory.NewStore())
	t.Cleanup(func() { _ = mcStore.Close() })

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v2/post/", strings.NewReader("encrypted-scene"))
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("未登录创建分享链接状态码 = %d，期望 %d", recorder.Code, http.StatusUnauthorized)
	}
}

func TestAnonymousRoomSnapshotRoutesDoNotRequireLogin(t *testing.T) {
	t.Setenv("DATA_SOURCE_NAME", t.TempDir()+"/room-snapshot-route.db")
	router, mcStore := setupRouter(memory.NewStore())
	t.Cleanup(func() { _ = mcStore.Close() })
	const roomID = "0123456789abcdefabcd"

	write := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPut,
		"/api/v2/collab/rooms/"+roomID,
		strings.NewReader(`{"expectedRevision":0,"sceneVersion":1,"ciphertext":"ZW5jcnlwdGVk","iv":"MTIzNDU2Nzg5MDEy"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(write, request)
	if write.Code != http.StatusOK {
		t.Fatalf("匿名房间写入状态码 = %d，响应 = %s", write.Code, write.Body.String())
	}

	read := httptest.NewRecorder()
	router.ServeHTTP(
		read,
		httptest.NewRequest(http.MethodGet, "/api/v2/collab/rooms/"+roomID, nil),
	)
	if read.Code != http.StatusOK {
		t.Fatalf("匿名房间读取状态码 = %d，响应 = %s", read.Code, read.Body.String())
	}
}

func TestHandleNotFoundRejectsUnknownAPIRoute(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v2/chat/completions/chat/completions", nil)

	handleNotFound().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("状态码 = %d，期望 %d", recorder.Code, http.StatusNotFound)
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("Content-Type = %q，期望 JSON", contentType)
	}
	if strings.Contains(strings.ToLower(recorder.Body.String()), "<!doctype html") {
		t.Fatal("未知 API 路径不应回退到 SPA HTML")
	}
}

func TestHandleNotFoundKeepsSPAHistoryFallback(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/canvas/example", nil)

	handleNotFound().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 %d", recorder.Code, http.StatusOK)
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Fatalf("Content-Type = %q，期望 HTML", contentType)
	}
}
