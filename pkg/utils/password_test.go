package utils

import (
	"testing"
	"unicode/utf8"
)

func Test密码掩码不暴露内容和长度(t *testing.T) {
	if PasswordMask("") != "" {
		t.Fatal("空密码不应显示为已设置")
	}
	want := PasswordMask("模拟密码")
	for _, input := range []string{"a", "sensitive-example", "中文", "更长的模拟密码"} {
		if got := PasswordMask(input); got != want || !utf8.ValidString(got) || got == "" {
			t.Fatal("非空密码掩码应为有效且固定的文本")
		}
	}
}
