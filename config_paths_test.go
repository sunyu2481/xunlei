package xunlei

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
)

func Test配置目录边界与分隔符(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		downloads  []string
		wantErr    bool
	}{
		{"隐藏目录", ".data", []string{".downloads"}, false},
		{"逗号和空白", "数据", []string{"甲,乙", " 尾部空白 "}, false},
		{"数据目录越界", "../外部数据", []string{"下载"}, true},
		{"下载目录越界", "数据", []string{"../外部下载"}, true},
		{"数据目录为主目录", ".", []string{"下载"}, true},
		{"下载目录为主目录", "数据", []string{"."}, true},
		{"下载路径含冒号", "数据", []string{"甲:乙"}, true},
		{"数据路径含冒号", "数据:甲", []string{"下载"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := setupConfigTest(t)
			os.Args = append(os.Args, "--dir_data", filepath.Join(root, tc.data))
			var want []string
			for _, name := range tc.downloads {
				p := filepath.Join(root, name)
				want = append(want, p)
				os.Args = append(os.Args, "-d", p)
			}
			var cfg Config
			err := ConfigBind(&cfg)
			if (err != nil) != tc.wantErr {
				t.Fatalf("路径校验错误：%v", err)
			}
			if err == nil && !reflect.DeepEqual(cfg.DirDownload, want) {
				t.Fatalf("目录字符被改变：%q", cfg.DirDownload)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("配置解析改动了文件系统：%v，%v", entries, err)
			}
		})
	}
}

func Test下载目录环境保留逗号并以冒号分隔(t *testing.T) {
	root := setupConfigTest(t)
	want := []string{filepath.Join(root, "甲,乙"), filepath.Join(root, ".下载")}
	t.Setenv("XL_DIR_DOWNLOAD", want[0]+":"+want[1])
	var cfg Config
	if err := ConfigBind(&cfg); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.DirDownload, want) {
		t.Fatalf("目录=%q", cfg.DirDownload)
	}
}

func Test非法路径准备失败不改动目录文件及属主(t *testing.T) {
	for _, kind := range []string{"外部数据", "外部下载", "主目录", "链接越界", "下载冒号"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			root, outside := filepath.Join(base, "主目录"), filepath.Join(base, "外部目录")
			varDir := filepath.Join(root, DIR_VAR)
			for _, dir := range []string{outside, varDir} {
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
			}
			marker := filepath.Join(varDir, "原有状态")
			if err := os.WriteFile(marker, []byte("保留状态"), 0640); err != nil {
				t.Fatal(err)
			}
			cfg := Config{Root: root, DirData: filepath.Join(root, "数据"), DirDownload: []string{filepath.Join(root, "下载")}, Uid: os.Getuid(), Gid: os.Getgid(), SpkUrl: "无效来源"}
			switch kind {
			case "外部数据":
				cfg.DirData = outside
			case "外部下载":
				cfg.DirDownload[0] = outside
			case "主目录":
				cfg.DirData = root
			case "下载冒号":
				cfg.DirDownload[0] = filepath.Join(root, "甲:乙")
			case "链接越界":
				cfg.DirData = filepath.Join(root, "链接")
				if err := os.Symlink(outside, cfg.DirData); err != nil {
					t.Fatal(err)
				}
			}
			type state struct {
				mode     fs.FileMode
				uid, gid uint32
				size     int64
			}
			snapshot := func() map[string]state {
				items := make(map[string]state)
				if err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
					if err != nil {
						return err
					}
					info, err := d.Info()
					if err != nil {
						return err
					}
					st := info.Sys().(*syscall.Stat_t)
					items[p] = state{info.Mode(), st.Uid, st.Gid, info.Size()}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				return items
			}
			before := snapshot()
			if _, err := Before(cfg)(context.Background()); err == nil {
				t.Fatal("应在准备前拒绝非法路径")
			}
			if after := snapshot(); !reflect.DeepEqual(before, after) {
				t.Fatalf("准备失败改变了目录或属主：之前=%v，之后=%v", before, after)
			}
			if b, err := os.ReadFile(marker); err != nil || string(b) != "保留状态" {
				t.Fatalf("原有状态被改变：%q，%v", b, err)
			}
		})
	}
}
