package vms

import (
	"context"
	"fmt"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cnk3x/xunlei/pkg/cmdx"
	"github.com/cnk3x/xunlei/pkg/web"
)

// 身份和根目录只在一次性子进程中改变，避免影响测试运行器。
func Test业务子进程隔离(t *testing.T) {
	if os.Getenv("XL_TEST_ISOLATION_CHILD") == "1" {
		testIsolationChild(t)
		return
	}
	if os.Geteuid() != 0 {
		t.Skip("需要在独立环境中以根用户运行")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	args := []string{"-test.run=^Test业务子进程隔离$"}
	program := self
	if executor := os.Getenv("XL_TEST_EXECUTOR"); executor != "" {
		program, args = executor, append([]string{self}, args...)
	}
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Env = append(os.Environ(), "XL_TEST_ISOLATION_CHILD=1", "XL_TEST_ISOLATION_DIR="+t.TempDir())
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("独立进程验证失败：%v\n%s", err, output)
	}
}

func testIsolationChild(t *testing.T) {
	base := os.Getenv("XL_TEST_ISOLATION_DIR")
	jail := filepath.Join(base, "隔离")
	if err := os.Mkdir(jail, 0755); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jail, "验证程序"), data, 0755); err != nil {
		t.Fatal(err)
	}
	program, args := "/验证程序", []string{"-test.run=^Test隔离内身份与句柄$"}
	if executor := os.Getenv("XL_TEST_EXECUTOR"); executor != "" {
		data, err := os.ReadFile(executor)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(jail, "模拟器"), data, 0755); err != nil {
			t.Fatal(err)
		}
		program, args = "/模拟器", append([]string{program}, args...)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, ids := range [][2]int{{60123, 60124}, {0, 60124}, {60123, 0}, {0, 0}} {
		err := Execute(context.Background(), Root(jail), User(ids[0], ids[1]), Run(func(ctx context.Context) error {
			// 父进程需要保留恢复根目录和清理挂载的能力。
			if os.Geteuid() != 0 || os.Getegid() != 0 {
				return fmt.Errorf("父进程失去清理身份：%d:%d", os.Geteuid(), os.Getegid())
			}
			env := []string{"XL_TEST_ISOLATION_PROBE=1", "XL_TEST_UID=" + strconv.Itoa(ids[0]), "XL_TEST_GID=" + strconv.Itoa(ids[1])}
			if err := cmdx.Run(ctx, program,
				cmdx.Args(args...),
				cmdx.Env(env), cmdx.Stdin(os.Stdin),
				cmdx.Stdout(os.Stdout), cmdx.Stderr(os.Stderr)); err != nil {
				return err
			}
			handler := &web.CGIHandler{Handler: cgi.Handler{
				Path: program, Dir: "/", Args: args,
				Env: append(env, "XL_TEST_CGI_PROBE=1"), Stderr: os.Stderr,
			}, Credential: cmdx.ProcessCredential(ctx)}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest("POST", "http://localhost/", strings.NewReader("验证")))
			if response.Code != 200 || !strings.Contains(response.Body.String(), "PASS") || strings.Contains(response.Body.String(), "FAIL") {
				return fmt.Errorf("网关子进程验证失败：状态=%d，响应=%s", response.Code, response.Body.String())
			}
			return nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		if got, err := os.Getwd(); err != nil || got != wd || os.Geteuid() != 0 {
			t.Fatalf("未恢复宿主机目录与身份：目录=%q，错误=%v", got, err)
		}
	}
}

func Test隔离内身份与句柄(t *testing.T) {
	if os.Getenv("XL_TEST_ISOLATION_PROBE") != "1" {
		t.Skip("仅由隔离验证子进程调用")
	}
	if os.Getenv("XL_TEST_CGI_PROBE") == "1" {
		fmt.Print("Content-Type: text/plain\r\n\r\n")
	}
	uid, _ := strconv.Atoi(os.Getenv("XL_TEST_UID"))
	gid, _ := strconv.Atoi(os.Getenv("XL_TEST_GID"))
	if os.Getuid() != uid || os.Geteuid() != uid || os.Getgid() != gid || os.Getegid() != gid {
		t.Fatalf("子进程身份错误：用户=%d/%d，组=%d/%d", os.Getuid(), os.Geteuid(), os.Getgid(), os.Getegid())
	}
	groups, err := os.Getgroups()
	if err != nil || len(groups) != 0 {
		t.Fatalf("附加组未清空：%v，错误=%v", groups, err)
	}
	if uid != 0 && syscall.Seteuid(0) == nil {
		t.Fatal("非根用户子进程仍能恢复根权限")
	}
	if uid != 0 && gid != 0 && syscall.Setegid(0) == nil {
		t.Fatal("非根用户子进程仍能恢复根用户组")
	}
	// 隔离中没有挂载宿主机目录；任何可切换到目录的额外句柄都属于泄漏。
	var leaked []string
	for fd := 3; fd < 256; fd++ {
		if syscall.Fchdir(fd) == nil {
			leaked = append(leaked, strconv.Itoa(fd))
		}
	}
	if len(leaked) != 0 {
		t.Fatalf("继承了目录句柄：%s", strings.Join(leaked, ","))
	}
}
