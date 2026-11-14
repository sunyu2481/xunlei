package xunlei

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// insidePath 同时用于宿主机预检和隔离后的路径转换，不访问文件系统。
func insidePath(root, name string) (string, error) {
	if !filepath.IsAbs(root) || !filepath.IsAbs(name) {
		return "", fmt.Errorf("主目录和数据、下载目录必须是绝对路径")
	}
	rel, err := filepath.Rel(root, name)
	if err != nil {
		return "", err
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("目录 %q 必须是主目录 %q 的子目录，不能使用主目录本身", name, root)
	}
	return string(filepath.Separator) + rel, nil
}

func (cfg Config) validatePaths() error {
	if !filepath.IsAbs(cfg.Root) {
		return fmt.Errorf("主目录必须是绝对路径")
	}
	paths := append([]string{cfg.DirData}, cfg.DirDownload...)
	for _, name := range paths {
		if _, err := insidePath(cfg.Root, name); err != nil {
			return err
		}
	}
	for _, name := range cfg.DirDownload {
		if strings.ContainsRune(name, ':') {
			return fmt.Errorf("下载目录 %q 含冒号，与套件的目录列表协议冲突", name)
		}
	}
	// 与套件安装路径的约束一致：不跟随已有链接，避免准备操作落到其他目录。
	checked := make(map[string]bool)
	for _, name := range append(paths, cfg.Root) {
		for name = filepath.Clean(name); !checked[name]; name = filepath.Dir(name) {
			checked[name] = true
			info, err := os.Lstat(name)
			if err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("检查目录 %q：%w", name, err)
			}
			if err == nil && !info.IsDir() {
				return fmt.Errorf("路径 %q 必须是目录，不能是链接或普通文件", name)
			}
		}
	}
	return nil
}
