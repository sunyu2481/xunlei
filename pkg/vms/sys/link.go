package sys

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"

	"github.com/cnk3x/xunlei/pkg/utils"
)

type LinkOptions struct {
	Target  string
	Source  string
	DirMode fs.FileMode
	// Optional 忽略操作错误，但仍返回本步准备目录的回滚句柄。
	Optional bool
	Copy     bool
}

func Links(ctx context.Context, items []LinkOptions) (undo Undo, err error) {
	return doMulti(ctx, items, Link)
}

func LinkRs(ctx context.Context, root string, items []LinkOptions) (undo Undo, err error) {
	for i := range items {
		if items[i].Target == "" {
			items[i].Target = filepath.Join(root, items[i].Source)
		}
	}
	return doMulti(ctx, items, Link)
}

// Link 链接普通文件；指定独立副本或跨设备时保留源文件的权限和属主。
// 已有目标保持原样；源缺失或其他失败仅在 Optional 为真时跳过。
func Link(ctx context.Context, m LinkOptions) (undo Undo, err error) {
	bq := utils.BackQueue(&undo, &err)
	defer bq.ErrDefer()

	var real string
	act := utils.Iif(m.Copy, "copy", "link")
	var targetExists bool
	err = func() (err error) {
		if real, err = filepath.EvalSymlinks(m.Source); err != nil {
			return
		}

		du, e := Mkdir(ctx, filepath.Dir(m.Target), m.DirMode)
		if err = e; err != nil {
			return
		}
		bq.Put(du)

		if !m.Copy {
			if err = os.Link(real, m.Target); !errors.Is(err, syscall.EXDEV) {
				act = "link"
				targetExists = os.IsExist(err)
				if err == nil {
					bq.Put(newRm(ctx, m.Target, "unlink"))
				}
				return
			}
		}

		act = "copy"
		var created bool
		if created, err = copyFile(real, m.Target, m.Copy); err == nil && created {
			bq.Put(newRm(ctx, m.Target, "uncopy"))
		}
		targetExists = os.IsExist(err)
		return
	}()

	attrs := []slog.Attr{
		slog.String("target", m.Target),
		slog.String("source", m.Source),
		slog.String("source_real", real),
		slog.Bool("optional", m.Optional),
	}

	if targetExists {
		attrs = append(attrs, slog.String("err", os.ErrExist.Error()))
		slog.LogAttrs(ctx, slog.LevelDebug, act+" skip", attrs...)
		err = nil
	} else {
		err = logIt(ctx, err, m.Optional, act, attrs...)
	}

	return
}

func Symlinks(ctx context.Context, items []LinkOptions) (undo Undo, err error) {
	return doMulti(ctx, items, Symlink)
}

// Symlink 保留已有目标，并允许链接指向尚未创建的路径；操作失败遵循 Optional。
func Symlink(ctx context.Context, m LinkOptions) (undo Undo, err error) {
	bq := utils.BackQueue(&undo, &err)
	defer bq.ErrDefer()
	var targetExists bool

	err = func() (err error) {
		var dirUndo Undo
		if dirUndo, err = Mkdir(ctx, filepath.Dir(m.Target), m.DirMode); err != nil {
			return
		}
		bq.Put(dirUndo)

		if err = os.Symlink(m.Source, m.Target); err != nil {
			targetExists = os.IsExist(err)
			return
		}
		bq.Put(newRm(ctx, m.Target, "unlink"))

		return
	}()

	attrs := []slog.Attr{
		slog.String("target", m.Target),
		slog.String("source", m.Source),
		slog.Bool("optional", m.Optional),
	}

	act := "symlink"

	if targetExists {
		attrs = append(attrs, slog.String("err", os.ErrExist.Error()))
		slog.LogAttrs(ctx, slog.LevelDebug, act+" skip", attrs...)
		err = nil
	} else {
		err = logIt(ctx, err, m.Optional, act, attrs...)
	}

	return
}
