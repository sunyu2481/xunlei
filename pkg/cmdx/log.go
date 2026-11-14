package cmdx

import (
	"github.com/cnk3x/xunlei/pkg/utils"
	"io"
)

// LineWriter 复用有界分行与同步关闭实现。
func LineWriter(lineRead func(string)) io.WriteCloser { return utils.LineWriter(lineRead) }
