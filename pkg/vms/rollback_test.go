package vms

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/cnk3x/xunlei/pkg/utils"
)

func Test恢复失败仍关闭全部句柄(t *testing.T) {
	for _, failure := range []string{"", "定位根", "恢复根", "恢复目录", "关闭根", "关闭目录"} {
		t.Run(failure, func(t *testing.T) {
			want := errors.New("模拟" + failure + "失败")
			var steps []string
			call := func(name string) error {
				steps = append(steps, name)
				if name == failure {
					return want
				}
				return nil
			}
			state := rootState{ctx: context.Background(), rootFD: 1, cwdFD: 2, changedRoot: true, changedDir: true,
				calls: rootCalls{
					fchdir: func(fd int) error {
						if fd == 1 {
							return call("定位根")
						}
						return call("恢复目录")
					},
					chroot: func(string) error { return call("恢复根") },
					close: func(fd int) error {
						if fd == 1 {
							return call("关闭根")
						}
						return call("关闭目录")
					},
				}}
			err := errors.Join(state.restore(), state.release())
			if failure == "" && err != nil {
				t.Fatal(err)
			}
			if failure != "" && !errors.Is(err, want) {
				t.Fatalf("关键回滚错误丢失：%v", err)
			}
			expected := []string{"定位根", "恢复根", "恢复目录", "关闭根", "关闭目录"}
			if failure == "定位根" {
				expected = []string{"定位根", "关闭根", "关闭目录"}
			}
			if failure == "恢复根" {
				expected = []string{"定位根", "恢复根", "关闭根", "关闭目录"}
			}
			if !reflect.DeepEqual(steps, expected) {
				t.Fatalf("失败后调用不安全步骤或漏关句柄：%v", steps)
			}
			unsafe := failure == "定位根" || failure == "恢复根" || failure == "恢复目录"
			if errors.Is(err, utils.ErrUnsafeCleanup) != unsafe {
				t.Fatalf("不可信根目录标记错误：%v", err)
			}
		})
	}
}

// 真实根目录变更仅发生在一次性子进程，不执行挂载或宿主机业务。
func Test恢复句柄故障独立进程(t *testing.T) {
	if mode := os.Getenv("XL_TEST_RESTORE_CHILD"); mode != "" {
		testRestoreChild(t, mode)
		return
	}
	if os.Geteuid() != 0 {
		t.Skip("需要独立环境的根权限")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"正常", "根句柄失效", "目录句柄失效", "业务与恢复同时失败"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			program, args := self, []string{"-test.run=^Test恢复句柄故障独立进程$", "-test.v"}
			if executor := os.Getenv("XL_TEST_EXECUTOR"); executor != "" {
				program, args = executor, append([]string{self}, args...)
			}
			cmd := exec.CommandContext(ctx, program, args...)
			cmd.Env = append(os.Environ(), "XL_TEST_RESTORE_CHILD="+mode, "XL_TEST_RESTORE_DIR="+t.TempDir())
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("恢复故障验证失败：%v\n%s", err, out)
			}
			t.Logf("%s", out)
		})
	}
}

func testRestoreChild(t *testing.T, mode string) {
	base := os.Getenv("XL_TEST_RESTORE_DIR")
	jail, cwd := filepath.Join(base, "隔离"), filepath.Join(base, "工作")
	for _, dir := range []string{jail, cwd} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	var rootStat, cwdStat syscall.Stat_t
	if err := syscall.Stat("/", &rootStat); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Stat(".", &cwdStat); err != nil {
		t.Fatal(err)
	}
	cleaned, ran := false, false
	businessErr := errors.New("模拟业务失败")
	err := Execute(context.Background(), Root(jail),
		Before(func(context.Context) (func(), error) { return func() { cleaned = true }, nil }),
		Run(func(context.Context) error {
			ran = true
			if mode != "正常" {
				target := rootStat
				if mode == "目录句柄失效" {
					target = cwdStat
				}
				closed := false
				for fd := 3; fd < 256; fd++ {
					var stat syscall.Stat_t
					if syscall.Fstat(fd, &stat) == nil && stat.Dev == target.Dev && stat.Ino == target.Ino {
						if err := syscall.Close(fd); err != nil {
							return err
						}
						closed = true
						break
					}
				}
				if !closed {
					return fmt.Errorf("未找到待验证恢复句柄")
				}
			}
			if mode == "业务与恢复同时失败" {
				return businessErr
			}
			return nil
		}))
	if !ran && errors.Is(err, syscall.EPERM) {
		t.Skip("当前环境没有切换根目录能力")
	}
	if mode == "正常" {
		got, getErr := os.Getwd()
		if err != nil || !cleaned || getErr != nil || got != cwd {
			t.Fatalf("正常恢复失败：%v，清理=%t，目录=%q，目录错误=%v", err, cleaned, got, getErr)
		}
	} else {
		if !errors.Is(err, syscall.EBADF) || !errors.Is(err, utils.ErrUnsafeCleanup) || cleaned {
			t.Fatalf("恢复失败没有阻止路径回滚：%v，清理=%t", err, cleaned)
		}
		if mode == "业务与恢复同时失败" && !errors.Is(err, businessErr) {
			t.Fatal("业务错误被恢复错误覆盖")
		}
		if mode != "目录句柄失效" {
			var current syscall.Stat_t
			if err := syscall.Stat("/", &current); err != nil {
				t.Fatal(err)
			}
			if current.Dev == rootStat.Dev && current.Ino == rootStat.Ino {
				t.Fatal("故障没有触发根目录恢复失败")
			}
		}
	}
	t.Log("验证模式：" + mode + "，进程：" + strconv.Itoa(os.Getpid()))
}

func Test后续回调不能掩盖关键恢复错误(t *testing.T) {
	want := errors.New("模拟关键恢复失败")
	for _, critical := range []bool{false, true} {
		err := Execute(context.Background(), Root("/"), Run(func(context.Context) error {
			if critical {
				return &rollbackError{want}
			}
			return want
		}), After(func(context.Context, error) error { return nil }))
		if errors.Is(err, want) != critical {
			t.Fatalf("关键=%t，回调结果=%v", critical, err)
		}
	}
}
