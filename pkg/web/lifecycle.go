package web

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/cnk3x/xunlei/pkg/utils"
)

// Timeouts 配置连接与关闭期限，零值使用默认值，不允许关闭期限保护。
type Timeouts struct {
	ReadHeader, Read, Write, Idle time.Duration
	Shutdown, ForceShutdown       time.Duration
}

func (t Timeouts) defaults() Timeouts {
	fields := []*time.Duration{&t.ReadHeader, &t.Read, &t.Write, &t.Idle, &t.Shutdown, &t.ForceShutdown}
	values := []time.Duration{5 * time.Second, 2 * time.Minute, 2 * time.Minute, time.Minute, 5 * time.Second, 3 * time.Second}
	for i, p := range fields {
		if *p <= 0 {
			*p = values[i]
		}
	}
	return t
}

type activeRequests struct {
	mu      sync.Mutex
	count   int
	closing bool
	done    chan struct{}
}

func (a *activeRequests) enter() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closing {
		return false
	}
	a.count++
	return true
}
func (a *activeRequests) leave() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.count--
	if a.closing && a.count == 0 {
		close(a.done)
	}
}
func (a *activeRequests) stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closing = true
	if a.count == 0 {
		close(a.done)
	}
}

// Run 等待活动请求结束；强制关闭后仍未退出时返回禁止清理标记。
func (mux *Mux) Run(ctx context.Context, addr string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	timeouts := mux.Timeouts.defaults()
	active := &activeRequests{done: make(chan struct{})}
	server := &http.Server{
		Addr: addr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !active.enter() {
				http.Error(w, "服务正在关闭", http.StatusServiceUnavailable)
				return
			}
			defer active.leave()
			mux.ServeHTTP(w, r)
		}),
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ReadHeaderTimeout: timeouts.ReadHeader, ReadTimeout: timeouts.Read, WriteTimeout: timeouts.Write, IdleTimeout: timeouts.Idle,
	}
	stopped := make(chan error, 1)
	go func() {
		<-ctx.Done()
		active.stop()
		shutdownCtx, stop := context.WithTimeout(context.Background(), timeouts.Shutdown)
		defer stop()
		err := server.Shutdown(shutdownCtx)
		if err != nil {
			err = errors.Join(err, server.Close())
		}
		timer := time.NewTimer(timeouts.ForceShutdown)
		defer timer.Stop()
		select {
		case <-active.done:
		case <-timer.C:
			err = errors.Join(err, context.DeadlineExceeded, utils.ErrUnsafeCleanup)
		}
		stopped <- err
	}()
	if mux.onStarted != nil {
		mux.onStarted(listener.Addr().String())
	}
	serveErr := server.Serve(listener)
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	cancel()
	err = errors.Join(serveErr, <-stopped)
	if !errors.Is(err, utils.ErrUnsafeCleanup) && mux.onShutdown != nil {
		mux.onShutdown()
	}
	return err
}
