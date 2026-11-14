package sys

import (
	"cmp"
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/cnk3x/xunlei/pkg/utils"
)

func Mkdirs(ctx context.Context, dirs []string, perm fs.FileMode) (undo Undo, err error) {
	return doMulti(ctx, dirs,
		func(ctx context.Context, dir string) (Undo, error) {
			return Mkdir(ctx, dir, perm)
		},
	)
}

// Mkdir 创建缺失目录并提供逆序回滚；已有目录及指向目录的链接保持不变。
func Mkdir(ctx context.Context, dir string, perm fs.FileMode) (undo Undo, err error) {
	if dir, err = filepath.Abs(dir); err != nil {
		return
	}

	bq := utils.BackQueue(&undo, &err)
	defer bq.ErrDefer()

	vol := cmp.Or(filepath.VolumeName(dir), "/")
	items := strings.FieldsFunc(strings.TrimPrefix(dir, vol), func(r rune) bool { return r == '/' })

	var ok bool
	for i := range items {
		p := filepath.Join(vol, filepath.Join(items[:i+1]...))
		if ok, err = mkdir(p, perm, os.Stat); err != nil {
			slog.WarnContext(ctx, "mkdir fail", "dir", p, "err", err)
			break
		}

		if ok {
			bq.Put(newRm(ctx, p, utils.Iif(i == len(items)-1, "rmdir", "")))
		}
	}

	return
}

func newRm(ctx context.Context, target, act string) func() {
	return func() {
		if err := os.Remove(target); act != "" {
			logIt(ctx, err, true, act, slog.String("target", target))
		}
	}
}

func mkdir(dir string, perm fs.FileMode, stat func(string) (fs.FileInfo, error)) (ok bool, err error) {
	if err = os.Mkdir(dir, perm); err == nil {
		ok = true
		return
	}

	if errors.Is(err, syscall.EEXIST) {
		// 仅查询已有路径，不删除或替换链接；查询失败保留原始错误。
		info, statErr := stat(dir)
		if statErr != nil {
			return false, statErr
		}
		if info.IsDir() {
			err = nil
		}
	}
	return
}
