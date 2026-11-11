package xunlei

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

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
