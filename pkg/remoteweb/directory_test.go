package remoteweb

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/cnk3x/xunlei/pkg/remote"
)

type directoryService struct {
	fakeService
	directory string
}

func (s *directoryService) Folders(_ context.Context, target, directory string) ([]remote.Folder, error) {
	s.target, s.directory = target, directory
	return []remote.Folder{{ID: "所选编号", Name: "电影", Path: directory}}, s.err
}

func (s *directoryService) Configure(_ context.Context, settings remote.Settings) error {
	s.target, s.directory = settings.Target, settings.Directory
	return s.err
}

func (s *directoryService) Create(_ context.Context, req remote.CreateRequest) (remote.CreateResult, error) {
	s.target, s.directory = req.Target, req.Directory
	return remote.CreateResult{ID: "任务编号"}, s.err
}

func Test目录接口保留路径并返回歧义错误(t *testing.T) {
	for _, ambiguous := range []bool{false, true} {
		for _, route := range []struct{ method, path string }{{"GET", "/folders"}, {"PUT", "/settings"}, {"POST", "/tasks"}} {
			name := "成功"
			if ambiguous {
				name = "目录歧义"
			}
			t.Run(name+route.path, func(t *testing.T) {
				directory := "/downloads/ 电影\t\u3000 "
				s := &directoryService{}
				if ambiguous {
					s.err = &remote.Error{Kind: "directory_required", Message: "所选路径对应多个目录编号"}
				}
				path, body := "/api/remote"+route.path, ""
				if route.method == "GET" {
					path += "?" + url.Values{"target": {"设备一"}, "path": {directory}}.Encode()
				} else {
					encoded, err := json.Marshal(remote.Settings{Target: "设备一", Directory: directory})
					if err != nil {
						t.Fatal(err)
					}
					body = string(encoded)
				}
				w := httptest.NewRecorder()
				protected(s).ServeHTTP(w, request(route.method, path, body))
				if s.target != "设备一" || s.directory != directory {
					t.Fatalf("接口修改了所选对象：设备=%q，路径=%q", s.target, s.directory)
				}
				if ambiguous {
					var detail remote.Error
					if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil || w.Code != 400 || detail.Kind != "directory_required" || detail.Message != s.err.Error() {
						t.Fatalf("目录歧义响应不正确：%d，%s", w.Code, w.Body.String())
					}
				} else if w.Code != 200 {
					t.Fatalf("合法目录未被接受：%d，%s", w.Code, w.Body.String())
				} else if route.method == "GET" {
					var folders []remote.Folder
					if err := json.Unmarshal(w.Body.Bytes(), &folders); err != nil || len(folders) != 1 || folders[0].ID != "所选编号" || folders[0].Path != directory {
						t.Fatalf("目录响应修改了所选对象：%s", w.Body.String())
					}
				}
			})
		}
	}
}
