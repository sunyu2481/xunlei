package spk

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func Test下载拒绝不安全网络来源(t *testing.T) {
	payload := testPackage(t, testFiles())
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(payload) })
	plain := httptest.NewServer(handler)
	defer plain.Close()
	secure := httptest.NewUnstartedServer(handler)
	secure.Config.ErrorLog = log.New(io.Discard, "", 0)
	secure.StartTLS()
	defer secure.Close()
	for _, source := range []string{plain.URL, secure.URL} {
		if err := Download(context.Background(), source, t.TempDir(), true); err == nil {
			t.Fatalf("应拒绝明文来源或未信任证书：%s", source)
		}
	}
}

func trustedClient(t *testing.T, server *httptest.Server) *http.Client {
	t.Helper()
	client := newDownloadClient()
	client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{
		RootCAs: server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs,
	}
	t.Cleanup(client.CloseIdleConnections)
	return client
}

func fetchTestSource(ctx context.Context, source, dir string, client *http.Client) error {
	u, checksum, err := parseSource(source)
	if err != nil {
		return err
	}
	return downloadHTTP(ctx, u, dir, checksum, client)
}

func Test下载HTTPS响应(t *testing.T) {
	payload := testPackage(t, testFiles())
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/正常", "/摘要":
			w.Write(payload)
		case "/压缩":
			if r.Header.Get("Accept-Encoding") != "gzip" {
				t.Error("客户端应仅自动协商 gzip")
			}
			w.Header().Set("Content-Encoding", "gzip")
			zw := gzip.NewWriter(w)
			zw.Write(payload)
			zw.Close()
		case "/错误状态":
			w.WriteHeader(http.StatusInternalServerError)
			w.Write(payload)
		case "/未找到":
			w.WriteHeader(http.StatusNotFound)
		case "/部分响应":
			w.WriteHeader(http.StatusPartialContent)
			w.Write(payload)
		case "/未知编码":
			w.Header().Set("Content-Encoding", "br")
			w.Write(payload)
		case "/空套件":
			w.Write(testTar(t, nil))
		}
	}))
	defer server.Close()
	client := trustedClient(t, server)
	for _, name := range []string{"正常", "压缩", "摘要", "错误状态", "未找到", "部分响应", "未知编码", "空套件"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			source := server.URL + "/" + name
			if name == "摘要" || name == "压缩" {
				source += fmt.Sprintf("#sha256=%x", sha256.Sum256(payload))
			}
			err := fetchTestSource(context.Background(), source, dir, client)
			if name == "正常" || name == "压缩" || name == "摘要" {
				if err != nil {
					t.Fatal(err)
				}
				assertFiles(t, dir)
			} else {
				if err == nil {
					t.Fatal("异常响应应返回错误")
				}
				if _, err := os.Stat(filepath.Join(dir, versionFile)); !os.IsNotExist(err) {
					t.Fatalf("异常响应不应安装文件：%v", err)
				}
			}
			assertNoStage(t, dir)
		})
	}
}

func Test下载验证证书主机名(t *testing.T) {
	payload := testPackage(t, testFiles())
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(payload) }))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	client := trustedClient(t, server)
	client.Transport.(*http.Transport).TLSClientConfig.ServerName = "不匹配.example"
	if err := fetchTestSource(context.Background(), server.URL, t.TempDir(), client); err == nil {
		t.Fatal("应拒绝证书主机名不匹配")
	}
}

func Test下载重定向边界(t *testing.T) {
	var plainRequests atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { plainRequests.Add(1) }))
	defer plain.Close()
	payload := testPackage(t, testFiles())
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/降级":
			http.Redirect(w, r, plain.URL, http.StatusFound)
		case "/循环":
			http.Redirect(w, r, "/循环", http.StatusFound)
		case "/安全":
			http.Redirect(w, r, "/套件", http.StatusFound)
		default:
			w.Write(payload)
		}
	}))
	defer server.Close()
	client := trustedClient(t, server)
	for _, name := range []string{"降级", "循环", "安全"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			err := fetchTestSource(context.Background(), server.URL+"/"+name, dir, client)
			if name == "安全" {
				if err != nil {
					t.Fatal(err)
				}
				assertFiles(t, dir)
			} else if err == nil {
				t.Fatal("应拒绝降级或无限重定向")
			}
		})
	}
	if plainRequests.Load() != 0 {
		t.Fatal("拒绝降级前不应访问明文目标")
	}
}

