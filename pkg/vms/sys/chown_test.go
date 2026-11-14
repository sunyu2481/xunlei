package sys

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func Test改属主范围和用户组(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("需要在独立环境中以根用户运行")
	}
	for _, tc := range []struct {
		name             string
		uid, gid         int
		wantUID, wantGID uint32
	}{
		{"不同用户与组", 60123, 60124, 60123, 60124},
		{"仅改组", 0, 60124, 0, 60124},
		{"仅改用户", 60123, 0, 60123, 0},
		{"均不修改", 0, 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, outside := t.TempDir(), t.TempDir()
			file, external := filepath.Join(dir, "文件"), filepath.Join(outside, "外部文件")
			for _, path := range []string{file, external} {
				if err := os.WriteFile(path, []byte("临时数据"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			for name, target := range map[string]string{"文件链接": external, "目录链接": outside, "悬空链接": filepath.Join(outside, "不存在")} {
				if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
					t.Fatal(err)
				}
			}
			if err := Chown(context.Background(), []string{dir}, tc.uid, tc.gid, true); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{dir, file, filepath.Join(dir, "文件链接"), filepath.Join(dir, "目录链接"), filepath.Join(dir, "悬空链接")} {
				assertOwner(t, path, tc.wantUID, tc.wantGID)
			}
			assertOwner(t, outside, 0, 0)
			assertOwner(t, external, 0, 0)
		})
	}
}

func Test改属主错误传播(t *testing.T) {
	dir := t.TempDir()
	if err := Chown(context.Background(), []string{filepath.Join(dir, "不存在")}, os.Getuid(), os.Getgid()+1, true); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("丢失不存在路径错误：%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Chown(ctx, []string{dir}, 1, 1, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("丢失取消错误：%v", err)
	}
	if os.Geteuid() == 0 {
		t.Skip("权限不足分支需要普通用户")
	}
	file := filepath.Join(dir, "文件")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Chown(context.Background(), []string{file}, os.Getuid()+1, 0, false); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("丢失修改权限错误：%v", err)
	}
	if err := os.Chmod(dir, 0000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0700)
	if err := Chown(context.Background(), []string{dir}, os.Getuid(), os.Getgid(), true); !errors.Is(err, syscall.EACCES) {
		t.Fatalf("丢失目录遍历错误：%v", err)
	}
}

func Test根路径符号链接不跟随(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("需要根权限")
	}
	for _, recursive := range []bool{false, true} {
		dir, outside := t.TempDir(), t.TempDir()
		link := filepath.Join(dir, "链接")
		if err := os.Symlink(outside, link); err != nil {
			t.Fatal(err)
		}
		if err := Chown(context.Background(), []string{link}, 60123, 60124, recursive); err != nil {
			t.Fatal(err)
		}
		assertOwner(t, link, 60123, 60124)
		assertOwner(t, outside, 0, 0)
	}
}

func Test目录替换不越界(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("需要根权限")
	}
	base, outside := t.TempDir(), t.TempDir()
	path := filepath.Join(base, "目录")
	for _, dir := range []string{path, outside} {
		if err := os.MkdirAll(filepath.Join(dir, "子目录"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "子目录", "文件"), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	dir, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	old := filepath.Join(base, "原目录")
	if err := os.Rename(path, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if err := chownAt(context.Background(), int(dir.Fd()), "子目录", filepath.Join(path, "子目录"), 60123, 60124, true); err != nil {
		t.Fatal(err)
	}
	assertOwner(t, filepath.Join(old, "子目录", "文件"), 60123, 60124)
	assertOwner(t, filepath.Join(outside, "子目录"), 0, 0)
	assertOwner(t, filepath.Join(outside, "子目录", "文件"), 0, 0)
}

func assertOwner(t *testing.T, path string, uid, gid uint32) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	if stat.Uid != uid || stat.Gid != gid {
		t.Fatalf("%s 属主应为 %d:%d，实际为 %d:%d", path, uid, gid, stat.Uid, stat.Gid)
	}
}
