package openai

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestBuildChatCompletionsURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		want    string
		wantErr bool
	}{
		{
			name:    "根地址",
			baseURL: "https://api.openai.com",
			want:    "https://api.openai.com/v1/chat/completions",
		},
		{
			name:    "根地址尾斜杠",
			baseURL: "https://provider.example/",
			want:    "https://provider.example/v1/chat/completions",
		},
		{
			name:    "保留服务商路径",
			baseURL: "https://provider.example/v1/",
			want:    "https://provider.example/v1/chat/completions",
		},
		{
			name:    "保留查询参数",
			baseURL: "https://provider.example/openai/deployments/model?api-version=2025-01-01",
			want:    "https://provider.example/openai/deployments/model/chat/completions?api-version=2025-01-01",
		},
		{
			name:    "接受完整 endpoint",
			baseURL: "https://provider.example/v1/chat/completions",
			want:    "https://provider.example/v1/chat/completions",
		},
		{
			name:    "规范化完整 endpoint 尾斜杠",
			baseURL: "https://provider.example/v1/chat/completions/?api-version=2025-01-01",
			want:    "https://provider.example/v1/chat/completions?api-version=2025-01-01",
		},
		{
			name:    "拒绝非 HTTP 协议",
			baseURL: "file:///tmp/provider",
			wantErr: true,
		},
		{
			name:    "拒绝相对地址",
			baseURL: "/api/v1",
			wantErr: true,
		},
		{
			name:    "拒绝 userinfo",
			baseURL: "https://user:password@provider.example/v1",
			wantErr: true,
		},
		{
			name:    "拒绝 fragment",
			baseURL: "https://provider.example/v1#fragment",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildChatCompletionsURL(tt.baseURL)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("buildChatCompletionsURL() 未返回错误，结果为 %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("buildChatCompletionsURL() 返回错误: %v", err)
			}
			if got.String() != tt.want {
				t.Fatalf("buildChatCompletionsURL() = %q，期望 %q", got, tt.want)
			}
		})
	}
}

func TestSelectProxyAuthorization(t *testing.T) {
	tests := []struct {
		name         string
		clientHeader string
		serverAPIKey string
		clientTarget bool
		want         string
		wantErr      bool
	}{
		{
			name:         "动态目标接受任意前缀的客户端密钥",
			clientHeader: "Bearer vendor-key_123",
			serverAPIKey: "server-secret",
			clientTarget: true,
			want:         "Bearer vendor-key_123",
		},
		{
			name:         "动态目标绝不回退服务端密钥",
			serverAPIKey: "server-secret",
			clientTarget: true,
			wantErr:      true,
		},
		{
			name:         "服务端目标优先服务端密钥",
			clientHeader: "Bearer application-jwt",
			serverAPIKey: "server-secret",
			want:         "Bearer server-secret",
		},
		{
			name:         "无服务端密钥时使用客户端密钥",
			clientHeader: "bearer another-provider-token",
			want:         "Bearer another-provider-token",
		},
		{
			name:    "无任何密钥时失败",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := selectProxyAuthorization(tt.clientHeader, tt.serverAPIKey, tt.clientTarget)
			if tt.wantErr {
				if !errors.Is(err, errMissingClientAPIKey) {
					t.Fatalf("selectProxyAuthorization() 错误 = %v，期望 %v", err, errMissingClientAPIKey)
				}
				return
			}
			if err != nil {
				t.Fatalf("selectProxyAuthorization() 返回错误: %v", err)
			}
			if got != tt.want {
				t.Fatalf("selectProxyAuthorization() = %q，期望 %q", got, tt.want)
			}
		})
	}
}

