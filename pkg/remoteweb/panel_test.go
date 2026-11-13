package remoteweb

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestPanelExtendsOnlyOriginalHTML(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		t.Run(fmt.Sprint(compressed), func(t *testing.T) {
			original := []byte(`<!doctype html><html><head><script src="assets/original.js"></script></head><body><div id="app"></div></body></html>`)
			body := original
			if compressed {
				var b bytes.Buffer
				g := gzip.NewWriter(&b)
				_, _ = g.Write(body)
				_ = g.Close()
				body = b.Bytes()
			}
			handler := Panel(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Accept-Encoding") != "identity" || r.Header.Get("If-None-Match") != "" {
					t.Fatal("应重新获取原版首页并避免压缩")
				}
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Header().Set("Content-Length", strconv.Itoa(len(body)))
				w.Header().Set("ETag", "原版标签")
				w.Header().Add("Set-Cookie", "original=1")
				if compressed {
					w.Header().Set("Content-Encoding", "gzip")
				}
				_, _ = w.Write(body)
			}))
			r := httptest.NewRequest("GET", PanelPath, nil)
			r.Header.Set("If-None-Match", "旧标签")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != 200 || !strings.Contains(w.Body.String(), panelExtension) {
				t.Fatal("原版首页没有挂载侧栏扩展")
			}
			if strings.Replace(w.Body.String(), panelExtension, "", 1) != string(original) {
				t.Fatal("扩展改变了原有页面内容")
			}
			if w.Header().Get("Content-Length") != "" || w.Header().Get("Content-Encoding") != "" || w.Header().Get("ETag") != "" || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Set-Cookie") != "original=1" {
				t.Fatal("首页响应头处理不正确")
			}
		})
	}
}

func TestPanelPassesAPIsAndLargeDownloads(t *testing.T) {
	for _, tc := range []struct {
		name, path, contentType, body string
		status                        int
	}{
		{"接口", PanelPath + "drive/v1/tasks", "application/json", `{"tasks":[]}`, 200},
		{"下载文件", PanelPath + "files/video", "application/octet-stream", strings.Repeat("数据", maxPanelSize), 200},
		{"首页非网页", PanelPath, "application/json", `{"error":"未登录"}`, 200},
		{"登录跳转", PanelPath, "text/html", "跳转响应", 302},
		{"超大网页", PanelPath, "text/html", strings.Repeat("x", maxPanelSize+1), 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			handler := Panel(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", tc.contentType)
				w.Header().Set("Content-Length", strconv.Itoa(len(tc.body)))
				w.WriteHeader(tc.status)
				for offset := 0; offset < len(tc.body); offset += 8192 {
					_, _ = w.Write([]byte(tc.body[offset:min(offset+8192, len(tc.body))]))
				}
			}))
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
			if calls != 1 || w.Code != tc.status || w.Body.String() != tc.body || w.Header().Get("Content-Length") != strconv.Itoa(len(tc.body)) {
				t.Fatal("原版接口或下载流被扩展修改")
			}
		})
	}
}

func TestPanelDoesNotDuplicateExtension(t *testing.T) {
	body := "<html><head>" + panelExtension + "</head><body></body></html>"
	handler := Panel(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(body))
	}))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", PanelPath, nil))
	if w.Body.String() != body {
		t.Fatal("重复插入了侧栏扩展")
	}
}
