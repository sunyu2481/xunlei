package vms

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"syscall"

	"github.com/cnk3x/xunlei/pkg/cmdx"
	"github.com/cnk3x/xunlei/pkg/utils"
	"github.com/cnk3x/xunlei/pkg/vms/sys"
)

// type Undo func()
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
	var opts options
	for _, option := range execOpts {
		option(&opts)
	}
	if opts.userErr != nil {
		return opts.userErr
	}

	if opts.root != "" && opts.root != "/" {
		//mounts
		unmounts, e := sys.Mounts(ctx, opts.mounts)
		if err = e; err != nil {
			return
		}
		defer unmounts()

		//binds
		unbinds, e := sys.BindRs(ctx, opts.root, opts.binds)
		if err = e; err != nil {
			return
		}
		defer unbinds()

		//links
		unlinks, e := sys.LinkRs(ctx, opts.root, opts.links)
		if err = e; err != nil {
			return
		}
		defer unlinks()

		unSymlinks, e := sys.Symlinks(ctx, opts.symbols)
		if err = e; err != nil {
			return
		}
		defer unSymlinks()
	}

	//before
	if opts.before != nil {
		beforeRestore, e := opts.before(ctx)
		if err = e; err != nil {
			return
		}
		defer beforeRestore()
	}

	//chroot
	err = chrootRun(ctx, opts.root, opts.run, opts)

	if err != nil && opts.wait {
		slog.ErrorContext(ctx, "chroot run", "err", err.Error())
		slog.WarnContext(ctx, "调试模式，在有错误发生的情况下，会一直不退出，等待人工调试，请按 ctrl+c 退出.")
		<-ctx.Done()
	}

	//after
	if opts.after != nil {
		if err = opts.after(ctx, err); err != nil {
			return
		}
	}
	return
}

// chroot & run
func chrootRun(ctx context.Context, root string, run func(ctx context.Context) error, opts options) (err error) {
	chRootBack, e := chroot(ctx, root)
	if err = e; err != nil {
		return
	}
	defer chRootBack()

	err = run(cmdx.WithCredential(ctx, uint32(opts.uid), uint32(opts.gid)))
	return
}

func chroot(ctx context.Context, root string) (rollback func(), err error) {
	slog.InfoContext(ctx, "chroot start", "root", root)
	defer slog.InfoContext(ctx, "chroot done")

	bq := utils.BackQueue(&rollback, &err)
	defer bq.ErrDefer()

	bq.Put(func() { slog.InfoContext(ctx, "chroot rollback done") })
	defer bq.Put(func() { slog.InfoContext(ctx, "chroot rollback start") })

	if root != "" && root != "/" {
		if err = errcheck(ctx, slog.LevelDebug, "check root", checkUid("chroot")); err != nil {
			return
		}

		wd, e := os.Getwd()
		if err = errcheck(ctx, slog.LevelDebug, "getwd", e, "wd", wd); err != nil {
			return
		}

		fd, e := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
		if err = errcheck(ctx, slog.LevelDebug, "openFd", e, "dir", "/", "fd", fd); err != nil {
			return
		}
		bq.Put(func() { errcheck(ctx, slog.LevelDebug, "closeFd", syscall.Close(fd), "fd", fd) })

		if err = errcheck(ctx, slog.LevelDebug, "chdir", os.Chdir(root), "dir", root); err != nil {
			return
		}
		bq.Put(func() { errcheck(ctx, slog.LevelDebug, "chdir", os.Chdir(wd), "dir", wd) })

		if err = errcheck(ctx, slog.LevelDebug, "chroot", syscall.Chroot("."), "dir", "."); err != nil {
			return
		}
		bq.Put(func() { errcheck(ctx, slog.LevelDebug, "chroot", syscall.Chroot("."), "fd", fd) })
		bq.Put(func() { errcheck(ctx, slog.LevelDebug, "fchdir", syscall.Fchdir(fd), "fd", fd) })

		if err = errcheck(ctx, slog.LevelDebug, "chdir", os.Chdir("/"), "dir", "/"); err != nil {
			return
		}
	}
	return
}

func errcheck(ctx context.Context, level slog.Level, name string, err error, args ...any) error {
	if err == nil {
		slog.Log(ctx, level, "call "+name, args...)
	} else {
		slog.Log(ctx, utils.Iif(level < 0, level, slog.LevelWarn), "call "+name, append(args, "err", err.Error())...)
	}
	return err
}

func checkUid(name string) (err error) {
	if syscall.Getuid() != 0 {
		err = fmt.Errorf("%s: only the root process (UID=0) supports.", name)
	}
	return
}
