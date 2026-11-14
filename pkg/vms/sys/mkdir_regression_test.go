package sys

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func Test创建目录保留已有链接(t *testing.T) {
	for _, relative := range []bool{false, true} {
		t.Run(map[bool]string{false: "绝对链接", true: "相对链接"}[relative], func(t *testing.T) {
			base := t.TempDir()
			target, link := filepath.Join(base, "原目录"), filepath.Join(base, "链接")
			if err := os.Mkdir(target, 0700); err != nil {
				t.Fatal(err)
			}
			data := filepath.Join(target, "原数据")
			if err := os.WriteFile(data, []byte("保留内容"), 0600); err != nil {
				t.Fatal(err)
			}
			linkTarget := target
			if relative {
				linkTarget = filepath.Base(target)
			}
			if err := os.Symlink(linkTarget, link); err != nil {
				t.Fatal(err)
			}
			undo, err := Mkdir(context.Background(), filepath.Join(link, "新增", "末级"), 0700)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := os.Readlink(link); err != nil || got != linkTarget {
				t.Fatalf("已有链接被替换：%q，%v", got, err)
			}
			if info, err := os.Stat(filepath.Join(target, "新增", "末级")); err != nil || !info.IsDir() {
				t.Fatalf("没有通过原链接创建目录：%v", err)
			}
			undo()
			if got, err := os.Readlink(link); err != nil || got != linkTarget {
				t.Fatalf("回滚移除了已有链接：%q，%v", got, err)
			}
			if _, err := os.Stat(filepath.Join(target, "新增")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("新增空目录没有回滚：%v", err)
			}
			if got, err := os.ReadFile(data); err != nil || string(got) != "保留内容" {
				t.Fatalf("原数据被改动：%q，%v", got, err)
			}
		})
	}
}

func Test创建目录拒绝无效链接(t *testing.T) {
	for _, kind := range []string{"目标缺失", "普通文件", "循环链接"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			target, link := filepath.Join(base, "目标"), filepath.Join(base, "链接")
			if kind == "普通文件" {
				if err := os.WriteFile(target, []byte("原文件"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "循环链接" {
				target = link
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			_, err := Mkdir(context.Background(), link, 0700)
			if err == nil {
				t.Error("无效目录链接必须报错")
			}
			if got, err := os.Readlink(link); err != nil || got != target {
				t.Fatalf("失败后已有链接被改动：%q，%v", got, err)
			}
		})
	}
}

func Test创建目录返回查询错误(t *testing.T) {
	for _, want := range []error{syscall.ENOENT, syscall.EACCES, syscall.EIO} {
		t.Run(want.Error(), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "目录")
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			queried := false
			created, err := mkdir(path, 0700, func(name string) (fs.FileInfo, error) {
				queried = true
				if name != path {
					t.Fatalf("查询了错误路径：%q", name)
				}
				if want == syscall.ENOENT {
					// 确定性模拟 Mkdir 返回已存在之后、查询之前路径消失。
					if err := os.Remove(name); err != nil {
						t.Fatal(err)
					}
					return os.Stat(name)
				}
				return nil, &os.PathError{Op: "stat", Path: name, Err: want}
			})
			if !queried || created || !errors.Is(err, want) {
				t.Fatalf("查询失败未保留：已查询=%t，新建=%t，错误=%v", queried, created, err)
			}
		})
	}
}

func Test目录回滚保留非空目录和已有路径(t *testing.T) {
	base := t.TempDir()
	newDir := filepath.Join(base, "新增", "末级")
	undo, err := Mkdir(context.Background(), newDir, 0700)
	if err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(newDir, "新增数据")
	if err := os.WriteFile(data, []byte("保留内容"), 0600); err != nil {
		t.Fatal(err)
	}
	undo()
	if got, err := os.ReadFile(data); err != nil || string(got) != "保留内容" {
		t.Fatalf("非空目录被清理：%q，%v", got, err)
	}
	if info, err := os.Stat(base); err != nil || !info.IsDir() {
		t.Fatalf("已有目录被清理：%v", err)
	}
}

func Test批量创建失败回滚新增目录(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "已有文件")
	if err := os.WriteFile(file, []byte("保留内容"), 0600); err != nil {
		t.Fatal(err)
	}
	created := filepath.Join(base, "新增")
	_, err := Mkdirs(context.Background(), []string{filepath.Join(created, "末级"), file}, 0700)
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("非目录冲突没有上报：%v", err)
	}
	if _, err := os.Stat(created); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("失败未清理前序新增目录：%v", err)
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != "保留内容" {
		t.Fatalf("已有文件被改动：%q，%v", got, err)
	}
}
