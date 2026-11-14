package web

import (
	"bufio"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
)

func internalError(w http.ResponseWriter, r *http.Request, err error) {
	slog.ErrorContext(r.Context(), "网页响应失败", "err", err)
	writeInternalError(w)
}

func writeInternalError(w http.ResponseWriter) {
	// 原处理器可能已准备压缩或定长响应，失败正文不能沿用这些实体头。
	w.Header().Del("Content-Encoding")
	http.Error(w, "服务器内部错误", http.StatusInternalServerError)
}

// Recoverer 记录故障原因与堆栈，保留中止语义，已经发送的响应不能改写为错误正文。
func Recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state := &recoveryResponse{ResponseWriter: w}
		defer func() {
			if value := recover(); value != nil {
				if state.conn != nil {
					_ = state.conn.Close()
				}
				if value == http.ErrAbortHandler {
					panic(value)
				}
				slog.ErrorContext(r.Context(), "网页处理发生故障", "panic", value, "stack", string(debug.Stack()))
				if state.committed || state.conn != nil {
					panic(http.ErrAbortHandler)
				}
				writeInternalError(w)
			}
		}()
		next.ServeHTTP(state.wrap(), r)
	})
}

type recoveryResponse struct {
	http.ResponseWriter
	committed bool
	conn      net.Conn
}

func (w *recoveryResponse) WriteHeader(status int) {
	// 101 结束 HTTP 响应；其余信息响应不阻止后续返回最终的 500。
	if status == http.StatusSwitchingProtocols || status >= 200 {
		w.committed = true
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *recoveryResponse) Write(data []byte) (int, error) {
	w.committed = true
	return w.ResponseWriter.Write(data)
}

func (w *recoveryResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }

type recoveryFlusher struct{ state *recoveryResponse }

func (f recoveryFlusher) FlushError() error {
	f.state.committed = true
	return http.NewResponseController(f.state.ResponseWriter).Flush()
}

func (f recoveryFlusher) Flush() { _ = f.FlushError() }

type recoveryHijacker struct{ state *recoveryResponse }

func (h recoveryHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, buffered, err := http.NewResponseController(h.state.ResponseWriter).Hijack()
	if err == nil {
		h.state.conn = conn
	}
	return conn, buffered, err
}

// 只声明底层实际支持的可选接口，同时跟踪刷新与接管，避免绕过响应状态。
func (w *recoveryResponse) wrap() http.ResponseWriter {
	var flush, hijack bool
	for current := w.ResponseWriter; ; {
		_, fl := current.(http.Flusher)
		_, fe := current.(interface{ FlushError() error })
		_, hj := current.(http.Hijacker)
		flush = flush || fl || fe
		hijack = hijack || hj
		unwrapper, ok := current.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			break
		}
		current = unwrapper.Unwrap()
	}
	push, pushes := w.ResponseWriter.(http.Pusher)
	f, h := recoveryFlusher{w}, recoveryHijacker{w}
	switch {
	case flush && hijack && pushes:
		return struct {
			*recoveryResponse
			recoveryFlusher
			recoveryHijacker
			http.Pusher
		}{w, f, h, push}
	case flush && hijack:
		return struct {
			*recoveryResponse
			recoveryFlusher
			recoveryHijacker
		}{w, f, h}
	case flush && pushes:
		return struct {
			*recoveryResponse
			recoveryFlusher
			http.Pusher
		}{w, f, push}
	case hijack && pushes:
		return struct {
			*recoveryResponse
			recoveryHijacker
			http.Pusher
		}{w, h, push}
	case flush:
		return struct {
			*recoveryResponse
			recoveryFlusher
		}{w, f}
	case hijack:
		return struct {
			*recoveryResponse
			recoveryHijacker
		}{w, h}
	case pushes:
		return struct {
			*recoveryResponse
			http.Pusher
		}{w, push}
	default:
		return w
	}
}
