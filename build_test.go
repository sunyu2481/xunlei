package xunlei

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"debug/elf"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func Test并行构建归档保留对应架构(t *testing.T) {
	dir := prepareBuildTest(t, "Makefile", "xlp.go")
	cp := requireBuildTool(t, "cp")
	tarTool := requireBuildTool(t, "tar")
	for _, arch := range []string{"amd64", "arm64"} {
		data, err := os.ReadFile("embed/authenticate_cgi/authenticate_cgi_linux_" + arch)
		if err != nil {
			t.Fatal(err)
		}
		writeBuildTestFile(t, dir, "fixtures/xlp-"+arch, string(data))
	}
	writeBuildTestFile(t, dir, "tools/compile", `#!/bin/sh
set -eu
while [ "$1" != -o ]; do shift; done
shift
mkdir -p "$(dirname "$1")"
exec `+quoteBuildShell(cp)+` "fixtures/xlp-$GOARCH" "$1"
`)
	// 只控制合法调度顺序，复制与归档仍由真实工具完成。
	wait := `
wait_file() {
  count=0
  while [ ! -e "$1" ]; do
    count=$((count + 1))
    if [ "$count" -gt 500 ]; then echo '等待构建屏障超时' >&2; exit 1; fi
    sleep 0.01
  done
}
`
	writeBuildTestFile(t, dir, "tools/cp", "#!/bin/sh\nset -eu\n"+wait+`
if [ "$1" = artifacts/xlp-arm64 ]; then wait_file amd64-copied; fi
`+quoteBuildShell(cp)+` "$@"
case "$1" in
  artifacts/xlp-amd64) touch amd64-copied; wait_file arm64-copied ;;
  artifacts/xlp-arm64) touch arm64-copied ;;
esac
`)
	writeBuildTestFile(t, dir, "tools/tar", "#!/bin/sh\nset -eu\n"+wait+`
`+quoteBuildShell(tarTool)+` "$@"
case "$*" in
  *linux-amd64*) touch amd64-packed ;;
  *linux-arm64*) touch arm64-packed ;;
esac
wait_file amd64-packed
wait_file arm64-packed
`)
	output, err := runBuildMake(t, dir, nil, "-j2", "build", "VERSION=test", "GBuild=tools/compile")
	if err != nil {
		t.Errorf("并行构建失败：%v\n%s", err, output)
	}
	for arch, machine := range map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64} {
		t.Run(arch, func(t *testing.T) {
			archive, err := os.Open(filepath.Join(dir, "artifacts/xlp-test-linux-"+arch+".tar.gz"))
			if err != nil {
				t.Fatal(err)
			}
			defer archive.Close()
			gz, err := gzip.NewReader(archive)
			if err != nil {
				t.Fatal(err)
			}
			defer gz.Close()
			tr := tar.NewReader(gz)
			hdr, err := tr.Next()
			if err != nil {
				t.Fatal(err)
			}
			if hdr.Name != "xlp" || hdr.Typeflag != tar.TypeReg || hdr.Mode&0111 == 0 {
				t.Fatalf("归档入口错误：%+v", hdr)
			}
			data, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
			program, err := elf.NewFile(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			if program.Machine != machine {
				t.Errorf("归档架构为 %s，需要 %s", program.Machine, machine)
			}
			want, err := os.ReadFile(filepath.Join(dir, "artifacts/xlp-"+arch))
			if err != nil || !bytes.Equal(data, want) {
				t.Errorf("归档与对应架构的构建结果不一致：%v", err)
			}
			if _, err := tr.Next(); err != io.EOF {
				t.Errorf("归档应只有 xlp：%v", err)
			}
		})
	}
}

func Test镜像目标按宿主和指定架构构建(t *testing.T) {
	dir := prepareBuildTest(t, "Makefile", "xlp.go")
	writeBuildTestFile(t, dir, "tools/go", `#!/bin/sh
set -eu
test "$*" = 'env GOHOSTARCH'
printf '%s\n' "$XLP_TEST_HOSTARCH"
`)
	for _, host := range []string{"amd64", "arm64"} {
		for _, target := range []string{"", "amd64", "arm64"} {
			for _, image := range []string{"busybox", "ubuntu", "debian"} {
				t.Run(host+"/"+target+"/"+image, func(t *testing.T) {
					arch := target
					args := []string{"-n", image}
					if arch == "" {
						arch = host
					} else {
						args = append(args, "TARGETARCH="+target)
					}
					output, err := runBuildMake(t, dir, []string{"XLP_TEST_HOSTARCH=" + host}, args...)
					if err != nil {
						t.Fatalf("镜像依赖解析失败：%v\n%s", err, output)
					}
					if !strings.Contains(output, "GOARCH="+arch+" ") || !strings.Contains(output, "--platform linux/"+arch+" ") {
						t.Errorf("缺少 %s 的编译依赖或镜像平台：\n%s", arch, output)
					}
				})
			}
		}
	}
}

