package xunlei

import (
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cnk3x/xunlei/pkg/flags"
	"github.com/spf13/pflag"
)

func setupConfigTest(t *testing.T, args ...string) string {
	t.Helper()
	oldFlags, oldArgs, oldLogger := flags.CommandLine, os.Args, slog.Default()
	t.Cleanup(func() {
		flags.CommandLine, os.Args = oldFlags, oldArgs
		slog.SetDefault(oldLogger)
	})
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "XL_") || key == "UID" || key == "GID" {
			t.Setenv(key, "")
		}
	}
	flags.CommandLine = pflag.NewFlagSet("配置测试", pflag.ContinueOnError)
	os.Args = append([]string{"xlp"}, args...)
	root := t.TempDir()
	t.Setenv("XL_ROOT", root)
	return root
}

func Test配置拒绝非法环境值且保留原配置(t *testing.T) {
	for _, tc := range []struct{ key, value, flag, valid string }{
		{"XL_DASHBOARD_PORT", "abc", "dashboard_port", "2346"},
		{"XL_DASHBOARD_PORT", "70000", "dashboard_port", "2346"},
		{"XL_DASHBOARD_IP", "错误地址", "dashboard_ip", "127.0.0.1"},
		{"XL_UID", "abc", "uid", "1000"},
		{"XL_GID", "abc", "gid", "1001"},
		{"XL_PREVENT_UPDATE", "错误", "prevent_update", "true"},
		{"XL_SPK_FORCE_DOWNLOAD", "错误", "spk_force_download", "true"},
		{"XL_DEBUG", "错误", "debug", "false"},
		{"XL_UPLOAD_LIMIT", "-1", "upload_limit", "100"},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			setupConfigTest(t)
			t.Setenv(tc.key, tc.value)
			cfg := Config{Port: 2347, Uid: 1002, Root: "原配置", DirDownload: []string{"原下载目录"}}
			want := cfg
			if err := ConfigBind(&cfg); err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("未定位非法环境变量：%v", err)
			}
			if !reflect.DeepEqual(cfg, want) {
				t.Fatalf("失败改变了原配置：%+v", cfg)
			}
		})
		t.Run(tc.key+tc.value+"命令行覆盖", func(t *testing.T) {
			setupConfigTest(t, "--"+tc.flag+"="+tc.valid)
			t.Setenv(tc.key, tc.value)
			if err := ConfigBind(&Config{}); err != nil {
				t.Fatalf("合法命令行未覆盖非法环境值：%v", err)
			}
		})
	}
}

func Test配置身份范围(t *testing.T) {
	for _, field := range []string{"uid", "gid"} {
		for _, value := range []string{"-1", "4294967295", "4294967296", "18446744073709551615"} {
			for _, env := range []bool{false, true} {
				t.Run(field+value+map[bool]string{false: "参数", true: "环境"}[env], func(t *testing.T) {
					setupConfigTest(t)
					if env {
						t.Setenv("XL_"+strings.ToUpper(field), value)
					} else {
						os.Args = append(os.Args, "--"+field+"="+value)
					}
					if err := ConfigBind(&Config{}); err == nil {
						t.Fatal("应拒绝非法系统身份")
					}
				})
			}
		}
	}
	for _, value := range []string{"0", "1000", "4294967294"} {
		t.Run("有效身份"+value, func(t *testing.T) {
			setupConfigTest(t, "--uid="+value, "--gid="+value)
			if err := ConfigBind(&Config{}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func Test下载目录命令行替换环境并按顺序追加(t *testing.T) {
	root := setupConfigTest(t)
	t.Setenv("XL_DIR_DOWNLOAD", filepath.Join(root, "环境下载"))
	want := []string{filepath.Join(root, "下载一"), filepath.Join(root, "下载二")}
	os.Args = append(os.Args, "-d", want[0], "--dir_download", want[1])
	var cfg Config
	if err := ConfigBind(&cfg); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.DirDownload, want) {
		t.Fatalf("下载目录=%v", cfg.DirDownload)
	}
}
