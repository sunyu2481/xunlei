package web

import (
	"io"
	"log"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCGI兼容标准库(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		code         int
	}{
		{"请求环境和正文", "printf 'Content-Type: text/plain\\r\\nX-Test: yes\\r\\n\\r\\n'; printf '%s\\n' \"$REQUEST_METHOD\" \"$QUERY_STRING\" \"$PATH_INFO\" \"$HTTP_COOKIE\" \"$CONTENT_TYPE\" \"$HTTP_PROXY\"; cat", 200},
		{"状态码", "printf 'Status: 201 Created\\r\\nContent-Type: text/plain\\r\\n\\r\\n创建成功'", 201},
		{"重定向", "printf 'Location: /target\\r\\n\\r\\n'", 302},
		{"无效响应头", "printf '错误头: value\\r\\nContent-Type: text/plain\\r\\n\\r\\n正文'", 200},
		{"缺少类型", "printf 'X-Test: value\\r\\n\\r\\n正文'", 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "模拟网关")
			if err := os.WriteFile(path, []byte("#!/bin/sh\n"+tc.script+"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			h := cgi.Handler{Path: path, Root: "/cgi", Logger: log.New(io.Discard, "", 0)}
			responses := []*httptest.ResponseRecorder{httptest.NewRecorder(), httptest.NewRecorder()}
			for i, handler := range []http.Handler{&h, &CGIHandler{Handler: h}} {
				req := httptest.NewRequest("POST", "http://localhost/cgi/path?name=test", strings.NewReader("请求正文"))
				req.Header.Set("Content-Type", "text/plain")
				req.Header.Add("Cookie", "a=1")
				req.Header.Add("Cookie", "b=2")
				req.Header.Set("Proxy", "不能进入代理环境变量")
				handler.ServeHTTP(responses[i], req)
			}
			want, got := responses[0], responses[1]
			if got.Code != tc.code || got.Code != want.Code || !reflect.DeepEqual(got.Header(), want.Header()) || got.Body.String() != want.Body.String() {
				t.Fatalf("网关语义改变：标准库=%d %v %q，扩展=%d %v %q", want.Code, want.Header(), want.Body.String(), got.Code, got.Header(), got.Body.String())
			}
		})
	}
}
