package utils

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"
)

func Test随机文本满足长度和字符约定(t *testing.T) {
	for n := 0; n <= 128; n++ {
		got := RandText(n)
		if len(got) != n {
			t.Errorf("长度=%d，实际=%d", n, len(got))
		}
		if strings.ContainsFunc(got, func(r rune) bool { return !strings.ContainsRune("abcdefghijklmnopqrstuvwxyz234567", r) }) {
			t.Errorf("长度=%d，输出包含约定之外的字符", n)
		}
	}
}

func Test随机文本拒绝非法长度(t *testing.T) {
	for _, n := range []int{-1, math.MinInt, math.MaxInt, math.MaxInt - 6} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("非法长度未被拒绝")
				}
			}()
			RandText(n)
		})
	}
}

func Test整数格式化保留符号和全部位数(t *testing.T) {
	type namedUint uint64
	type namedInt int64
	for _, tc := range []struct{ got, want string }{
		{String(uint64(math.MaxUint64)), "18446744073709551615"},
		{String(namedUint(1 << 63)), "9223372036854775808"},
		{String(namedInt(math.MinInt64)), "-9223372036854775808"},
		{String(int64(math.MaxInt64)), "9223372036854775807"},
		{String(uint8(0)), "0"},
		{String(int8(-1)), "-1"},
	} {
		if tc.got != tc.want {
			t.Errorf("得到=%q，期望=%q", tc.got, tc.want)
		}
	}
}

func checkIntegerParser[T IntT | UintT](t *testing.T, parse func(string) (T, error), valid map[string]T, invalid []string) {
	t.Helper()
	for input, want := range valid {
		if got, err := parse(input); err != nil || got != want {
			t.Errorf("输入=%q，结果=%v，错误=%v，期望=%v", input, got, err, want)
		}
	}
	for _, input := range invalid {
		got, err := parse(input)
		var numberError *strconv.NumError
		if got != 0 || !errors.Is(err, strconv.ErrRange) || !errors.As(err, &numberError) || numberError.Num != input {
			t.Errorf("越界输入=%q，结果=%v，错误=%v", input, got, err)
		}
	}
}

func Test整数解析拒绝窄化和符号溢出(t *testing.T) {
	type namedInt int8
	type namedUint uint8
	checkIntegerParser(t, StrParser[namedInt](strconv.ParseInt, 10, 64), map[string]namedInt{"-128": -128, "127": 127}, []string{"-129", "128", "256"})
	checkIntegerParser(t, StrParser[namedUint](strconv.ParseUint, 10, 64), map[string]namedUint{"0": 0, "255": 255}, []string{"256", "18446744073709551615"})
	checkIntegerParser(t, StrParser[uint64](strconv.ParseInt, 10, 64), map[string]uint64{"9223372036854775807": math.MaxInt64}, []string{"-1", "-9223372036854775808"})
	checkIntegerParser(t, StrParser[int64](strconv.ParseUint, 10, 64), map[string]int64{"9223372036854775807": math.MaxInt64}, []string{"9223372036854775808", "18446744073709551615"})
	checkIntegerParser(t, ParseUint64, map[string]uint64{"18446744073709551615": math.MaxUint64}, []string{"18446744073709551616"})
	checkIntegerParser(t, ParseInt8, map[string]int8{"-128": -128, "127": 127}, []string{"-129", "128"})
	wantErr := errors.New("模拟解析错误")
	parse := StrParser[uint8](func(string, int, int) (uint64, error) { return 5, wantErr }, 0, 0)
	if got, err := parse("错误输入"); got != 0 || err != wantErr {
		t.Fatalf("原始解析错误被改写：值=%d，错误=%v", got, err)
	}
}

func Test大小格式化保留显式精度(t *testing.T) {
	for _, tc := range []struct {
		name string
		n    int
		prec []int
		want string
	}{
		{"默认", 1024, nil, "1.00 KiB"},
		{"显式零位", 1536, []int{0}, "2 KiB"},
		{"显式一位", 1536, []int{1}, "1.5 KiB"},
		{"首参数零位", 1536, []int{0, 3}, "2 KiB"},
		{"最短精度", 1536, []int{-1}, "1.5 KiB"},
		{"字节", 1023, []int{0}, "1023 bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := HumanBytes(tc.n, tc.prec...); got != tc.want {
				t.Fatalf("实际=%q，期望=%q", got, tc.want)
			}
		})
	}
}
