package authenticate_cgi

import (
	"os"
)

func WriteTo(w *os.File) error {
	// 写入期间仅允许安装用户访问，成功后再开放读取和执行。
	if err := w.Chmod(0700); err != nil {
		return err
	}
	if _, err := w.Write(Bytes); err != nil {
		return err
	}
	return w.Chmod(0755)
}
