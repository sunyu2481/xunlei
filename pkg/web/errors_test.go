package web

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func captureWebLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &logs
}

func caughtWebPanic(fn func()) (value any) {
	defer func() { value = recover() }()
	fn()
	return nil
}

func Test恢复器保留中止信号(t *testing.T) {
	logs := captureWebLogs(t)
	response := httptest.NewRecorder()
	handler := Recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	value := caughtWebPanic(func() { handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil)) })
	if value != http.ErrAbortHandler {
		t.Fatalf("中止信号被改变：%v", value)
	}
	if response.Body.Len() != 0 || logs.Len() != 0 {
		t.Fatalf("中止信号不应生成错误正文或故障日志：正文 %q，日志 %q", response.Body.String(), logs.String())
	}
}

func Test恢复器记录故障并拒绝升级头伪成功(t *testing.T) {
	logs := captureWebLogs(t)
	for _, connection := range []string{"", "Upgrade", "keep-alive, Upgrade"} {
		t.Run(connection, func(t *testing.T) {
			logs.Reset()
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Header.Set("Connection", connection)
			handler := Recoverer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Content-Length", "9999")
				w.Header().Set("Content-Encoding", "gzip")
				panic("模拟处理故障")
			}))
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusInternalServerError || response.Body.String() != "服务器内部错误\n" {
				t.Fatalf("故障响应不正确：状态 %d，正文 %q", response.Code, response.Body.String())
			}
			if response.Header().Get("Content-Length") != "" || response.Header().Get("Content-Encoding") != "" {
				t.Fatalf("保留了失败响应的实体头：%v", response.Header())
			}
			var record map[string]any
			if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
				t.Fatalf("未记录结构化故障日志：%q，错误 %v", logs.String(), err)
			}
			if record["panic"] != "模拟处理故障" || !strings.Contains(record["stack"].(string), "Test恢复器记录故障并拒绝升级头伪成功") {
				t.Fatalf("故障日志缺少原因或堆栈：%v", record)
			}
		})
	}
}

type faultResponse struct {
	header http.Header
	codes  []int
	body   bytes.Buffer
	conn   net.Conn
	err    error
	flush  bool
}

func (w *faultResponse) Header() http.Header            { return w.header }
func (w *faultResponse) WriteHeader(code int)           { w.codes = append(w.codes, code) }
func (w *faultResponse) Write(data []byte) (int, error) { return w.body.Write(data) }
func (w *faultResponse) Flush()                         { w.flush = true }
func (w *faultResponse) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if w.err != nil {
		return nil, nil, w.err
	}
	return w.conn, bufio.NewReadWriter(bufio.NewReader(w.conn), bufio.NewWriter(w.conn)), nil
}

func Test恢复器区分响应状态(t *testing.T) {
	logs := captureWebLogs(t)
	for _, test := range []struct {
		name  string
		write func(http.ResponseWriter)
		abort bool
	}{
		{"信息响应", func(w http.ResponseWriter) { w.WriteHeader(http.StatusEarlyHints) }, false},
		{"最终响应", func(w http.ResponseWriter) { w.WriteHeader(http.StatusAccepted) }, true},
		{"升级响应", func(w http.ResponseWriter) { w.WriteHeader(http.StatusSwitchingProtocols) }, true},
		{"正文", func(w http.ResponseWriter) { _, _ = io.WriteString(w, "未完成") }, true},
		{"刷新", func(w http.ResponseWriter) { w.(http.Flusher).Flush() }, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			logs.Reset()
			response := &faultResponse{header: make(http.Header)}
			handler := Recoverer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				test.write(w)
				panic("模拟响应中途故障")
			}))
			value := caughtWebPanic(func() { handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil)) })
			if test.abort {
				if value != http.ErrAbortHandler {
					t.Fatalf("已提交响应未被中止：%v", value)
				}
				for _, code := range response.codes {
					if code == http.StatusInternalServerError {
						t.Fatalf("已提交响应被追加错误状态：%v", response.codes)
					}
				}
			} else if value != nil || response.codes[len(response.codes)-1] != http.StatusInternalServerError {
				t.Fatalf("信息响应之后未返回失败：中止 %v，状态 %v", value, response.codes)
			}
			if !strings.Contains(logs.String(), "模拟响应中途故障") {
				t.Fatalf("没有记录故障原因：%q", logs.String())
			}
		})
	}
}

func Test恢复器关闭故障接管连接(t *testing.T) {
	captureWebLogs(t)
	for _, failed := range []bool{false, true} {
		local, peer := net.Pipe()
		response := &faultResponse{header: make(http.Header), conn: local}
		if failed {
			response.err = errors.New("模拟接管失败")
		}
		handler := Recoverer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _, _ = http.NewResponseController(w).Hijack()
			panic("模拟连接处理故障")
		}))
		value := caughtWebPanic(func() { handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil)) })
		if failed {
			if value != nil || response.codes[len(response.codes)-1] != http.StatusInternalServerError {
				t.Errorf("接管失败后应返回错误响应：中止 %v，状态 %v", value, response.codes)
			}
		} else {
			if value != http.ErrAbortHandler || len(response.codes) != 0 || response.body.Len() != 0 {
				t.Errorf("已接管连接仍按普通响应处理：中止 %v，状态 %v", value, response.codes)
			}
			_ = peer.SetReadDeadline(time.Now().Add(time.Second))
			if _, err := peer.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
				t.Errorf("故障接管连接未关闭：%v", err)
			}
		}
		_ = local.Close()
		_ = peer.Close()
	}
}

