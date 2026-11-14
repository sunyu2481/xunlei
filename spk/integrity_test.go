package spk

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/ulikunitz/xz"
)

func Test损坏缓存不能跳过安装(t *testing.T) {
	for _, kind := range []string{"正常", "零字节", "同长度损坏", "不可执行", "版本损坏", "版本越界", "文件链接", "目录链接", "缺文件", "无安装清单", "损坏清单"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			if err := Extract(context.Background(), bytes.NewReader(testPackage(t, testFiles())), dir); err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(dir, launcherFile)
			switch kind {
			case "零字节":
				if err := os.Truncate(name, 0); err != nil {
					t.Fatal(err)
				}
			case "同长度损坏":
				if err := os.WriteFile(name, bytes.Repeat([]byte("x"), len("模拟启动器")), 0755); err != nil {
					t.Fatal(err)
				}
			case "不可执行":
				if err := os.Chmod(name, 0600); err != nil {
					t.Fatal(err)
				}
			case "版本损坏", "版本越界":
				value := "错误版本"
				if kind == "版本越界" {
					value = "../../外部"
				}
				if err := os.WriteFile(filepath.Join(dir, versionFile), []byte(value), 0644); err != nil {
					t.Fatal(err)
				}
			case "文件链接", "目录链接":
				if kind == "目录链接" {
					name = filepath.Join(dir, "bin/bin")
				}
				outside := filepath.Join(t.TempDir(), "原内容")
				if err := os.Rename(name, outside); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, name); err != nil {
					t.Fatal(err)
				}
			case "缺文件":
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
			case "无安装清单":
				if err := os.Remove(filepath.Join(dir, manifestFile)); err != nil {
					t.Fatal(err)
				}
			case "损坏清单":
				if err := os.WriteFile(filepath.Join(dir, manifestFile), []byte("不完整清单"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if got := allExists(context.Background(), dir); got != (kind == "正常") {
				t.Fatalf("缓存完整性判断=%t", got)
			}
			if kind == "零字节" || kind == "同长度损坏" || kind == "不可执行" {
				source := filepath.Join(t.TempDir(), "模拟.spk")
				if err := os.WriteFile(source, testPackage(t, testFiles()), 0600); err != nil {
					t.Fatal(err)
				}
				if err := Download(context.Background(), (&url.URL{Scheme: "file", Path: source}).String(), dir, false); err != nil {
					t.Fatal(err)
				}
				assertFiles(t, dir)
				if !allExists(context.Background(), dir) {
					t.Fatal("重装未修复缓存")
				}
			}
		})
	}
}

func Test解包完整校验压缩尾部并保留旧文件(t *testing.T) {
	for _, kind := range []string{"损坏xz尾部", "截断xz尾部", "外层读取失败", "截断文件", "损坏gzip尾部"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			if err := Extract(context.Background(), bytes.NewReader(testPackage(t, testFiles())), dir); err != nil {
				t.Fatal(err)
			}
			entries := testFiles()
			entries[1].data = "新的启动器"
			var data bytes.Buffer
			w, err := xz.NewWriter(&data)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write(testTar(t, entries)); err != nil {
				t.Fatal(err)
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			compressed := data.Bytes()
			var src io.Reader
			switch kind {
			case "损坏xz尾部":
				compressed[len(compressed)-1] ^= 1
			case "截断xz尾部":
				compressed = compressed[:len(compressed)-6]
			case "截断文件":
				var partial bytes.Buffer
				tw := tar.NewWriter(&partial)
				if err := tw.WriteHeader(&tar.Header{Name: gatewayFile, Size: 100, Mode: 0755}); err != nil {
					t.Fatal(err)
				}
				if _, err := tw.Write([]byte("短内容")); err != nil {
					t.Fatal(err)
				}
				data.Reset()
				zw, err := xz.NewWriter(&data)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := zw.Write(partial.Bytes()); err != nil {
					t.Fatal(err)
				}
				if err := zw.Close(); err != nil {
					t.Fatal(err)
				}
				compressed = data.Bytes()
			}
			spkData := testTar(t, []testEntry{{name: "package.tgz", data: string(compressed)}})
			src = bytes.NewReader(spkData)
			if kind == "外层读取失败" {
				src = io.MultiReader(src, errorReader{})
			}
			if kind == "损坏gzip尾部" {
				var encoded bytes.Buffer
				gw := gzip.NewWriter(&encoded)
				if _, err := gw.Write(spkData); err != nil {
					t.Fatal(err)
				}
				if err := gw.Close(); err != nil {
					t.Fatal(err)
				}
				encoded.Bytes()[encoded.Len()-8] ^= 1
				gr, err := gzip.NewReader(&encoded)
				if err != nil {
					t.Fatal(err)
				}
				defer gr.Close()
				src = gr
			}
			if err := Extract(context.Background(), src, dir); err == nil {
				t.Fatal("应拒绝不完整或损坏的归档")
			}
			assertFiles(t, dir)
			assertNoStage(t, dir)
			if !allExists(context.Background(), dir) {
				t.Fatal("归档失败破坏了旧安装")
			}
		})
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("模拟尾部读取失败") }

type failingCloser struct {
	io.Reader
	err error
}

func (r failingCloser) Close() error { return r.err }

func Test归档传播解码器关闭失败(t *testing.T) {
	want := errors.New("模拟关闭失败")
	err := Walk(context.Background(), bytes.NewReader(testTar(t, nil)), func(io.Reader, *tar.Header) error { return nil }, func(r io.Reader) (io.ReadCloser, error) { return failingCloser{r, want}, nil })
	if !errors.Is(err, want) {
		t.Fatalf("关闭错误被丢弃：%v", err)
	}
}
