package log

import (
	"bytes"
	"context"
	"errors"
	stdlog "log"
	"log/slog"
	"strings"
	"testing"
)

func useDefaultLogger(t *testing.T, logger *slog.Logger) {
	t.Helper()
	previous := slog.Default()
	previousWriter, previousFlags := stdlog.Writer(), stdlog.Flags()
	slog.SetDefault(logger)
	t.Cleanup(func() {
		slog.SetDefault(previous)
		stdlog.SetOutput(previousWriter)
		stdlog.SetFlags(previousFlags)
	})
}

func Test标准写入器接入标准日志(t *testing.T) {
	var out bytes.Buffer
	useDefaultLogger(t, slog.New(NewHandler(&out, &Options{NoColor: true})))
	w := Writer(Prefix(context.Background(), "标准日志"))
	stdlog.New(w, "来源：", 0).Print("首行\n次行")
	got := out.String()
	if strings.Count(got, "\n") != 2 || !strings.Contains(got, "INF [标准日志] 来源：首行\n") || !strings.Contains(got, "INF [标准日志] 次行\n") {
		t.Fatalf("标准日志接入输出错误：%q", got)
	}
}

func Test标准写入器过滤消费全部字节(t *testing.T) {
	var out bytes.Buffer
	var level slog.LevelVar
	level.Set(slog.LevelError)
	useDefaultLogger(t, slog.New(NewHandler(&out, &Options{Level: &level, NoColor: true})))
	w := Writer(context.Background())
	input := []byte("被过滤\n尾行")
	if n, err := w.Write(input); n != len(input) || err != nil {
		t.Fatalf("过滤返回字节数=%d，错误=%v", n, err)
	}
	if out.Len() != 0 {
		t.Fatalf("过滤仍写入：%q", out.String())
	}
	level.Set(slog.LevelInfo)
	if n, err := w.Write([]byte("恢复\n")); n != len("恢复\n") || err != nil {
		t.Fatalf("恢复返回字节数=%d，错误=%v", n, err)
	}
	if got := out.String(); strings.Contains(got, "被过滤") || !strings.Contains(got, "INF 恢复\n") {
		t.Fatalf("过滤日志残留或恢复失败：%q", got)
	}
}

func Test标准写入器逐次分行且不缓存(t *testing.T) {
	for _, tc := range []struct {
		name   string
		writes []string
		want   []string
	}{
		{"空写入", []string{""}, nil},
		{"单行", []string{"单行\n"}, []string{"单行"}},
		{"多行与尾行", []string{"首行\r\n\n尾行"}, []string{"首行", "", "尾行"}},
		{"独立写入", []string{"上半", "下半\n"}, []string{"上半", "下半"}},
		{"空行", []string{"\n\n"}, []string{"", ""}},
		{"行内回车", []string{"首\r尾\n"}, []string{`"首\r尾"`}},
		{"超长单行", []string{strings.Repeat("长", 70000) + "\n"}, []string{strings.Repeat("长", 70000)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			useDefaultLogger(t, slog.New(NewHandler(&out, &Options{NoColor: true})))
			w := Writer(context.Background())
			for _, input := range tc.writes {
				if n, err := w.Write([]byte(input)); n != len(input) || err != nil {
					t.Fatalf("返回字节数=%d，期望=%d，错误=%v", n, len(input), err)
				}
			}
			lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
			if out.Len() == 0 {
				lines = nil
			}
			if len(lines) != len(tc.want) {
				t.Fatalf("日志行数=%d，期望=%d", len(lines), len(tc.want))
			}
			for i, want := range tc.want {
				if !strings.HasSuffix(lines[i], "INF "+want) {
					t.Errorf("第%d行内容不匹配", i)
				}
			}
		})
	}
}

type failingLogWriter struct {
	bytes.Buffer
	writes int
	err    error
}

func (w *failingLogWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == 2 {
		return 0, w.err
	}
	return w.Buffer.Write(p)
}

func Test标准写入器返回处理失败前的消费字节(t *testing.T) {
	wantErr := errors.New("模拟写入失败")
	out := &failingLogWriter{err: wantErr}
	useDefaultLogger(t, slog.New(NewHandler(out, &Options{NoColor: true})))
	n, err := Writer(context.Background()).Write([]byte("首行\n失败行\n未处理"))
	if n != len("首行\n") || !errors.Is(err, wantErr) {
		t.Fatalf("字节数=%d，错误=%v", n, err)
	}
	if out.writes != 2 || strings.Count(out.String(), "\n") != 1 {
		t.Fatalf("错误后继续处理或首行丢失：次数=%d，输出=%q", out.writes, out.String())
	}
}
