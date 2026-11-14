package cmdx

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	stdlog "log"
	"log/slog"
	"reflect"
	"testing"
)

func Test日志选项尊重等级与过滤(t *testing.T) {
	for _, tc := range []struct {
		name     string
		level    slog.Level
		minimum  slog.Level
		filtered bool
	}{
		{"调试", slog.LevelDebug, slog.LevelDebug, false},
		{"信息", slog.LevelInfo, slog.LevelInfo, false},
		{"警告", slog.LevelWarn, slog.LevelInfo, false},
		{"错误", slog.LevelError, slog.LevelInfo, false},
		{"自定义", slog.LevelWarn + 1, slog.LevelInfo, false},
		{"低于阈值", slog.LevelInfo, slog.LevelWarn, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := slog.Default()
			previousWriter, previousFlags := stdlog.Writer(), stdlog.Flags()
			t.Cleanup(func() {
				slog.SetDefault(previous)
				stdlog.SetOutput(previousWriter)
				stdlog.SetFlags(previousFlags)
			})
			var output bytes.Buffer
			slog.SetDefault(slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: tc.minimum})))
			err := Run(context.Background(), "/bin/sh",
				Args("-c", "printf '输出\\n'; printf '错误\\n' >&2"), Slog("子进程", tc.level))
			if err != nil {
				t.Fatal(err)
			}

			messages := map[string]int{}
			decoder := json.NewDecoder(&output)
			for {
				var record struct {
					Level   string `json:"level"`
					Message string `json:"msg"`
					Prefix  string `json:"prefix"`
				}
				if err := decoder.Decode(&record); err == io.EOF {
					break
				} else if err != nil {
					t.Fatal(err)
				}
				if record.Prefix != "子进程" {
					continue
				}
				if record.Level != tc.level.String() {
					t.Errorf("记录等级=%s，期望=%s", record.Level, tc.level)
				}
				messages[record.Message]++
			}
			want := map[string]int{}
			if !tc.filtered {
				want = map[string]int{"[stdout] 输出": 1, "[stderr] 错误": 1}
			}
			if !reflect.DeepEqual(messages, want) {
				t.Fatalf("子进程日志=%v，期望=%v", messages, want)
			}
		})
	}
}
