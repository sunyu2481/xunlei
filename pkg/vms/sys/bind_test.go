package sys

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func Test只读绑定及失败回滚(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "成功", true: "重挂载失败"}[fail], func(t *testing.T) {
			source, target := t.TempDir(), filepath.Join(t.TempDir(), "绑定")
			calls, unmounts := 0, 0
			mount := func(src, dst, kind string, flags uintptr, data string) error {
				calls++
				if dst != target {
					t.Fatalf("绑定目标错误：%q", dst)
				}
				if calls == 1 && flags != syscall.MS_BIND {
					t.Fatalf("首次挂载标志错误：%x", flags)
				}
				if calls == 2 {
					want := uintptr(syscall.MS_BIND | syscall.MS_REMOUNT | syscall.MS_RDONLY)
					if flags&want != want {
						t.Fatalf("未执行只读重挂载：%x", flags)
					}
					if fail {
						return syscall.EPERM
					}
				}
				return nil
			}
			undo, err := bind(context.Background(), BindOptions{Source: source, Target: target, ReadOnly: true, Optional: true}, mount,
				func(string, int) error { unmounts++; return nil })
			if fail {
				if !errors.Is(err, syscall.EPERM) || unmounts != 1 {
					t.Fatalf("只读失败未上报或未卸载：%v，卸载次数=%d", err, unmounts)
				}
			} else {
				if err != nil || calls != 2 {
					t.Fatalf("绑定未完成：%v，调用次数=%d", err, calls)
				}
				undo()
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatalf("绑定目录未回滚：%v", err)
			}
		})
	}
}

// 真实挂载只在显式启用的一次性子进程和独立挂载命名空间中执行。
func Test只读绑定集成(t *testing.T) {
	if os.Getenv("XL_TEST_MOUNT_CHILD") == "1" {
		if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
			t.Fatal(err)
		}
		source, target := t.TempDir(), filepath.Join(t.TempDir(), "绑定")
		if err := os.WriteFile(filepath.Join(source, "文件"), []byte("源内容"), 0600); err != nil {
			t.Fatal(err)
		}
		undo, err := Bind(context.Background(), BindOptions{Source: source, Target: target, ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		defer undo()
		if err := os.WriteFile(filepath.Join(target, "文件"), []byte("修改"), 0600); !errors.Is(err, syscall.EROFS) {
			t.Fatalf("只读绑定仍可写：%v", err)
		}
		if err := os.WriteFile(filepath.Join(source, "文件"), []byte("宿主机更新"), 0600); err != nil {
			t.Fatalf("源挂载被意外改为只读：%v", err)
		}
		return
	}
	if os.Getenv("XL_TEST_MOUNT") != "1" {
		t.Skip("需要在有挂载权限的独立环境中设置 XL_TEST_MOUNT=1")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^Test只读绑定集成$")
	cmd.Env = append(os.Environ(), "XL_TEST_MOUNT_CHILD=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWNS}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("独立挂载验证失败：%v\n%s", err, output)
	}
}
