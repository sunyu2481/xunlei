package sys

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/cnk3x/xunlei/pkg/fo"
)

func Test新建文件失败清理并允许重试(t *testing.T) {
	for _, closeEarly := range []bool{false, true} {
		t.Run(map[bool]string{false: "部分写入失败", true: "文件提前关闭"}[closeEarly], func(t *testing.T) {
			root := t.TempDir()
			name := filepath.Join(root, "子目录", "文件")
			_, err := NewFile(context.Background(), name, func(f *os.File) error {
				if _, err := f.WriteString("半成品"); err != nil {
					return err
				}
				if closeEarly {
					return f.Close()
				}
				return errors.New("模拟写入失败")
			})
			if err == nil {
				t.Fatal("应报告失败")
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("失败残留：%v，%v", entries, err)
			}
			undo, err := NewFile(context.Background(), name, fo.Content("完整内容"))
			if err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(name)
			if err != nil || string(b) != "完整内容" {
				t.Fatalf("重试未写入完整文件：%q，%v", b, err)
			}
			undo()
		})
	}
}

func Test新建文件校验已有内容(t *testing.T) {
	for _, content := range []string{"", "半成品", "完整内容"} {
		t.Run("已有"+content, func(t *testing.T) {
			name := filepath.Join(t.TempDir(), "文件")
			if err := os.WriteFile(name, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			undo, err := NewFile(context.Background(), name, fo.Content("完整内容"), fo.Perm(0600))
			if (err != nil) != (content != "完整内容") {
				t.Fatalf("校验已有文件错误：%v", err)
			}
			if undo != nil {
				undo()
			}
			if b, err := os.ReadFile(name); err != nil || string(b) != content {
				t.Fatalf("已有文件被改动：%q，%v", b, err)
			}
		})
	}
}
