package spk

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// 逐层打开目录并拒绝链接，之后的创建、检查和替换都相对于目录句柄执行。
func openDirectory(parent *os.File, name string) (*os.File, error) {
	return openDirectoryMode(parent, name, true)
}

func openDirectoryMode(parent *os.File, name string, create bool) (*os.File, error) {
	flags := syscall.O_RDONLY | syscall.O_DIRECTORY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC
	fd, err := syscall.Openat(int(parent.Fd()), name, flags, 0)
	if create && errors.Is(err, syscall.ENOENT) {
		if err = syscall.Mkdirat(int(parent.Fd()), name, 0755); err != nil && !errors.Is(err, syscall.EEXIST) {
			return nil, fmt.Errorf("创建套件目录 %q：%w", name, err)
		}
		fd, err = syscall.Openat(int(parent.Fd()), name, flags, 0)
	}
	if err != nil {
		return nil, fmt.Errorf("打开套件目录 %q：%w", name, err)
	}
	return os.NewFile(uintptr(fd), name), nil
}

func openDestination(name string) (*os.File, error) {
	return openDestinationMode(name, true)
}

func openDestinationMode(name string, create bool) (*os.File, error) {
	abs, err := filepath.Abs(name)
	if err != nil {
		return nil, err
	}
	dir, err := os.Open("/")
	if err != nil {
		return nil, err
	}
	for part := range strings.SplitSeq(strings.TrimPrefix(abs, "/"), "/") {
		if part == "" {
			continue
		}
		next, err := openDirectoryMode(dir, part, create)
		dir.Close()
		if err != nil {
			return nil, err
		}
		dir = next
	}
	return dir, nil
}

func openFileAt(dir *os.File, name string, flags int, mode uint32) (*os.File, error) {
	fd, err := syscall.Openat(int(dir.Fd()), name, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, mode)
	if err != nil {
		return nil, fmt.Errorf("打开套件文件 %q：%w", name, err)
	}
	return os.NewFile(uintptr(fd), name), nil
}

func newStage(root *os.File) (*os.File, func(), error) {
	name := ".spk-" + rand.Text()
	if err := syscall.Mkdirat(int(root.Fd()), name, 0700); err != nil {
		return nil, nil, err
	}
	stage, err := openDirectory(root, name)
	if err != nil {
		removeDirectoryAt(root, name)
		return nil, nil, err
	}
	var st syscall.Stat_t
	if err = syscall.Fstat(int(stage.Fd()), &st); err != nil || st.Uid != uint32(os.Geteuid()) || st.Mode&0777 != 0700 {
		stage.Close()
		removeDirectoryAt(root, name)
		return nil, nil, fmt.Errorf("套件临时目录的属主或权限不可信：%v", err)
	}
	cleanup := func() {
		entries, _ := stage.ReadDir(-1)
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "backup-") {
				// 恢复失败的旧文件须留给后续人工恢复，不能作为临时文件删除。
				slog.Error("保留未恢复的套件备份", "stage", name)
				stage.Close()
				return
			}
		}
		for _, entry := range entries {
			syscall.Unlinkat(int(stage.Fd()), entry.Name())
		}
		stage.Close()
		removeDirectoryAt(root, name)
	}
	return stage, cleanup, nil
}

func removeDirectoryAt(parent *os.File, name string) {
	p, err := syscall.BytePtrFromString(name)
	if err != nil {
		return
	}
	// 标准库未导出带 AT_REMOVEDIR 的 Unlinkat；两种受支持架构均使用此标志。
	const atRemoveDir = 0x200
	syscall.Syscall(syscall.SYS_UNLINKAT, parent.Fd(), uintptr(unsafe.Pointer(p)), atRemoveDir)
}

func backupFile(ctx context.Context, source *os.File, name string, stage *os.File, backup string) (err error) {
	src, err := openFileAt(source, name, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("备份源不是普通文件")
	}
	dst, err := openFileAt(stage, backup, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, dst.Close())
		if err != nil {
			err = errors.Join(err, syscall.Unlinkat(int(stage.Fd()), backup))
		}
	}()
	if _, err := io.Copy(dst, contextReader{ctx, src}); err != nil {
		return err
	}
	st := info.Sys().(*syscall.Stat_t)
	if err := dst.Chown(int(st.Uid), int(st.Gid)); err != nil {
		return err
	}
	if err := dst.Chmod(info.Mode()); err != nil {
		return err
	}
	return dst.Sync()
}

