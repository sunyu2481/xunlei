package remote

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testMagnet = "magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func resourceLeaf(id, parent, name string, index any, size any) object {
	r := object{"id": id, "parent_id": parent, "name": name, "is_dir": false, "file_size": size}
	if index != nil {
		r["file_index"] = index
	}
	return r
}
func resourceDir(id, parent string, count int, children ...any) object {
	return object{"id": id, "parent_id": parent, "name": id, "is_dir": true, "file_count": count, "dir": object{"resources": children}}
}
func testResource() object {
	return resourceDir("资料包", "", 4,
		resourceLeaf("零", "资料包", "影片.mp4", 0, "100"),
		resourceDir("子目录", "资料包", 2, resourceLeaf("二", "子目录", "同名.mkv", 2, "200"), resourceLeaf("三", "子目录", "同名.mkv", 3, "300")),
		resourceDir("空目录", "资料包", 0), resourceLeaf("五", "资料包", "说明.txt", 5, "400"))
}
func resourceClient(t *testing.T, r object, extra func(http.ResponseWriter, *http.Request) bool) *Client {
	t.Helper()
	return mockClient(t, driveFixture(t, func(w http.ResponseWriter, req *http.Request) bool {
		if extra != nil && extra(w, req) {
			return true
		}
		if req.URL.Path == "/drive/v1/resource/list" {
			respond(w, object{"list_id": "列表", "list": object{"resources": []any{r}}})
			return true
		}
		return false
	}))
}
func selectedRequest(p ResourcePreview, indices ...int64) CreateRequest {
	r := sampleRequest()
	r.URL = p.URL
	r.PreviewID = p.PreviewID
	r.ResourceVersion = p.ResourceVersion
	r.SelectionMode = p.Kind
	r.FileIndices = indices
	return r
}

func Test解析完整树及显式选择创建(t *testing.T) {
	posted := 0
	var c *Client
	c = resourceClient(t, testResource(), func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/drive/v1/task" {
			return false
		}
		posted++
		d := bodyOf(t, r)
		p := obj(d["params"])
		if d["file_size"] != "600" || p["sub_file_index"] != "0,2-3" || p["total_file_count"] != "4" || p["parent_folder_path"] != "/downloads/电影/" || p["parent_folder_id"] != "movie-id" {
			t.Fatalf("参数错误：%v", d)
		}
		s, err := readState(c.path)
		if err != nil || s.Submissions[sampleRequest().RequestID].Version != 2 || !reflect.DeepEqual(s.Submissions[sampleRequest().RequestID].FileIndices, []int64{0, 2, 3}) {
			t.Fatal("发送前没有保存选择记录")
		}
		respond(w, object{"task": object{"id": "已选择"}})
		return true
	})
	p, err := c.ResolveResource(context.Background(), ResolveRequest{Target: "设备一", URL: testMagnet})
	if err != nil || !p.Complete || p.TotalFileCount != 4 || p.TotalSize != "1000" || len(p.Files) != 7 {
		t.Fatalf("预览错误：%+v %v", p, err)
	}
	if posted != 0 || len(c.snapshot().Submissions) != 0 {
		t.Fatal("解析有创建副作用")
	}
	p.Files[0].Name = "页面修改副本"
	req := selectedRequest(p, 3, 0, 2, 2)
	result, err := c.Create(context.Background(), req)
	if err != nil || result.ID != "已选择" {
		t.Fatalf("创建失败：%v", err)
	}
	req.FileIndices = []int64{2, 3, 0}
	req.PreviewID = "已过期或已丢失"
	result, err = c.Create(context.Background(), req)
	if err != nil || !result.Reused || posted != 1 {
		t.Fatalf("同集合没有复用：%v", err)
	}
	req.FileIndices = []int64{0}
	_, err = c.Create(context.Background(), req)
	wantKind(t, err, "invalid_request")
	if posted != 1 {
		t.Fatal("更改选择重复创建")
	}
}

