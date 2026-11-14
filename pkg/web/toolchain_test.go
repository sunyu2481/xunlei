package web

import (
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func Test隧道成功响应后关闭连接(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m := NewMux()
	var requests atomic.Int32
	m.Handle("/", FBlob(func() (string, error) { requests.Add(1); return "模拟状态", nil }, "text/plain"))
	started := make(chan string, 1)
	m.OnStarted(func(addr string) { started <- addr })
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx, "127.0.0.1:0") }()
	var addr string
	select {
	case addr = <-started:
	case err := <-done:
		t.Fatalf("服务未启动：%v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, "CONNECT / HTTP/1.1\r\nHost: localhost\r\n\r\nGET / HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("连接未及时关闭：%v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("隧道响应后仍复用连接：请求数=%d，响应=%q", got, data)
	}
}