func installFiles(ctx context.Context, root, stage *os.File, names []string, files map[string]string) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	dirs := map[string]*os.File{".": root}
	defer func() {
		for name, dir := range dirs {
			if name != "." {
				dir.Close()
			}
		}
	}()
	for _, name := range []string{"bin", "bin/bin", "ui"} {
		dir, err := openDirectory(dirs[path.Dir(name)], path.Base(name))
		if err != nil {
			return err
		}
		dirs[name] = dir
	}
	type change struct {
		name, backup              string
		existed, saved, published bool
	}
	changes := make([]change, 0, len(names))
	// 所有目标和暂存文件均先完成检查、改权限、同步及关闭，再开始替换。
	for i, name := range names {
		item := change{name: name, backup: fmt.Sprintf("backup-%d", i)}
		f, err := openFileAt(dirs[path.Dir(name)], path.Base(name), os.O_RDONLY, 0)
		if err != nil && !errors.Is(err, syscall.ENOENT) {
			return err
		}
		if err == nil {
			var st syscall.Stat_t
			err = syscall.Fstat(int(f.Fd()), &st)
			err = errors.Join(err, f.Close())
			if err != nil {
				return err
			}
			if st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Nlink != 1 {
				return fmt.Errorf("套件目标 %q 不是独立的普通文件", name)
			}
			item.existed = true
		}
		f, err = openFileAt(stage, files[name], os.O_RDONLY, 0)
		if err != nil {
			return err
		}
		perm := os.FileMode(0755)
		if name == versionFile || name == manifestFile {
			perm = 0644
		}
		err = f.Chmod(perm)
		if err == nil {
			err = f.Sync()
		}
		err = errors.Join(err, f.Close())
		if err != nil {
			return err
		}
		changes = append(changes, item)
	}
	if err := stage.Sync(); err != nil {
		return err
	}
	defer func() {
		if err == nil {
			return
		}
		// 使用完整备份回滚，保留旧内容、权限及属主；恢复失败时留下备份。
		for i := len(changes) - 1; i >= 0; i-- {
			item := changes[i]
			dir := dirs[path.Dir(item.name)]
			var restoreErr error
			if item.saved && item.published {
				restoreErr = syscall.Renameat(int(stage.Fd()), item.backup, int(dir.Fd()), path.Base(item.name))
			} else if item.saved {
				restoreErr = syscall.Unlinkat(int(stage.Fd()), item.backup)
			} else if item.published {
				restoreErr = syscall.Unlinkat(int(dir.Fd()), path.Base(item.name))
			}
			if restoreErr != nil {
				err = errors.Join(err, fmt.Errorf("恢复套件文件 %q：%w", item.name, restoreErr))
			}
		}
		for _, dir := range dirs {
			err = errors.Join(err, dir.Sync())
		}
		err = errors.Join(err, stage.Sync())
	}()
	for i := range changes {
		if err := ctx.Err(); err != nil {
			return err
		}
		item := &changes[i]
		dir := dirs[path.Dir(item.name)]
		if item.existed {
			// 独立备份不会让异常退出留下的硬链接影响后续缓存与目标检查。
			if err := backupFile(ctx, dir, path.Base(item.name), stage, item.backup); err != nil {
				return fmt.Errorf("备份套件文件 %q：%w", item.name, err)
			}
			item.saved = true
			if err := stage.Sync(); err != nil {
				return err
			}
		}
		if err := syscall.Renameat(int(stage.Fd()), files[item.name], int(dir.Fd()), path.Base(item.name)); err != nil {
			return fmt.Errorf("安装套件文件 %q：%w", item.name, err)
		}
		item.published = true
		if err := dir.Sync(); err != nil {
			return err
		}
	}
	for _, item := range changes {
		if item.saved {
			if e := syscall.Unlinkat(int(stage.Fd()), item.backup); e != nil {
				slog.Warn("安装成功但旧备份清理失败", "file", item.name, "err", e)
			}
		}
	}
	return nil
}
