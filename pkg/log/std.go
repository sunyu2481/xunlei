package log

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"runtime"
	"time"
)

// Writer 将标准库 log.Logger 的输出按信息等级转交给创建时的默认处理器。
// 每次 Write 按换行分成记录，去掉行尾的换行及其前一个回车，保留空行。
// 未换行的末尾立即输出，不跨 Write 合并；空写入不产生日志，无需关闭或刷新。
// 被等级过滤的写入仍视为全部消费；处理失败时返回此前完整行的字节数和错误。
func Writer(ctx context.Context) io.Writer {
	return &stdWriter{ctx: ctx, level: slog.LevelInfo, h: slog.Default().Handler()}
}

func Caller(skip int) (pc uintptr) {
	var pcs [1]uintptr
	runtime.Callers(2+skip, pcs[:])
	return pcs[0]
}

func ErrDebug(err error) slog.Level {
	if err != nil {
		return slog.LevelError
	}
	return slog.LevelDebug
}

func WarnDebug(err error) slog.Level {
	if err != nil {
		return slog.LevelWarn
	}
	return slog.LevelDebug
}

type stdWriter struct {
	ctx       context.Context
	level     slog.Leveler
	h         slog.Handler
	capturePC bool
}

func (w *stdWriter) Write(buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	level := w.level.Level()
	if !w.h.Enabled(w.ctx, level) {
		return len(buf), nil
	}
	var pc uintptr
	if w.capturePC {
		pc = Caller(3)
	}

	n := 0
	for line := range bytes.Lines(buf) {
		message := line
		if message[len(message)-1] == '\n' {
			message = bytes.TrimSuffix(message[:len(message)-1], []byte{'\r'})
		}
		r := slog.NewRecord(time.Now(), level, string(message), pc)
		if err := w.h.Handle(w.ctx, r); err != nil {
			return n, err
		}
		n += len(line)
	}
	return n, nil
}
