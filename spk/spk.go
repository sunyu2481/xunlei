package spk

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"regexp"
	"runtime"
	"strings"

	"github.com/ulikunitz/xz"
)

const versionFile = "bin/bin/version"
const launcherFile = "bin/bin/xunlei-pan-cli-launcher." + runtime.GOARCH
const gatewayFile = "ui/index.cgi"

var versionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Extract 在受限临时目录验证归档，只安装当前架构及版本需要的普通文件。
func Extract(ctx context.Context, src io.Reader, dstDir string) error {
	root, err := openDestination(dstDir)
	if err != nil {
		return err
	}
	defer root.Close()
	stage, cleanup, err := newStage(root)
	if err != nil {
		return err
	}
	defer cleanup()
	files := make(map[string]string)
	found := false
	err = Walk(ctx, src, func(tr io.Reader, h *tar.Header) error {
		name, err := archivePath(h.Name)
		if err != nil {
			return err
		}
		if name != "package.tgz" {
			return nil
		}
		if found || h.Typeflag != tar.TypeReg {
			return fmt.Errorf("内层套件重复或不是普通文件")
		}
		found = true
		return Walk(ctx, tr, func(tr io.Reader, h *tar.Header) error {
			name, err := archivePath(h.Name)
			if err != nil {
				return err
			}
			if !packageFile(name) {
				return nil
			}
			if h.Typeflag != tar.TypeReg {
				return fmt.Errorf("套件文件 %q 不是普通文件", name)
			}
			if _, exists := files[name]; exists {
				return fmt.Errorf("套件文件 %q 重复", name)
			}
			temp := fmt.Sprint(len(files))
			f, err := openFileAt(stage, temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				return err
			}
			files[name] = temp
			_, err = io.Copy(f, tr)
			if closeErr := f.Close(); err == nil {
				err = closeErr
			}
			return err
		}, Xz)
	})
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("归档缺少 package.tgz")
	}
	version, ok := files[versionFile]
	if !ok {
		return fmt.Errorf("套件缺少版本文件")
	}
	f, err := openFileAt(stage, version, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	b, err := io.ReadAll(io.LimitReader(f, 256))
	f.Close()
	if err != nil {
		return err
	}
	version = strings.TrimSpace(string(b))
	if len(b) == 256 || !versionPattern.MatchString(version) {
		return fmt.Errorf("套件版本无效")
	}
	names := []string{launcherFile, "bin/bin/xunlei-pan-cli." + version + "." + runtime.GOARCH, gatewayFile, versionFile}
	for _, name := range names {
		if _, ok := files[name]; !ok {
			return fmt.Errorf("套件缺少文件 %q", name)
		}
	}
	if err := installFiles(root, stage, names, files); err != nil {
		return err
	}
	slog.DebugContext(ctx, "套件提取完成", "target_dir", dstDir, "version", version)
	return nil
}

func archivePath(name string) (string, error) {
	if name == "" || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\\x00") {
		return "", fmt.Errorf("不安全的归档路径 %q", name)
	}
	for part := range strings.SplitSeq(name, "/") {
		if part == ".." {
			return "", fmt.Errorf("不安全的归档路径 %q", name)
		}
	}
	return path.Clean(name), nil
}

func packageFile(name string) bool {
	if name == versionFile || name == launcherFile || name == gatewayFile {
		return true
	}
	version, ok := strings.CutPrefix(name, "bin/bin/xunlei-pan-cli.")
	if !ok {
		return false
	}
	version, ok = strings.CutSuffix(version, "."+runtime.GOARCH)
	return ok && versionPattern.MatchString(version)
}

// 以下为归档遍历和解码接口。

func Walk(ctx context.Context, src io.Reader, walk WalkFunc, decoder ...Decoder) (err error) {
	dr := io.NopCloser(src)

	for _, d := range decoder {
		if d != nil {
			if dr, err = d(dr); err != nil {
				return
			}
			defer dr.Close()
		}
	}

	for tr := tar.NewReader(dr); ; {
		hdr, e := tr.Next()
		if err = e; err != nil {
			break
		}
		if err = walk(tr, hdr); err != nil {
			break
		}
	}

	if err == io.EOF {
		err = nil
	}
	return
}

func Xz(src io.Reader) (io.ReadCloser, error) {
	xzr, err := xz.NewReader(src)
	return io.NopCloser(xzr), err
}

type Decoder func(io.Reader) (io.ReadCloser, error)

type WalkFunc func(r io.Reader, h *tar.Header) (err error)
