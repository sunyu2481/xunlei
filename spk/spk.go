package spk

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"regexp"
	"runtime"
	"strings"
)

const versionFile = "bin/bin/version"
const launcherFile = "bin/bin/xunlei-pan-cli-launcher." + runtime.GOARCH
const gatewayFile = "ui/index.cgi"

var versionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Extract 在受限临时目录验证归档，只安装当前架构及版本需要的普通文件。
func Extract(ctx context.Context, src io.Reader, dstDir string) error {
	return extractLimited(ctx, src, dstDir, defaultLimits())
}

func extractLimited(ctx context.Context, src io.Reader, dstDir string, limits resourceLimits) error {
	src = limitReader(src, limits.source, "套件来源")
	if err := ctx.Err(); err != nil {
		return err
	}
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
	err = walkLimited(ctx, src, func(tr io.Reader, h *tar.Header) error {
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
		return walkLimited(ctx, tr, func(tr io.Reader, h *tar.Header) error {
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
			if h.Size == 0 {
				return fmt.Errorf("套件文件 %q 为空", name)
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
		}, limits, func(r io.Reader) (io.ReadCloser, error) { return xzLimited(r, limits) })
	}, limits)
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
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	version = strings.TrimSpace(string(b))
	if len(b) == 256 || !versionPattern.MatchString(version) {
		return fmt.Errorf("套件版本无效")
	}
	names := requiredFiles(version)
	for _, name := range names {
		if _, ok := files[name]; !ok {
			return fmt.Errorf("套件缺少文件 %q", name)
		}
	}
	if err := stageManifest(ctx, stage, version, names, files); err != nil {
		return err
	}
	// 清单最后发布，启动时只有内容与完整清单一致才会复用缓存。
	if err := installFiles(ctx, root, stage, append(names, manifestFile), files); err != nil {
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

func Walk(ctx context.Context, src io.Reader, walk WalkFunc, decoder ...Decoder) error {
	return walkLimited(ctx, src, walk, defaultLimits(), decoder...)
}

func walkLimited(ctx context.Context, src io.Reader, walk WalkFunc, limits resourceLimits, decoder ...Decoder) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	dr := io.NopCloser(contextReader{ctx, src})

	for _, d := range decoder {
		if d != nil {
			if dr, err = d(dr); err != nil {
				return
			}
			defer func(r io.ReadCloser) { err = errors.Join(err, r.Close()) }(dr)
		}
	}

	decoded := limitReader(contextReader{ctx, dr}, limits.expanded, "解压总字节数")
	entries := 0
	for tr := tar.NewReader(decoded); ; {
		if err = ctx.Err(); err != nil {
			return err
		}
		hdr, e := tr.Next()
		if e == io.EOF {
			// tar 的结束块不代表压缩流结束，必须继续读取以验证校验和、索引和尾部。
			_, err = io.Copy(io.Discard, decoded)
			return err
		}
		if e != nil {
			return e
		}
		entries++
		if entries > limits.entries || hdr.Size > limits.file {
			return fmt.Errorf("%w：归档条目数量或单文件大小", errResourceLimit)
		}
		if err = walk(tr, hdr); err != nil {
			return err
		}
	}
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.r.Read(p)
	if r.ctx.Err() != nil {
		return n, r.ctx.Err()
	}
	return n, err
}

func Xz(src io.Reader) (io.ReadCloser, error) {
	return xzLimited(src, defaultLimits())
}

type Decoder func(io.Reader) (io.ReadCloser, error)

type WalkFunc func(r io.Reader, h *tar.Header) (err error)
