package vms

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/cnk3x/xunlei/pkg/vms/sys"
)

func Test基础挂载使用最终根目录(t *testing.T) {
	base := t.TempDir()
	root, oldRoot := filepath.Join(base, "最终根目录"), filepath.Join(base, "旧根目录")
	run := Run(func(context.Context) error { t.Fatal("配置选项时执行了业务回调"); return nil })
	for name, items := range map[string][]Option{
		"根目录在前":    {Root(root), Basic, run},
		"基础挂载在前":   {Basic, Root(root), run},
		"根目录在最后":   {Basic, run, Root(root)},
		"回调在最前":    {run, Basic, Root(root)},
		"覆盖较早的根目录": {Root(oldRoot), Basic, Root(root), run},
		"挂载前覆盖根目录": {Root(oldRoot), Root(root), Basic, run},
	} {
		t.Run(name, func(t *testing.T) {
			opts := newOptions(items...)
			if opts.root != root || opts.run == nil || len(opts.mounts) != 4 {
				t.Fatalf("选项收集错误：根目录=%q，挂载数=%d", opts.root, len(opts.mounts))
			}
			for i, name := range []string{"proc", "dev", "sys", "tmp"} {
				if got, want := opts.mounts[i].Target, filepath.Join(root, name); got != want {
					t.Errorf("挂载 %s 的目标=%q，期望=%q", name, got, want)
				}
			}
		})
	}
	entries, err := os.ReadDir(base)
	if err != nil || len(entries) != 0 {
		t.Fatalf("配置选项不应准备目录或执行挂载：目录项=%v，错误=%v", entries, err)
	}
	if opts := newOptions(Root(root)); len(opts.mounts) != 0 {
		t.Fatalf("未启用基础挂载仍添加了 %d 个挂载", len(opts.mounts))
	}
}

func Test基础挂载权限参数(t *testing.T) {
	root := t.TempDir()
	flags := uintptr(syscall.MS_NOSUID | syscall.MS_NODEV | syscall.MS_NOEXEC)
	want := []sys.MountOptions{
		{Target: filepath.Join(root, "proc"), Source: "proc", Fstype: "proc", Flags: flags},
		{Target: filepath.Join(root, "dev"), Source: "devtmpfs", Fstype: "devtmpfs", Flags: syscall.MS_NOSUID | syscall.MS_NOEXEC, Data: "mode=0755"},
		{Target: filepath.Join(root, "sys"), Source: "sysfs", Fstype: "sysfs", Flags: flags, Optional: true},
		{Target: filepath.Join(root, "tmp"), Source: "tmpfs", Fstype: "tmpfs", Flags: flags, Data: "mode=01777,size=100m", Optional: true},
	}
	if got := newOptions(Root(root), Basic).mounts; !reflect.DeepEqual(got, want) {
		t.Fatalf("基础挂载参数错误：\n实际=%+v\n期望=%+v", got, want)
	}
}

// 真实挂载仅在显式启用的独立环境中执行，子进程另建私有挂载命名空间。
func Test基础挂载集成(t *testing.T) {
	if os.Getenv("XL_TEST_BASIC_MOUNT_CHILD") != "1" {
		if os.Getenv("XL_TEST_MOUNT") != "1" {
			t.Skip("需要在有挂载权限的独立环境中设置 XL_TEST_MOUNT=1")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^Test基础挂载集成$", "-test.v")
		cmd.Env = append(os.Environ(), "XL_TEST_BASIC_MOUNT_CHILD=1")
		cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWNS}
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("独立基础挂载验证失败：%v\n%s", err, output)
		} else {
			t.Logf("子进程验证：\n%s", output)
		}
		return
	}
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}
	opts := newOptions(Basic, Root(t.TempDir()))
	for _, m := range opts.mounts {
		t.Run(m.Fstype, func(t *testing.T) {
			// 集成验证不允许可选项隐藏挂载失败。
			m.Optional = false
			undo, err := sys.Mount(context.Background(), m)
			if err != nil {
				t.Fatal(err)
			}
			defer undo()
			var stat syscall.Statfs_t
			if err := syscall.Statfs(m.Target, &stat); err != nil {
				t.Fatal(err)
			}
			if got := uintptr(stat.Flags); got&m.Flags != m.Flags {
				t.Fatalf("实际挂载标志=%#x，缺少要求的标志=%#x", got, m.Flags)
			}
			if m.Fstype == "devtmpfs" {
				file, err := os.Open(filepath.Join(m.Target, "null"))
				if err != nil {
					t.Fatalf("设备节点不可用：%v", err)
				}
				file.Close()
			}
			if m.Fstype == "tmpfs" {
				info, err := os.Stat(m.Target)
				if err != nil {
					t.Fatal(err)
				}
				if mode := info.Mode(); mode.Perm() != 0777 || mode&os.ModeSticky == 0 {
					t.Fatalf("临时目录模式不是 01777：%v", mode)
				}
				script := filepath.Join(m.Target, "执行检查")
				if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
					t.Fatal(err)
				}
				if err := exec.Command(script).Run(); !errors.Is(err, syscall.EACCES) {
					t.Fatalf("临时目录未拒绝执行：%v", err)
				}
				if err := os.Remove(script); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
