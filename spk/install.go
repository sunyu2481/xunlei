package spk

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// 逐层打开目录并拒绝链接，之后的创建、检查和替换都相对于目录句柄执行。
func openDirectory(parent *os.File, name string) (*os.File, error) {
	flags := syscall.O_RDONLY | syscall.O_DIRECTORY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC
	fd, err := syscall.Openat(int(parent.Fd()), name, flags, 0)
	if errors.Is(err, syscall.ENOENT) {
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
		next, err := openDirectory(dir, part)
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

func installFiles(root, stage *os.File, names []string, files map[string]string) error {
	dirs := make(map[string]*os.File)
	defer func() {
		for _, dir := range dirs {
			dir.Close()
		}
	}()
	for _, name := range []string{"bin", "bin/bin", "ui"} {
		parent := root
		if dir := path.Dir(name); dir != "." {
			parent = dirs[dir]
		}
		dir, err := openDirectory(parent, path.Base(name))
		if err != nil {
			return err
		}
		dirs[name] = dir
	}
	// 先检查所有目标，拒绝已有链接及特殊文件，不对旧文件截断或改权限。
	for _, name := range names {
		f, err := openFileAt(dirs[path.Dir(name)], path.Base(name), os.O_RDONLY, 0)
		if errors.Is(err, syscall.ENOENT) {
			continue
		}
		if err != nil {
			return err
		}
		var st syscall.Stat_t
		err = syscall.Fstat(int(f.Fd()), &st)
		f.Close()
		if err != nil {
			return err
		}
		if st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Nlink != 1 {
			return fmt.Errorf("套件目标 %q 不是独立的普通文件", name)
		}
	}
	for _, name := range names {
		temp := files[name]
		f, err := openFileAt(stage, temp, os.O_RDONLY, 0)
		if err != nil {
			return err
		}
		perm := os.FileMode(0755)
		if name == versionFile {
			perm = 0644
		}
		err = f.Chmod(perm)
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		if err := syscall.Renameat(int(stage.Fd()), temp, int(dirs[path.Dir(name)].Fd()), path.Base(name)); err != nil {
			return fmt.Errorf("安装套件文件 %q：%w", name, err)
		}
	}
	return nil
}
