package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
