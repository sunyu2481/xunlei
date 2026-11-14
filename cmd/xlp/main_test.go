package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"testing"

	"github.com/cnk3x/xunlei"
	"github.com/cnk3x/xunlei/pkg/utils"
	"github.com/cnk3x/xunlei/pkg/vms"
)

func Test入口错误与清理结果(t *testing.T) {
	for _, mode := range []string{"正常", "限速准备失败", "隔离准备失败", "业务运行失败", "清理失败", "禁止清理"} {
		t.Run(mode, func(t *testing.T) {
			want := errors.New("模拟" + mode)
			cleaned := false
			apply := func(context.Context, uint64, string) (func() error, error) {
				if mode == "限速准备失败" {
					return nil, want
				}
				return func() error {
					cleaned = true
					if mode == "清理失败" {
						return want
					}
					return nil
				}, nil
			}
			execute := func(context.Context, ...vms.Option) error {
				if mode == "隔离准备失败" || mode == "业务运行失败" {
					return want
				}
				if mode == "禁止清理" {
					return errors.Join(want, utils.ErrUnsafeCleanup)
				}
				return nil
			}
			code := exitCode(func() error { return runEnvironment(context.Background(), xunlei.Config{}, apply, execute) })
			if (code == 0) != (mode == "正常") {
				t.Fatalf("退出码错误：%d", code)
			}
			if cleaned != (mode != "限速准备失败" && mode != "禁止清理") {
				t.Fatalf("清理状态错误：%t", cleaned)
			}
		})
	}
}

func Test入口进程退出码(t *testing.T) {
	if value := os.Getenv("XL_TEST_EXIT_CHILD"); value != "" {
		code, _ := strconv.Atoi(value)
		os.Exit(exitCode(func() error {
			if code == 0 {
				return nil
			}
			return errors.New("模拟运行失败")
		}))
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []int{0, 1} {
		program, args := self, []string{"-test.run=^Test入口进程退出码$"}
		if executor := os.Getenv("XL_TEST_EXECUTOR"); executor != "" {
			program, args = executor, append([]string{self}, args...)
		}
		cmd := exec.Command(program, args...)
		cmd.Env = append(os.Environ(), "XL_TEST_EXIT_CHILD="+strconv.Itoa(want))
		err := cmd.Run()
		if want == 0 && err != nil {
			t.Fatal(err)
		}
		if want != 0 {
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.ExitCode() != want {
				t.Fatalf("退出码应为 %d：%v", want, err)
			}
		}
	}
}

func Test运行与清理错误同时保留(t *testing.T) {
	business, cleanup := errors.New("模拟业务失败"), errors.New("模拟清理失败")
	err := runEnvironment(context.Background(), xunlei.Config{}, func(context.Context, uint64, string) (func() error, error) {
		return func() error { return cleanup }, nil
	}, func(context.Context, ...vms.Option) error { return business })
	if !errors.Is(err, business) || !errors.Is(err, cleanup) {
		t.Fatalf("错误被覆盖：%v", err)
	}
}
