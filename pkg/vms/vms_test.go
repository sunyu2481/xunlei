package vms

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/cnk3x/xunlei/pkg/vms/sys"
)

// 不切换身份和根目录，普通用户也能验证准备、运行与清理的完整生命周期。
func Test执行完成清理资源并允许重试(t *testing.T) {
	for _, mode := range []string{"成功", "准备失败", "业务失败", "主动取消"} {
		t.Run(mode, func(t *testing.T) {
			base := t.TempDir()
			dir := filepath.Join(base, "运行", "数据")
			var want error
			if mode == "准备失败" || mode == "业务失败" {
				want = errors.New("模拟" + mode)
			} else if mode == "主动取消" {
				want = context.Canceled
			}
			for range 3 {
				ctx, cancel := context.WithCancel(context.Background())
				t.Cleanup(cancel)
				var steps []string
				err := Execute(ctx, Root("/"),
					Before(func(ctx context.Context) (func(), error) {
						steps = append(steps, "准备")
						if mode == "准备失败" {
							return nil, want
						}
						undo, err := sys.Mkdir(ctx, dir, 0700)
						if err != nil {
							return nil, err
						}
						return func() { steps = append(steps, "清理"); undo() }, nil
					}),
					Run(func(ctx context.Context) error {
						steps = append(steps, "运行")
						if info, err := os.Stat(dir); err != nil || !info.IsDir() {
							t.Fatalf("运行前未准备目录：%v", err)
						}
						if mode == "主动取消" {
							cancel()
							return ctx.Err()
						}
						return want
					}))
				cancel()
				if !errors.Is(err, want) {
					t.Fatalf("执行结果=%v，期望=%v", err, want)
				}
				expected := []string{"准备", "运行", "清理"}
				if mode == "准备失败" {
					expected = []string{"准备"}
				}
				if !reflect.DeepEqual(steps, expected) {
					t.Fatalf("生命周期顺序=%v，期望=%v", steps, expected)
				}
				if entries, err := os.ReadDir(base); err != nil || len(entries) != 0 {
					t.Fatalf("执行后临时资源残留：%v，错误=%v", entries, err)
				}
			}
		})
	}
}
