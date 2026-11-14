package remote

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func Test禁用重定向处理深层父目录(t *testing.T) {
	c := mockClient(t, func(http.ResponseWriter, *http.Request) { t.Fatal("应使用模拟传输") })
	calls := 0
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": {strings.Repeat("a/", 4096) + strings.Repeat("../", 4096)}}, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})
	_, err := c.raw(context.Background(), "POST", accountURL+"/test", nil, nil, c.headers(false))
	wantKind(t, err, "upstream")
	if calls != 1 {
		t.Fatalf("不应跟随重定向：请求数=%d", calls)
	}
}

// 保留小规模分配量对照，检查工具链对重定向地址解析的影响。
func Benchmark重定向父目录解析(b *testing.B) {
	for _, count := range []int{1024, 2048, 4096} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			location := strings.Repeat("a/", count) + strings.Repeat("../", count)
			client := &http.Client{
				Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 302, Header: http.Header{"Location": {location}}, Body: io.NopCloser(strings.NewReader("{}"))}, nil
				}),
				CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			}
			b.ReportAllocs()
			for b.Loop() {
				resp, err := client.Get("https://example.com/test")
				if err != nil {
					b.Fatal(err)
				}
				resp.Body.Close()
			}
		})
	}
}