func Test认证C构建声明双架构编译器(t *testing.T) {
	dir := prepareBuildTest(t, "cmd/authenticate_cgi_c/Makefile", "cmd/authenticate_cgi_c/main.c")
	output, err := runBuildMake(t, dir, nil, "-n", "-C", "cmd/authenticate_cgi_c", "build_static")
	if err != nil {
		t.Fatalf("认证构建依赖解析失败：%v\n%s", err, output)
	}
	for _, compiler := range []string{"x86_64-linux-gnu-gcc", "aarch64-linux-gnu-gcc"} {
		if !strings.Contains(output, compiler+" main.c ") {
			t.Errorf("未显式使用目标编译器 %s：\n%s", compiler, output)
		}
	}
}

func Test认证C构建拒绝错误架构并保留原文件(t *testing.T) {
	requireBuildTool(t, "readelf")
	cp := requireBuildTool(t, "cp")
	for arch, other := range map[string]string{"amd64": "arm64", "arm64": "amd64"} {
		for _, failure := range []string{"编译器目标错误", "ELF架构错误", "编译失败"} {
			t.Run(arch+"/"+failure, func(t *testing.T) {
				dir := prepareBuildTest(t, "cmd/authenticate_cgi_c/Makefile", "cmd/authenticate_cgi_c/main.c")
				fixture, err := os.ReadFile("embed/authenticate_cgi/authenticate_cgi_linux_" + other)
				if err != nil {
					t.Fatal(err)
				}
				writeBuildTestFile(t, dir, "fixture", string(fixture))
				triple := map[string]string{"amd64": "x86_64-linux-gnu", "arm64": "aarch64-linux-gnu"}[arch]
				if failure == "编译器目标错误" {
					triple = "unsupported-linux-gnu"
				}
				writeBuildTestFile(t, dir, "tools/compiler", `#!/bin/sh
set -eu
if [ "$1" = -dumpmachine ]; then printf '%s\n' `+quoteBuildShell(triple)+`; exit 0; fi
while [ "$1" != -o ]; do shift; done
shift
`+quoteBuildShell(cp)+` "$XLP_TEST_FIXTURE" "$1"
if [ "$XLP_TEST_CC_FAILURE" = '编译失败' ]; then exit 1; fi
`)
				name := "cmd/authenticate_cgi_c/out/authenticate_cgi_linux_" + arch
				writeBuildTestFile(t, dir, name, "原认证程序")
				output, err := runBuildMake(t, dir, []string{
					"XLP_TEST_FIXTURE=" + filepath.Join(dir, "fixture"),
					"XLP_TEST_CC_FAILURE=" + failure,
				}, "-C", "cmd/authenticate_cgi_c", arch, "TARGET=out/authenticate_cgi_linux_",
					"CC_"+strings.ToUpper(arch)+"="+filepath.Join(dir, "tools/compiler"))
				if err == nil {
					t.Fatalf("错误构建意外成功：\n%s", output)
				}
				if failure != "编译失败" && !strings.Contains(output, "不匹配") {
					t.Errorf("错误架构缺少明确诊断：\n%s", output)
				}
				data, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil || string(data) != "原认证程序" {
					t.Errorf("失败构建覆盖原文件：%v", err)
				}
				entries, err := os.ReadDir(filepath.Join(dir, "cmd/authenticate_cgi_c/out"))
				if err != nil || len(entries) != 1 {
					t.Errorf("失败后残留临时产物：%v，%v", entries, err)
				}
			})
		}
	}
}

func prepareBuildTest(t *testing.T, files ...string) string {
	t.Helper()
	requireBuildTool(t, "make")
	dir := t.TempDir()
	for _, name := range files {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		writeBuildTestFile(t, dir, name, string(data))
	}
	return dir
}

func requireBuildTool(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("构建验证需要 %s：%v", name, err)
	}
	return path
}

func writeBuildTestFile(t *testing.T, dir, name, data string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0755); err != nil {
		t.Fatal(err)
	}
}

func runBuildMake(t *testing.T, dir string, env []string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, requireBuildTool(t, "make"), args...)
	cmd.Dir = dir
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "MAKEFLAGS", "MFLAGS", "MAKELEVEL", "TARGETARCH", "CC_AMD64", "CC_ARM64":
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, "PATH="+filepath.Join(dir, "tools")+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd.Env = append(cmd.Env, env...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func quoteBuildShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
