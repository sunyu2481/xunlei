package vms

import (
	"context"
	"errors"
	"testing"

	"github.com/cnk3x/xunlei/pkg/utils"
)

func Test活动请求未退出时跳过路径回滚(t *testing.T) {
	err := Execute(context.Background(), Root("/"),
		Before(func(context.Context) (func(), error) {
			return func() { t.Error("活动请求未退出仍回滚文件") }, nil
		}),
		Run(func(context.Context) error { return utils.ErrUnsafeCleanup }),
		After(func(context.Context, error) error { t.Error("危险状态下仍调用后续处理"); return nil }))
	if !errors.Is(err, utils.ErrUnsafeCleanup) {
		t.Fatalf("丢失禁止清理状态：%v", err)
	}
}
