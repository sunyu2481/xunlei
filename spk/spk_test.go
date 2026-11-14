package spk

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ulikunitz/xz"
)

type testEntry struct {
	name, data, link string
	kind             byte
}

func testTar(t *testing.T, entries []testEntry) []byte {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for _, entry := range entries {
		kind := entry.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		h := &tar.Header{Name: entry.name, Mode: 0777, Typeflag: kind, Linkname: entry.link}
		if kind == tar.TypeReg {
			h.Size = int64(len(entry.data))
		}
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size != 0 {
			if _, err := io.WriteString(w, entry.data); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func testPackage(t *testing.T, entries []testEntry) []byte {
	t.Helper()
	var b bytes.Buffer
	w, err := xz.NewWriter(&b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(testTar(t, entries)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return testTar(t, []testEntry{{name: "package.tgz", data: b.String()}})
}

func testFiles() []testEntry {
	return []testEntry{
		{name: "bin/bin/version", data: "3.1.10\n"},
		{name: "bin/bin/xunlei-pan-cli-launcher." + runtime.GOARCH, data: "模拟启动器"},
		{name: "bin/bin/xunlei-pan-cli.3.1.10." + runtime.GOARCH, data: "模拟主程序"},
		{name: "ui/index.cgi", data: "模拟网关"},
	}
}

func assertFiles(t *testing.T, dir string) {
	t.Helper()
	for _, entry := range testFiles() {
		got, err := os.ReadFile(filepath.Join(dir, entry.name))
		if err != nil || string(got) != entry.data {
			t.Fatalf("文件 %s 内容错误：%q，错误：%v", entry.name, got, err)
		}
	}
}

func Test解包文件白名单(t *testing.T) {
	files := testFiles()
	files = append(files,
		testEntry{name: "bin/bin/version.bak", data: "无关版本"},
		testEntry{name: "bin/bin/xunlei-pan-cli-launcher." + runtime.GOARCH + ".bak", data: "无关文件"},
		testEntry{name: "bin/bin/xunlei-pan-cli.fake", data: "无关文件"},
		testEntry{name: "bin/bin/xunlei-pan-cli.0.0.1." + runtime.GOARCH, data: "其他版本"},
		testEntry{name: "ui/其他文件", data: "无关文件"},
		testEntry{name: "lib/可忽略链接", kind: tar.TypeSymlink, link: "库文件"},
	)
	dir := t.TempDir()
	if err := Extract(context.Background(), bytes.NewReader(testPackage(t, files)), dir); err != nil {
		t.Fatal(err)
	}
	assertFiles(t, dir)
	for _, entry := range testFiles() {
		info, err := os.Stat(filepath.Join(dir, entry.name))
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0755)
		if entry.name == versionFile {
			want = 0644
		}
		if info.Mode().Perm() != want {
			t.Errorf("文件 %s 权限为 %o，期望 %o", entry.name, info.Mode().Perm(), want)
		}
	}
	for _, entry := range files[4:] {
		if _, err := os.Lstat(filepath.Join(dir, entry.name)); !os.IsNotExist(err) {
			t.Errorf("不应提取白名单外文件 %s：%v", entry.name, err)
		}
	}
}

func Test解包拒绝路径穿越(t *testing.T) {
	for _, name := range []string{
		"bin/bin/xunlei-pan-cli/../../../../外部文件",
		"/bin/bin/version", "../外部文件", "bin/../bin/bin/version",
		`bin\bin\version`,
	} {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			outside := filepath.Join(base, "外部文件")
			if err := os.WriteFile(outside, []byte("原内容"), 0600); err != nil {
				t.Fatal(err)
			}
			entries := append(testFiles(), testEntry{name: name, data: "越界覆盖"})
			dir := filepath.Join(base, "目标")
			if err := Extract(context.Background(), bytes.NewReader(testPackage(t, entries)), dir); err == nil {
				t.Fatal("应拒绝不安全条目路径")
			}
			got, err := os.ReadFile(outside)
			if err != nil || string(got) != "原内容" {
				t.Fatalf("目录外文件被改写：%q，错误：%v", got, err)
			}
			if _, err := os.Stat(filepath.Join(dir, "bin/bin/version")); !os.IsNotExist(err) {
				t.Fatalf("归档验证失败前不应发布文件：%v", err)
			}
			assertNoStage(t, dir)
		})
	}
}

func assertNoStage(t *testing.T, dir string) {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(dir, ".spk-*"))
	if err != nil || len(names) != 0 {
		t.Fatalf("临时目录未清理：%v，错误：%v", names, err)
	}
}

func Test解包拒绝根路径祖先链接(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "外部")
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "链接")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := Extract(context.Background(), bytes.NewReader(testPackage(t, testFiles())), filepath.Join(link, "目标")); err == nil {
		t.Fatal("应拒绝根路径中的祖先链接")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("外部目录不应改变：%v，错误：%v", entries, err)
	}
}

