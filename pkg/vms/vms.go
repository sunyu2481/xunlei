package vms

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"syscall"

	"github.com/cnk3x/xunlei/pkg/utils"
	"github.com/cnk3x/xunlei/pkg/vms/sys"
)

type Option func(ro *options)

type options struct {
	root    string
	uid     int
	gid     int
	wait    bool
	userErr error

	mounts  []sys.MountOptions
	binds   []sys.BindOptions
	links   []sys.LinkOptions
	symbols []sys.LinkOptions

	before func(ctx context.Context) (undo func(), err error)
	after  func(ctx context.Context, err error) error
	run    func(ctx context.Context) error
}

// Execute 通过根目录切换实现文件系统隔离。
//
// 父进程保留准备和回滚所需的身份，业务子进程通过上下文中的凭据降权。
func Execute(ctx context.Context, execOpts ...Option) (err error) {
	opts := newOptions(execOpts...)
	if opts.userErr != nil {
		return opts.userErr
	}

	cleanup := func(undo func()) {
		if !errors.Is(err, utils.ErrUnsafeCleanup) && undo != nil {
			undo()
		}
	}

	if opts.root != "" && opts.root != "/" {
		//mounts
		unmounts, e := sys.Mounts(ctx, opts.mounts)
		if err = e; err != nil {
			return
		}
		defer cleanup(unmounts)

		//binds
		unbinds, e := sys.BindRs(ctx, opts.root, opts.binds)
		if err = e; err != nil {
			return
		}
		defer cleanup(unbinds)

		//links
		unlinks, e := sys.LinkRs(ctx, opts.root, opts.links)
		if err = e; err != nil {
			return
		}
		defer cleanup(unlinks)

		unSymlinks, e := sys.Symlinks(ctx, opts.symbols)
		if err = e; err != nil {
			return
		}
		defer cleanup(unSymlinks)
	}

	//before
	if opts.before != nil {
		beforeRestore, e := opts.before(ctx)
		if err = e; err != nil {
			return
		}
		defer cleanup(beforeRestore)
	}

	//chroot
	err = chrootRun(ctx, opts.root, opts.run, opts)

	if errors.Is(err, utils.ErrUnsafeCleanup) {
		return err
	}
	if err != nil && opts.wait {
		slog.ErrorContext(ctx, "chroot run", "err", err.Error())
		slog.WarnContext(ctx, "调试模式，在有错误发生的情况下，会一直不退出，等待人工调试，请按 ctrl+c 退出.")
		<-ctx.Done()
	}

	//after
	if opts.after != nil {
		afterErr := opts.after(ctx, err)
		var recovery *rollbackError
		if errors.As(err, &recovery) {
			err = errors.Join(err, afterErr)
		} else {
			err = afterErr
		}
	}
	return
}

func checkUid(name string) (err error) {
	if syscall.Getuid() != 0 {
		err = fmt.Errorf("%s: only the root process (UID=0) supports.", name)
	}
	return
}
