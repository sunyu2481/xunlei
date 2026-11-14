package spk

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ulikunitz/xz"
)

func Test归档小规模资源上限(t *testing.T) {
	data := testTar(t, []testEntry{{name: "忽略文件", data: strings.Repeat("字", 20)}, {name: "第二项", data: "内容"}})
	for _, kind := range []string{"单文件", "总字节", "条目数", "尾部"} {
		t.Run(kind, func(t *testing.T) {
			limits := defaultLimits()
			src := data
			switch kind {
			case "单文件":
				limits.file = 32
			case "总字节":
				limits.expanded = 600
			case "条目数":
				limits.entries = 1
			case "尾部":
				limits.expanded = int64(len(data))
				src = append(bytes.Clone(data), 1)
			}
			err := walkLimited(context.Background(), bytes.NewReader(src), func(io.Reader, *tar.Header) error { return nil }, limits)
			if !errors.Is(err, errResourceLimit) {
				t.Fatalf("应返回资源超限：%v", err)
			}
		})
	}
	if err := Walk(context.Background(), bytes.NewReader(data), func(io.Reader, *tar.Header) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func Test读取限制区分恰好上限和超限(t *testing.T) {
	for _, n := range []int{7, 8, 9} {
		var dst bytes.Buffer
		_, err := io.Copy(&dst, limitReader(strings.NewReader(strings.Repeat("x", n)), 8, "测试来源"))
		if (n > 8) != errors.Is(err, errResourceLimit) || dst.Len() != min(n, 8) {
			t.Fatalf("长度=%d，写入=%d，错误=%v", n, dst.Len(), err)
		}
	}
}

func Test提取超限保留安装并清理暂存(t *testing.T) {
	dir := t.TempDir()
	data := testPackage(t, testFiles())
	if err := Extract(context.Background(), bytes.NewReader(data), dir); err != nil {
		t.Fatal(err)
	}
	for _, checksum := range []bool{false, true} {
		limits := defaultLimits()
		limits.source = int64(len(data) - 1)
		var digest []byte
		if checksum {
			digest = make([]byte, 32)
		}
		if err := extractSourceLimited(context.Background(), bytes.NewReader(data), dir, digest, limits); !errors.Is(err, errResourceLimit) {
			t.Fatalf("应拒绝完整来源超限：%v", err)
		}
		assertFiles(t, dir)
		assertNoStage(t, dir)
	}
	limits := defaultLimits()
	limits.entries = 2
	if err := extractLimited(context.Background(), bytes.NewReader(data), dir, limits); !errors.Is(err, errResourceLimit) {
		t.Fatalf("应拒绝内层超限：%v", err)
	}
	assertFiles(t, dir)
	assertNoStage(t, dir)
}

func testXZ(t *testing.T, data []byte, blockSize int64) []byte {
	t.Helper()
	var b bytes.Buffer
	w, err := (xz.WriterConfig{DictCap: 4096, BlockSize: blockSize}).NewWriter(&b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestXZ逐块检查字典及拼接流(t *testing.T) {
	plain := []byte(strings.Repeat("验证内容", 20))
	data := testXZ(t, plain, 16)
	for _, concat := range []bool{false, true} {
		src := bytes.Clone(data)
		want := plain
		if concat {
			src = append(src, data...)
			want = append(bytes.Clone(plain), plain...)
		}
		r, err := xzLimited(bytes.NewReader(src), defaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(r)
		r.Close()
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("合法多块流错误：%v", err)
		}
	}
	// 仅改写头声明并重算校验和，不生成大字典或大文件。
	one := testXZ(t, plain, 0)
	for _, later := range []bool{false, true} {
		bad := bytes.Clone(one)
		h := bad[12 : 12+(int(bad[12])+1)*4]
		h[4] = 40
		binary.LittleEndian.PutUint32(h[len(h)-4:], crc32.ChecksumIEEE(h[:len(h)-4]))
		if later {
			bad = append(bytes.Clone(one), bad...)
		}
		r, err := xzLimited(bytes.NewReader(bad), defaultLimits())
		if err == nil {
			_, err = io.Copy(io.Discard, r)
			r.Close()
		}
		if !errors.Is(err, errResourceLimit) {
			t.Fatalf("未拒绝字典声明：%v", err)
		}
	}
	badBlock := bytes.Clone(data)
	headerSize := (int(data[12]) + 1) * 4
	second := bytes.Index(badBlock[12+headerSize:], data[12:12+headerSize]) + 12 + headerSize
	if second < 12+headerSize {
		t.Fatal("未找到后续压缩块头")
	}
	h := badBlock[second : second+headerSize]
	h[4] = 40
	binary.LittleEndian.PutUint32(h[len(h)-4:], crc32.ChecksumIEEE(h[:len(h)-4]))
	br, err := xzLimited(bytes.NewReader(badBlock), defaultLimits())
	if err == nil {
		_, err = io.Copy(io.Discard, br)
		br.Close()
	}
	if !errors.Is(err, errResourceLimit) {
		t.Fatalf("未拒绝后续块字典：%v", err)
	}
	limits := defaultLimits()
	limits.blocks = 1
	r, err := xzLimited(bytes.NewReader(data), limits)
	if err == nil {
		_, err = io.Copy(io.Discard, r)
		r.Close()
	}
	if !errors.Is(err, errResourceLimit) {
		t.Fatalf("未拒绝过多压缩块：%v", err)
	}
}

func Test下载同时限制压缩前后字节(t *testing.T) {
	payload := testPackage(t, testFiles())
	for _, wireOversized := range []bool{false, true} {
		var compressed bytes.Buffer
		writer := gzip.NewWriter(&compressed)
		if wireOversized {
			writer.Header.Extra = bytes.Repeat([]byte{1}, len(payload)+128)
		}
		if _, err := writer.Write(payload); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Encoding", "gzip")
			// 分块响应没有可预先信任的长度，必须在读取途中拒绝超限。
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			w.Write(compressed.Bytes())
		}))
		t.Cleanup(server.Close)
		limits := defaultLimits()
		limits.source = int64(len(payload) - 1)
		if wireOversized {
			limits.source = int64(len(payload) + 1)
		}
		source, err := url.Parse(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		err = downloadHTTPLimited(context.Background(), source, dir, nil, trustedClient(t, server), limits)
		if !errors.Is(err, errResourceLimit) {
			t.Fatalf("压缩传输超限=%t，错误=%v", wireOversized, err)
		}
		assertNoStage(t, dir)
	}
}
