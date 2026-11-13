package remoteweb

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cnk3x/xunlei/pkg/remote"
	"github.com/cnk3x/xunlei/pkg/web"
	"github.com/go-chi/chi/v5"
)

type fakeService struct {
	calls  int
	err    error
	target string
}

func (f *fakeService) Status() remote.Status {
	return remote.Status{LoggedIn: true, Username: "测试账号", Target: "设备一"}
}
func (f *fakeService) Login(context.Context, string, string, bool) error { f.calls++; return f.err }
func (f *fakeService) Logout(context.Context) error                      { f.calls++; return f.err }
func (f *fakeService) SendSMS(context.Context) error                     { f.calls++; return f.err }
func (f *fakeService) CheckSMS(context.Context, string) error            { f.calls++; return f.err }
func (f *fakeService) Devices(context.Context) ([]remote.Device, error) {
	return []remote.Device{{Target: "设备一", Name: "测试设备"}}, f.err
}
func (f *fakeService) Configure(context.Context, remote.Settings) error { f.calls++; return f.err }
func (f *fakeService) Folders(context.Context, string, string) ([]remote.Folder, error) {
	return []remote.Folder{}, f.err
}
func (f *fakeService) Tasks(context.Context, string, string) (remote.TaskPage, error) {
	return remote.TaskPage{Tasks: []remote.Task{}}, f.err
}
func (f *fakeService) Create(_ context.Context, r remote.CreateRequest) (remote.CreateResult, error) {
	f.calls++
	f.target = r.Target
	return remote.CreateResult{ID: "任务一"}, f.err
}
func (f *fakeService) TaskAction(_ context.Context, target, id, action string) error {
	f.calls++
	f.target = target
	return f.err
}

func request(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, "http://local.test"+path, strings.NewReader(body))
	r.SetBasicAuth("admin", "面板密码")
	if method != "GET" {
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-XLP-Request", "1")
	}
	return r
}
func protected(service Service) http.Handler {
	return web.BasicAuth("admin", "面板密码")(New(service))
}

func TestAuthenticationAndCrossOriginProtection(t *testing.T) {
	for _, tc := range []struct {
		name   string
		path   string
		method string
		modify func(*http.Request)
		want   int
	}{
		{"未认证页面", "/remote/", "GET", func(r *http.Request) { r.Header.Del("Authorization") }, 401},
		{"未认证接口", "/api/remote/status", "GET", func(r *http.Request) { r.Header.Del("Authorization") }, 401},
		{"跨站写入", "/api/remote/logout", "POST", func(r *http.Request) { r.Header.Set("Origin", "https://evil.test") }, 403},
		{"跨站请求标记", "/api/remote/logout", "POST", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, 403},
		{"缺少自定义头", "/api/remote/logout", "POST", func(r *http.Request) { r.Header.Del("X-XLP-Request") }, 403},
		{"表单请求", "/api/remote/logout", "POST", func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") }, 403},
		{"同源请求", "/api/remote/logout", "POST", func(r *http.Request) { r.Header.Set("Origin", "http://local.test") }, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &fakeService{}
			r := request(tc.method, tc.path, "{}")
			tc.modify(r)
			w := httptest.NewRecorder()
			protected(s).ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("响应状态：%d，内容：%s", w.Code, w.Body.String())
			}
			if tc.want != 200 && s.calls != 0 {
				t.Fatal("被拒绝的请求调用了服务")
			}
		})
	}
}

func TestRemoteRoutesMountedInExistingRouter(t *testing.T) {
	s := &fakeService{}
	mux := chi.NewRouter()
	handler := protected(s)
	mux.Mount("/remote", handler)
	mux.Mount("/api/remote", handler)
	for _, path := range []string{"/remote/view.html", "/remote/style.css", "/remote/app.js", "/remote/panel.js", "/remote/panel.css", "/api/remote/status", "/api/remote/devices", "/api/remote/folders?target=x", "/api/remote/tasks?target=x"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, request("GET", path, ""))
		if w.Code != 200 {
			t.Fatalf("路由 %s：%d %s", path, w.Code, w.Body.String())
		}
		if w.Header().Get("Content-Security-Policy") == "" {
			t.Fatal("缺少页面资源边界")
		}
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, request("POST", "/api/remote/tasks/123/pause", `{"target":"设备一"}`))
	if w.Code != 200 || s.target != "设备一" {
		t.Fatal("任务操作未路由到所选设备")
	}
}

func TestRemoteViewUsesExistingOptionalAuthentication(t *testing.T) {
	handler := web.BasicAuth("", "")(New(&fakeService{}))
	for _, path := range []string{"/remote/view.html", "/api/remote/status"} {
		r := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("未设置面板认证时不应要求新凭据：%d", w.Code)
		}
		if path == "/remote/view.html" && (!strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'self'") || strings.Contains(w.Body.String(), "<aside")) {
			t.Fatal("远程内容应允许在原面板中显示，且不应有第二个侧栏")
		}
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/remote/", nil))
	if w.Code != http.StatusTemporaryRedirect || w.Header().Get("Location") != PanelPath+"#/home?xlpRemote=1" {
		t.Fatal("旧入口应返回原面板页签")
	}
}

func TestRejectMalformedAndOversizedRequests(t *testing.T) {
	for _, body := range []string{`{"username":"测试","password":"密码","unexpected":true}`, `{} {}`, `{"username":"` + strings.Repeat("a", 33000) + `"}`} {
		s := &fakeService{}
		w := httptest.NewRecorder()
		protected(s).ServeHTTP(w, request("POST", "/api/remote/login", body))
		if w.Code != 400 || s.calls != 0 {
			t.Fatalf("不应接受无效请求：%d", w.Code)
		}
	}
}

func TestErrorResponsesAndCacheProtection(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{
		{&remote.Error{Kind: "review_required", Message: "需要短信验证"}, 422},
		{&remote.Error{Kind: "submission_unknown", Message: "提交结果待确认"}, 409},
		{context.DeadlineExceeded, 504},
		{errors.New("内部错误包含令牌 secret-value"), 502},
	} {
		w := httptest.NewRecorder()
		protected(&fakeService{err: tc.err}).ServeHTTP(w, request("POST", "/api/remote/logout", "{}"))
		if w.Code != tc.want || strings.Contains(w.Body.String(), "secret-value") {
			t.Fatalf("错误响应不正确：%d %s", w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("登录接口不能被缓存")
		}
	}
}
