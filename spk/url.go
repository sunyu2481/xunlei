package spk

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/cnk3x/xunlei/pkg/utils"
)

var DownloadUrl = utils.Iif(runtime.GOARCH == "amd64",
	"https://down.sandai.net/nas/nasxunlei-DSM7-x86_64.spk",
	"https://down.sandai.net/nas/nasxunlei-DSM7-armv8.spk",
)

const downloadTimeout = 10 * time.Minute

// sourceError 隐藏地址及底层错误中的签名、用户信息和私有路径，同时保留错误链。
type sourceError struct {
	message string
	err     error
}

func (e *sourceError) Error() string {
	if errors.Is(e.err, context.Canceled) {
		return e.message + "：操作已取消"
	}
	var timeout net.Error
	if errors.As(e.err, &timeout) && timeout.Timeout() {
		return e.message + "：操作超时"
	}
	return e.message
}

func (e *sourceError) Unwrap() error { return e.err }

// Download 检查并下载套件。强制下载或 URL 附带可信摘要时，重新读取来源并验证。
func Download(ctx context.Context, spkUrl string, dir string, force bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	source, checksum, err := parseSource(spkUrl)
	if err != nil {
		return err
	}
	if !force && len(checksum) == 0 && allExists(ctx, dir) {
		slog.InfoContext(ctx, "check spk all spk file exists")
		return ctx.Err()
	}
	if source.Scheme == "file" {
		// 非阻塞打开并检查类型，避免本地管道等特殊文件无限等待。
		f, err := os.OpenFile(source.Path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			return &sourceError{"读取本地套件失败", err}
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return &sourceError{"读取本地套件信息失败", err}
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("本地套件必须是普通文件")
		}
		if info.Size() > defaultLimits().source {
			return fmt.Errorf("%w：本地套件大小", errResourceLimit)
		}
		return extractSource(ctx, f, dir, checksum)
	}
	client := newDownloadClient()
	defer client.CloseIdleConnections()
	return downloadHTTP(ctx, source, dir, checksum, client)
}

func parseSource(raw string) (*url.URL, []byte, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, nil, &sourceError{"套件地址格式错误", err}
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Opaque != "" || u.User != nil {
		return nil, nil, fmt.Errorf("套件地址不支持不透明路径或用户信息")
	}
	var checksum []byte
	if u.Fragment != "" {
		value, ok := strings.CutPrefix(u.Fragment, "sha256=")
		checksum, err = hex.DecodeString(value)
		if !ok || err != nil || len(checksum) != sha256.Size {
			return nil, nil, fmt.Errorf("套件地址片段必须为 sha256= 加 64 位十六进制摘要")
		}
		u.Fragment, u.RawFragment = "", ""
	}
	switch u.Scheme {
	case "https":
		if err := validateHTTPS(u); err != nil {
			return nil, nil, err
		}
	case "file":
		if (u.Host != "" && !strings.EqualFold(u.Host, "localhost")) || !filepath.IsAbs(u.Path) || strings.ContainsRune(u.Path, 0) || u.RawQuery != "" || u.ForceQuery {
			return nil, nil, fmt.Errorf("本地套件地址须使用绝对路径，主机仅支持空值或 localhost，不能带查询参数")
		}
	default:
		return nil, nil, fmt.Errorf("套件来源仅支持 HTTPS 或本地 file URL")
	}
	return u, checksum, nil
}

func validateHTTPS(u *url.URL) error {
	if !strings.EqualFold(u.Scheme, "https") || u.Hostname() == "" || u.User != nil {
		return fmt.Errorf("网络套件及重定向目标必须使用不带用户信息的 HTTPS 地址")
	}
	return nil
}

func newDownloadClient() *http.Client {
	return &http.Client{
		Timeout: downloadTimeout,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DisableCompression:    true,
			DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			IdleConnTimeout:       30 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("套件下载重定向次数过多")
			}
			return validateHTTPS(req.URL)
		},
	}
}

func downloadHTTP(ctx context.Context, source *url.URL, dir string, checksum []byte, client *http.Client) error {
	return downloadHTTPLimited(ctx, source, dir, checksum, client, defaultLimits())
}

func downloadHTTPLimited(ctx context.Context, source *url.URL, dir string, checksum []byte, client *http.Client, limits resourceLimits) error {
	slog.InfoContext(ctx, "下载套件", "host", source.Host)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source.String(), nil)
	if err != nil {
		return &sourceError{"创建套件下载请求失败", err}
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "xlp")
	// 显式协商 gzip，在解码前后分别计数，避免压缩传输绕过字节上限。
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := client.Do(req)
	if err != nil {
		return &sourceError{"套件下载请求失败", err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("套件下载响应状态错误：%d", resp.StatusCode)
	}
	if resp.ContentLength > limits.source {
		return fmt.Errorf("%w：下载响应长度", errResourceLimit)
	}
	body := limitReader(contextReader{ctx, resp.Body}, limits.source, "网络传输字节数")
	switch strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))) {
	case "", "identity":
	case "gzip":
		decoded, err := gzip.NewReader(body)
		if err != nil {
			return err
		}
		defer decoded.Close()
		body = decoded
	default:
		return fmt.Errorf("套件响应使用不支持的内容编码 %q", resp.Header.Get("Content-Encoding"))
	}
	return extractSourceLimited(ctx, body, dir, checksum, limits)

}

func extractSource(ctx context.Context, src io.Reader, dir string, checksum []byte) error {
	return extractSourceLimited(ctx, src, dir, checksum, defaultLimits())
}

func extractSourceLimited(ctx context.Context, src io.Reader, dir string, checksum []byte, limits resourceLimits) error {
	src = limitReader(src, limits.source, "套件来源")
	if len(checksum) == 0 {
		return extractLimited(ctx, src, dir, limits)
	}
	// 摘要验证针对完整 SPK；验证通过之前不接触安装目录。
	f, err := os.CreateTemp("", "xlp-spk-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, hash), contextReader{ctx, src}); err != nil {
		return err
	}
	if !bytes.Equal(checksum, hash.Sum(nil)) {
		return fmt.Errorf("套件 SHA-256 摘要不匹配")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return extractLimited(ctx, f, dir, limits)
}
