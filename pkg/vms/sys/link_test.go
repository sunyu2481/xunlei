package sys

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func Test跨设备复制权限(t *testing.T) {
	base := os.Getenv("XL_TEST_CROSS_DEVICE_DIR")
	if base == "" {
		t.Skip("需要指定另一设备上的临时目录")
	}
	source := filepath.Join(t.TempDir(), "模拟敏感文件")
	if err := os.WriteFile(source, []byte("模拟敏感内容"), 0600); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(source, 0, 60124); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("复制", func(t *testing.T) {
		t.Setenv("TMPDIR", base)
		target := filepath.Join(t.TempDir(), "副本")
		srcInfo, err := os.Stat(source)
		if err != nil {
			t.Fatal(err)
		}
		dirInfo, err := os.Stat(filepath.Dir(target))
		if err != nil {
			t.Fatal(err)
		}
		if srcInfo.Sys().(*syscall.Stat_t).Dev == dirInfo.Sys().(*syscall.Stat_t).Dev {
			t.Fatal("测试目录必须位于另一设备")
		}
		undo, err := Link(context.Background(), LinkOptions{Source: source, Target: target})
		if err != nil {
			t.Fatal(err)
		}
		defer undo()
		assertCopy(t, source, target)
	})
}

func assertCopy(t *testing.T, source, target string) {
	t.Helper()
	src, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	dst, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	ss, ds := src.Sys().(*syscall.Stat_t), dst.Sys().(*syscall.Stat_t)
	if src.Mode().Perm() != dst.Mode().Perm() || ss.Uid != ds.Uid || ss.Gid != ds.Gid {
		t.Fatalf("副本权限或属主不匹配：源=%04o %d:%d，目标=%04o %d:%d", src.Mode().Perm(), ss.Uid, ss.Gid, dst.Mode().Perm(), ds.Uid, ds.Gid)
	}
	if os.SameFile(src, dst) {
		t.Fatal("独立副本仍与源共享文件")
	}
	if err := os.WriteFile(target, []byte("副本修改"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(source)
	if err != nil || string(data) != "模拟敏感内容" {
		t.Fatalf("修改副本影响了源：%q，错误=%v", data, err)
	}
}

func Test硬链接对照(t *testing.T) {
	dir := t.TempDir()
	source, target := filepath.Join(dir, "源"), filepath.Join(dir, "链接")
	if err := os.WriteFile(source, []byte("模拟敏感内容"), 0600); err != nil {
		t.Fatal(err)
	}
	undo, err := Link(context.Background(), LinkOptions{Source: source, Target: target})
	if err != nil {
		t.Fatal(err)
	}
	defer undo()
	src, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	dst, err := os.Stat(target)
	if err != nil || !os.SameFile(src, dst) {
		t.Fatalf("显式硬链接行为改变：%v", err)
	}
}

func Test系统配置独立副本(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "新建", true: "解除遗留硬链接"}[existing], func(t *testing.T) {
			dir := t.TempDir()
			source, target := filepath.Join(dir, "源"), filepath.Join(dir, "配置")
			if err := os.WriteFile(source, []byte("模拟敏感内容"), 0600); err != nil {
				t.Fatal(err)
			}
			if existing {
				if err := os.Link(source, target); err != nil {
					t.Fatal(err)
				}
			}
			undo, err := Link(context.Background(), LinkOptions{Source: source, Target: target, Copy: true})
			if err != nil {
				t.Fatal(err)
			}
			assertCopy(t, source, target)
			undo()
			if _, err := os.Stat(target); !existing && !os.IsNotExist(err) {
				t.Fatalf("新建副本未被回滚：%v", err)
			}
		})
	}
}

func Test复制保留已有目标(t *testing.T) {
	dir := t.TempDir()
	source, target := filepath.Join(dir, "源"), filepath.Join(dir, "已有")
	for _, path := range []string{source, target} {
		if err := os.WriteFile(path, []byte(path), 0600); err != nil {
			t.Fatal(err)
		}
	}
	undo, err := Link(context.Background(), LinkOptions{Source: source, Target: target, Copy: true})
	if err != nil {
		t.Fatal(err)
	}
	undo()
	if data, err := os.ReadFile(target); err != nil || string(data) != target {
		t.Fatalf("已有目标被修改：%q，错误=%v", data, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("复制临时文件残留：%v，错误=%v", entries, err)
	}
}
