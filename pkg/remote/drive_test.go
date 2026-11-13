package remote

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func driveFixture(t *testing.T, extra func(http.ResponseWriter, *http.Request) bool) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if extra != nil && extra(w, r) {
			return
		}
		switch r.URL.Path {
		case "/drive/v1/apps/INNER_API":
			respond(w, object{"links": []any{object{"url": "https://relay.xunlei.com/device/?plugin_app_id=app&plugin_app_token=plugin-token"}}})
		case "/device/drive/v1/files":
			q := r.URL.Query()
			if r.URL.Host != "relay.xunlei.com" || q.Get("plugin_app_token") != "plugin-token" || q.Get("space") != "设备一" {
				t.Fatal("目录请求未使用设备专属接口")
			}
			if q.Get("filters") != `{"kind":{"eq":"drive#folder"}}` {
				t.Fatal("目录过滤不应丢弃缺少任务状态字段的目录")
			}
			if q.Get("parent_id") == "" {
				respond(w, object{"files": []any{object{"id": "root-id", "name": "downloads", "kind": "drive#folder", "params": object{"RealPath": "/downloads"}}}})
			} else {
				respond(w, object{"files": []any{object{"id": "movie-id", "name": "电影", "kind": "drive#folder", "params": object{"RealPath": "/downloads/电影"}}}})
			}
		case "/drive/v1/resource/list":
			respond(w, object{"list": object{"resources": []any{object{"name": "测试文件", "file_size": "100", "file_count": "1", "file_id": "resource-id"}}}})
		case "/drive/v1/task":
			respond(w, object{"task": object{"id": "task-id"}})
		case "/drive/v1/tasks":
			respond(w, object{"tasks": []any{}})
		default:
			t.Errorf("意外路径：%s", r.URL.Path)
			w.WriteHeader(500)
		}
	}
}
func sampleRequest() CreateRequest {
	return CreateRequest{RequestID: "0123456789abcdef", Target: "设备一", Directory: "/downloads/电影", URL: "https://example.com/file"}
}

func TestCreateUsesResolvedDirectoryAndDeduplicates(t *testing.T) {
	posted := 0
	c := mockClient(t, driveFixture(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/drive/v1/task" {
			return false
		}
		posted++
		d := bodyOf(t, r)
		p := obj(d["params"])
		if r.URL.Host != "api-pan.xunlei.com" || p["parent_folder_id"] != "movie-id" || p["parent_folder_path"] != "/downloads/电影/" || d["space"] != "设备一" || p["target"] != "设备一" {
			t.Fatalf("创建任务的目标或目录不正确：%v", d)
		}
		respond(w, object{"task": object{"id": 123}})
		return true
	}))
	req := sampleRequest()
	result, err := c.Create(context.Background(), req)
	if err != nil || result.ID != "123" {
		t.Fatalf("创建失败：%v %v", result, err)
	}
	result, err = c.Create(context.Background(), req)
	if err != nil || !result.Reused || posted != 1 {
		t.Fatalf("重复请求再次创建了任务：%v", err)
	}
	req.URL = "https://example.com/changed"
	_, err = c.Create(context.Background(), req)
	wantKind(t, err, "invalid_request")
	if posted != 1 {
		t.Fatal("变更内容不应复用请求编号")
	}
}

func TestCreateNeverUsesUnknownDirectoryOrAnotherDevice(t *testing.T) {
	posted := false
	c := mockClient(t, driveFixture(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/drive/v1/task" {
			posted = true
		}
		return false
	}))
	req := sampleRequest()
	req.Directory = "/downloads-other"
	_, err := c.Create(context.Background(), req)
	wantKind(t, err, "directory_required")
	req.Target = "其他设备"
	_, err = c.Create(context.Background(), req)
	wantKind(t, err, "settings_changed")
	if posted {
		t.Fatal("无效目录或目标不能创建任务")
	}
}

func TestUnknownSubmissionSurvivesRestartWithoutResend(t *testing.T) {
	posted := 0
	found := false
	historical := true
	handler := driveFixture(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/drive/v1/tasks" {
			return false
		}
		list := []any{}
		if found {
			created := time.Now()
			if historical {
				created = created.Add(-time.Hour)
			}
			list = append(list, object{"id": "confirmed", "type": "user#download-url", "space": "设备一", "created_time": created.Format(time.RFC3339), "params": object{"url": "https://example.com/file", "parent_folder_path": "/downloads/电影"}})
		}
		respond(w, object{"tasks": list})
		return true
	})
	c := mockClient(t, handler)
	transport := c.http.Transport
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == "POST" && r.URL.Path == "/drive/v1/task" {
			posted++
			return nil, io.ErrUnexpectedEOF
		}
		return transport.RoundTrip(r)
	})
	req := sampleRequest()
	_, err := c.Create(context.Background(), req)
	wantKind(t, err, "submission_unknown")
	restored, err := New(c.path)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	restored.http.Transport = c.http.Transport
	_, err = restored.Create(context.Background(), req)
	wantKind(t, err, "submission_unknown")
	found = true
	_, err = restored.Create(context.Background(), req)
	wantKind(t, err, "submission_unknown")
	historical = false
	result, err := restored.Create(context.Background(), req)
	if err != nil || result.ID != "confirmed" || !result.Reused {
		t.Fatalf("核对提交失败：%v %v", result, err)
	}
	if posted != 1 {
		t.Fatalf("结果不确定时重复提交了 %d 次", posted)
	}
}

