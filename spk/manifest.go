package spk

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"runtime"
	"strings"
	"syscall"
)

const manifestFile = ".xlp-install.json"

type installation struct {
	Format  int                   `json:"format"`
	Arch    string                `json:"arch"`
	Version string                `json:"version"`
	Files   map[string]fileDigest `json:"files"`
}

type fileDigest struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func requiredFiles(version string) []string {
	return []string{launcherFile, "bin/bin/xunlei-pan-cli." + version + "." + runtime.GOARCH, gatewayFile, versionFile}
}

func digestFile(ctx context.Context, f *os.File) (fileDigest, error) {
	h := sha256.New()
	size, err := io.Copy(h, contextReader{ctx, f})
	return fileDigest{size, hex.EncodeToString(h.Sum(nil))}, err
}

func stageManifest(ctx context.Context, stage *os.File, version string, names []string, files map[string]string) error {
	m := installation{Format: 1, Arch: runtime.GOARCH, Version: version, Files: make(map[string]fileDigest)}
	for _, name := range names {
		f, err := openFileAt(stage, files[name], os.O_RDONLY, 0)
		if err != nil {
			return err
		}
		digest, err := digestFile(ctx, f)
		err = errors.Join(err, f.Close())
		if err != nil {
			return err
		}
		m.Files[name] = digest
	}
	temp := fmt.Sprint(len(files))
	f, err := openFileAt(stage, temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	err = json.NewEncoder(f).Encode(m)
	err = errors.Join(err, f.Close())
	if err == nil {
		files[manifestFile] = temp
	}
	return err
}

func allExists(ctx context.Context, dir string) bool {
	root, err := openDestinationMode(dir, false)
	if err != nil {
		return false
	}
	defer root.Close()
	f, err := openFileAt(root, manifestFile, os.O_RDONLY, 0)
	if err != nil {
		return false
	}
	b, err := readSmallFile(f)
	if err != nil {
		return false
	}
	var m installation
	if json.Unmarshal(b, &m) != nil || m.Format != 1 || m.Arch != runtime.GOARCH || !versionPattern.MatchString(m.Version) {
		return false
	}
	names := requiredFiles(m.Version)
	if len(m.Files) != len(names) {
		return false
	}
	for _, name := range names {
		want, ok := m.Files[name]
		if !ok || want.Size <= 0 || len(want.SHA256) != sha256.Size*2 {
			return false
		}
		parent, err := openRelativeDirectory(root, path.Dir(name))
		if err != nil {
			return false
		}
		f, err := openFileAt(parent, path.Base(name), os.O_RDONLY, 0)
		parent.Close()
		if err != nil {
			return false
		}
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() != want.Size || info.Sys().(*syscall.Stat_t).Nlink != 1 || (name != versionFile && info.Mode().Perm()&0111 != 0111) {
			f.Close()
			return false
		}
		got, err := digestFile(ctx, f)
		if err == nil && name == versionFile {
			if _, err = f.Seek(0, io.SeekStart); err == nil {
				var version []byte
				version, err = io.ReadAll(io.LimitReader(f, 256))
				if len(version) == 256 || strings.TrimSpace(string(version)) != m.Version {
					err = fmt.Errorf("安装版本不符")
				}
			}
		}
		err = errors.Join(err, f.Close())
		if err != nil || got != want {
			return false
		}
	}
	return ctx.Err() == nil
}

func readSmallFile(f *os.File) ([]byte, error) {
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Sys().(*syscall.Stat_t).Nlink != 1 || info.Size() > 16<<10 {
		return nil, fmt.Errorf("安装清单类型或大小无效")
	}
	return io.ReadAll(io.LimitReader(f, (16<<10)+1))
}

func openRelativeDirectory(root *os.File, name string) (*os.File, error) {
	current, err := openDirectoryMode(root, ".", false)
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(name, "/") {
		next, err := openDirectoryMode(current, part, false)
		current.Close()
		if err != nil {
			return nil, err
		}
		current = next
	}
	return current, nil
}