func Test资源异常不发放预览(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(object)
		kind   string
	}{
		{"缺失索引", func(r object) { delete(obj(items(obj(r["dir"])["resources"])[0]), "file_index") }, "resource_incomplete"},
		{"小数索引", func(r object) { obj(items(obj(r["dir"])["resources"])[0])["file_index"] = "0.1" }, "resource_incomplete"},
		{"超大索引", func(r object) { obj(items(obj(r["dir"])["resources"])[0])["file_index"] = "9007199254740992" }, "resource_incomplete"},
		{"负索引", func(r object) { obj(items(obj(r["dir"])["resources"])[0])["file_index"] = -1 }, "resource_incomplete"},
		{"负大小", func(r object) { obj(items(obj(r["dir"])["resources"])[0])["file_size"] = -1 }, "resource_incomplete"},
		{"大小溢出", func(r object) {
			obj(items(obj(r["dir"])["resources"])[0])["file_size"] = strconv.FormatInt(math.MaxInt64, 10)
		}, "resource_incomplete"},
		{"索引冲突", func(r object) { obj(items(obj(r["dir"])["resources"])[3])["file_index"] = 0 }, "resource_incomplete"},
		{"总数不符", func(r object) { r["file_count"] = 5 }, "resource_incomplete"},
		{"非法分页令牌", func(r object) { obj(r["dir"])["next_page_token"] = []any{"页"} }, "resource_incomplete"},
		{"资源错误", func(r object) { r["meta"] = object{"error": "上游私人内容"} }, "resource_unavailable"},
		{"节点冲突", func(r object) { children := items(obj(r["dir"])["resources"]); obj(children[3])["id"] = "零" }, "resource_incomplete"},
		{"父编号错误", func(r object) { obj(items(obj(r["dir"])["resources"])[0])["parent_id"] = "其他目录" }, "resource_incomplete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := testResource()
			tc.mutate(r)
			posts := 0
			c := resourceClient(t, r, func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path == "/drive/v1/task" {
					posts++
				}
				return false
			})
			_, err := c.ResolveResource(context.Background(), ResolveRequest{Target: "设备一", URL: testMagnet})
			wantKind(t, err, tc.kind)
			if len(c.previews) != 0 || len(c.snapshot().Submissions) != 0 || posts != 0 {
				t.Fatal("异常解析发布预览或创建任务")
			}
		})
	}
}

func Test目录分页补齐去重及循环(t *testing.T) {
	for _, mode := range []string{"完整", "自循环", "长循环", "错误", "新子目录"} {
		t.Run(mode, func(t *testing.T) {
			root := resourceDir("根", "", 2, resourceLeaf("甲", "根", "甲.mp4", 0, 10))
			obj(root["dir"])["next_page_token"] = "页一 +/"
			pages := 0
			c := resourceClient(t, root, func(w http.ResponseWriter, r *http.Request) bool {
				if !strings.HasPrefix(r.URL.Path, "/drive/v1/resource/list/") {
					return false
				}
				pages++
				if r.URL.Query().Get("page_token") == "" {
					t.Fatal("没有分页令牌")
				}
				if mode == "错误" {
					respond(w, object{"error_code": 100})
					return true
				}
				children := []any{resourceLeaf("甲", "根", "甲.mp4", 0, 10), resourceLeaf("乙", "根", "乙.txt", 8, 20)}
				next := ""
				if mode == "自循环" {
					next = "页一 +/"
				}
				if mode == "长循环" {
					if pages == 1 {
						next = "页二"
					} else {
						next = "页一 +/"
					}
				}
				if mode == "新子目录" {
					if pages == 1 {
						dir := resourceDir("后续目录", "根", 1)
						obj(dir["dir"])["next_page_token"] = "子页"
						children = []any{dir}
					} else {
						children = []any{resourceLeaf("乙", "后续目录", "乙.txt", 8, 20)}
					}
				}
				respond(w, object{"list": object{"resources": children, "next_page_token": next}})
				return true
			})
			p, err := c.ResolveResource(context.Background(), ResolveRequest{Target: "设备一", URL: testMagnet})
			if mode == "完整" || mode == "新子目录" {
				if err != nil || p.TotalFileCount != 2 || p.TotalSize != "30" {
					t.Fatalf("分页错误：%+v %v", p, err)
				}
			} else if err == nil || len(c.previews) != 0 {
				t.Fatal("异常分页不应成功")
			}
			if pages > 2 {
				t.Fatal("分页循环未终止")
			}
		})
	}
}

func Test内容版本与临时编号及顺序无关(t *testing.T) {
	var version string
	for _, id := range []string{"临时甲", "临时乙"} {
		r := resourceDir(id, "", 2, resourceLeaf(id+"一", id, "包含/斜线\n中文", 0, "9007199254740993"), resourceLeaf(id+"二", id, "文件", maxFileIndex, 0))
		r["name"] = "资源名"
		if id == "临时乙" {
			children := items(obj(r["dir"])["resources"])
			children[0], children[1] = children[1], children[0]
		}
		c := resourceClient(t, r, nil)
		p, err := c.ResolveResource(context.Background(), ResolveRequest{Target: "设备一", URL: testMagnet})
		if err != nil {
			t.Fatal(err)
		}
		if version != "" && p.ResourceVersion != version {
			t.Fatal("临时编号或顺序改变版本")
		}
		version = p.ResourceVersion
		if p.TotalSize != "9007199254740993" {
			t.Fatal("大整数精度损失")
		}
	}
}