func Test网络等待响应取消(t *testing.T) {
	for _, phase := range []string{"响应头", "响应体"} {
		t.Run(phase, func(t *testing.T) {
			started := make(chan struct{})
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if phase == "响应体" {
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
				}
				close(started)
				<-r.Context().Done()
			}))
			defer server.Close()
			client := trustedClient(t, server)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			dir := t.TempDir()
			done := make(chan error, 1)
			go func() { done <- fetchTestSource(ctx, server.URL, dir, client) }()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("模拟服务未收到请求")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("应保留取消错误，实际为 %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("取消后下载没有退出")
			}
			assertNoStage(t, dir)
		})
	}
}

func Test下载等待有界(t *testing.T) {
	for _, phase := range []string{"总时限", "响应头", "调用方时限"} {
		t.Run(phase, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
			defer server.Close()
			client := trustedClient(t, server)
			transport := client.Transport.(*http.Transport)
			if client.Timeout <= 0 || transport.TLSHandshakeTimeout <= 0 || transport.ResponseHeaderTimeout <= 0 || transport.DialContext == nil {
				t.Fatal("默认下载客户端必须限制总等待和各连接阶段")
			}
			ctx := context.Background()
			switch phase {
			case "总时限":
				client.Timeout = 200 * time.Millisecond
			case "响应头":
				transport.ResponseHeaderTimeout = 200 * time.Millisecond
			case "调用方时限":
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 200*time.Millisecond)
				defer cancel()
			}
			err := fetchTestSource(ctx, server.URL, t.TempDir(), client)
			var timeout net.Error
			if !errors.As(err, &timeout) || !timeout.Timeout() {
				t.Fatalf("应返回超时错误，实际为 %v", err)
			}
		})
	}
}

func Test本地套件URL(t *testing.T) {
	file := filepath.Join(t.TempDir(), "套件 空格#%.spk")
	if err := os.WriteFile(file, testPackage(t, testFiles()), 0600); err != nil {
		t.Fatal(err)
	}
	for _, scheme := range []string{"file", "FILE", "FiLe"} {
		for _, host := range []string{"", "localhost", "LOCALHOST"} {
			source := (&url.URL{Scheme: scheme, Host: host, Path: file}).String()
			t.Run(scheme+host, func(t *testing.T) {
				dir := t.TempDir()
				if err := Download(context.Background(), source, dir, true); err != nil {
					t.Fatal(err)
				}
				assertFiles(t, dir)
			})
		}
	}
	for _, source := range []string{
		"file://remote.example/tmp/a.spk", "file:relative.spk", "file://localhost:80/tmp/a.spk",
		"file:///tmp/a.spk?query=1", "file:///tmp/a.spk#未知片段", "file:///tmp/%00.spk", "file:///tmp/%GG",
	} {
		if err := Download(context.Background(), source, t.TempDir(), true); err == nil {
			t.Fatalf("应拒绝不支持的本地地址：%s", source)
		}
	}
}

func Test下载校验可信摘要(t *testing.T) {
	payload := testPackage(t, testFiles())
	file := filepath.Join(t.TempDir(), "套件.spk")
	if err := os.WriteFile(file, payload, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	source := (&url.URL{Scheme: "file", Path: file}).String()
	for _, checksum := range []string{fmt.Sprintf("%x", sum), strings.Repeat("0", 64), "无效摘要"} {
		dir := t.TempDir()
		err := Download(context.Background(), source+"#sha256="+checksum, dir, true)
		if checksum == fmt.Sprintf("%x", sum) {
			if err != nil {
				t.Fatal(err)
			}
			assertFiles(t, dir)
		} else {
			if err == nil {
				t.Fatal("应拒绝错误摘要")
			}
			entries, readErr := os.ReadDir(dir)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("摘要验证前不应安装文件：%v，错误：%v", entries, readErr)
			}
		}
	}
}

