package cmdx

import (
	"context"
	"reflect"
	"testing"
)

func Test标准输出与错误选项顺序无关(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		var out, errLines []string
		options := []Option{LineErr(func(s string) { errLines = append(errLines, s) }), LineOut(func(s string) { out = append(out, s) })}
		if reverse {
			options[0], options[1] = options[1], options[0]
		}
		options = append(options, Args("-c", "printf '输出\\n'; printf '错误\\n' >&2"))
		if err := Run(context.Background(), "/bin/sh", options...); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(out, []string{"输出"}) || !reflect.DeepEqual(errLines, []string{"错误"}) {
			t.Fatalf("反转=%t，输出=%q，错误=%q", reverse, out, errLines)
		}
	}
}

func Test显式合并输出(t *testing.T) {
	var lines []string
	if err := Run(context.Background(), "/bin/sh", Args("-c", "printf '输出\\n'; printf '错误\\n' >&2"), LineCombined(func(s string) { lines = append(lines, s) })); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(lines, []string{"输出", "错误"}) {
		t.Fatalf("合并输出错误：%q", lines)
	}
}