func Test恢复器中止已发送HTTP正文(t *testing.T) {
	captureWebLogs(t)
	server := httptest.NewServer(Recoverer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "未完成")
		w.(http.Flusher).Flush()
		panic("模拟流式响应故障")
	})))
	t.Cleanup(server.Close)
	client := server.Client()
	client.Timeout = 2 * time.Second
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if !errors.Is(err, io.ErrUnexpectedEOF) || string(data) != "未完成" {
		t.Fatalf("部分正文被表现为完整成功：正文 %q，错误 %v", data, err)
	}
}

type pushFaultResponse struct{ *faultResponse }

func (*pushFaultResponse) Push(string, *http.PushOptions) error { return nil }

func Test恢复器保留响应可选接口(t *testing.T) {
	base := &faultResponse{header: make(http.Header)}
	for _, writer := range []http.ResponseWriter{
		struct{ http.ResponseWriter }{base},
		struct {
			http.ResponseWriter
			http.Flusher
		}{base, base},
		struct {
			http.ResponseWriter
			http.Hijacker
		}{base, base},
		struct {
			http.ResponseWriter
			http.Pusher
		}{base, &pushFaultResponse{base}},
		struct {
			http.ResponseWriter
			http.Flusher
			http.Pusher
		}{base, base, &pushFaultResponse{base}},
		struct {
			http.ResponseWriter
			http.Hijacker
			http.Pusher
		}{base, base, &pushFaultResponse{base}},
		base,
		&pushFaultResponse{base},
	} {
		Recoverer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, wantFlush := writer.(http.Flusher)
			_, wantHijack := writer.(http.Hijacker)
			_, wantPush := writer.(http.Pusher)
			_, gotFlush := w.(http.Flusher)
			_, gotHijack := w.(http.Hijacker)
			_, gotPush := w.(http.Pusher)
			if gotFlush != wantFlush || gotHijack != wantHijack || gotPush != wantPush {
				t.Errorf("可选接口改变：刷新 %v/%v，接管 %v/%v，推送 %v/%v", gotFlush, wantFlush, gotHijack, wantHijack, gotPush, wantPush)
			}
			if w.(interface{ Unwrap() http.ResponseWriter }).Unwrap() != writer {
				t.Error("底层响应不可访问")
			}
		})).ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/", nil))
	}
}

func Test响应辅助函数拒绝内部错误与编码失败(t *testing.T) {
	logs := captureWebLogs(t)
	secret := "模拟内部路径 /private/mock，模拟令牌 mock-token"
	for _, test := range []struct {
		name    string
		handler http.Handler
	}{
		{"字节回调", FBlob(func() (string, error) { return "", errors.New(secret) }, "text/plain")},
		{"JSON回调", FJSON(func() (any, error) { return nil, errors.New(secret) })},
		{"JSON编码", FJSON(func() (any, error) { return make(chan int), nil })},
		{"静态JSON编码", JSON(make(chan int), http.StatusOK)},
	} {
		t.Run(test.name, func(t *testing.T) {
			logs.Reset()
			response := httptest.NewRecorder()
			test.handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
			if response.Code != http.StatusInternalServerError || response.Body.String() != "服务器内部错误\n" {
				t.Fatalf("内部错误响应未脱敏：状态 %d，正文 %q", response.Code, response.Body.String())
			}
			if !strings.Contains(response.Header().Get("Content-Type"), "text/plain") || logs.Len() == 0 {
				t.Fatalf("错误类型或日志缺失：响应头 %v，日志 %q", response.Header(), logs.String())
			}
			if strings.Contains(test.name, "回调") && !strings.Contains(logs.String(), secret) {
				t.Fatalf("内部原因未写入日志：%q", logs.String())
			}
		})
	}
}

func Test响应辅助函数保留成功结果(t *testing.T) {
	for _, test := range []struct {
		name    string
		handler http.Handler
		code    int
		body    string
	}{
		{"字节", FBlob(func() (string, error) { return "正常", nil }, "text/plain"), http.StatusOK, "正常"},
		{"动态JSON", FJSON(func() (string, error) { return "正常", nil }), http.StatusOK, `"正常"`},
		{"静态JSON", JSON("正常", http.StatusCreated), http.StatusCreated, `"正常"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			test.handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
			if response.Code != test.code || response.Body.String() != test.body {
				t.Fatalf("成功响应改变：状态 %d，正文 %q", response.Code, response.Body.String())
			}
		})
	}
}
