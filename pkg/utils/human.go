package utils

import (
	"fmt"
	"slices"
	"strconv"
)

// HumanBytes 按二进制单位格式化大小，首个精度参数默认是二，显式零值表示整数精度。
func HumanBytes[T UintT | IntT](n T, prec ...int) string {
	precision := 2
	if len(prec) > 0 {
		precision = prec[0]
	}
	if f := float64(n); f >= 1024 {
		for i, u := range slices.Backward([]rune("KMGTE")) {
			if base := float64(int64(1) << (10 * (i + 1))); float64(n) >= base {
				return fmt.Sprintf("%s %ciB", strconv.FormatFloat(f/base, 'f', precision, 64), u)
			}
		}
	}
	return fmt.Sprintf("%d bytes", n)
}

// PasswordMask 只区分是否设置密码，不暴露其内容或长度。
func PasswordMask(s string) string {
	if s == "" {
		return ""
	}
	return "******"
}
