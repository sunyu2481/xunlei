package sys

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"syscall"

	"github.com/cnk3x/xunlei/pkg/utils"
)

type MountOptions struct {
	Target   string
	Source   string
	Fstype   string
	Flags    uintptr
	Data     string
	Optional bool
	Root     string
}

func Mounts(ctx context.Context, mounts []MountOptions) (undo Undo, err error) {
	return doMulti(ctx, mounts, Mount)
}

// 完整的绑定
func Mount(ctx context.Context, m MountOptions) (undo Undo, err error) {
	return mount(ctx, m, mountCalls{syscall.Mount, syscall.Unmount, syscall.Statfs})
}

type mountCalls struct {
	mount   func(string, string, string, uintptr, string) error
	unmount func(string, int) error
	statfs  func(string, *syscall.Statfs_t) error
}

func mount(ctx context.Context, m MountOptions, calls mountCalls) (undo Undo, err error) {
	defer func() {
		if err != nil {
			slog.DebugContext(ctx, string(utils.Eon(json.Marshal(m))))
		}
	}()

	bq := utils.BackQueue(&undo, &err)
	defer bq.ErrDefer()

	var dirUndo Undo
	if dirUndo, err = Mkdir(ctx, m.Target, 0777); err == nil {
		bq.Put(dirUndo)
		err = calls.mount(m.Source, m.Target, m.Fstype, m.Flags, m.Data)
		if err == nil {
			bq.Put(mkUnmount(ctx, m.Target, "unmount", calls.unmount))
		} else if m.Fstype == "devtmpfs" && err == syscall.ENODEV {
			// 内核未提供设备文件系统时，复用现有设备目录及其子挂载。
			devtmpfsErr := err
			err = calls.mount("/dev", m.Target, "", uintptr(syscall.MS_BIND|syscall.MS_REC), "")
			if err == nil {
				bq.Put(mkUnmount(ctx, m.Target, "unmount", calls.unmount))
				if m.Flags != 0 {
					var stat syscall.Statfs_t
					if err = calls.statfs(m.Target, &stat); err == nil {
						// 绑定不会应用权限标志；只重挂载目标，保留源已有约束。
						flags := uintptr(stat.Flags) & (syscall.MS_RDONLY | syscall.MS_NOSUID | syscall.MS_NODEV | syscall.MS_NOEXEC | syscall.MS_NOATIME | syscall.MS_NODIRATIME | syscall.MS_RELATIME)
						err = calls.mount("", m.Target, "", syscall.MS_BIND|syscall.MS_REMOUNT|m.Flags|flags, "")
					}
					if err != nil {
						// 绑定已建立后收紧失败必须回滚，不能按可选项忽略。
						return nil, fmt.Errorf("收紧设备回退挂载权限失败：%w", err)
					}
				}
				slog.WarnContext(ctx, "devtmpfs unavailable, fallback to rbind /dev",
					slog.String("target", m.Target),
					slog.String("err", devtmpfsErr.Error()))
			} else {
				err = fmt.Errorf("mount devtmpfs failed: %v; fallback rbind /dev failed: %w",
					devtmpfsErr, err)
			}
		}
	}

	err = logIt(ctx, err, m.Optional, "mount",
		slog.String("target", m.Target),
		slog.String("source", m.Source),
		slog.Bool("optional", m.Optional))
	return
}

func mkUnmount(ctx context.Context, target, act string, unmount func(string, int) error) Undo {
	return func() {
		err := unmount(target, syscall.MNT_DETACH|syscall.MNT_FORCE)
		if err != nil {
			if os.IsNotExist(err) {
				slog.LogAttrs(ctx, slog.LevelWarn, act, slog.String("target", target), slog.String("err", os.ErrNotExist.Error()))
			} else {
				slog.LogAttrs(ctx, slog.LevelWarn, act, slog.String("target", target), slog.String("err", err.Error()))
			}
			return
		}
		slog.LogAttrs(ctx, slog.LevelDebug, act, slog.String("target", target))
	}
}
