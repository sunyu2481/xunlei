package remoteweb

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/cnk3x/xunlei/pkg/remote"
	"github.com/cnk3x/xunlei/pkg/web"
	"github.com/go-chi/chi/v5"
)

func Test读取路由HEAD与GET一致(t *testing.T) {
	for _, route := range []struct {
		name, path string
		status     int
		err        error
	}{
		{"状态", "/api/remote/status", 200, nil},
		{"设备", "/api/remote/devices", 200, nil},
		{"目录", "/api/remote/folders?target=设备一&path=/downloads", 200, nil},
		{"任务", "/api/remote/tasks?target=设备一&page_token=下一页", 200, nil},
		{"业务错误", "/api/remote/tasks?target=设备一", 422, &remote.Error{Kind: "login_required", Message: "需要登录"}},
		{"超时错误", "/api/remote/tasks?target=设备一", 504, context.DeadlineExceeded},
		{"页面", "/remote/view.html", 200, nil},
		{"页面样式", "/remote/style.css", 200, nil},
		{"页面脚本", "/remote/app.js", 200, nil},
		{"面板样式", "/remote/panel.css", 200, nil},
		{"面板脚本", "/remote/panel.js", 200, nil},
		{"入口跳转", "/remote/", 307, nil},
	} {
		for _, auth := range []struct {
			name, password string
			enabled        bool
		}{
			{"已认证", "面板密码", true},
			{"未认证", "", true},
			{"错误凭据", "错误密码", true},
			{"无需认证", "", false},
		} {
			t.Run(route.name+"/"+auth.name, func(t *testing.T) {
				service := &fakeService{err: route.err}
				username, password := "", ""
				if auth.enabled {
					username, password = "admin", "面板密码"
				}
				handler := web.BasicAuth(username, password)(New(service))
				mux := chi.NewRouter()
				mux.Mount("/remote", handler)
				mux.Mount("/api/remote", handler)
				server := httptest.NewServer(mux)
				defer func() {
					server.Close()
					if service.calls != 0 {
						t.Error("读取请求调用了写入接口")
					}
				}()
				client := server.Client()
				client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
				want := route.status
				if auth.enabled && auth.password != password {
					want = http.StatusUnauthorized
				}
				var getHeader http.Header
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					req, err := http.NewRequest(method, server.URL+route.path, nil)
					if err != nil {
						t.Fatal(err)
					}
					if auth.password != "" {
						req.SetBasicAuth("admin", auth.password)
					}
					response, err := client.Do(req)
					if err != nil {
						t.Fatal(err)
					}
					body, readErr := io.ReadAll(response.Body)
					response.Body.Close()
					if readErr != nil {
						t.Fatal(readErr)
					}
					if response.StatusCode != want {
						t.Errorf("%s 状态应为 %d，实际为 %d，正文为 %q", method, want, response.StatusCode, body)
					}
					if method == http.MethodGet {
						getHeader = response.Header
						if len(body) == 0 && want != http.StatusUnauthorized {
							t.Error("GET 对照没有正文")
						}
						continue
					}
					if len(body) != 0 {
						t.Errorf("HEAD 不应返回正文：%q", body)
					}
					for _, key := range []string{"Content-Type", "Cache-Control", "Content-Security-Policy", "X-Content-Type-Options", "Referrer-Policy", "Www-Authenticate", "Location", "Accept-Ranges"} {
						if !reflect.DeepEqual(response.Header.Values(key), getHeader.Values(key)) {
							t.Errorf("HEAD 的 %s 与 GET 不同：%q，预期为 %q", key, response.Header.Values(key), getHeader.Values(key))
						}
					}
					if want != http.StatusTemporaryRedirect && want != http.StatusUnauthorized && response.Header.Get("Content-Length") != getHeader.Get("Content-Length") {
						t.Errorf("HEAD 的长度与 GET 不同：%q，预期为 %q", response.Header.Get("Content-Length"), getHeader.Get("Content-Length"))
					}
				}
			})
		}
	}
}

func Test安全读取不放宽写入方法与请求头(t *testing.T) {
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/remote/login"},
		{http.MethodPost, "/api/remote/logout"},
		{http.MethodPost, "/api/remote/review/send"},
		{http.MethodPost, "/api/remote/review/check"},
		{http.MethodPut, "/api/remote/settings"},
		{http.MethodPost, "/api/remote/tasks"},
		{http.MethodPost, "/api/remote/tasks/任务一/pause"},
	} {
		for _, mode := range []string{"读取", "无自定义头", "无内容类型"} {
			t.Run(route.path+"/"+mode, func(t *testing.T) {
				service := &fakeService{}
				method, want := route.method, http.StatusForbidden
				if mode == "读取" {
					method = http.MethodHead
					want = http.StatusMethodNotAllowed
					if route.path == "/api/remote/tasks" {
						want = http.StatusOK
					}
				}
				req := request(method, route.path, "{}")
				if mode != "无内容类型" {
					req.Header.Del("X-XLP-Request")
				}
				if mode != "无自定义头" {
					req.Header.Del("Content-Type")
				}
				response := httptest.NewRecorder()
				protected(service).ServeHTTP(response, req)
				if response.Code != want || service.calls != 0 {
					t.Fatalf("写入边界不正确：状态=%d，预期=%d，写入次数=%d", response.Code, want, service.calls)
				}
			})
		}
	}
}
