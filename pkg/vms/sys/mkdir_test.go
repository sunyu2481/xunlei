package sys

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func Test创建目录回滚后可重复执行(t *testing.T) {
	base := t.TempDir()
	marker := filepath.Join(base, "原文件")
	if err := os.WriteFile(marker, []byte("保留内容"), 0600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "新增", "末级")
	for i := range 3 {
		t.Run("第"+strconv.Itoa(i+1)+"次", func(t *testing.T) {
			undo, err := Mkdir(context.Background(), dir, 0700)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(undo)
			if info, err := os.Stat(dir); err != nil || !info.IsDir() {
				t.Fatalf("未创建末级目录：%v", err)
			}
			keep, err := Mkdir(context.Background(), dir, 0700)
			if err != nil {
				t.Fatal(err)
			}
			keep()
			if info, err := os.Stat(dir); err != nil || !info.IsDir() {
				t.Fatalf("重复创建的回滚删除了已有目录：%v", err)
			}
		})
		if _, err := os.Stat(filepath.Join(base, "新增")); !os.IsNotExist(err) {
			t.Fatalf("新增目录未完全回滚：%v", err)
		}
		if data, err := os.ReadFile(marker); err != nil || string(data) != "保留内容" {
			t.Fatalf("回滚改变了原文件：%q，错误=%v", data, err)
		}
	}
}
