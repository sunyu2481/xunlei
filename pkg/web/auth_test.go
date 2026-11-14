package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func Test基础认证凭据边界(t *testing.T) {
	const password = "模拟面板密码:123"
	for _, account := range []struct {
		name     string
		username string
		user     string
	}{
		{"默认用户名", "", "admin"},
		{"自定义用户名", "面板管理员", "面板管理员"},
	} {
		t.Run(account.name, func(t *testing.T) {
			for _, test := range []struct {
				name    string
				user    string
				pass    string
				allowed bool
			}{
				{"正确凭据", account.user, password, true},
				{"空用户名", "", password, false},
				{"空密码", account.user, "", false},
				{"用户名较短", account.user[:len(account.user)-1], password, false},
				{"用户名较长", account.user + "1", password, false},
				{"用户名等长错误", strings.Repeat("x", len(account.user)), password, false},
				{"密码较短", account.user, password[:len(password)-1], false},
				{"密码较长", account.user, password + "1", false},
				{"密码等长错误", account.user, strings.Repeat("x", len(password)), false},
				{"均不匹配", "错误用户", "错误密码", false},
				{"用户名含空白", " " + account.user, password, false},
				{"密码含空白", account.user, password + " ", false},
			} {
				t.Run(test.name, func(t *testing.T) {
					request := httptest.NewRequest(http.MethodGet, "/", nil)
					request.SetBasicAuth(test.user, test.pass)
					checkBasicAuth(t, BasicAuth(account.username, password), request, test.allowed)
				})
			}
		})
	}
}

func Test基础认证拒绝缺失和畸形凭据(t *testing.T) {
	for _, authorization := range []string{"", "Bearer 模拟凭据", "Basic !!!", "Basic YWRtaW4=", "Basic Og=="} {
		t.Run(authorization, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Header.Set("Authorization", authorization)
			checkBasicAuth(t, BasicAuth("", "模拟面板密码"), request, false)
		})
	}
}

func Test基础认证空密码关闭认证(t *testing.T) {
	for _, username := range []string{"", "面板管理员"} {
		for _, authorization := range []string{"", "Basic !!!"} {
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Header.Set("Authorization", authorization)
			checkBasicAuth(t, BasicAuth(username, ""), request, true)
		}
	}
}

func checkBasicAuth(t *testing.T, middleware Middleware, request *http.Request, allowed bool) {
	t.Helper()
	called := false
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r != request {
			t.Error("认证后没有传递原请求")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if called != allowed {
		t.Fatalf("业务处理器调用状态为 %t，预期为 %t", called, allowed)
	}
	wantStatus, wantChallenge := http.StatusUnauthorized, `Basic realm="xlp"`
	if allowed {
		wantStatus, wantChallenge = http.StatusNoContent, ""
	}
	if response.Code != wantStatus || response.Header().Get("WWW-Authenticate") != wantChallenge {
		t.Fatalf("认证响应不正确：状态 %d，质询头 %q", response.Code, response.Header().Get("WWW-Authenticate"))
	}
	if response.Body.Len() != 0 {
		t.Fatalf("认证响应包含意外正文：%q", response.Body.String())
	}
}