func Test预览边界与首次提交校验(t *testing.T) {
	for _, mode := range []string{"过期", "缺失", "版本", "链接", "模式", "非法索引", "空选择", "目录期间过期", "发送前保存失败"} {
		t.Run(mode, func(t *testing.T) {
			posts := 0
			var c *Client
			expire := false
			c = resourceClient(t, testResource(), func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path == "/drive/v1/task" {
					posts++
				}
				if expire && r.URL.Path == "/device/drive/v1/files" {
					for _, p := range c.previews {
						p.preview.ExpiresAt = time.Now()
					}
				}
				return false
			})
			p, err := c.ResolveResource(context.Background(), ResolveRequest{Target: "设备一", URL: testMagnet})
			if err != nil {
				t.Fatal(err)
			}
			req := selectedRequest(p, 0)
			kind := "preview_mismatch"
			switch mode {
			case "过期":
				c.previews[p.PreviewID].preview.ExpiresAt = time.Now()
				kind = "preview_expired"
			case "缺失":
				c.clearPreviews()
				kind = "preview_expired"
			case "版本":
				req.ResourceVersion = "其他版本"
			case "链接":
				req.URL = "magnet:?xt=urn:btih:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			case "模式":
				req.SelectionMode = "single"
				req.FileIndices = nil
			case "非法索引":
				req.FileIndices = []int64{1}
				kind = "invalid_selection"
			case "空选择":
				req.FileIndices = nil
				kind = "invalid_selection"
			case "目录期间过期":
				expire = true
				kind = "preview_expired"
			case "发送前保存失败":
				restore := blockStateSave(t, c)
				defer restore()
				kind = ""
			}
			_, err = c.Create(context.Background(), req)
			if kind != "" {
				wantKind(t, err, kind)
			} else if err == nil {
				t.Fatal("保存失败仍成功")
			}
			if posts != 0 || len(c.snapshot().Submissions) != 0 {
				t.Fatal("无效首次提交创建任务或写入记录")
			}
		})
	}
}

func Test预览取消容量及会话失效(t *testing.T) {
	calls := 0
	c := resourceClient(t, testResource(), func(w http.ResponseWriter, r *http.Request) bool { calls++; return false })
	c.gate <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.ResolveResource(ctx, ResolveRequest{Target: "设备一", URL: testMagnet})
	c.unlock()
	if !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatal("等待锁取消仍访问上游")
	}
	p, err := c.ResolveResource(context.Background(), ResolveRequest{Target: "设备一", URL: testMagnet})
	if err != nil {
		t.Fatal(err)
	}
	copy, err := c.ResolveResource(context.Background(), ResolveRequest{Target: "设备一", URL: testMagnet})
	if err != nil || copy.PreviewID != p.PreviewID || calls != 1 {
		t.Fatal("缓存未复用")
	}
	c.update(func(s *state) { s.AccessToken = "续期令牌" })
	if len(c.previews) != 1 {
		t.Fatal("普通续期清空预览")
	}
	for len(c.previews) < maxPreviews {
		q := *c.previews[p.PreviewID]
		q.preview.URL = "其他链接"
		c.previews[randomID()] = &q
	}
	_, err = c.ResolveResource(context.Background(), ResolveRequest{Target: "设备一", URL: "https://example.com/other"})
	wantKind(t, err, "rate_limited")
	c.update(func(s *state) { s.Target = "其他设备" })
	if len(c.previews) != 0 {
		t.Fatal("设备改变未清空预览")
	}
	c.update(func(s *state) { s.Target = "设备一" })
	p, err = c.ResolveResource(context.Background(), ResolveRequest{Target: "设备一", URL: testMagnet})
	if err != nil {
		t.Fatal(err)
	}
	c.update(func(s *state) { s.Username = "其他账号" })
	if len(c.previews) != 0 {
		t.Fatal("账号改变未清空预览")
	}
}

func Test单文件兼容及磁力缺索引拒绝(t *testing.T) {
	for _, link := range []string{testMagnet, "MAGNET:" + testMagnet[7:], "thunder://" + base64.StdEncoding.EncodeToString([]byte("AA"+testMagnet+"ZZ")), "https://example.com/file"} {
		c := resourceClient(t, resourceLeaf("单文件", "", "单文件", nil, 100), nil)
		p, err := c.ResolveResource(context.Background(), ResolveRequest{Target: "设备一", URL: link})
		if link != "https://example.com/file" {
			wantKind(t, err, "resource_incomplete")
		} else if err != nil || p.Kind != "single" {
			t.Fatalf("直链单文件错误：%v", err)
		}
	}
	c := resourceClient(t, testResource(), nil)
	req := sampleRequest()
	req.URL = testMagnet
	_, err := c.Create(context.Background(), req)
	wantKind(t, err, "selection_required")
	req.URL = "https://example.com/multi"
	_, err = c.Create(context.Background(), req)
	wantKind(t, err, "selection_required")
}

