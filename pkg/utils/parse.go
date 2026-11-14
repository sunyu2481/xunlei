package utils

import "strconv"

type UintT interface {
	~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64
}

type IntT interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64
}

// StrParser 适配整数解析函数，目标类型无法表示结果时返回 strconv.ErrRange。
func StrParser[R UintT | IntT, T1, T2 any, R64 uint64 | int64](f func(string, T1, T2) (R64, error), t1 T1, t2 T2) func(string) (R, error) {
	return func(s string) (R, error) {
		r, err := f(s, t1, t2)
		if err != nil {
			return 0, err
		}
		value := R(r)
		// 往返转换检查丢失的高位，符号检查防止同位宽转换后仍能还原原始位模式。
		if R64(value) != r || (r < 0) != (value < 0) {
			return 0, &strconv.NumError{Func: "StrParser", Num: s, Err: strconv.ErrRange}
		}
		return value, nil
	}
}

var (
	ParseUint   = StrParser[uint](strconv.ParseUint, 0, 64)
	ParseUint8  = StrParser[uint8](strconv.ParseUint, 0, 8)
	ParseUint16 = StrParser[uint16](strconv.ParseUint, 0, 16)
	ParseUint32 = StrParser[uint32](strconv.ParseUint, 0, 32)
	ParseUint64 = StrParser[uint64](strconv.ParseUint, 0, 64)
	ParseInt    = StrParser[int](strconv.ParseInt, 0, 64)
	ParseInt8   = StrParser[int8](strconv.ParseInt, 0, 8)
	ParseInt16  = StrParser[int16](strconv.ParseInt, 0, 16)
	ParseInt32  = StrParser[int32](strconv.ParseInt, 0, 32)
	ParseInt64  = StrParser[int64](strconv.ParseInt, 0, 64)
)

func String[T IntT | UintT](v T) string {
	if v < 0 {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatUint(uint64(v), 10)
}
