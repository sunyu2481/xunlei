package remoteweb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cnk3x/xunlei/pkg/remote"
)

type resourceService struct {
	fakeService
	resolved, created, checked int
}

func (s *resourceService) ResolveResource(_ context.Context, r remote.ResolveRequest) (remote.ResourcePreview, error) {
	s.resolved++
	return remote.ResourcePreview{Target: r.Target, URL: r.URL, Complete: true}, s.err
}
func (s *resourceService) Create(_ context.Context, r remote.CreateRequest) (remote.CreateResult, error) {
	s.created++
	return remote.CreateResult{ID: "任务"}, s.err
}
func (s *resourceService) CheckSubmission(_ context.Context, target, id string) (remote.SubmissionResult, error) {
	s.checked++
	return remote.SubmissionResult{RequestID: id, Status: "missing"}, s.err
}

func Test资源与核对接口边界(t *testing.T) {
	for _, tc := range []struct {
		path, method, body, origin string
		want                       int
	}{
		{"/api/remote/resources/resolve", "POST", `{"target":"设备","url":"链接"}`, "", 200},
		{"/api/remote/resources/resolve", "POST", `{"target":"设备","url":"链接"}`, "https://evil.test", 403},
		{"/api/remote/resources/resolve", "POST", `{"url":"链接","unknown":1}`, "", 400},
		{"/api/remote/resources/resolve", "POST", `{"url":"` + strings.Repeat("a", 33<<10) + `"}`, "", 400},
		{"/api/remote/tasks", "POST", `{"file_indices":[1.5]}`, "", 400},
		{"/api/remote/tasks", "POST", `{"sub_file_index":"--1,"}`, "", 400},
		{"/api/remote/tasks", "POST", `{"url":"` + strings.Repeat("a", 40<<10) + `"}`, "", 200},
		{"/api/remote/tasks", "POST", `{"url":"` + strings.Repeat("a", 128<<10) + `"}`, "", 400},
		{"/api/remote/submissions/0123456789abcdef?target=设备", "GET", "", "", 200},
	} {
		s := &resourceService{}
		w := httptest.NewRecorder()
		r := request(tc.method, tc.path, tc.body)
		if tc.origin != "" {
			r.Header.Set("Origin", tc.origin)
		}
		New(s).ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("接口边界错误：%s %d %s", tc.path, w.Code, w.Body.String())
		}
		if strings.Contains(tc.path, "/submissions/") && (s.checked != 1 || s.created != 0 || s.resolved != 0) {
			t.Fatal("检查结果调用了创建或解析")
		}
		if strings.Contains(tc.path, "resources/resolve") && s.created != 0 {
			t.Fatal("解析调用创建")
		}
		if tc.want != 200 && s.created+s.checked+s.resolved != 0 {
			t.Fatal("无效请求调用服务")
		}
	}
	for _, kind := range []string{"selection_required", "invalid_selection", "resource_unavailable", "resource_incomplete", "resource_too_large", "preview_expired", "preview_mismatch", "rate_limited", "submission_unknown"} {
		s := &resourceService{fakeService: fakeService{err: &remote.Error{Kind: kind, Message: "稳定中文错误"}}}
		w := httptest.NewRecorder()
		New(s).ServeHTTP(w, request(http.MethodPost, "/api/remote/resources/resolve", `{"url":"链接"}`))
		want := 422
		switch kind {
		case "selection_required", "invalid_selection":
			want = 400
		case "preview_expired", "preview_mismatch", "submission_unknown":
			want = 409
		case "rate_limited":
			want = 429
		}
		if w.Code != want {
			t.Fatalf("错误映射 %s：%d", kind, w.Code)
		}
	}
}
