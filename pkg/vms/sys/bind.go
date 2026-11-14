package sys

import (
	"context"
	"log/slog"
	"path/filepath"
	"syscall"

	"github.com/cnk3x/xunlei/pkg/utils"
)

type BindOptions struct {
	Source   string
	Target   string
	Optional bool
	ReadOnly bool
}

// Binds 按顺序绑定各项，直接使用其目标路径；失败时回滚，成功时返回逆序回滚函数。
func Binds(ctx context.Context, items []BindOptions) (undo Undo, err error) {
	return doMulti(ctx, items, Bind)
}

func BindRs(ctx context.Context, root string, items []BindOptions) (undo Undo, err error) {
	for i := range items {
		if items[i].Target == "" {
			items[i].Target = filepath.Join(root, items[i].Source)
		}
	}
	return doMulti(ctx, items, Bind)
}

// 绑定文件夹
func Bind(ctx context.Context, m BindOptions) (undo Undo, err error) {
	return bind(ctx, m, syscall.Mount, syscall.Unmount)
}

func bind(ctx context.Context, m BindOptions, mount func(string, string, string, uintptr, string) error, unmount func(string, int) error) (undo Undo, err error) {
	src := m.Source

	bq := utils.BackQueue(&undo, &err)
	defer bq.ErrDefer()

	if src, err = filepath.EvalSymlinks(src); err == nil {
		var dirUndo Undo
		if dirUndo, err = Mkdir(ctx, m.Target, 0777); err == nil {
			bq.Put(dirUndo)
			if err = mount(src, m.Target, "", syscall.MS_BIND, ""); err == nil {
				bq.Put(func() {
					logIt(ctx, unmount(m.Target, syscall.MNT_DETACH), false, "unbind", slog.String("target", m.Target))
				})
				if m.ReadOnly {
					var stat syscall.Statfs_t
					if err = syscall.Statfs(m.Target, &stat); err == nil {
						// 只读重挂载保留源挂载已有的安全和访问时间限制。
						flags := uintptr(syscall.MS_BIND | syscall.MS_REMOUNT | syscall.MS_RDONLY)
						flags |= uintptr(stat.Flags) & (syscall.MS_NOSUID | syscall.MS_NODEV | syscall.MS_NOEXEC | syscall.MS_NOATIME | syscall.MS_NODIRATIME | syscall.MS_RELATIME)
						err = mount("", m.Target, "", flags, "")
					}
					if err != nil {
						// 已完成绑定后，收紧权限失败必须回滚并阻止启动。
						return nil, err
					}
				}
			}
		}
	}

	attrs := []slog.Attr{
		slog.String("target", m.Target),
		slog.String("source", m.Source),
		slog.String("source_real", src),
	}

	err = logIt(ctx, err, m.Optional, "bind", attrs...)
	return
}
