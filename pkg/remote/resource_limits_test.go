package remote

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func Test完整预览资源上限(t *testing.T) {
	for _, count := range []int{maxResourceFiles, maxResourceFiles + 1} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			files := make([]any, count)
			for i := range files {
				files[i] = resourceLeaf(strconv.Itoa(i), "根", "文件", i*3, 1)
			}
			c := resourceClient(t, resourceDir("根", "", count, files...), nil)
			p, err := c.ResolveResource(context.Background(), ResolveRequest{Target: "设备一", URL: testMagnet})
			if count == maxResourceFiles {
				if err != nil || p.TotalFileCount != count {
					t.Fatalf("边界资源应成功：%v", err)
				}
			} else {
				wantKind(t, err, "resource_too_large")
			}
		})
	}
	for _, count := range []int{maxResourceNodes, maxResourceNodes + 1} {
		t.Run("节点"+strconv.Itoa(count), func(t *testing.T) {
			files := []any{resourceLeaf("文件", "根", "文件", 0, 1)}
			for i := 2; i < count; i++ {
				files = append(files, resourceDir(strconv.Itoa(i), "根", 0))
			}
			c := resourceClient(t, resourceDir("根", "", 1, files...), nil)
			_, err := c.ResolveResource(context.Background(), ResolveRequest{Target: "设备一", URL: testMagnet})
			if count == maxResourceNodes {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				wantKind(t, err, "resource_too_large")
			}
		})
	}
}

func Test目录深度和解析请求上限(t *testing.T) {
	for _, depth := range []int{maxResourceDepth, maxResourceDepth + 1} {
		r := resourceLeaf("叶子", strconv.Itoa(depth-1), "文件", 0, 1)
		for i := depth - 1; i >= 1; i-- {
			parent := ""
			if i > 1 {
				parent = strconv.Itoa(i - 1)
			}
			r = resourceDir(strconv.Itoa(i), parent, 1, r)
		}
		c := resourceClient(t, r, nil)
		_, err := c.ResolveResource(context.Background(), ResolveRequest{Target: "设备一", URL: testMagnet})
		if depth == maxResourceDepth {
			if err != nil {
				t.Fatal(err)
			}
		} else {
			wantKind(t, err, "resource_too_large")
		}
	}
	for _, requests := range []int{maxResourceRequests, maxResourceRequests + 1} {
		r := resourceDir("根", "", 1)
		obj(r["dir"])["next_page_token"] = "1"
		pages := 0
		c := resourceClient(t, r, func(w http.ResponseWriter, r *http.Request) bool {
			if !strings.HasPrefix(r.URL.Path, "/drive/v1/resource/list/") {
				return false
			}
			pages++
			children := []any{}
			next := strconv.Itoa(pages + 1)
			if pages == requests-1 {
				children = append(children, resourceLeaf("叶子", "根", "文件", 0, 1))
				next = ""
			}
			respond(w, object{"list": object{"resources": children, "next_page_token": next}})
			return true
		})
		_, err := c.ResolveResource(context.Background(), ResolveRequest{Target: "设备一", URL: testMagnet})
		if requests == maxResourceRequests {
			if err != nil {
				t.Fatal(err)
			}
		} else {
			wantKind(t, err, "resource_too_large")
		}
		if pages+1 > maxResourceRequests {
			t.Fatal("超过请求上限")
		}
	}
}

func Test预览序列化及累计响应字节上限(t *testing.T) {
	c := resourceClient(t, resourceDir("根", "", 1, resourceLeaf("叶子", "根", strings.Repeat("长", maxPreviewBytes/3+1), 0, 1)), nil)
	_, err := c.ResolveResource(context.Background(), ResolveRequest{Target: "设备一", URL: testMagnet})
	wantKind(t, err, "resource_too_large")
	if len(c.previews) != 0 {
		t.Fatal("超限预览被缓存")
	}
	r := resourceDir("根", "", 1)
	obj(r["dir"])["next_page_token"] = "下一页"
	pages := 0
	c = resourceClient(t, r, func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasPrefix(r.URL.Path, "/drive/v1/resource/list/") {
			return false
		}
		pages++
		respond(w, object{"填充": strings.Repeat("a", 6<<20), "list": object{"resources": []any{}, "next_page_token": strconv.Itoa(pages)}})
		return true
	})
	_, err = c.ResolveResource(context.Background(), ResolveRequest{Target: "设备一", URL: testMagnet})
	wantKind(t, err, "resource_too_large")
	if pages != 3 {
		t.Fatalf("累计预算应在第三页停止：%d", pages)
	}
}

func Test切换状态同步失败仍使预览失效(t *testing.T) {
	for _, change := range []string{"账号", "设备"} {
		t.Run(change, func(t *testing.T) {
			c := resourceClient(t, testResource(), func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path == "/drive/v1/tasks" {
					respond(w, object{"tasks": []any{object{"name": "设备二", "type": "user#runner", "params": object{"target": "设备二"}}}})
					return true
				}
				return false
			})
			_, err := c.ResolveResource(context.Background(), ResolveRequest{Target: "设备一", URL: testMagnet})
			if err != nil {
				t.Fatal(err)
			}
			faults := &stateFaults{fail: "状态目录同步"}
			faults.install(c)
			if change == "账号" {
				err = c.Login(context.Background(), "新账号", "密码", false)
			} else {
				err = c.Configure(context.Background(), Settings{Target: "设备二"})
			}
			if err == nil || len(c.previews) != 0 {
				t.Fatal("已替换状态未使预览失效")
			}
		})
	}
}

func Test根列表分页与取消分页不发布半成品(t *testing.T) {
	for _, cancelPage := range []bool{false, true} {
		root := resourceDir("根", "", 2, resourceLeaf("甲", "根", "文件甲", 0, 1))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		c := resourceClient(t, root, func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Path == "/drive/v1/resource/list" {
				respond(w, object{"list_id": "列表", "list": object{"resources": []any{root}, "next_page_token": "根页"}})
				return true
			}
			if strings.HasPrefix(r.URL.Path, "/drive/v1/resource/list/") {
				if cancelPage {
					cancel()
				}
				respond(w, object{"list": object{"resources": []any{resourceDir("根", "", 2, resourceLeaf("乙", "根", "文件乙", 4, 2))}}})
				return true
			}
			return false
		})
		p, err := c.ResolveResource(ctx, ResolveRequest{Target: "设备一", URL: testMagnet})
		if cancelPage {
			if err == nil || len(c.previews) != 0 {
				t.Fatal("取消分页仍发布预览")
			}
		} else if err != nil || p.TotalFileCount != 2 {
			t.Fatalf("根分页失败：%v", err)
		}
	}
}
