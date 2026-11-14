package authenticate_cgi

import (
	"bytes"
	"context"
	"debug/elf"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func Test认证文件权限(t *testing.T) {
	for _, mode := range []os.FileMode{0600, 0777} {
		t.Run(mode.String(), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "authenticate.cgi")
			f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err := f.Chmod(mode); err != nil {
				t.Fatal(err)
			}
			if err := WriteTo(f); err != nil {
				t.Fatal(err)
			}
			info, err := f.Stat()
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0755 {
				t.Fatalf("认证文件权限应为 0755，实际为 %04o", info.Mode().Perm())
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, Bytes) {
				t.Fatalf("认证文件内容错误：%v", err)
			}
		})
	}
}

func Test非可信用户不能改写认证文件(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("需要在独立环境中以根用户运行")
	}
	dir := t.TempDir()
	// 临时目录允许遍历，确保拒绝写入来自认证文件本身的权限。
	for _, path := range []string{filepath.Dir(dir), dir} {
		if err := os.Chmod(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "authenticate.cgi")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteTo(f); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", `test -r "$1" && test -x "$1" && ! printf '不可信写入' > "$1"`, "权限验证", path)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 60123, Gid: 60124}}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("非可信用户权限验证失败：%v，输出：%s", err, output)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, Bytes) {
		t.Fatalf("非可信用户改写了认证文件：%v", err)
	}
}

func TestAuthenticateCGIStatic(t *testing.T) {
	f, err := elf.NewFile(bytes.NewReader(Bytes))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, program := range f.Progs {
		if program.Type == elf.PT_INTERP || program.Type == elf.PT_DYNAMIC {
			t.Fatal("认证程序不能依赖隔离环境中的动态链接器或 GLIBC")
		}
	}
}

func TestAuthenticateCGIOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "authenticate.cgi")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteTo(f); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("认证程序执行失败：%v，错误输出：%s", err, &stderr)
	}
	if want := "Content-Type: text/plain; charset=utf-8\n\nadmin\n"; string(output) != want {
		t.Errorf("CGI 标准输出错误：%q", output)
	}
	if stderr.Len() != 0 {
		t.Errorf("CGI 内容不应写入标准错误：%q", stderr.String())
	}
}
