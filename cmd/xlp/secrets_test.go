package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/cnk3x/xunlei"
)

func Test启动日志只显示认证状态且省略私有套件地址(t *testing.T) {
	old := slog.Default()
	t.Cleanup(func() { slog.SetDefault(old) })
	var output bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, &slog.HandlerOptions{ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey {
			return slog.Attr{}
		}
		return a
	}})))
	var previous string
	for _, password := range []string{"a", "模拟中文密码", "another-mock-secret", ""} {
		output.Reset()
		logConfiguration(context.Background(), xunlei.Config{
			DashboardPassword: password,
			SpkUrl:            "https://模拟账号:模拟密码@example.invalid/模拟私有路径?signature=模拟签名",
		})
		got := output.String()
		if strings.Contains(got, "模拟") || strings.Contains(got, "another-mock-secret") || strings.Contains(got, "example.invalid") {
			t.Fatal("启动日志包含模拟密码或私有套件地址")
		}
		if password == "" {
			if !strings.Contains(got, "enabled=false") {
				t.Fatal("空密码应显示认证未启用")
			}
		} else {
			if !strings.Contains(got, "enabled=true") || previous != "" && previous != got {
				t.Fatal("已配置密码的日志应与密码内容和长度无关")
			}
			previous = got
		}
	}
}
