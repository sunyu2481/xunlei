package remote

import (
	"encoding/base64"
	"net/url"
	"regexp"
	"strings"
)

var btihPattern = regexp.MustCompile(`(?i)^urn:btih:([a-f0-9]{40}|[a-z2-7]{32})$`)
var btmhPattern = regexp.MustCompile(`(?i)^urn:btmh:1220[a-f0-9]{64}$`)

func NormalizeLink(link string) (string, error) {
	link = strings.TrimSpace(link)
	if len(link) == 0 || len(link) > 16384 || strings.ContainsAny(link, "\r\n\x00") {
		return "", failure("invalid_request", "下载链接为空、过长或包含无效字符")
	}
	if strings.HasPrefix(strings.ToLower(link), "thunder://") {
		encoded := strings.TrimSuffix(link[len("thunder://"):], "/")
		var decoded []byte
		for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
			b, err := encoding.DecodeString(encoded)
			if err == nil {
				decoded = b
				break
			}
		}
		s := string(decoded)
		if !strings.HasPrefix(s, "AA") || !strings.HasSuffix(s, "ZZ") {
			return "", failure("invalid_request", "迅雷专用链接格式不正确")
		}
		link = strings.TrimSpace(s[2 : len(s)-2])
	}
	u, err := url.Parse(link)
	if err != nil {
		return "", failure("invalid_request", "下载链接格式不正确")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "ftp":
		if u.Hostname() == "" || u.User != nil {
			return "", failure("invalid_request", "请提供有效且不含账号密码的下载链接")
		}
	case "magnet":
		valid := false
		for _, xt := range u.Query()["xt"] {
			if btihPattern.MatchString(xt) || btmhPattern.MatchString(xt) {
				valid = true
			}
		}
		if !valid {
			return "", failure("invalid_request", "磁力链接缺少有效的资源标识")
		}
	default:
		return "", failure("invalid_request", "支持磁力链接、HTTP、HTTPS、FTP 及迅雷专用链接")
	}
	return link, nil
}
