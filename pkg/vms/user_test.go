package vms

import (
	"context"
	"testing"
)

func Test非法身份在准备环境前被拒绝(t *testing.T) {
	for name, option := range map[string]Option{
		"负用户": User(-1, 0), "负组": User(0, -1),
		"保留用户": User(uint64(4294967295), 0), "保留组": User(0, uint64(4294967295)),
		"溢出用户": User(^uint64(0), 0), "溢出组": User(0, ^uint64(0)),
	} {
		t.Run(name, func(t *testing.T) {
			called := false
			err := Execute(context.Background(), option,
				Before(func(context.Context) (func(), error) { called = true; return func() {}, nil }),
				Run(func(context.Context) error { called = true; return nil }))
			if err == nil || called {
				t.Fatalf("错误=%v，已执行回调=%t", err, called)
			}
		})
	}
}
