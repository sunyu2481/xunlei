package log

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func Test派生处理器并发写入(t *testing.T) {
	var out bytes.Buffer
	base := slog.New(NewHandler(&out, &Options{NoColor: true}))
	loggers := []*slog.Logger{base, base.With("来源", "属性"), base.WithGroup("分组")}
	var wg sync.WaitGroup
	for _, logger := range loggers {
		for range 4 {
			wg.Go(func() {
				for range 100 {
					logger.Info("并发消息", "键", "值")
				}
			})
		}
	}
	wg.Wait()
	if got := strings.Count(out.String(), "\n"); got != 1200 {
		t.Fatalf("日志行数=%d，期望1200", got)
	}
	if got := strings.Count(out.String(), "来源=属性"); got != 400 {
		t.Fatalf("带预置属性的日志数=%d，期望400", got)
	}
	if got := strings.Count(out.String(), "分组.键=值"); got != 400 {
		t.Fatalf("带分组属性的日志数=%d，期望400", got)
	}
}

func Test派生处理器分组保持独立(t *testing.T) {
	var out bytes.Buffer
	base := slog.New(NewHandler(&out, &Options{
		NoColor: true,
		ReplaceAttr: func(groups []string, attr slog.Attr) slog.Attr {
			if attr.Key == "路径" {
				return slog.String(attr.Key, strings.Join(groups, "/"))
			}
			return attr
		},
	})).WithGroup("一").WithGroup("二").WithGroup("三")
	left := base.WithGroup("甲")
	right := base.WithGroup("乙")
	withAttrs := base.With(slog.Group("预置", slog.String("路径", "")))
	left.Info("消息", "路径", "")
	right.Info("消息", "路径", "")
	withAttrs.Info("消息", slog.Group("记录", slog.String("路径", "")))
	base.Info("消息", "路径", "")
	for _, want := range []string{
		"一.二.三.甲.路径=一/二/三/甲",
		"一.二.三.乙.路径=一/二/三/乙",
		"一.二.三.预置.路径=一/二/三/预置",
		"一.二.三.记录.路径=一/二/三/记录",
		"一.二.三.路径=一/二/三",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("输出缺少 %q：\n%s", want, out.String())
		}
	}
}

func Test分组并发派生与递归属性(t *testing.T) {
	var mismatches atomic.Int64
	base := slog.New(NewHandler(io.Discard, &Options{
		NoColor: true,
		ReplaceAttr: func(groups []string, attr slog.Attr) slog.Attr {
			if attr.Key == "路径" {
				runtime.Gosched()
				if strings.Join(groups, "/") != attr.Value.String() {
					mismatches.Add(1)
				}
			}
			return attr
		},
	})).WithGroup("一").WithGroup("二").WithGroup("三")
	var wg sync.WaitGroup
	for _, name := range []string{"甲", "乙", "丙", "丁"} {
		wg.Go(func() {
			for range 100 {
				path := "一/二/三/" + name
				base.WithGroup(name).Info("派生", "路径", path)
				base.Info("递归", slog.Group(name, slog.String("路径", path),
					slog.Group("子组", slog.String("路径", path+"/子组"))))
				base.With(slog.Group(name, slog.String("路径", path))).Info("预置")
			}
		})
	}
	wg.Wait()
	if got := mismatches.Load(); got != 0 {
		t.Fatalf("分组路径串改次数=%d", got)
	}
}

func Test动态等级由派生处理器共享(t *testing.T) {
	var external slog.LevelVar
	external.Set(slog.LevelWarn)
	base := slog.New(NewHandler(io.Discard, &Options{Level: &external}))
	attrs := base.With("来源", "属性")
	group := base.WithGroup("分组")
	independent := slog.New(NewHandler(io.Discard, &Options{Level: slog.LevelError}))
	check := func(want slog.Level) {
		t.Helper()
		for i, logger := range []*slog.Logger{base, attrs, group} {
			for _, level := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
				if got := logger.Enabled(context.Background(), level); got != (level >= want) {
					t.Errorf("处理器%d 等级%s 的启用状态=%t，最低等级应为%s", i, level, got, want)
				}
			}
		}
	}
	check(slog.LevelWarn)
	external.Set(slog.LevelDebug)
	check(slog.LevelDebug)
	Level(attrs, "ERROR")
	check(slog.LevelError)
	Level(group, "无效等级")
	check(slog.LevelError)
	Level(base, "info")
	check(slog.LevelInfo)
	if independent.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("动态更新影响了独立创建的处理器")
	}
}

func Test动态等级并发更新与记录(t *testing.T) {
	base := slog.New(NewHandler(io.Discard, nil))
	loggers := []*slog.Logger{base, base.With("来源", "属性"), base.WithGroup("分组")}
	var wg sync.WaitGroup
	for _, logger := range loggers {
		wg.Go(func() {
			for range 500 {
				Level(logger, "debug")
				Level(logger, "warn")
			}
		})
		wg.Go(func() {
			for range 500 {
				logger.Debug("并发等级")
				logger.Warn("并发等级")
			}
		})
	}
	wg.Wait()
	Level(base, "error")
	for i, logger := range loggers {
		if logger.Enabled(context.Background(), slog.LevelWarn) {
			t.Errorf("处理器%d 未应用最终等级", i)
		}
	}
}
