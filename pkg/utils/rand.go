package utils

import (
	"crypto/rand"
	"encoding/base32"
)

var b32 = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// RandText 生成指定长度的小写 Base32 随机字符串，零长度返回空字符串。
// 负长度或无法安全按八字符分组取整的长度会触发 panic。
func RandText(n int) string {
	if n < 0 || n > int(^uint(0)>>1)-7 {
		panic("随机文本长度超出有效范围")
	}
	// 每五个随机字节提供八个字符，先补足完整分组再截取，避免尾字符随机位不足。
	d := make([]byte, (n+7)/8*5)
	_, _ = rand.Read(d)
	return b32.EncodeToString(d)[:n]
}