func Test指定摘要不跳过来源校验(t *testing.T) {
	payload := testPackage(t, testFiles())
	file := filepath.Join(t.TempDir(), "套件.spk")
	if err := os.WriteFile(file, payload, 0600); err != nil {
		t.Fatal(err)
	}
	source := (&url.URL{Scheme: "file", Path: file}).String() + fmt.Sprintf("#sha256=%x", sha256.Sum256(payload))
	dir := t.TempDir()
	if err := Download(context.Background(), source, dir, false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, append(payload, []byte("附加内容")...), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Download(context.Background(), source, dir, false); err == nil {
		t.Fatal("已有安装不能跳过指定摘要的校验")
	}
	assertFiles(t, dir)
}

func Test已有套件仍校验来源地址(t *testing.T) {
	dir := t.TempDir()
	if err := Extract(context.Background(), bytes.NewReader(testPackage(t, testFiles())), dir); err != nil {
		t.Fatal(err)
	}
	if err := Download(context.Background(), "http://example.invalid/套件.spk", dir, false); err == nil {
		t.Fatal("已有安装不能绕过来源校验")
	}
}

func Test本地套件拒绝特殊文件(t *testing.T) {
	file := filepath.Join(t.TempDir(), "管道")
	if err := syscall.Mkfifo(file, 0600); err != nil {
		t.Fatal(err)
	}
	source := (&url.URL{Scheme: "file", Path: file}).String()
	if err := Download(context.Background(), source, t.TempDir(), true); err == nil {
		t.Fatal("本地套件不应接受管道")
	}
}

func Test下载与解包响应取消(t *testing.T) {
	payload := testPackage(t, testFiles())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dir := filepath.Join(t.TempDir(), "未创建")
	if err := Extract(ctx, bytes.NewReader(payload), dir); !errors.Is(err, context.Canceled) {
		t.Fatalf("应返回取消错误，实际为 %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("取消后不应创建目录：%v", err)
	}
	if err := Download(ctx, "file:///不存在的套件.spk", dir, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("下载应优先返回取消错误，实际为 %v", err)
	}
}

type cancelReader struct {
	r      *bytes.Reader
	cancel context.CancelFunc
	left   int
}

func (r *cancelReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p[:min(len(p), 64)])
	r.left -= n
	if r.left <= 0 {
		r.cancel()
	}
	return n, err
}

func Test解包中途取消清理临时文件(t *testing.T) {
	payload := testPackage(t, testFiles())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src := &cancelReader{bytes.NewReader(payload), cancel, 640}
	dir := t.TempDir()
	if err := Extract(ctx, src, dir); !errors.Is(err, context.Canceled) {
		t.Fatalf("解包应保留取消错误，实际为 %v", err)
	}
	assertNoStage(t, dir)
	if _, err := os.Stat(filepath.Join(dir, versionFile)); !os.IsNotExist(err) {
		t.Fatalf("取消的归档不应发布文件：%v", err)
	}
}

func Test解包要求完整套件(t *testing.T) {
	cases := [][]byte{nil, testTar(t, nil), testTar(t, []testEntry{{name: "其他文件", data: "无内层包"}})}
	for i := range testFiles() {
		files := testFiles()
		cases = append(cases, testPackage(t, append(files[:i], files[i+1:]...)))
		files = testFiles()
		files[i].data = ""
		cases = append(cases, testPackage(t, files))
	}
	for i, payload := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			dir := t.TempDir()
			if err := Extract(context.Background(), bytes.NewReader(payload), dir); err == nil {
				t.Fatal("应拒绝缺包、缺少必要文件或空文件")
			}
			if _, err := os.Stat(filepath.Join(dir, versionFile)); !os.IsNotExist(err) {
				t.Fatalf("无效套件不应发布文件：%v", err)
			}
			assertNoStage(t, dir)
		})
	}
}
