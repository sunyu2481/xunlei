package web

import (
	"context"
	"errors"
	"github.com/cnk3x/xunlei/pkg/remoteweb"
	"github.com/cnk3x/xunlei/pkg/utils"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cgi"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func startTestMux(t *testing.T, mux *Mux) (context.CancelFunc, <-chan error, string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	started := make(chan string, 1)
	done := make(chan error, 1)
	mux.OnStarted(func(addr string) { started <- addr })
	go func() { done <- mux.Run(ctx, "127.0.0.1:0") }()
	select {
	case addr := <-started:
		return cancel, done, addr
	case err := <-done:
		t.Fatalf("启动失败：%v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("启动超时")
	}
	return nil, nil, ""
}

func Test关闭等待活动请求完成(t *testing.T) {
	mux := NewMux()
	entered, release := make(chan struct{}), make(chan struct{})
	mux.Get("/", func(w http.ResponseWriter, r *http.Request) { close(entered); <-release; io.WriteString(w, "结束") })
	callback := make(chan struct{})
	mux.OnShutDown(func() { close(callback) })
	cancel, done, addr := startTestMux(t, mux)
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		res, err := http.Get("http://" + addr)
		if err == nil {
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
		}
	}()
	<-entered
	cancel()
	select {
	case <-done:
		close(release)
		t.Fatal("请求结束前服务已返回")
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case <-callback:
		close(release)
		t.Fatal("完成回调提前执行")
	default:
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("关闭未完成")
	}
	<-requestDone
	select {
	case <-callback:
	default:
		t.Fatal("关闭完成后未通知")
	}
}

func TestCGI取消回收进程与继承管道(t *testing.T) {
	for _, script := range []string{"echo $$ > \"$1\"; sleep 30", "sleep 30 & echo $! > \"$1\""} {
		dir := t.TempDir()
		pidPath := filepath.Join(dir, "进程号")
		program := filepath.Join(dir, "模拟网关")
		if err := os.WriteFile(program, []byte("#!/bin/sh\n"+script+"\n"), 0700); err != nil {
			t.Fatal(err)
		}
		mux := NewMux()
		mux.Handle("/", &CGIHandler{Handler: cgi.Handler{Path: program, Args: []string{pidPath}, Logger: log.New(io.Discard, "", 0)}})
		cancel, done, addr := startTestMux(t, mux)
		responseDone := make(chan struct{})
		go func() {
			defer close(responseDone)
			res, err := http.Get("http://" + addr)
			if err == nil {
				io.Copy(io.Discard, res.Body)
				res.Body.Close()
			}
		}()
		var pid int
		for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
			data, _ := os.ReadFile(pidPath)
			pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			if pid > 0 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if pid == 0 {
			t.Fatal("未收到网关进程编号")
		}
		defer syscall.Kill(pid, syscall.SIGKILL)
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("网关关闭超时")
		}
		<-responseDone
		b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err == nil {
			_, tail, _ := strings.Cut(string(b), ") ")
			if !strings.HasPrefix(tail, "Z ") {
				t.Fatalf("服务返回时网关仍在运行：%s", tail)
			}
		}
	}
}

func Test关闭上限和慢请求期限(t *testing.T) {
	mux := NewMux()
	mux.Timeouts = Timeouts{ReadHeader: 40 * time.Millisecond, Read: 50 * time.Millisecond, Write: 50 * time.Millisecond, Idle: 40 * time.Millisecond, Shutdown: 40 * time.Millisecond, ForceShutdown: 40 * time.Millisecond}
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	mux.Get("/", func(w http.ResponseWriter, r *http.Request) { close(entered); <-release })
	cancel, done, addr := startTestMux(t, mux)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, "GET / HTTP/1.1\r\nHost: ")
	conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := io.ReadAll(conn); err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			t.Fatal("慢请求头未按期断开")
		}
	}
	go func() {
		res, err := http.Get("http://" + addr)
		if err == nil {
			res.Body.Close()
		}
	}()
	<-entered
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("关闭超限未返回错误：%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("关闭超限未返回")
	}
}

func Test正常CGI响应完整及单次请求期限(t *testing.T) {
	for _, timed := range []bool{false, true} {
		script := "printf 'Content-Type: application/octet-stream\\r\\n\\r\\n'; head -c 150000 /dev/zero"
		if timed {
			script = "sleep 30"
		}
		path := filepath.Join(t.TempDir(), "模拟网关")
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0700); err != nil {
			t.Fatal(err)
		}
		mux := NewMux()
		mux.Handle("/", &CGIHandler{Handler: cgi.Handler{Path: path, Logger: log.New(io.Discard, "", 0)}, Timeout: 100 * time.Millisecond})
		cancel, done, addr := startTestMux(t, mux)
		client := &http.Client{Timeout: time.Second}
		res, err := client.Get("http://" + addr)
		if !timed {
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(res.Body)
			res.Body.Close()
			if err != nil || res.StatusCode != 200 || len(body) != 150000 {
				t.Fatalf("正常响应被关闭清理截断：状态=%d，长度=%d，错误=%v", res.StatusCode, len(body), err)
			}
		} else if err == nil {
			res.Body.Close()
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func Test请求正文与空闲连接期限(t *testing.T) {
	mux := NewMux()
	mux.Timeouts = Timeouts{Read: 40 * time.Millisecond, Idle: 40 * time.Millisecond}
	mux.Post("/", func(w http.ResponseWriter, r *http.Request) { io.Copy(io.Discard, r.Body) })
	mux.Get("/", func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Content-Length", "0") })
	cancel, done, addr := startTestMux(t, mux)
	defer func() { cancel(); <-done }()
	for _, request := range []string{"POST / HTTP/1.1\r\nHost: localhost\r\nContent-Length: 100\r\n\r\nx", "GET / HTTP/1.1\r\nHost: localhost\r\n\r\n"} {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(conn, request)
		conn.SetReadDeadline(time.Now().Add(time.Second))
		_, err = io.ReadAll(conn)
		conn.Close()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				t.Fatalf("连接未按期限关闭：%v", err)
			}
		}
	}
}

func TestCGI慢正文关闭不留下活动请求(t *testing.T) {
	dir := t.TempDir()
	path, pidPath := filepath.Join(dir, "模拟网关"), filepath.Join(dir, "进程号")
	script := "#!/bin/sh\necho $$ > \"$1\"; cat >/dev/null; printf 'Content-Type: text/plain\\r\\n\\r\\n完成'\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	mux := NewMux()
	mux.Timeouts = Timeouts{Shutdown: 80 * time.Millisecond, ForceShutdown: time.Second}
	mux.Handle("/", remoteweb.Panel(&CGIHandler{Handler: cgi.Handler{Path: path, Args: []string{pidPath}, Logger: log.New(io.Discard, "", 0)}}))
	cancel, done, addr := startTestMux(t, mux)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, "POST / HTTP/1.1\r\nHost: localhost\r\nContent-Length: 10000\r\n\r\nx")
	var pid int
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		data, _ := os.ReadFile(pidPath)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		if pid > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("网关没有启动")
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	cancel()
	select {
	case err := <-done:
		if errors.Is(err, utils.ErrUnsafeCleanup) {
			t.Fatalf("慢正文留下活动请求：%v", err)
		}
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("取消没有解除标准输入复制等待")
	}
}