func Test解包拒绝外层异常条目(t *testing.T) {
	for _, entry := range []testEntry{
		{name: "package.tgz", kind: tar.TypeSymlink, link: "外部"},
		{name: "../package.tgz", data: "无效"},
		{name: "/package.tgz", data: "无效"},
	} {
		t.Run(entry.name, func(t *testing.T) {
			if err := Extract(context.Background(), bytes.NewReader(testTar(t, []testEntry{entry})), t.TempDir()); err == nil {
				t.Fatal("应拒绝异常外层条目")
			}
		})
	}
}

func Test解包允许相对前缀和覆盖普通文件(t *testing.T) {
	files := testFiles()
	for i := range files {
		files[i].name = "./" + files[i].name
	}
	dir := t.TempDir()
	for range 2 {
		if err := Extract(context.Background(), bytes.NewReader(testPackage(t, files)), dir); err != nil {
			t.Fatal(err)
		}
		assertFiles(t, dir)
		assertNoStage(t, dir)
	}
}

func Test解包拒绝已有链接(t *testing.T) {
	for _, kind := range []string{"文件链接", "目录链接", "根目录链接", "硬链接"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			dir, outside := filepath.Join(base, "目标"), filepath.Join(base, "外部")
			if err := os.Mkdir(outside, 0700); err != nil {
				t.Fatal(err)
			}
			external := filepath.Join(outside, "version")
			if err := os.WriteFile(external, []byte("原内容"), 0600); err != nil {
				t.Fatal(err)
			}
			if kind == "根目录链接" {
				if err := os.Symlink(outside, dir); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.MkdirAll(filepath.Join(dir, "bin"), 0700); err != nil {
					t.Fatal(err)
				}
				if kind == "目录链接" {
					if err := os.Symlink(outside, filepath.Join(dir, "bin/bin")); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.Mkdir(filepath.Join(dir, "bin/bin"), 0700); err != nil {
						t.Fatal(err)
					}
					link := os.Symlink
					if kind == "硬链接" {
						link = os.Link
					}
					if err := link(external, filepath.Join(dir, "bin/bin/version")); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := Extract(context.Background(), bytes.NewReader(testPackage(t, testFiles())), dir); err == nil {
				t.Fatal("应拒绝已有链接")
			}
			got, err := os.ReadFile(external)
			if err != nil || string(got) != "原内容" {
				t.Fatalf("链接目标被改写：%q，错误：%v", got, err)
			}
		})
	}
}

func Test解包拒绝异常类型和重复文件(t *testing.T) {
	for _, kind := range []byte{tar.TypeSymlink, tar.TypeLink, tar.TypeDir, tar.TypeFifo, tar.TypeChar, tar.TypeBlock} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			files := testFiles()
			files[0].kind, files[0].link = kind, "外部文件"
			if err := Extract(context.Background(), bytes.NewReader(testPackage(t, files)), t.TempDir()); err == nil {
				t.Fatal("应拒绝白名单中的非普通文件")
			}
		})
	}
	files := append(testFiles(), testEntry{name: "./bin/bin/version", data: "重复版本"})
	if err := Extract(context.Background(), bytes.NewReader(testPackage(t, files)), t.TempDir()); err == nil {
		t.Fatal("规范化后重复的文件应报错")
	}
}
