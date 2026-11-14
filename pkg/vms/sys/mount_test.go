package sys

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func Test挂载参数透传与回滚(t *testing.T) {
	m := MountOptions{Target: filepath.Join(t.TempDir(), "临时挂载"), Source: "tmpfs", Fstype: "tmpfs",
		Flags: syscall.MS_NOSUID | syscall.MS_NODEV | syscall.MS_NOEXEC, Data: "mode=01777,size=100m"}
	mounts, unmounts := 0, 0
	undo, err := mount(context.Background(), m, mountCalls{
		mount: func(source, target, kind string, flags uintptr, data string) error {
			mounts++
			if source != m.Source || target != m.Target || kind != m.Fstype || flags != m.Flags || data != m.Data {
				t.Fatalf("挂载参数被修改：%q %q %q %#x %q", source, target, kind, flags, data)
			}
			return nil
		},
		unmount: func(target string, flags int) error {
			unmounts++
			if target != m.Target {
				t.Fatalf("卸载目标错误：%q", target)
			}
			return nil
		},
		statfs: func(string, *syscall.Statfs_t) error { t.Fatal("普通挂载不应查询绑定源"); return nil },
	})
	if err != nil || undo == nil || mounts != 1 || unmounts != 0 {
		t.Fatalf("挂载失败：%v，挂载=%d，卸载=%d", err, mounts, unmounts)
	}
	undo()
	if _, err := os.Stat(m.Target); !os.IsNotExist(err) || unmounts != 1 {
		t.Fatalf("回滚未卸载或清理本步目录：%v，卸载=%d", err, unmounts)
	}
}

func Test设备挂载回退保留权限(t *testing.T) {
	for name, failure := range map[string]string{"成功": "", "查询失败": "查询", "收紧失败": "重挂载"} {
		t.Run(name, func(t *testing.T) {
			m := MountOptions{Target: filepath.Join(t.TempDir(), "设备"), Source: "devtmpfs", Fstype: "devtmpfs",
				Flags: syscall.MS_NOSUID | syscall.MS_NOEXEC, Data: "mode=0755", Optional: true}
			mounts, unmounts, stats := 0, 0, 0
			undo, err := mount(context.Background(), m, mountCalls{
				mount: func(source, target, kind string, flags uintptr, data string) error {
					mounts++
					if target != m.Target {
						t.Fatalf("挂载目标错误：%q", target)
					}
					switch mounts {
					case 1:
						return syscall.ENODEV
					case 2:
						if source != "/dev" || kind != "" || flags != syscall.MS_BIND|syscall.MS_REC || data != "" {
							t.Fatalf("设备回退绑定参数错误：%q %q %#x %q", source, kind, flags, data)
						}
					case 3:
						want := uintptr(syscall.MS_BIND | syscall.MS_REMOUNT | syscall.MS_NOSUID | syscall.MS_NOEXEC | syscall.MS_RDONLY | syscall.MS_NOATIME)
						if source != "" || kind != "" || flags != want || data != "" {
							t.Fatalf("回退未保留源限制并收紧权限：%q %q %#x %q，期望标志=%#x", source, kind, flags, data, want)
						}
						if failure == "重挂载" {
							return syscall.EPERM
						}
					default:
						t.Fatalf("多余挂载：%d", mounts)
					}
					return nil
				},
				unmount: func(target string, flags int) error {
					unmounts++
					if target != m.Target {
						t.Fatalf("卸载目标错误：%q", target)
					}
					return nil
				},
				statfs: func(target string, stat *syscall.Statfs_t) error {
					stats++
					if target != m.Target {
						t.Fatalf("查询目标错误：%q", target)
					}
					stat.Flags = syscall.MS_RDONLY | syscall.MS_NOATIME
					if failure == "查询" {
						return syscall.EPERM
					}
					return nil
				},
			})
			if failure != "" {
				if !errors.Is(err, syscall.EPERM) || unmounts != 1 {
					t.Fatalf("权限准备失败必须上报并回滚：错误=%v，卸载=%d", err, unmounts)
				}
			} else {
				if err != nil || mounts != 3 || stats != 1 || unmounts != 0 {
					t.Fatalf("设备回退未完成权限设置：错误=%v，挂载=%d，查询=%d，卸载=%d", err, mounts, stats, unmounts)
				}
				undo()
			}
			if _, err := os.Stat(m.Target); !os.IsNotExist(err) || unmounts != 1 {
				t.Fatalf("设备回退未清理本步资源：错误=%v，卸载=%d", err, unmounts)
			}
		})
	}
}

// 强制走设备回退，但所有真实系统调用仅作用于独立环境中的私有挂载命名空间。
func Test设备回退挂载集成(t *testing.T) {
	if os.Getenv("XL_TEST_DEVICE_MOUNT_CHILD") != "1" {
		if os.Getenv("XL_TEST_MOUNT") != "1" {
			t.Skip("需要在有挂载权限的独立环境中设置 XL_TEST_MOUNT=1")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^Test设备回退挂载集成$", "-test.v")
		cmd.Env = append(os.Environ(), "XL_TEST_DEVICE_MOUNT_CHILD=1")
		cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWNS}
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("独立设备回退验证失败：%v\n%s", err, output)
		} else {
			t.Logf("子进程验证：\n%s", output)
		}
		return
	}
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}
	var sourceBefore, sourceAfter, target syscall.Statfs_t
	if err := syscall.Statfs("/dev", &sourceBefore); err != nil {
		t.Fatal(err)
	}
	m := MountOptions{Target: filepath.Join(t.TempDir(), "设备"), Source: "devtmpfs", Fstype: "devtmpfs",
		Flags: syscall.MS_NOSUID | syscall.MS_NOEXEC, Data: "mode=0755"}
	undo, err := mount(context.Background(), m, mountCalls{
		mount: func(source, target, kind string, flags uintptr, data string) error {
			if kind == "devtmpfs" {
				return syscall.ENODEV
			}
			return syscall.Mount(source, target, kind, flags, data)
		},
		unmount: syscall.Unmount,
		statfs:  syscall.Statfs,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer undo()
	if err := syscall.Statfs(m.Target, &target); err != nil {
		t.Fatal(err)
	}
	if got := uintptr(target.Flags); got&m.Flags != m.Flags {
		t.Fatalf("设备回退缺少权限限制：%#x", got)
	}
	if err := syscall.Statfs("/dev", &sourceAfter); err != nil {
		t.Fatal(err)
	}
	if sourceAfter.Flags != sourceBefore.Flags {
		t.Fatalf("源设备挂载的标志被修改：之前=%#x，之后=%#x", sourceBefore.Flags, sourceAfter.Flags)
	}
	file, err := os.Open(filepath.Join(m.Target, "null"))
	if err != nil {
		t.Fatalf("回退后的设备节点不可用：%v", err)
	}
	file.Close()
	for _, name := range []string{"pts", "shm"} {
		var child, sourceChild syscall.Statfs_t
		if err := syscall.Statfs(filepath.Join("/dev", name), &sourceChild); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Statfs(filepath.Join(m.Target, name), &child); err != nil {
			t.Fatalf("递归绑定缺少设备子挂载 %s：%v", name, err)
		}
		if child.Type != sourceChild.Type || child.Fsid != sourceChild.Fsid {
			t.Fatalf("设备子目录 %s 没有保留原文件系统", name)
		}
	}
}
