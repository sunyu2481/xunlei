package vms

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"syscall"

	"github.com/cnk3x/xunlei/pkg/cmdx"
	"github.com/cnk3x/xunlei/pkg/utils"
)

type rollbackError struct{ err error }

func (e *rollbackError) Error() string { return e.err.Error() }
func (e *rollbackError) Unwrap() error { return e.err }

type rootCalls struct {
	fchdir func(int) error
	chroot func(string) error
	close  func(int) error
}

type rootState struct {
	ctx                     context.Context
	rootFD, cwdFD           int
	changedRoot, changedDir bool
	calls                   rootCalls
}

func (s *rootState) failure(name string, err error) error {
	if err == nil {
		return nil
	}
	err = fmt.Errorf("%s：%w", name, err)
	slog.WarnContext(s.ctx, "隔离恢复失败", "err", err)
	return &rollbackError{err}
}

func (s *rootState) restore() error {
	if s.changedRoot {
		// 定位失败时不能继续对当前目录调用 chroot，否则会得到错误的根目录。
		if err := s.failure("定位宿主机根目录", s.calls.fchdir(s.rootFD)); err != nil {
			return errors.Join(err, utils.ErrUnsafeCleanup)
		}
		if err := s.failure("恢复宿主机根目录", s.calls.chroot(".")); err != nil {
			return errors.Join(err, utils.ErrUnsafeCleanup)
		}
		s.changedRoot = false
	}
	if s.changedDir {
		if err := s.failure("恢复宿主机工作目录", s.calls.fchdir(s.cwdFD)); err != nil {
			return errors.Join(err, utils.ErrUnsafeCleanup)
		}
		s.changedDir = false
	}
	return nil
}

func (s *rootState) release() (err error) {
	// 即使恢复失败，也要尝试关闭所有保存句柄；关闭不依赖当前路径视角。
	for _, fd := range []*int{&s.rootFD, &s.cwdFD} {
		if *fd >= 0 {
			err = errors.Join(err, s.failure("关闭恢复句柄", s.calls.close(*fd)))
			*fd = -1
		}
	}
	return err
}

func chroot(ctx context.Context, root string) (state *rootState, err error) {
	state = &rootState{ctx: ctx, rootFD: -1, cwdFD: -1, calls: rootCalls{syscall.Fchdir, syscall.Chroot, syscall.Close}}
	if root == "" || root == "/" {
		return state, nil
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, state.restore(), state.release())
		}
	}()
	if err = checkUid("chroot"); err != nil {
		return
	}
	flags := syscall.O_RDONLY | syscall.O_DIRECTORY | syscall.O_CLOEXEC
	if state.rootFD, err = syscall.Open("/", flags, 0); err != nil {
		return
	}
	if state.cwdFD, err = syscall.Open(".", flags, 0); err != nil {
		return
	}
	if err = os.Chdir(root); err != nil {
		return
	}
	state.changedDir = true
	if err = syscall.Chroot("."); err != nil {
		return
	}
	state.changedRoot = true
	if err = os.Chdir("/"); err != nil {
		return
	}
	return
}

// chrootRun 在返回业务结果前恢复根目录与工作目录，并汇总关键回滚错误。
func chrootRun(ctx context.Context, root string, run func(context.Context) error, opts options) (err error) {
	state, err := chroot(ctx, root)
	if err != nil {
		return err
	}
	defer func() {
		if !errors.Is(err, utils.ErrUnsafeCleanup) {
			err = errors.Join(err, state.restore())
		}
		err = errors.Join(err, state.release())
	}()
	return run(cmdx.WithCredential(ctx, uint32(opts.uid), uint32(opts.gid)))
}
