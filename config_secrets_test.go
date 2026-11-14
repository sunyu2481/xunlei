package xunlei

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cnk3x/xunlei/pkg/cmdx"
	"github.com/cnk3x/xunlei/pkg/flags"
	"github.com/cnk3x/xunlei/pkg/web"
)

func Test敏感配置不进入帮助且仍可解析(t *testing.T) {
	const password = "模拟面板密码甲"
	const source = "https://example.invalid/模拟私有路径?signature=模拟签名乙"
	for _, mode := range []string{"环境", "旧环境别名", "初始值", "命令行覆盖"} {
		t.Run(mode, func(t *testing.T) {
			setupConfigTest(t)
			cfg := Config{}
			switch mode {
			case "初始值":
				cfg.DashboardPassword, cfg.SpkUrl = password, source
			case "命令行覆盖":
				t.Setenv("XL_DASHBOARD_PASSWORD", "模拟被覆盖密码")
				t.Setenv("XL_SPK", "模拟被覆盖地址")
				os.Args = append(os.Args, "--dashboard_password="+password, "--spk="+source)
			default:
				key := "XL_DASHBOARD_PASSWORD"
				if mode == "旧环境别名" {
					key = "XL_BA_PASSWORD"
				}
				t.Setenv(key, password)
				t.Setenv("XL_SPK", source)
			}
			if err := ConfigBind(&cfg); err != nil {
				t.Fatal(err)
			}
			if cfg.DashboardPassword != password || cfg.SpkUrl != source {
				t.Fatal("敏感配置解析或覆盖语义发生改变")
			}
			var output bytes.Buffer
			flags.CommandLine.SetOutput(&output)
			flags.CommandLine.PrintDefaults()
			if strings.Contains(output.String(), "模拟") {
				t.Fatal("参数帮助包含模拟秘密")
			}
			for _, key := range []string{"dashboard_password", "XL_DASHBOARD_PASSWORD", "--spk", "XL_SPK"} {
				if !strings.Contains(output.String(), key) {
					t.Errorf("帮助丢失配置入口 %s", key)
				}
			}
		})
	}
}

func Test启动器和网关只传递允许的环境(t *testing.T) {
	for key, value := range map[string]string{
		"XL_DASHBOARD_PASSWORD": "模拟面板秘密", "XL_BA_PASSWORD": "模拟旧密码",
		"XL_SPK": "https://example.invalid/?token=模拟下载秘密", "PRIVATE_TEST_TOKEN": "模拟无关秘密",
		"http_proxy": "https://example.invalid/?token=模拟代理秘密", "HTTPS_PROXY": "模拟代理秘密",
		"PATH": "/模拟宿主路径", "LD_LIBRARY_PATH": "/模拟宿主库路径", "LD_PRELOAD": "",
		"HOME": "/模拟宿主主目录", "ConfigPath": "/模拟错误配置目录",
		"TZ": "Asia/Shanghai", "LANG": "C.UTF-8", "LC_ALL": "C", "LC_CTYPE": "C.UTF-8",
	} {
		t.Setenv(key, value)
	}
	data, download := t.TempDir(), t.TempDir()
	env := mockEnv(data, download)
	for _, entry := range env {
		if strings.Contains(entry, "模拟") {
			key, _, _ := strings.Cut(entry, "=")
			t.Errorf("子进程环境保留不允许的宿主值：%s", key)
		}
	}
	script := `for name in XL_DASHBOARD_PASSWORD XL_BA_PASSWORD XL_SPK PRIVATE_TEST_TOKEN http_proxy HTTPS_PROXY; do
  eval 'value=${'"$name"'-}'
  if [ -n "$value" ]; then printf '禁止变量:%s\n' "$name"; fi
done
printf '%s\n' "$HOME" "$ConfigPath" "$DownloadPATH" "$LauncherListen" "$DriveListen" "$TZ" "$LANG" "$LC_ALL" "$LC_CTYPE" "$PATH" "$LD_LIBRARY_PATH"
`
	want := []string{filepath.Join(data, ".drive"), data, download, "unix://" + SOCK_LAUNCHER_LISTEN, "unix://" + SOCK_DRIVE_LISTEN, "Asia/Shanghai", "C.UTF-8", "C", "C.UTF-8"}
	for _, mode := range []string{"启动器", "网关"} {
		t.Run(mode, func(t *testing.T) {
			var output bytes.Buffer
			if mode == "启动器" {
				if err := cmdx.Run(context.Background(), "/bin/sh", cmdx.Args("-c", script), cmdx.Env(env), cmdx.Stdout(&output)); err != nil {
					t.Fatal(err)
				}
			} else {
				program := filepath.Join(t.TempDir(), "模拟网关")
				if err := os.WriteFile(program, []byte("#!/bin/sh\nprintf 'Content-Type: text/plain\\r\\n\\r\\n'\n"+script), 0700); err != nil {
					t.Fatal(err)
				}
				h := &web.CGIHandler{Handler: cgi.Handler{Path: program, Env: env, Stderr: io.Discard, Logger: log.New(io.Discard, "", 0)}}
				rw := httptest.NewRecorder()
				h.ServeHTTP(rw, httptest.NewRequest("GET", "http://example.invalid/cgi", nil))
				if rw.Code != 200 {
					t.Fatalf("网关状态错误：%d", rw.Code)
				}
				output.Write(rw.Body.Bytes())
			}
			got := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
			if strings.Contains(output.String(), "禁止变量:") || strings.Contains(output.String(), "模拟") || len(got) != len(want)+2 {
				t.Fatal("子进程继承了宿主秘密或丢失必要环境")
			}
			for i, value := range want {
				if got[i] != value {
					t.Errorf("第 %d 项必要环境不匹配", i)
				}
			}
			if got[len(want)] == "" || got[len(want)+1] != "/lib" {
				t.Error("未提供固定的可执行文件或动态库路径")
			}
		})
	}
}
