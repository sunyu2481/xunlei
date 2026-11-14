package utils

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

type lineReadFailure struct{ err error }

func (r lineReadFailure) Read([]byte) (int, error) { return 0, r.err }

func Test分行保留内容并传播读取错误(t *testing.T) {
	want := errors.New("模拟读取故障")
	var lines []string
	err := LineWalk(io.MultiReader(strings.NewReader("首行\r\n\n末行"), lineReadFailure{want}), func(s string) { lines = append(lines, s) })
	if !errors.Is(err, want) || !reflect.DeepEqual(lines, []string{"首行", "", "末行"}) {
		t.Fatalf("分行=%q，错误=%v", lines, err)
	}
	long := strings.Repeat("长", 50000)
	var got strings.Builder
	if err := LineWalk(strings.NewReader(long), func(s string) {
		if !utf8.ValidString(s) {
			t.Error("长行片段切断了中文字符")
		}
		got.WriteString(s)
	}); err != nil {
		t.Fatal(err)
	}
	if got.String() != long {
		t.Fatal("长行内容丢失")
	}
	var seq strings.Builder
	for s := range LineSeq(strings.NewReader(long + "\n末行")) {
		seq.WriteString(s)
	}
	if seq.String() != long+"末行" {
		t.Fatal("迭代分行丢失内容")
	}
}
