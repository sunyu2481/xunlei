package spk

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type secretRoundTripper func(*http.Request) (*http.Response, error)

func (f secretRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func Test套件地址解析失败不回显秘密(t *testing.T) {
	for _, source := range []string{
		"https://模拟账号:模拟密码@example.invalid/%zz?token=模拟签名",
		"https://example.invalid/?token=模拟签名\n",
		"https://example.invalid:模拟错误端口/?token=模拟签名",
	} {
		_, _, err := parseSource(source)
		if err == nil || strings.Contains(err.Error(), "模拟") {
			t.Fatal("无效地址错误回显了模拟秘密或未返回错误")
		}
	}
}

func Test套件下载失败不回显地址且保留错误分类(t *testing.T) {
	const source = "https://example.invalid/private-package?token=模拟签名"
	u, err := url.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, failure := range []error{context.Canceled, context.DeadlineExceeded, errors.New("模拟底层错误可能含地址：" + source)} {
		client := &http.Client{Transport: secretRoundTripper(func(r *http.Request) (*http.Response, error) {
			if r.URL.String() != source {
				t.Error("下载请求丢失完整签名地址")
			}
			return nil, failure
		})}
		err := downloadHTTP(context.Background(), u, t.TempDir(), nil, client)
		if err == nil || strings.Contains(err.Error(), "模拟") || !errors.Is(err, failure) {
			t.Fatal("下载错误暴露模拟秘密或丢失原始错误分类")
		}
	}
}
