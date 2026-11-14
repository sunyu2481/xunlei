package spk

import (
	"errors"
	"fmt"
	"io"
)

var errResourceLimit = errors.New("套件资源超过上限")

type resourceLimits struct {
	source, file, expanded int64
	entries, blocks        int
	dictionary             int64
}

func defaultLimits() resourceLimits {
	return resourceLimits{source: 512 << 20, file: 512 << 20, expanded: 2 << 30, entries: 10000, blocks: 10000, dictionary: 64 << 20}
}

type boundedReader struct {
	r         io.Reader
	remaining int64
	name      string
}

func limitReader(r io.Reader, size int64, name string) io.Reader {
	return &boundedReader{r: r, remaining: size, name: name}
}

func (r *boundedReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.remaining == 0 {
		var probe [1]byte
		n, err := r.r.Read(probe[:])
		if n > 0 {
			return 0, fmt.Errorf("%w：%s", errResourceLimit, r.name)
		}
		return 0, err
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.r.Read(p)
	r.remaining -= int64(n)
	return n, err
}
