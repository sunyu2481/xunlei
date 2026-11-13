package xunlei

import (
	"log/slog"
	"os"
	"testing"

	"github.com/cnk3x/xunlei/pkg/flags"
	"github.com/spf13/pflag"
)

func TestConfigUploadLimit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		env     string
		args    []string
		want    uint64
		wantErr bool
	}{
		{name: "默认关闭"},
		{name: "环境变量", env: "500", want: 500},
		{name: "命令行优先", env: "500", args: []string{"--upload_limit", "250"}, want: 250},
		{name: "无效环境变量", env: "abc", wantErr: true},
		{name: "负数环境变量", env: "-1", wantErr: true},
		{name: "环境变量溢出", env: "18446744073709551616", wantErr: true},
		{name: "命令行覆盖无效环境变量", env: "abc", args: []string{"--upload_limit", "250"}, want: 250},
		{name: "负数命令行", args: []string{"--upload_limit", "-1"}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldFlags, oldArgs, oldLogger := flags.CommandLine, os.Args, slog.Default()
			t.Cleanup(func() {
				flags.CommandLine, os.Args = oldFlags, oldArgs
				slog.SetDefault(oldLogger)
			})
			flags.CommandLine = pflag.NewFlagSet("上传限速测试", pflag.ContinueOnError)
			os.Args = append([]string{"xlp"}, tc.args...)
			t.Setenv("XL_ROOT", t.TempDir())
			t.Setenv("XL_UPLOAD_LIMIT", tc.env)
			t.Setenv("XL_UPLOAD_INTERFACE", "eth1")
			var cfg Config
			err := ConfigBind(&cfg)
			if (err != nil) != tc.wantErr {
				t.Fatalf("配置解析结果错误：%v", err)
			}
			if err == nil && (cfg.UploadLimit != tc.want || cfg.UploadInterface != "eth1") {
				t.Fatalf("配置值错误：限速=%d，网卡=%s", cfg.UploadLimit, cfg.UploadInterface)
			}
		})
	}
}

func TestConfigWithoutAdditionalPanelCredentials(t *testing.T) {
	oldFlags, oldArgs, oldLogger := flags.CommandLine, os.Args, slog.Default()
	t.Cleanup(func() { flags.CommandLine, os.Args = oldFlags, oldArgs; slog.SetDefault(oldLogger) })
	flags.CommandLine = pflag.NewFlagSet("面板配置测试", pflag.ContinueOnError)
	os.Args = []string{"xlp"}
	t.Setenv("XL_ROOT", t.TempDir())
	t.Setenv("XL_DASHBOARD_USERNAME", "")
	t.Setenv("XL_DASHBOARD_PASSWORD", "")
	t.Setenv("XL_BA_USER", "")
	t.Setenv("XL_BA_PASSWORD", "")
	t.Setenv("XL_UPLOAD_LIMIT", "")
	var cfg Config
	if err := ConfigBind(&cfg); err != nil {
		t.Fatalf("不应要求额外设置面板账号：%v", err)
	}
	if cfg.DashboardUsername != "" || cfg.DashboardPassword != "" {
		t.Fatal("不应自动生成面板访问凭据")
	}
}
