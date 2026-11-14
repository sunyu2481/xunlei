package sys

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
)

// Chown 更改指定文件的数字用户和组，零值表示保留原值。
// 链接仅修改链接本身；递归时使用目录句柄，避免路径替换后跟随外部链接。
func Chown(ctx context.Context, paths []string, uid, gid int, recursive bool) error {
	uid, gid = cmp.Or(uid, -1), cmp.Or(gid, -1)
	if uid == -1 && gid == -1 {
		return nil
	}
	for _, path := range paths {
		path = filepath.Clean(path)
		fd, err := syscall.Open(filepath.Dir(path), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if err != nil {
			return fmt.Errorf("打开改属主目录 %q：%w", filepath.Dir(path), err)
		}
		err = chownAt(ctx, fd, filepath.Base(path), path, uid, gid, recursive)
		closeErr := syscall.Close(fd)
		if err != nil {
			return err
		}
		if closeErr != nil {
			return fmt.Errorf("关闭改属主目录 %q：%w", path, closeErr)
		}
	}
	return nil
}

func chownAt(ctx context.Context, parent int, name, path string, uid, gid int, recursive bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// 此标志在 Linux 两种受支持架构上均相同，标准库未导出对应常量。
	const atSymlinkNoFollow = 0x100
	var err error
	if recursive {
		var fd int
		fd, err = syscall.Openat(parent, name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if err == nil {
			dir := os.NewFile(uintptr(fd), path)
			defer dir.Close()
			var entries []os.DirEntry
			entries, err = dir.ReadDir(-1)
			if err != nil {
				return fmt.Errorf("遍历改属主目录 %q：%w", path, err)
			}
			for _, entry := range entries {
				if err := chownAt(ctx, fd, entry.Name(), filepath.Join(path, entry.Name()), uid, gid, true); err != nil {
					return err
				}
			}
			err = dir.Chown(uid, gid)
		} else if errors.Is(err, syscall.ENOTDIR) || errors.Is(err, syscall.ELOOP) {
			err = syscall.Fchownat(parent, name, uid, gid, atSymlinkNoFollow)
		}
	} else {
		err = syscall.Fchownat(parent, name, uid, gid, atSymlinkNoFollow)
	}
	if err != nil {
		return fmt.Errorf("修改属主 %q 为 %d:%d：%w", path, uid, gid, err)
	}
	slog.DebugContext(ctx, "chown", "uid", uid, "gid", gid, "path", path)
	return nil
}
