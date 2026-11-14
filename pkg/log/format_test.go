package log

import (
	"bytes"
	"context"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"
)

func Test消息和前缀控制字符转义(t *testing.T) {
	for _, input := range []string{"换行\n伪造", "回车\r覆盖", "制表\t间隔", "清屏\x1b[2J", "响铃\a退格\b", "换页\f纵移\v", "删除\x7f", "控制\u009b2J", "分隔\u2028\u2029", "方向\u202e", "无效编码\x9b2J"} {
		t.Run(strconv.Quote(input), func(t *testing.T) {
			for _, noColor := range []bool{true, false} {
				for _, mode := range []string{"上下文", "属性", "替换消息"} {
					var out bytes.Buffer
					opts := &Options{NoColor: noColor}
					ctx := Prefix(context.Background(), input)
					r := slog.NewRecord(time.Time{}, slog.LevelInfo, input, 0)
					if mode == "属性" {
						ctx = context.Background()
						r.AddAttrs(PrefixAttr(input))
					}
					if mode == "替换消息" {
						r.Message = "原消息"
						opts.ReplaceAttr = func(_ []string, attr slog.Attr) slog.Attr {
							if attr.Key == slog.MessageKey {
								return slog.String(attr.Key, input)
							}
							return attr
						}
					}
					r.AddAttrs(slog.String("属性", input))
					if err := NewHandler(&out, opts).Handle(ctx, r); err != nil {
						t.Fatal(err)
					}
					got := out.String()
					if strings.Count(got, "\n") != 1 || strings.Contains(got, input) || strings.Count(got, strconv.Quote(input)) != 3 {
						t.Errorf("模式=%s，无颜色=%t，控制字符未统一转义：%q", mode, noColor, got)
					}
				}
			}
		})
	}
}

func Test普通消息和前缀保留显示(t *testing.T) {
	var out bytes.Buffer
	r := slog.NewRecord(time.Time{}, slog.LevelInfo, `下载 "文件" 到 /目录`, 0)
	if err := NewHandler(&out, &Options{NoColor: true}).Handle(Prefix(context.Background(), "普通 前缀"), r); err != nil {
		t.Fatal(err)
	}
	if want := "INF [普通 前缀] 下载 \"文件\" 到 /目录\n"; out.String() != want {
		t.Fatalf("输出=%q，期望=%q", out.String(), want)
	}
}

func Test内建同名属性保留业务含义(t *testing.T) {
	for _, attr := range []slog.Attr{
		slog.String(slog.LevelKey, "info"),
		slog.Int(slog.LevelKey, int(slog.LevelError)),
		slog.Any(slog.LevelKey, slog.LevelError),
		slog.String(slog.MessageKey, "业务消息"),
		slog.Time(slog.TimeKey, time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)),
		slog.Any(slog.SourceKey, &slog.Source{File: "业务来源.go", Line: 12}),
		slog.Any(slog.SourceKey, (*slog.Source)(nil)),
		slog.Int(prefixKey, 123),
	} {
		t.Run(attr.String(), func(t *testing.T) {
			for _, mode := range []string{"记录", "预置", "分组"} {
				var out bytes.Buffer
				h := NewHandler(&out, &Options{NoColor: true})
				r := slog.NewRecord(time.Time{}, slog.LevelInfo, "主消息", 0)
				key := attr.Key
				if mode == "预置" {
					h = h.WithAttrs([]slog.Attr{attr})
				} else {
					if mode == "分组" {
						h = h.WithGroup("业务")
						key = "业务." + key
					}
					r.AddAttrs(attr)
				}
				if err := h.Handle(context.Background(), r); err != nil {
					t.Fatal(err)
				}
				if got := out.String(); !strings.HasPrefix(got, "INF 主消息 ") || !strings.Contains(got, key+"=") {
					t.Errorf("模式=%s，同名属性覆盖记录字段或丢失：%q", mode, got)
				}
			}
		})
	}
}

func Test来源属性控制字符转义(t *testing.T) {
	var out bytes.Buffer
	r := slog.NewRecord(time.Time{}, slog.LevelInfo, "消息", 0)
	r.AddAttrs(slog.Any(slog.SourceKey, &slog.Source{File: "模拟\n\x1b[2J.go", Line: 12}))
	if err := NewHandler(&out, &Options{NoColor: true}).Handle(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Count(got, "\n") != 1 || strings.Contains(got, "\x1b") || !strings.Contains(got, `\n\x1b[2J.go`) {
		t.Fatalf("来源属性保留原始控制字符：%q", got)
	}
}

func Test日志记录器接受字符串等级属性(t *testing.T) {
	var out bytes.Buffer
	slog.New(NewHandler(&out, &Options{NoColor: true})).Info("主消息", "level", "info", "msg", "业务消息")
	if got := out.String(); !strings.Contains(got, "INF 主消息 level=info msg=业务消息\n") {
		t.Fatalf("字符串属性未按业务属性输出：%q", got)
	}
}
