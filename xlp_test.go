package xunlei

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cnk3x/xunlei/spk"
	"github.com/ulikunitz/xz"
)

func Test准备环境传播改属主错误(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("需要普通用户触发权限错误")
	}
	for _, recursive := range []bool{false, true} {
		t.Run(map[bool]string{false: "单层修改失败", true: "递归遍历失败"}[recursive], func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, DIR_SYNOPKG_PKGDEST)
			installTestPackage(t, target)
			cfg := Config{Root: root, DirData: filepath.Join(root, "数据"), Uid: os.Getuid() + 1, Gid: os.Getgid()}
			cfg.SpkUrl = (&url.URL{Scheme: "file", Path: filepath.Join(root, "仅复用缓存.spk")}).String()
			want := syscall.EPERM
			if recursive {
				cfg.Uid = os.Getuid()
				blocked := filepath.Join(target, "不可遍历")
				if err := os.Mkdir(blocked, 0000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.Chmod(blocked, 0700) })
				want = syscall.EACCES
			}
			_, err := Before(cfg)(context.Background())
			if !errors.Is(err, want) {
				t.Fatalf("准备阶段未传播预期权限错误 %v：%v", want, err)
			}
			if _, err := os.Stat(filepath.Join(root, FILE_SYNO_AUTHENTICATE_CGI)); !os.IsNotExist(err) {
				t.Fatalf("准备失败未执行回滚：%v", err)
			}
		})
	}
}

func installTestPackage(t *testing.T, target string) {
	t.Helper()
	var compressed, archive bytes.Buffer
	xw, err := xz.NewWriter(&compressed)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(xw)
	for _, file := range []string{"bin/bin/version", "bin/bin/xunlei-pan-cli-launcher." + runtime.GOARCH, "bin/bin/xunlei-pan-cli.test-1.0." + runtime.GOARCH, "ui/index.cgi"} {
		content := []byte("test-1.0")
		if err := tw.WriteHeader(&tar.Header{Name: file, Size: int64(len(content)), Mode: 0755}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := xw.Close(); err != nil {
		t.Fatal(err)
	}
	outer := tar.NewWriter(&archive)
	if err := outer.WriteHeader(&tar.Header{Name: "package.tgz", Size: int64(compressed.Len()), Mode: 0644}); err != nil {
		t.Fatal(err)
	}
	if _, err := outer.Write(compressed.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := outer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := spk.Extract(context.Background(), &archive, target); err != nil {
		t.Fatal(err)
	}
}

func TestMockEnvInternalListeners(t *testing.T) {
	t.Setenv("LauncherListen", "127.0.0.1:5051")
	t.Setenv("DriveListen", "127.0.0.1:5050")
	cmd := exec.Command(os.Args[0])
	cmd.Env = mockEnv(t.TempDir(), t.TempDir())
	env := make(map[string]string)
	for _, entry := range cmd.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			env[key] = value
		}
	}
	for key, want := range map[string]string{
		"LauncherListen": "unix://" + SOCK_LAUNCHER_LISTEN,
		"DriveListen":    "unix://" + SOCK_DRIVE_LISTEN,
	} {
		if got := env[key]; got != want {
			t.Errorf("子进程的 %s 地址应为 %q，实际为 %q", key, want, got)
		}
	}
}

func TestXxx(t *testing.T) {
	dt, _ := time.Parse("15:04:05.00", "05:52:48.18")

	t.Log(dt.Format(time.RFC3339Nano))
}