func TestKnownRejectionAllowsRetry(t *testing.T) {
	posted := 0
	c := mockClient(t, driveFixture(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/drive/v1/task" {
			return false
		}
		posted++
		if posted == 1 {
			respond(w, object{"error_code": 100, "error": "rejected"})
		} else {
			respond(w, object{"task": object{"id": "task-id"}})
		}
		return true
	}))
	_, err := c.Create(context.Background(), sampleRequest())
	wantKind(t, err, "upstream")
	if _, err = c.Create(context.Background(), sampleRequest()); err != nil {
		t.Fatal(err)
	}
	if posted != 2 {
		t.Fatal("明确拒绝后应允许用户重试")
	}
}

func TestTaskActionsUseActualTaskTypeAndSpace(t *testing.T) {
	patched := false
	deleted := false
	c := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/drive/v1/tasks" && r.Method == "GET" {
			if r.URL.Query().Get("space") != "设备一" {
				t.Fatal("跨设备查找了任务")
			}
			if r.URL.Query().Get("page_token") == "" {
				respond(w, object{"tasks": []any{}, "next_page_token": "下一页"})
				return
			}
			respond(w, object{"tasks": []any{object{"id": "123", "type": "user#download", "space": "设备一", "phase": "PHASE_TYPE_STOPPED"}}})
			return
		}
		if r.Method == "PATCH" {
			d := bodyOf(t, r)
			if d["type"] != "user#download" || d["space"] != "设备一" || obj(d["set_params"])["spec"] != `{"phase":"running"}` {
				t.Fatal("恢复任务参数不正确")
			}
			patched = true
			respond(w, object{})
			return
		}
		if r.Method == "DELETE" {
			deleted = true
			if r.URL.Query().Get("task_ids") != "123" {
				t.Fatal("删除参数不正确")
			}
			respond(w, object{})
			return
		}
		t.Fatal("不应调用其他接口")
	})
	if err := c.TaskAction(context.Background(), "设备一", "123", "resume"); err != nil {
		t.Fatal(err)
	}
	if err := c.TaskAction(context.Background(), "设备一", "123", "delete"); err != nil {
		t.Fatal(err)
	}
	if !patched || !deleted {
		t.Fatal("未执行任务操作")
	}
	if err := c.TaskAction(context.Background(), "设备一", "不存在", "pause"); err == nil {
		t.Fatal("不存在的任务不能操作")
	}
}

func TestDirectoryPaginationAndRelayRefresh(t *testing.T) {
	discoveries := 0
	calls := 0
	c := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/drive/v1/apps/INNER_API" {
			discoveries++
			respond(w, object{"link": "https://relay.xunlei.com/prefix?plugin_app_token=test"})
			return
		}
		if r.URL.Path != "/prefix/drive/v1/files" {
			t.Fatalf("中继路径丢失：%s", r.URL.Path)
		}
		calls++
		if calls == 1 {
			respond(w, object{"error_code": 404, "error": "expired_plugin"})
			return
		}
		if r.URL.Query().Get("page_token") == "" {
			respond(w, object{"files": []any{object{"id": "one", "name": "目录一", "kind": "drive#folder"}}, "next_page_token": "second"})
			return
		}
		respond(w, object{"files": []any{object{"id": "two", "name": "目录二", "kind": "drive#folder"}, object{"id": "file", "name": "文件", "kind": "drive#file"}}})
	})
	folders, err := c.Folders(context.Background(), "设备一", "")
	if err != nil || len(folders) != 2 || discoveries != 2 {
		t.Fatalf("目录分页或中继续期失败：%v %v %d", folders, err, discoveries)
	}
}

func TestTaskCreationDoesNotRetryDeviceTimeout(t *testing.T) {
	posted := 0
	c := mockClient(t, driveFixture(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != "POST" || r.URL.Path != "/drive/v1/task" {
			return false
		}
		posted++
		respond(w, object{"error_code": 2, "error": "external_unknown_err"})
		return true
	}))
	_, err := c.Create(context.Background(), sampleRequest())
	wantKind(t, err, "submission_unknown")
	if posted != 1 {
		t.Fatal("写请求不应因设备超时自动重试")
	}
}

func TestExplicitTargetIsRequiredAndDirectoryTraversalRejected(t *testing.T) {
	c := mockClient(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("输入错误时不应访问云端") })
	c.update(func(s *state) { s.Target = "" })
	_, err := c.Tasks(context.Background(), "", "")
	wantKind(t, err, "device_required")
	for _, p := range []string{"relative", "/downloads/../etc", "/downloads\\etc"} {
		if _, err = cleanDirectory(p); err == nil {
			t.Fatal("目录校验未生效")
		}
	}
	if _, err = NormalizeLink("magnet:?xt=urn:btih:" + strings.Repeat("g", 40)); err == nil {
		t.Fatal("无效资源标识通过")
	}
}
