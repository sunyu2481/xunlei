package authenticate_cgi

import (
	"bytes"
	"debug/elf"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

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