func Test未知选择提交重启只核对且不重发(t *testing.T) {
	posts := 0
	selection := ""
	extraMissing := false
	c := resourceClient(t, testResource(), nil)
	transport := c.http.Transport
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/drive/v1/task" {
			posts++
			return nil, io.ErrUnexpectedEOF
		}
		if r.URL.Path == "/drive/v1/tasks" {
			s := c.snapshot().Submissions["0123456789abcdef"]
			tasks := []any{object{"id": "候选", "type": "user#download-url", "space": "设备一", "created_time": s.At, "params": object{"url": testMagnet, "parent_folder_path": s.Directory, "sub_file_index": selection}}}
			if extraMissing {
				tasks = append(tasks, object{"id": "缺证据", "type": "user#download-url", "space": "设备一", "created_time": s.At, "params": object{"url": testMagnet, "parent_folder_path": s.Directory}})
			}
			b, _ := json.Marshal(object{"tasks": tasks})
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(b))), Header: http.Header{}}, nil
		}
		return transport.RoundTrip(r)
	})
	p, err := c.ResolveResource(context.Background(), ResolveRequest{Target: "设备一", URL: testMagnet})
	if err != nil {
		t.Fatal(err)
	}
	req := selectedRequest(p, 0, 2, 3)
	_, err = c.Create(context.Background(), req)
	wantKind(t, err, "submission_unknown")
	restored, err := New(c.path)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	restored.http.Transport = c.http.Transport
	for _, encoded := range []string{"", "0,2", "0-9007199254740991", "0,2-3"} {
		selection = encoded
		extraMissing = encoded == "0,2-3"
		result, err := restored.CheckSubmission(context.Background(), "设备一", req.RequestID)
		if err != nil || result.Status != "unknown" {
			t.Fatalf("缺证据或额外候选应未知：%+v %v", result, err)
		}
	}
	extraMissing = false
	result, err := restored.CheckSubmission(context.Background(), "设备一", req.RequestID)
	if err != nil || result.Status != "confirmed" || posts != 1 {
		t.Fatalf("核对失败：%+v %v", result, err)
	}
	req.PreviewID = "失效"
	reused, err := restored.Create(context.Background(), req)
	if err != nil || !reused.Reused || posts != 1 {
		t.Fatal("失去预览后未复用已知任务")
	}
	missing, err := restored.CheckSubmission(context.Background(), "设备一", "不存在的请求编号00000")
	if err != nil || missing.Status != "missing" || posts != 1 {
		t.Fatal("缺失记录创建了任务")
	}
}

func Test索引编码及状态深复制(t *testing.T) {
	values, err := normalizeSelection("files", []int64{5, 3, 2, 2, 0})
	if err != nil || encodeIndices(values) != "0,2-3,5" {
		t.Fatal("索引未规范化")
	}
	if selectionFingerprint("设备", "目录", "链接", "files", "版本", values) == selectionFingerprint("设备", "目录", "链接", "files", "版本", []int64{0}) {
		t.Fatal("不同集合指纹相同")
	}
	c := resourceClient(t, testResource(), nil)
	c.update(func(s *state) { s.Submissions = map[string]submission{"记录": {FileIndices: []int64{0, 2}}} })
	s := c.snapshot()
	v := s.Submissions["记录"]
	v.FileIndices[0] = 99
	s.Submissions["记录"] = v
	if !reflect.DeepEqual(c.snapshot().Submissions["记录"].FileIndices, []int64{0, 2}) {
		t.Fatal("快照没有深复制")
	}
}

func Test未知记录版本保留原字段且不创建(t *testing.T) {
	c := resourceClient(t, testResource(), nil)
	var future submission
	if err := json.Unmarshal([]byte(`{"version":99,"target":"设备一","at":1,"future_selection":{"未知字段":[1,2]}}`), &future); err != nil {
		t.Fatal(err)
	}
	id := "未来版本记录00000000"
	c.update(func(s *state) { s.Submissions = map[string]submission{id: future} })
	if err := c.save(); err != nil {
		t.Fatal(err)
	}
	_, err := c.CheckSubmission(context.Background(), "设备一", id)
	wantKind(t, err, "invalid_request")
	p, err := c.ResolveResource(context.Background(), ResolveRequest{Target: "设备一", URL: testMagnet})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Create(context.Background(), selectedRequest(p, 0))
	if err != nil {
		t.Fatal(err)
	}
	restored, err := New(c.path)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	data, err := json.Marshal(restored.snapshot().Submissions[id])
	if err != nil {
		t.Fatal(err)
	}
	var got object
	if err = json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if len(items(obj(got["future_selection"])["未知字段"])) != 2 {
		t.Fatal("旧程序保存或清理丢弃了未知版本字段")
	}
}
