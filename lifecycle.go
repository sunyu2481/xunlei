package xunlei

import (
	"context"
	"errors"
	"github.com/cnk3x/xunlei/pkg/cmdx"
	"syscall"

	"github.com/cnk3x/xunlei/pkg/utils"
)

// runServices 等待启动器和网页均结束，再关闭会话并清理运行文件。
func runServices(ctx context.Context, launch, serve func(context.Context) error, closeClient func(), cleanup func() error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	webDone := make(chan error, 1)
	go func() { err := serve(ctx); cancel(); webDone <- err }()
	launchErr := launch(ctx)
	cancel()
	err := errors.Join(serviceError(launchErr), serviceError(<-webDone))
	if errors.Is(err, utils.ErrUnsafeCleanup) {
		return err
	}
	closeClient()
	return errors.Join(err, cleanup())
}

// 主服务主动取消时允许进程组被终止；混合的业务或清理错误仍须返回。
func serviceError(err error) error {
	var canceledOnly func(error) bool
	canceledOnly = func(e error) bool {
		if e == nil || e == context.Canceled {
			return true
		}
		if joined, ok := e.(interface{ Unwrap() []error }); ok {
			for _, child := range joined.Unwrap() {
				if !canceledOnly(child) {
					return false
				}
			}
			return true
		}
		if wrapped, ok := e.(interface{ Unwrap() error }); ok {
			return canceledOnly(wrapped.Unwrap())
		}
		return cmdx.GetSignal(e) == syscall.SIGKILL
	}
	if errors.Is(err, context.Canceled) && canceledOnly(err) {
		return nil
	}
	return err
}
