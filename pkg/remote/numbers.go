package remote

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

var decimalInteger = regexp.MustCompile(`^([+-]?)([0-9]+)(?:\.([0-9]+))?(?:[eE]([+-]?[0-9]+))?$`)
var errInteger = errors.New("数值不是有效的六十四位整数")

// integer 接受整数、小数形式的整数及科学计数表示，不经浮点转换或截断。
func integer(v any) (int64, error) {
	s := strings.TrimSpace(str(v))
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n, nil
	}
	parts := decimalInteger.FindStringSubmatch(s)
	if parts == nil {
		return 0, errInteger
	}
	digits := strings.TrimLeft(parts[2]+parts[3], "0")
	if digits == "" {
		return 0, nil
	}
	trimmed := strings.TrimRight(digits, "0")
	scale := int64(len(parts[3]) - (len(digits) - len(trimmed)))
	var exponent int64
	if parts[4] != "" {
		var err error
		if exponent, err = strconv.ParseInt(parts[4], 10, 64); err != nil {
			return 0, errInteger
		}
	}
	// 先限制结果位数，极端指数也不分配随指数增长的内存。
	if len(trimmed) > 19 || exponent < scale || exponent > scale+int64(19-len(trimmed)) {
		return 0, errInteger
	}
	n, err := strconv.ParseInt(parts[1]+trimmed+strings.Repeat("0", int(exponent-scale)), 10, 64)
	if err != nil {
		return 0, errInteger
	}
	return n, nil
}

// integerField 将省略和空字符串视为缺失，显式零值与无效字段分别返回。
func integerField(d object, key string) (int64, bool, error) {
	v, present := d[key]
	if !present || v == "" {
		return 0, false, nil
	}
	n, err := integer(v)
	return n, true, err
}
