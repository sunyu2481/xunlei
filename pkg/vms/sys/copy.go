package sys

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// copyFile 先在目标目录中完成受限权限副本，再原子发布，失败不留下半成品。
func copyFile(source, target string, replaceShared bool) (created bool, err error) {
	src, err := os.Open(source)
	if err != nil {
		return false, err
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("复制源不是普通文件：%s", source)
	}
	dst, err := os.CreateTemp(filepath.Dir(target), ".xlp-copy-*")
	if err != nil {
		return false, err
	}
	defer os.Remove(dst.Name())
	defer dst.Close()
	if _, err := io.Copy(dst, src); err != nil {
		return false, err
	}
	stat := info.Sys().(*syscall.Stat_t)
	if err := dst.Chown(int(stat.Uid), int(stat.Gid)); err != nil {
		return false, err
	}
	if err := dst.Chmod(info.Mode().Perm()); err != nil {
		return false, err
	}
	if err := dst.Sync(); err != nil {
		return false, err
	}
	if err := dst.Close(); err != nil {
		return false, err
	}
	if replaceShared {
		old, err := os.Lstat(target)
		if err == nil && os.SameFile(info, old) {
			// 上次异常退出遗留的共享文件需要解除共享；回滚也不恢复不安全的硬链接。
			return false, os.Rename(dst.Name(), target)
		}
		if err != nil && !os.IsNotExist(err) {
			return false, err
		}
	}
	// 同目录临时文件的硬链接只用于原子发布，随后删除临时名字。
	if err := os.Link(dst.Name(), target); err != nil {
		return false, err
	}
	return true, nil
}
