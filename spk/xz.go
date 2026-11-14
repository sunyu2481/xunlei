package spk

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/ulikunitz/xz"
	"github.com/ulikunitz/xz/lzma"
)

// 字典检查发生在块头交给解码器之前。不能将 xz 的 DictCap 当作容量上限。
// 此读取器只定位容器和压缩块边界；格式、索引、校验和仍由 xz 解码器验证。
type xzGuard struct {
	src             io.Reader
	limits          resourceLimits
	pending         []byte
	phase           byte
	payload         int64
	blockBytes      int64
	checkSize       int
	blocks, streams int
}

func xzLimited(src io.Reader, limits resourceLimits) (io.ReadCloser, error) {
	guard := &xzGuard{src: limitReader(src, limits.source, "压缩来源"), limits: limits}
	r, err := (xz.ReaderConfig{DictCap: 4096}).NewReader(guard)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(r), nil
}

func (g *xzGuard) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(g.pending) == 0 {
		if g.payload > 0 {
			if int64(len(p)) > g.payload {
				p = p[:g.payload]
			}
			n, err := g.src.Read(p)
			g.payload -= int64(n)
			return n, err
		}
		if err := g.next(); err != nil {
			return 0, err
		}
	}
	n := copy(p, g.pending)
	g.pending = g.pending[n:]
	return n, nil
}

func (g *xzGuard) take(n int) ([]byte, error) {
	p := make([]byte, n)
	_, err := io.ReadFull(g.src, p)
	return p, err
}

func (g *xzGuard) next() error {
	switch g.phase {
	case 0:
		p, err := g.take(4)
		if err != nil {
			return err
		}
		if bytes.Equal(p, []byte{0, 0, 0, 0}) {
			g.pending = p
			return nil
		}
		rest, err := g.take(8)
		if err != nil {
			return err
		}
		p = append(p, rest...)
		if !bytes.Equal(p[:6], []byte{0xfd, '7', 'z', 'X', 'Z', 0}) {
			return fmt.Errorf("无效的 xz 流头")
		}
		switch p[7] {
		case 0:
			g.checkSize = 0
		case 1:
			g.checkSize = 4
		case 4:
			g.checkSize = 8
		case 10:
			g.checkSize = 32
		default:
			return fmt.Errorf("不支持的 xz 校验类型")
		}
		g.streams++
		if g.streams > g.limits.blocks {
			return fmt.Errorf("%w：xz 流数量", errResourceLimit)
		}
		g.pending = p
		g.phase = 1
	case 1:
		p, err := g.take(1)
		if err != nil {
			return err
		}
		if p[0] == 0 {
			return g.index(p)
		}
		rest, err := g.take((int(p[0])+1)*4 - 1)
		if err != nil {
			return err
		}
		p = append(p, rest...)
		g.blocks++
		if g.blocks > g.limits.blocks {
			return fmt.Errorf("%w：xz 块数量", errResourceLimit)
		}
		// 当前依赖只支持单个 LZMA2 滤镜，明确拒绝其余组合。
		if p[1]&0x3f != 0 {
			return fmt.Errorf("不支持的 xz 块滤镜")
		}
		fields := bytes.NewReader(p[2 : len(p)-4])
		for _, flag := range []byte{0x40, 0x80} {
			if p[1]&flag != 0 {
				if _, err := binary.ReadUvarint(fields); err != nil {
					return err
				}
			}
		}
		id, err := binary.ReadUvarint(fields)
		if err != nil {
			return err
		}
		size, err := binary.ReadUvarint(fields)
		if err != nil {
			return err
		}
		if id != 0x21 || size != 1 {
			return fmt.Errorf("不支持的 xz 块滤镜")
		}
		prop, err := fields.ReadByte()
		if err != nil {
			return err
		}
		dict, err := lzma.DecodeDictCap(prop)
		if err != nil {
			return err
		}
		if dict > g.limits.dictionary {
			return fmt.Errorf("%w：xz 字典声明 %d 字节", errResourceLimit, dict)
		}
		g.pending = p
		g.blockBytes = 0
		g.phase = 2
	case 2:
		p, err := g.take(1)
		if err != nil {
			return err
		}
		control := p[0]
		if control == 0 {
			g.blockBytes++
			rest, err := g.take(int((4-g.blockBytes%4)%4) + g.checkSize)
			if err != nil {
				return err
			}
			g.pending = append(p, rest...)
			g.phase = 1
			return nil
		}
		n := 0
		switch {
		case control == 1 || control == 2:
			n = 3
		case control >= 0xc0:
			n = 6
		case control >= 0x80:
			n = 5
		default:
			return fmt.Errorf("无效的 LZMA2 块头")
		}
		rest, err := g.take(n - 1)
		if err != nil {
			return err
		}
		p = append(p, rest...)
		offset := 1
		if control >= 0x80 {
			offset = 3
		}
		g.payload = int64(binary.BigEndian.Uint16(p[offset:offset+2])) + 1
		g.blockBytes += int64(n) + g.payload
		g.pending = p
	}
	return nil
}

func (g *xzGuard) index(p []byte) error {
	// 索引最多每块两项九字节整数，拒绝超量声明后才读取，避免大分配。
	readInt := func() (uint64, error) {
		var value uint64
		for i := 0; i < 9; i++ {
			b, err := g.take(1)
			if err != nil {
				return 0, err
			}
			p = append(p, b[0])
			value |= uint64(b[0]&0x7f) << uint(7*i)
			if b[0]&0x80 == 0 {
				return value, nil
			}
		}
		return 0, fmt.Errorf("xz 索引整数越界")
	}
	count, err := readInt()
	if err != nil {
		return err
	}
	if count > uint64(g.limits.blocks) {
		return fmt.Errorf("%w：xz 索引记录", errResourceLimit)
	}
	for i := uint64(0); i < count*2; i++ {
		if _, err := readInt(); err != nil {
			return err
		}
	}
	rest, err := g.take((4-len(p)%4)%4 + 4 + 12)
	if err != nil {
		return err
	}
	g.pending = append(p, rest...)
	g.phase = 0
	return nil
}
