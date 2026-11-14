package fo

import (
	"os"
	"path/filepath"
	"testing"
)

var fileWriters = []struct {
	name  string
	write func(string, Process, ...Option) error
}{
	{"直接写入", OpenWrite},
	{"原子写入", AtomicWrite},
}

func Test写入保留零权限(t *testing.T) {
	source, err := os.OpenFile(filepath.Join(t.TempDir(), "权限来源"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { source.Close() })
	if err := source.Chmod(0); err != nil {
		t.Fatal(err)
	}
	for _, writer := range fileWriters {
		for _, tc := range []struct {
			name string
			opts []Option
		}{
			{"显式零权限", []Option{Perm(0)}},
			{"覆盖已有权限", []Option{Perm(0600), Perm(0)}},
			{"继承零权限", []Option{Perm(0600), PermFrom(source)}},
		} {
			t.Run(writer.name+"/"+tc.name, func(t *testing.T) {
				name := filepath.Join(t.TempDir(), "文件")
				if err := writer.write(name, Content("内容"), tc.opts...); err != nil {
					t.Fatal(err)
				}
				info, err := os.Stat(name)
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != 0 || info.Size() != int64(len("内容")) {
					t.Fatalf("文件权限=%04o，大小=%d，期望权限=0000且内容完整", info.Mode().Perm(), info.Size())
				}
			})
		}
	}
}

func Test写入目录保留零权限(t *testing.T) {
	for _, writer := range fileWriters {
		t.Run(writer.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "零权限目录")
			t.Cleanup(func() {
				if err := os.Chmod(dir, 0700); err != nil && !os.IsNotExist(err) {
					t.Errorf("恢复临时目录权限失败：%v", err)
				}
			})
			called := false
			err := writer.write(filepath.Join(dir, "文件"), func(f *os.File) error {
				called = true
				return Content("内容")(f)
			}, DirPerm(0700), DirPerm(0))
			// 普通用户不能在零权限目录中继续创建文件；特权用户仍可写入。
			if err != nil && !os.IsPermission(err) {
				t.Fatalf("写入错误=%v，期望成功或权限不足", err)
			}
			if called != (err == nil) {
				t.Fatalf("回调执行=%v，写入错误=%v", called, err)
			}
			info, err := os.Stat(dir)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0 {
				t.Fatalf("目录权限=%04o，期望=0000", info.Mode().Perm())
			}
		})
	}
}

func Test写入默认权限(t *testing.T) {
	for _, writer := range fileWriters {
		t.Run(writer.name, func(t *testing.T) {
			root := t.TempDir()
			reference := filepath.Join(root, "对照目录")
			if err := os.Mkdir(reference, 0777); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(reference, "文件"), []byte("内容"), 0666); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(root, "输出目录")
			if err := writer.write(filepath.Join(dir, "文件"), Content("内容")); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{".", "文件"} {
				want, err := os.Stat(filepath.Join(reference, name))
				if err != nil {
					t.Fatal(err)
				}
				got, err := os.Stat(filepath.Join(dir, name))
				if err != nil {
					t.Fatal(err)
				}
				if got.Mode().Perm() != want.Mode().Perm() {
					t.Errorf("%s 权限=%04o，期望=%04o", name, got.Mode().Perm(), want.Mode().Perm())
				}
			}
		})
	}
}

func Test排他打开(t *testing.T) {
	for _, writer := range fileWriters {
		for _, tc := range []struct {
			name    string
			exists  bool
			existOk bool
		}{
			{"已存在时跳过", true, true},
			{"已存在时报错", true, false},
			{"允许跳过时新建", false, true},
			{"不允许跳过时新建", false, false},
		} {
			t.Run(writer.name+"/"+tc.name, func(t *testing.T) {
				name := filepath.Join(t.TempDir(), "文件")
				if tc.exists {
					if err := os.WriteFile(name, []byte("原内容"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				calls := 0
				err := writer.write(name, func(f *os.File) error {
					calls++
					if f == nil {
						t.Error("回调收到空文件")
						return os.ErrInvalid
					}
					return Content("新内容")(f)
				}, FlagExcl(tc.existOk))
				if tc.exists && !tc.existOk {
					if !os.IsExist(err) {
						t.Errorf("排他打开错误=%v，期望文件已存在", err)
					}
				} else if err != nil {
					t.Errorf("排他打开失败：%v", err)
				}
				wantCalls, wantContent := 1, "新内容"
				if tc.exists {
					wantCalls, wantContent = 0, "原内容"
				}
				if calls != wantCalls {
					t.Errorf("回调次数=%d，期望=%d", calls, wantCalls)
				}
				content, err := os.ReadFile(name)
				if err != nil || string(content) != wantContent {
					t.Fatalf("文件内容=%q，错误=%v，期望=%q", content, err, wantContent)
				}
			})
		}
	}
}

func Test排他打开保留其他错误(t *testing.T) {
	called := false
	err := OpenWrite(filepath.Join(t.TempDir(), "不存在"), func(*os.File) error {
		called = true
		return nil
	}, Flag(os.O_WRONLY), FlagExcl(true))
	if !os.IsNotExist(err) || called {
		t.Fatalf("打开错误=%v，回调执行=%v，期望返回不存在错误且跳过回调", err, called)
	}
}

func Test匹配文件保留零权限(t *testing.T) {
	name := filepath.Join(t.TempDir(), "文件")
	if err := os.WriteFile(name, []byte("内容"), 0666); err != nil {
		t.Fatal(err)
	}
	if err := MatchFile(name, Content("内容")); err != nil {
		t.Fatalf("默认权限应匹配：%v", err)
	}
	if err := MatchFile(name, Content("内容"), Perm(0)); err == nil {
		t.Fatal("可读写文件不应匹配零权限要求")
	}
}

func TestXxx(t *testing.T) {
	wRepl := func(flag int, wFlag int) int {
		return flag&^(os.O_APPEND|os.O_TRUNC|os.O_EXCL) | wFlag
	}

	const (
		TRUNC  = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
		APPEND = os.O_CREATE | os.O_WRONLY | os.O_APPEND
		EXCL   = os.O_CREATE | os.O_WRONLY | os.O_EXCL
	)

	t.Log(wRepl(TRUNC, os.O_APPEND), APPEND)
	t.Log(wRepl(APPEND, os.O_EXCL), EXCL)
	t.Log(wRepl(EXCL, os.O_TRUNC), TRUNC)
}
