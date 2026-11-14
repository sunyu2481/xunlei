package fo

import (
	"cmp"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// AtomicWrite 在同一文件系统的私有临时目录中完成写入、同步及关闭后发布。
// 支持覆盖和排他创建，不支持追加；失败不会截断已有文件。
func AtomicWrite(name string, process Process, fopts ...Option) error {
	opts := options{flag: os.O_CREATE | os.O_WRONLY | os.O_TRUNC}
	for _, apply := range fopts {
		apply(&opts)
	}
	if opts.flag&os.O_APPEND != 0 {
		return fmt.Errorf("原子写入不支持追加模式")
	}
	if opts.flag&os.O_EXCL != 0 {
		if _, err := os.Lstat(name); err == nil {
			if opts.existOk {
				return nil
			}
			return &os.PathError{Op: "创建", Path: name, Err: os.ErrExist}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	temp, cleanup, err := prepareFile(name, process, opts)
	if err != nil {
		return err
	}
	defer cleanup()
	if opts.flag&os.O_EXCL != 0 {
		err := os.Link(temp, name)
		if opts.existOk && os.IsExist(err) {
			return nil
		}
		return err
	}
	return os.Rename(temp, name)
}

func prepareFile(name string, process Process, opts options) (temp string, cleanup func(), err error) {
	if err := os.MkdirAll(filepath.Dir(name), cmp.Or(opts.dirPerm, 0777)); err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp(filepath.Dir(name), ".xlp-file-*")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { os.RemoveAll(dir) }
	defer func() {
		if err != nil {
			cleanup()
		}
	}()
	temp = filepath.Join(dir, "内容")
	f, err := os.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_RDWR, cmp.Or(opts.perm, 0666))
	if err != nil {
		return "", cleanup, err
	}
	err = process(f)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	return temp, cleanup, err
}

// MatchFile 校验已有文件是否与生成内容及权限一致，不改写已有文件。
func MatchFile(name string, process Process, fopts ...Option) error {
	opts := options{}
	for _, apply := range fopts {
		apply(&opts)
	}
	temp, cleanup, err := prepareFile(name, process, opts)
	if err != nil {
		return err
	}
	defer cleanup()
	want, err := os.Stat(temp)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	got, err := f.Stat()
	if err != nil {
		return err
	}
	if !got.Mode().IsRegular() || got.Size() != want.Size() || got.Mode().Perm() != want.Mode().Perm() {
		return fmt.Errorf("已有文件 %q 的类型、大小或权限与生成内容不符", name)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return err
	}
	actual := string(hash.Sum(nil))
	hash.Reset()
	expected, err := os.Open(temp)
	if err != nil {
		return err
	}
	defer expected.Close()
	if _, err := io.Copy(hash, expected); err != nil {
		return err
	}
	if actual != string(hash.Sum(nil)) {
		return fmt.Errorf("已有文件 %q 内容不完整或与生成内容不符", name)
	}
	return nil
}
