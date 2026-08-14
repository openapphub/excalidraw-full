package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGenerateAndValidateOAuthState(t *testing.T) {
	recorder := httptest.NewRecorder()
	loginRequest := httptest.NewRequest(http.MethodGet, "http://example.com/auth/login", nil)
	state, err := generateStateOauthCookie(recorder, loginRequest, "oauthstate")
	if err != nil {
		t.Fatalf("生成 OAuth state 失败: %v", err)
	}
	if state == "" {
		t.Fatal("OAuth state 不应为空")
	}

	response := recorder.Result()
	cookies := response.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("期望写入 1 个 cookie，实际为 %d", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Path != "/" || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("OAuth cookie 安全属性不完整: %+v", cookie)
	}

	callbackRequest := httptest.NewRequest(http.MethodGet, "http://example.com/auth/callback?state="+state, nil)
	callbackRequest.AddCookie(cookie)
	if !validateOAuthState(callbackRequest, "oauthstate") {
		t.Fatal("正确的 OAuth state 应通过校验")
	}
}

func TestValidateOAuthStateRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		cookie    *http.Cookie
		cookieKey string
	}{
		{name: "缺少请求 state", url: "http://example.com/auth/callback", cookie: &http.Cookie{Name: "oauthstate", Value: "expected"}, cookieKey: "oauthstate"},
		{name: "缺少 cookie", url: "http://example.com/auth/callback?state=expected", cookieKey: "oauthstate"},
		{name: "state 不匹配", url: "http://example.com/auth/callback?state=actual", cookie: &http.Cookie{Name: "oauthstate", Value: "expected"}, cookieKey: "oauthstate"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, tt.url, nil)
			if tt.cookie != nil {
				request.AddCookie(tt.cookie)
			}
			if validateOAuthState(request, tt.cookieKey) {
				t.Fatal("无效的 OAuth state 不应通过校验")
			}
		})
	}
}

func TestGenerateOAuthStateUsesSecureCookieBehindHTTPSProxy(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://example.com/auth/login", nil)
	request.Header.Set("X-Forwarded-Proto", "https")

	if _, err := generateStateOauthCookie(recorder, request, "oidc_state"); err != nil {
		t.Fatalf("生成 OIDC state 失败: %v", err)
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure {
		t.Fatal("HTTPS 反向代理下必须设置 Secure cookie")
	}
}
