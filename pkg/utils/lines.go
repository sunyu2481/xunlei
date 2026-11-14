package utils

import (
	"bufio"
	"errors"
	"io"
	"iter"
	"sync"
	"unicode/utf8"
)

// walkLines 将长行按约 64 KiB 且不截断中文字符的片段交付，持续读取后续内容，避免无界缓冲。
func walkLines(r io.Reader, yield func(string) bool) error {
	reader := bufio.NewReaderSize(r, 64<<10)
	continued := false
	var carry []byte
	for {
		data, err := reader.ReadSlice('\n')
		if len(carry) > 0 {
			data = append(carry, data...)
			carry = nil
		}
		complete := err != bufio.ErrBufferFull
		if complete {
			if len(data) > 0 && data[len(data)-1] == '\n' {
				data = data[:len(data)-1]
			}
			if len(data) > 0 && data[len(data)-1] == '\r' {
				data = data[:len(data)-1]
			}
		}
		if !complete && len(data) > 0 {
			start := len(data) - 1
			for start > 0 && !utf8.RuneStart(data[start]) && len(data)-start < utf8.UTFMax {
				start--
			}
			if !utf8.FullRune(data[start:]) {
				carry = append([]byte(nil), data[start:]...)
				data = data[:start]
			}
		}
		if len(data) > 0 || (!continued && err == nil) {
			if !yield(string(data)) {
				return nil
			}
		}
		continued = !complete
		if err == io.EOF {
			return nil
		}
		if err != nil && err != bufio.ErrBufferFull {
			return err
		}
	}
}

func LineSeq(r io.Reader) iter.Seq[string] {
	return func(yield func(string) bool) { walkLines(r, yield) }
}

// LineWalk 遍历全部行或长行片段，并返回底层读取错误。
func LineWalk(r io.Reader, f func(string)) error {
	return walkLines(r, func(s string) bool { f(s); return true })
}

type lineWriter struct {
	*io.PipeWriter
	done      chan struct{}
	closeOnce sync.Once
	err       error
}

// LineWriter 的回调须及时返回；Close 等待最后一个回调完成并返回读取错误。
func LineWriter(lineRead func(string)) io.WriteCloser {
	r, w := io.Pipe()
	writer := &lineWriter{PipeWriter: w, done: make(chan struct{})}
	go func() {
		writer.err = LineWalk(r, lineRead)
		r.CloseWithError(writer.err)
		close(writer.done)
	}()
	return writer
}

func (w *lineWriter) Close() error {
	var closeErr error
	w.closeOnce.Do(func() { closeErr = w.PipeWriter.Close() })
	<-w.done
	return errors.Join(closeErr, w.err)
}