func TestHandleChatCompletionAuthorizationBoundary(t *testing.T) {
	tests := []struct {
		name              string
		serverAPIKey      string
		clientHeader      string
		useClientOverride bool
		wantAuthorization string
	}{
		{
			name:              "动态目标只收到客户端密钥",
			serverAPIKey:      "server-secret",
			clientHeader:      "Bearer provider-specific-token",
			useClientOverride: true,
			wantAuthorization: "Bearer provider-specific-token",
		},
		{
			name:              "服务端目标收到服务端密钥",
			serverAPIKey:      "server-secret",
			clientHeader:      "Bearer application-jwt",
			wantAuthorization: "Bearer server-secret",
		},
		{
			name:              "没有服务端密钥时客户端密钥仍可工作",
			clientHeader:      "Bearer custom-prefix-token",
			wantAuthorization: "Bearer custom-prefix-token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotAuthorization string
			var gotPath string
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAuthorization = r.Header.Get("Authorization")
				gotPath = r.URL.Path
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[]}`))
			}))
			defer provider.Close()

			setProxyConfigForTest(t, tt.serverAPIKey, provider.URL+"/v1")
			req := newChatCompletionRequest(t)
			req.Header.Set("Authorization", tt.clientHeader)
			if tt.useClientOverride {
				req.Header.Set("X-AI-Base-URL", provider.URL+"/v1")
			}
			res := httptest.NewRecorder()

			HandleChatCompletion().ServeHTTP(res, req)

			if res.Code != http.StatusOK {
				t.Fatalf("代理状态码 = %d，期望 %d，响应: %s", res.Code, http.StatusOK, res.Body.String())
			}
			if gotAuthorization != tt.wantAuthorization {
				t.Fatalf("服务商收到 Authorization = %q，期望 %q", gotAuthorization, tt.wantAuthorization)
			}
			if gotPath != "/v1/chat/completions" {
				t.Fatalf("服务商收到路径 = %q，期望 %q", gotPath, "/v1/chat/completions")
			}
		})
	}
}

func TestHandleChatCompletionClientOverrideRequiresClientKey(t *testing.T) {
	var providerCalled atomic.Bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalled.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer provider.Close()

	setProxyConfigForTest(t, "server-secret", "https://server-configured.example/v1")
	req := newChatCompletionRequest(t)
	req.Header.Set("X-AI-Base-URL", provider.URL)
	res := httptest.NewRecorder()

	HandleChatCompletion().ServeHTTP(res, req)

	if res.Code != http.StatusUnauthorized {
		t.Fatalf("代理状态码 = %d，期望 %d，响应: %s", res.Code, http.StatusUnauthorized, res.Body.String())
	}
	if providerCalled.Load() {
		t.Fatal("缺少客户端密钥时不应请求动态目标")
	}
}

func TestHandleChatCompletionDoesNotFollowRedirects(t *testing.T) {
	var redirectedProviderCalled atomic.Bool
	redirectedProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedProviderCalled.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer redirectedProvider.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectedProvider.URL+"/captured", http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	setProxyConfigForTest(t, "server-secret", "https://server-configured.example/v1")
	req := newChatCompletionRequest(t)
	req.Header.Set("X-AI-Base-URL", redirector.URL)
	req.Header.Set("Authorization", "Bearer client-secret")
	res := httptest.NewRecorder()

	HandleChatCompletion().ServeHTTP(res, req)

	if res.Code != http.StatusBadGateway {
		t.Fatalf("代理状态码 = %d，期望 %d，响应: %s", res.Code, http.StatusBadGateway, res.Body.String())
	}
	if redirectedProviderCalled.Load() {
		t.Fatal("代理不应跟随服务商重定向")
	}
}

func setProxyConfigForTest(t *testing.T, apiKey, baseURL string) {
	t.Helper()
	previousAPIKey := openaiAPIKey
	previousBaseURL := openaiBaseURL
	openaiAPIKey = apiKey
	openaiBaseURL = baseURL
	t.Cleanup(func() {
		openaiAPIKey = previousAPIKey
		openaiBaseURL = previousBaseURL
	})
}

func newChatCompletionRequest(t *testing.T) *http.Request {
	t.Helper()
	return httptest.NewRequest(
		http.MethodPost,
		"http://proxy.example/api/v2/chat/completions",
		strings.NewReader(`{"model":"test-model","messages":[],"stream":false}`),
	)
}
