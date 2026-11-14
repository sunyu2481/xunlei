package sys

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func Test链接缺失源遵循可选约定(t *testing.T) {
	for _, copy := range []bool{false, true} {
		for _, optional := range []bool{false, true} {
			name := map[bool]string{false: "硬链接", true: "副本"}[copy] + map[bool]string{false: "必需", true: "可选"}[optional]
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				target := filepath.Join(dir, "新目录", "目标")
				undo, err := Link(context.Background(), LinkOptions{
					Source: filepath.Join(dir, "缺失源"), Target: target, Copy: copy, Optional: optional, DirMode: 0700,
				})
				if optional {
					if err != nil {
						t.Fatalf("可选源缺失应跳过：%v", err)
					}
					if undo == nil {
						t.Fatal("缺少回滚句柄")
					}
					undo()
				} else if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("必需源缺失应返回不存在错误：%v", err)
				}
				if _, err := os.Lstat(filepath.Dir(target)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("缺失源仍留下新目录：%v", err)
				}
			})
		}
	}
}

func Test链接失败遵循可选及回滚约定(t *testing.T) {
	for _, kind := range []string{"硬链接", "副本", "符号链接"} {
		for _, optional := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "必需", true: "可选"}[optional], func(t *testing.T) {
				dir := t.TempDir()
				target := filepath.Join(dir, "新目录", "目标")
				operation := Link
				options := LinkOptions{Source: dir, Target: target, DirMode: 0700, Optional: optional, Copy: kind == "副本"}
				if kind == "符号链接" {
					operation = Symlink
					options.Source = "无效\x00源"
				}
				undo, err := operation(context.Background(), options)
				if optional {
					if err != nil {
						t.Fatalf("可选失败应跳过：%v", err)
					}
					if undo == nil {
						t.Fatal("可选失败丢失本步回滚句柄")
					}
					if _, err := os.Stat(filepath.Dir(target)); err != nil {
						t.Fatalf("可选失败应保留准备目录直到回滚：%v", err)
					}
					undo()
				} else if err == nil {
					t.Fatal("必需失败未返回错误")
				}
				if _, err := os.Lstat(filepath.Dir(target)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("本步目录未被回滚：%v", err)
				}
			})
		}
	}
}

func Test链接区分已有目标与父路径冲突(t *testing.T) {
	for _, kind := range []string{"硬链接", "副本", "符号链接"} {
		for _, conflict := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "已有目标", true: "父路径冲突"}[conflict], func(t *testing.T) {
				dir := t.TempDir()
				source, existing := filepath.Join(dir, "源"), filepath.Join(dir, "已有文件")
				if err := os.WriteFile(source, []byte("源内容"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(existing, []byte("保留内容"), 0600); err != nil {
					t.Fatal(err)
				}
				target := existing
				if conflict {
					target = filepath.Join(existing, "目标")
				}
				operation := Link
				if kind == "符号链接" {
					operation = Symlink
				}
				undo, err := operation(context.Background(), LinkOptions{Source: source, Target: target, Copy: kind == "副本", DirMode: 0700})
				if conflict {
					if !errors.Is(err, os.ErrExist) {
						t.Fatalf("父路径冲突不能当作目标已存在跳过：%v", err)
					}
				} else {
					if err != nil {
						t.Fatalf("已有目标应保持原样并跳过：%v", err)
					}
					undo()
				}
				if data, err := os.ReadFile(existing); err != nil || string(data) != "保留内容" {
					t.Fatalf("已有文件被修改或删除：%q，错误：%v", data, err)
				}
			})
		}
	}
}

func Test符号链接允许预设缺失路径(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "新目录", "链接")
	source := "../尚未创建"
	undo, err := Symlink(context.Background(), LinkOptions{Source: source, Target: target, DirMode: 0700})
	if err != nil {
		t.Fatal(err)
	}
	if actual, err := os.Readlink(target); err != nil || actual != source {
		t.Fatalf("符号链接内容不符：%q，错误：%v", actual, err)
	}
	undo()
	if _, err := os.Lstat(filepath.Dir(target)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("符号链接及本步目录未被回滚：%v", err)
	}
}
