package spk

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func Test安装中途失败恢复全部旧文件(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("需要普通用户触发目录写入失败")
	}
	for _, installed := range []bool{false, true} {
		t.Run(map[bool]string{false: "首次安装", true: "已有安装"}[installed], func(t *testing.T) {
			dir := t.TempDir()
			if installed {
				if err := Extract(context.Background(), bytes.NewReader(testPackage(t, testFiles())), dir); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(filepath.Join(dir, launcherFile), 0751); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(filepath.Join(dir, "ui"), 0755); err != nil {
					t.Fatal(err)
				}
			}
			ui := filepath.Join(dir, "ui")
			if err := os.Chmod(ui, 0555); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(ui, 0755) })
			files := testFiles()
			for i := range files {
				if files[i].name != versionFile {
					files[i].data = "新版本内容"
				}
			}
			err := Extract(context.Background(), bytes.NewReader(testPackage(t, files)), dir)
			if err == nil || !strings.Contains(err.Error(), gatewayFile) {
				t.Fatalf("应在前两个文件替换后于网关安装失败：%v", err)
			}
			if installed {
				assertFiles(t, dir)
				info, err := os.Stat(filepath.Join(dir, launcherFile))
				if err != nil {
					t.Fatal(err)
				}
				st := info.Sys().(*syscall.Stat_t)
				if info.Mode().Perm() != 0751 || st.Uid != uint32(os.Getuid()) || st.Gid != uint32(os.Getgid()) || st.Nlink != 1 {
					t.Fatalf("回滚未保留权限、属主或独立文件：%v，%+v", info.Mode(), st)
				}
				if !allExists(context.Background(), dir) {
					t.Fatal("失败破坏旧安装清单")
				}
			} else {
				for _, file := range testFiles() {
					if _, err := os.Lstat(filepath.Join(dir, file.name)); !os.IsNotExist(err) {
						t.Fatalf("失败留下已发布文件：%s，%v", file.name, err)
					}
				}
			}
			assertNoStage(t, dir)
		})
	}
}

func Test安装准备失败不发布文件(t *testing.T) {
	dir := t.TempDir()
	if err := Extract(context.Background(), bytes.NewReader(testPackage(t, testFiles())), dir); err != nil {
		t.Fatal(err)
	}
	root, err := openDestination(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	stage, cleanup, err := newStage(root)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	files := make(map[string]string)
	for i, name := range requiredFiles("3.1.10") {
		files[name] = fmt.Sprint(i)
		if i == 2 {
			continue
		}
		f, err := openFileAt(stage, files[name], os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString("新的文件"); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := installFiles(context.Background(), root, stage, requiredFiles("3.1.10"), files); err == nil {
		t.Fatal("暂存文件缺失时应失败")
	}
	assertFiles(t, dir)
}

func Test缺失缓存检查不创建目录(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "尚未安装")
	if allExists(context.Background(), dir) {
		t.Fatal("不应接受缺失安装")
	}
	entries, err := os.ReadDir(base)
	if err != nil || len(entries) != 0 {
		t.Fatalf("缓存检查创建了目录：%v，%v", entries, err)
	}
}
