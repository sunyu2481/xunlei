package xunlei

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/cnk3x/xunlei/pkg/vms/sys"
)

func Test小工具默认禁止且允许显式覆盖(t *testing.T) {
	for _, tc := range []struct {
		name, env string
		args      []string
		want      bool
		wantErr   bool
	}{
		{name: "默认禁用"},
		{name: "显式允许", env: "true", want: true},
		{name: "命令行重新禁用", env: "true", args: []string{"--allow_tools=false"}},
		{name: "无效开关", env: "无效", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupConfigTest(t, tc.args...)
			t.Setenv("XL_ALLOW_TOOLS", tc.env)
			var cfg Config
			err := ConfigBind(&cfg)
			if (err != nil) != tc.wantErr || cfg.AllowTools != tc.want {
				t.Fatalf("小工具配置错误：允许=%v，错误=%v", cfg.AllowTools, err)
			}
		})
	}
}

func Test小工具隔离失败必须回滚(t *testing.T) {
	for failAt := 0; failAt <= 4; failAt++ {
		cfg := Config{Root: t.TempDir()}
		cfg.DirData = filepath.Join(cfg.Root, "data")
		var mounted, restored []string
		undo, err := prepareTools(context.Background(), cfg, func(_ context.Context, m sys.MountOptions) (sys.Undo, error) {
			if m.Flags != syscall.MS_RDONLY|syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC || m.Optional || m.Fstype != "tmpfs" {
				t.Fatalf("隔离没有使用强制只读空挂载：%+v", m)
			}
			if failAt > 0 && len(mounted)+1 == failAt {
				return nil, syscall.EPERM
			}
			mounted = append(mounted, m.Target)
			return func() { restored = append(restored, m.Target) }, nil
		})
		if failAt == 0 {
			if err != nil || undo == nil || len(mounted) != 4 || len(restored) != 0 {
				t.Fatalf("成功隔离状态错误：%v，挂载=%v，回滚=%v", err, mounted, restored)
			}
			undo()
		} else if !errors.Is(err, syscall.EPERM) || undo != nil {
			t.Fatalf("隔离失败未阻止启动：%v", err)
		}
		slices.Reverse(mounted)
		if !slices.Equal(mounted, restored) {
			t.Fatalf("隔离未完整逆序回滚：挂载=%v，回滚=%v", mounted, restored)
		}
	}
}

func Test小工具隔离不跟随链接或遮蔽下载目录(t *testing.T) {
	for _, name := range []string{"链接", "普通文件", "下载目录"} {
		t.Run(name, func(t *testing.T) {
			cfg := Config{Root: t.TempDir()}
			cfg.DirData = filepath.Join(cfg.Root, "data")
			dir := toolDirectories(cfg)[0]
			if err := os.MkdirAll(filepath.Dir(dir), 0755); err != nil {
				t.Fatal(err)
			}
			var err error
			switch name {
			case "链接":
				err = os.Symlink(t.TempDir(), dir)
			case "普通文件":
				err = os.WriteFile(dir, []byte("保留内容"), 0600)
			case "下载目录":
				cfg.DirDownload = []string{filepath.Join(dir, "下载")}
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = prepareTools(context.Background(), cfg, func(context.Context, sys.MountOptions) (sys.Undo, error) {
				t.Fatal("预检失败前不应挂载")
				return nil, nil
			})
			if err == nil {
				t.Fatal("应拒绝不安全的工具目录")
			}
		})
	}
	_, err := prepareTools(context.Background(), Config{AllowTools: true}, func(context.Context, sys.MountOptions) (sys.Undo, error) {
		t.Fatal("允许小工具时不应挂载")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func Test面板拒绝小工具但保留下载接口(t *testing.T) {
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", "/drive/v1/apps", "", 403},
		{"GET", "/drive/v1/apps/网心云", "", 403},
		{"GET", "/drive//v1/%61pps", "", 403},
		{"POST", "/drive/v1/task", `{"type":"user#app","params":{"action":"install"}}`, 403},
		{"POST", "/drive/v1/task", `{"type":"user\u0023app","params":{"action":"run"}}`, 403},
		{"POST", "/drive/v1/tasks", `{"type":"user#app","params":{"action":"update"}}`, 403},
		{"POST", "/drive/v1/task", `null`, 400},
		{"POST", "/drive/v1/task", `{}`, 204},
		{"POST", "/drive/v1/task", `{"type":"user#download","url":"https://example.test/file"}`, 204},
		{"POST", "/drive/v1/files", "模拟文件内容", 204},
		{"GET", "/drive/v1/files?plugin_app_token=示例", "", 204},
		{"GET", "/drive/v1/tasks?type=user%23download", "", 204},
		{"GET", "/apps/网心云/", "", 403},
	} {
		for _, prefix := range []string{"", "/webman/3rdparty/" + SYNOPKG_PKGNAME + "/index.cgi"} {
			t.Run(tc.method+prefix+tc.path+tc.body, func(t *testing.T) {
				called := false
				h := blockTools(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					called = true
					body, err := io.ReadAll(r.Body)
					if err != nil || string(body) != tc.body {
						t.Fatalf("透传修改了正文：%q，%v", body, err)
					}
					w.WriteHeader(204)
				}))
				w := httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest(tc.method, prefix+tc.path, strings.NewReader(tc.body)))
				if w.Code != tc.status || called != (tc.status == 204) {
					t.Fatalf("状态=%d，调用内核=%v，期望=%d", w.Code, called, tc.status)
				}
			})
		}
	}
}

func Test小工具真实挂载保留数据并阻止写入(t *testing.T) {
	if os.Getenv("XL_TEST_MOUNT") != "1" || os.Geteuid() != 0 {
		t.Skip("需在一次性隔离环境中设置 XL_TEST_MOUNT=1 并提供挂载权限")
	}
	cfg := Config{Root: t.TempDir()}
	cfg.DirData = filepath.Join(cfg.Root, "data")
	dirs := toolDirectories(cfg)
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "原有工具"), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	account := filepath.Join(cfg.DirData, ".drive", "模拟账号状态")
	if err := os.WriteFile(account, []byte("保留账号"), 0600); err != nil {
		t.Fatal(err)
	}
	undo, err := prepareTools(context.Background(), cfg, sys.Mount)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if undo != nil {
			undo()
		}
	}()
	for _, dir := range dirs {
		if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
			t.Fatalf("原有工具未被遮蔽：%v，%v", entries, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "新工具"), nil, 0755); !errors.Is(err, syscall.EROFS) {
			t.Fatalf("根用户仍能安装工具：%v", err)
		}
		if err := exec.Command(filepath.Join(dir, "原有工具")).Run(); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("原有工具仍可执行：%v", err)
		}
	}
	undo()
	undo = nil
	for _, dir := range dirs {
		if _, err := os.Stat(filepath.Join(dir, "原有工具")); err != nil {
			t.Fatalf("隔离删除了原有工具：%v", err)
		}
	}
	if b, err := os.ReadFile(account); err != nil || string(b) != "保留账号" {
		t.Fatalf("隔离修改了账号状态：%v", err)
	}
}
