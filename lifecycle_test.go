package xunlei

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/cnk3x/xunlei/pkg/utils"
)

func Test服务全部退出后才清理(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var steps []string
	done := make(chan error, 1)
	want := errors.New("模拟网页关闭失败")
	go func() {
		done <- runServices(context.Background(),
			func(context.Context) error { <-entered; return nil },
			func(ctx context.Context) error {
				close(entered)
				<-ctx.Done()
				<-release
				steps = append(steps, "网页结束")
				return want
			},
			func() { steps = append(steps, "远程关闭") }, func() error { steps = append(steps, "文件清理"); return nil })
	}()
	<-entered
	select {
	case <-done:
		close(release)
		t.Fatal("服务没有等待网页退出")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-done; !errors.Is(err, want) {
		t.Fatalf("网页错误丢失：%v", err)
	}
	if !reflect.DeepEqual(steps, []string{"网页结束", "远程关闭", "文件清理"}) {
		t.Fatalf("清理顺序错误：%v", steps)
	}
}

func Test网页失败取消启动器并禁止不安全清理(t *testing.T) {
	want := errors.New("模拟网页未结束")
	var canceled bool
	err := runServices(context.Background(), func(ctx context.Context) error { <-ctx.Done(); canceled = true; return ctx.Err() },
		func(context.Context) error { return errors.Join(want, utils.ErrUnsafeCleanup) },
		func() { t.Error("活动请求未结束时关闭远程客户端") }, func() error { t.Error("活动请求未结束时清理文件"); return nil })
	if !canceled || !errors.Is(err, want) || !errors.Is(err, utils.ErrUnsafeCleanup) {
		t.Fatalf("关闭状态错误：%v", err)
	}
}

func Test正常取消不掩盖混合错误(t *testing.T) {
	if err := serviceError(errors.Join(context.Canceled)); err != nil {
		t.Fatalf("主动取消不应视为运行失败：%v", err)
	}
	want := errors.New("真实运行失败")
	if err := serviceError(errors.Join(context.Canceled, want)); !errors.Is(err, want) {
		t.Fatalf("混合错误被丢弃：%v", err)
	}
}
