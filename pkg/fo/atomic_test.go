package fo

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func Test原子写入失败保留旧文件(t *testing.T) {
	for _, exists := range []bool{false, true} {
		for _, closeEarly := range []bool{false, true} {
			t.Run(map[bool]string{false: "新文件", true: "旧文件"}[exists]+map[bool]string{false: "写失败", true: "关闭失败"}[closeEarly], func(t *testing.T) {
				dir := t.TempDir()
				name := filepath.Join(dir, "正式文件")
				if exists {
					if err := os.WriteFile(name, []byte("旧内容"), 0640); err != nil {
						t.Fatal(err)
					}
				}
				err := AtomicWrite(name, func(f *os.File) error {
					if _, err := f.WriteString("部分内容"); err != nil {
						return err
					}
					if closeEarly {
						return f.Close()
					}
					return errors.New("模拟写入失败")
				})
				if err == nil {
					t.Fatal("应返回写入或关闭错误")
				}
				b, err := os.ReadFile(name)
				if exists && (err != nil || string(b) != "旧内容") {
					t.Fatalf("旧文件被破坏：%q，%v", b, err)
				}
				if !exists && !os.IsNotExist(err) {
					t.Fatalf("留下半成品：%v", err)
				}
				entries, err := os.ReadDir(dir)
				want := 0
				if exists {
					want = 1
				}
				if err != nil || len(entries) != want {
					t.Fatalf("临时文件未清理：%v，%v", entries, err)
				}
			})
		}
	}
}

func Test原子写入成功与排他发布(t *testing.T) {
	name := filepath.Join(t.TempDir(), "文件")
	for _, content := range []string{"初始", "新内容"} {
		if err := AtomicWrite(name, Content(content), Perm(0600)); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(name)
		if err != nil || string(b) != content {
			t.Fatalf("文件=%q，%v", b, err)
		}
	}
	if err := AtomicWrite(name, func(*os.File) error { t.Fatal("排他目标存在时不应写入"); return nil }, FlagExcl(false)); !os.IsExist(err) {
		t.Fatalf("排他错误=%v", err)
	}
}
