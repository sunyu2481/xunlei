package remote

import (
	"bytes"
	"context"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

type directoryRequests struct {
	parents   []string
	resources int
	created   []object
}

func directoryEntry(id, name, realPath string) object {
	f := object{"id": id, "name": name, "kind": "drive#folder"}
	if realPath != "" {
		f["params"] = object{"RealPath": realPath}
	}
	return f
}

func mockDirectories(t *testing.T, pages map[string][][]object) (*Client, *directoryRequests) {
	t.Helper()
	calls := &directoryRequests{}
	c := mockClient(t, driveFixture(t, func(w http.ResponseWriter, r *http.Request) bool {
		switch r.URL.Path {
		case "/device/drive/v1/files":
			parent, token := r.URL.Query().Get("parent_id"), r.URL.Query().Get("page_token")
			calls.parents = append(calls.parents, parent)
			page := 0
			if token != "" {
				var err error
				page, err = strconv.Atoi(token)
				if err != nil {
					t.Fatalf("意外的目录分页标记：%q", token)
				}
			}
			list := pages[parent]
			if page >= len(list) {
				respond(w, object{"files": []object{}})
				return true
			}
			d := object{"files": list[page]}
			if page+1 < len(list) {
				d["next_page_token"] = strconv.Itoa(page + 1)
			}
			respond(w, d)
			return true
		case "/drive/v1/tasks":
			if r.URL.Query().Get("type") == "user#runner" {
				respond(w, object{"tasks": []object{{"name": "测试设备", "params": object{"target": "设备一"}}}})
				return true
			}
		case "/drive/v1/resource/list":
			calls.resources++
		case "/drive/v1/task":
			calls.created = append(calls.created, bodyOf(t, r))
			respond(w, object{"task": object{"id": "新任务"}})
			return true
		}
		return false
	}))
	return c, calls
}

func Test目录保留合法空白与选择对象(t *testing.T) {
	for _, suffix := range []string{" ", "\u3000", "\u00a0", "\t", "\n"} {
		for _, neighbor := range []bool{false, true} {
			t.Run(strconv.Quote(suffix)+"/相邻目录="+strconv.FormatBool(neighbor), func(t *testing.T) {
				directory := "/downloads/电影" + suffix
				entries := []object{directoryEntry("所选编号", "电影"+suffix, directory)}
				if neighbor {
					entries = append(entries, directoryEntry("相邻编号", "电影", "/downloads/电影"))
				}
				c, calls := mockDirectories(t, map[string][][]object{
					"":     {{directoryEntry("根编号", "downloads", "/downloads")}},
					"根编号":  {entries},
					"所选编号": {{directoryEntry("子编号", "子目录", directory+"/子目录")}},
					"相邻编号": {{directoryEntry("错误子编号", "其他目录", "/downloads/电影/其他目录")}},
				})
				ctx := context.Background()
				listed, err := c.Folders(ctx, "设备一", "/downloads")
				if err != nil {
					t.Fatal(err)
				}
				var selected Folder
				for _, f := range listed {
					if f.ID == "所选编号" {
						selected = f
					}
				}
				if selected.Path != directory || selected.Name != "电影"+suffix {
					t.Fatalf("列表没有保留目录字符：%+v", selected)
				}
				children, err := c.Folders(ctx, "设备一", selected.Path)
				if err != nil || len(children) != 1 || children[0].ID != "子编号" {
					t.Errorf("浏览未使用所选目录对象：%+v，%v", children, err)
				}
				if err = c.Configure(ctx, Settings{Target: "设备一", Directory: selected.Path}); err != nil {
					t.Errorf("保存所选目录失败：%v", err)
				} else if c.Status().Directory != directory {
					t.Errorf("默认目录被改为 %q", c.Status().Directory)
				}
				req := sampleRequest()
				req.Directory = selected.Path
				result, err := c.Create(ctx, req)
				if err != nil || result.ID != "新任务" || len(calls.created) != 1 {
					t.Fatalf("创建任务失败：%+v，%v，请求数=%d", result, err, len(calls.created))
				}
				params := obj(calls.created[0]["params"])
				if params["parent_folder_id"] != selected.ID || params["parent_folder_path"] != directory+"/" {
					t.Errorf("创建任务使用了其他目录：%v", params)
				}
				saved, err := readState(c.path)
				if err != nil || saved.Directory != directory || saved.Submissions[req.RequestID].Directory != directory {
					t.Errorf("落盘未保留所选目录：默认=%q，提交=%q，错误=%v", saved.Directory, saved.Submissions[req.RequestID].Directory, err)
				}
				result, err = c.Create(ctx, req)
				if err != nil || !result.Reused || len(calls.created) != 1 {
					t.Fatalf("同一目录重试未复用已创建任务：%+v，%v", result, err)
				}
				req.Directory = "/downloads/电影"
				_, err = c.Create(ctx, req)
				wantKind(t, err, "invalid_request")
			})
		}
	}
}

func Test目录拒绝路径外空白(t *testing.T) {
	for _, directory := range []string{" ", "\u3000", "\t\n", " /downloads", "\u3000/downloads"} {
		t.Run(strconv.Quote(directory), func(t *testing.T) {
			_, err := cleanDirectory(directory)
			wantKind(t, err, "invalid_request")
		})
	}
}

func Test目录拒绝同路径多编号(t *testing.T) {
	for _, level := range []string{"根目录", "子目录"} {
		for _, source := range []string{"真实路径", "名称拼接", "规范化路径", "跨页", "反序", "祖先路径"} {
			t.Run(level+"/"+source, func(t *testing.T) {
				directory, parent := "/downloads/电影", ""
				pages := map[string][][]object{}
				if level == "子目录" {
					parent = "根编号"
					pages[""] = [][]object{{directoryEntry(parent, "downloads", "/downloads")}}
				}
				entries := []object{directoryEntry("编号甲", "电影", directory), directoryEntry("编号乙", "电影", directory)}
				if source == "名称拼接" {
					delete(entries[0], "params")
					delete(entries[1], "params")
					if level == "根目录" {
						directory = "/电影"
					}
				}
				if source == "反序" {
					entries[0], entries[1] = entries[1], entries[0]
				}
				if source == "规范化路径" {
					entries[1]["params"] = object{"RealPath": "//downloads/./电影/"}
				}
				pages[parent] = [][]object{entries}
				if source == "跨页" {
					pages[parent] = [][]object{entries[:1], entries[1:]}
				}
				if source == "祖先路径" {
					directory += "/子目录"
					for _, id := range []string{"编号甲", "编号乙"} {
						pages[id] = [][]object{{directoryEntry(id+"的子编号", "子目录", directory)}}
					}
				}
				c, calls := mockDirectories(t, pages)
				c.update(func(s *state) {
					s.Directory = "/原目录"
					s.Submissions = map[string]submission{"已有记录": {TaskID: "已有任务"}}
				})
				if err := c.save(); err != nil {
					t.Fatal(err)
				}
				before, disk := copyState(c.snapshot()), stateBytes(t, c.path)
				ctx := context.Background()
				for _, operation := range []string{"浏览", "设置", "创建"} {
					t.Run(operation, func(t *testing.T) {
						var err error
						switch operation {
						case "浏览":
							_, err = c.Folders(ctx, "设备一", directory)
						case "设置":
							err = c.Configure(ctx, Settings{Target: "设备一", Directory: directory})
						case "创建":
							req := sampleRequest()
							req.Directory = directory
							_, err = c.Create(ctx, req)
						}
						if calls.resources != 0 || len(calls.created) != 0 || !reflect.DeepEqual(c.snapshot(), before) || !bytes.Equal(stateBytes(t, c.path), disk) {
							t.Error("歧义目录触发了资源解析、任务创建或状态修改")
						}
						for _, id := range calls.parents {
							if strings.HasPrefix(id, "编号") {
								t.Errorf("不应继续浏览歧义目录编号：%q", id)
							}
						}
						wantKind(t, err, "directory_required")
						if !strings.Contains(err.Error(), "多个目录编号") {
							t.Errorf("错误未解释目录歧义：%v", err)
						}
					})
				}
			})
		}
	}
}

func Test目录选择唯一最长路径(t *testing.T) {
	for _, tc := range []struct {
		name      string
		directory string
		pages     map[string][][]object
		want      Folder
	}{
		{"同名不同路径", "/盘乙/电影", map[string][][]object{"": {{directoryEntry("甲", "电影", "/盘甲/电影"), directoryEntry("乙", "电影", "/盘乙/电影")}}}, Folder{ID: "乙", Name: "电影", Path: "/盘乙/电影"}},
		{"同编号跨页重复", "/电影", map[string][][]object{"": {{directoryEntry("甲", "电影", "/电影")}, {directoryEntry("甲", "电影", "/电影")}}}, Folder{ID: "甲", Name: "电影", Path: "/电影"}},
		{"唯一更长路径在前", "/电影/子目录", map[string][][]object{"": {{directoryEntry("甲", "上级甲", "/电影"), directoryEntry("乙", "上级乙", "/电影"), directoryEntry("子", "一号目录", "/电影/子目录")}}}, Folder{ID: "子", Name: "一号目录", Path: "/电影/子目录"}},
		{"唯一更长路径在后", "/电影/子目录", map[string][][]object{"": {{directoryEntry("甲", "上级甲", "/电影"), directoryEntry("乙", "上级乙", "/电影"), directoryEntry("子", "末级", "/电影/子目录")}}}, Folder{ID: "子", Name: "末级", Path: "/电影/子目录"}},
		{"无关歧义路径", "/电影", map[string][][]object{"": {{directoryEntry("甲", "甲", "/其他"), directoryEntry("乙", "乙", "/其他"), directoryEntry("丙", "电影", "/电影")}}}, Folder{ID: "丙", Name: "电影", Path: "/电影"}},
		{"空值选择唯一根目录", "", map[string][][]object{"": {{directoryEntry("甲", "电影 ", "/电影 ")}}}, Folder{ID: "甲", Name: "电影 ", Path: "/电影 "}},
		{"目录边界及路径整理", "//电影二/./子目录/", map[string][][]object{"": {{directoryEntry("甲", "甲", "/电影"), directoryEntry("乙", "乙", "/电影二")}}, "乙": {{directoryEntry("子", "子目录", "")}}}, Folder{ID: "子", Name: "子目录", Path: "/电影二/子目录"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := mockDirectories(t, tc.pages)
			got, err := c.resolve(context.Background(), "设备一", tc.directory)
			if err != nil || got != tc.want {
				t.Fatalf("未选中唯一目录：%+v，%v", got, err)
			}
		})
	}
}
