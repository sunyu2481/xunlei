package fo

import (
	"io/fs"
	"os"
)

// options 创建文件时候的选项
type options struct {
	flag    int
	perm    fs.FileMode
	dirPerm fs.FileMode
	existOk bool
}

// Option 创建文件时候的选项
type Option func(*options)

func newOptions(defaultFlag int, fopts ...Option) options {
	opts := options{flag: defaultFlag, perm: 0666, dirPerm: 0777}
	for _, apply := range fopts {
		apply(&opts)
	}
	return opts
}

// Perm 创建文件时候的权限
func Perm(perm fs.FileMode) Option { return func(o *options) { o.perm = perm } }

// DirPerm 设置新建父目录的权限，受进程权限掩码限制；零值有效，已有目录不变。
func DirPerm(perm fs.FileMode) Option { return func(o *options) { o.dirPerm = perm } }

// Flag 替换全部打开标志，包括 OpenWrite 或 OpenRead 的默认标志。
func Flag(flag int) Option { return func(o *options) { o.flag = flag } }

// FlagExcl 创建文件时候如果文件已存在则返回错误，如果同时 existOK 为 true 则忽略该错误，且什么都不会做。
func FlagExcl(existOk bool) Option {
	return func(o *options) {
		o.flag, o.existOk = fr0(o.flag, os.O_EXCL), existOk
	}
}

// FlagAppend 选择追加模式，清除截断和排他标志，保留其他打开标志。
func FlagAppend(o *options) { o.flag = fr0(o.flag, os.O_APPEND) }

// FlagTrunc 选择截断模式，清除追加和排他标志，保留其他打开标志。
func FlagTrunc(o *options) { o.flag = fr0(o.flag, os.O_TRUNC) }

// FlagWo 选择只写模式，保留访问模式之外的标志。
func FlagWo(o *options) { o.flag = fr1(o.flag, os.O_WRONLY) }

// FlagRw 选择读写模式，保留访问模式之外的标志。
func FlagRw(o *options) { o.flag = fr1(o.flag, os.O_RDWR) }

// PermFrom 将源文件的普通权限用于新建文件；查询失败时保留当前设置。
func PermFrom(r *os.File) Option {
	return func(o *options) {
		if stat, err := r.Stat(); err == nil {
			o.perm = stat.Mode().Perm()
		}
	}
}

func fr0(src int, newFlag int) int { return src&^(os.O_APPEND|os.O_TRUNC|os.O_EXCL) | newFlag }
func fr1(src int, newFlag int) int { return src&^(os.O_RDWR|os.O_WRONLY) | newFlag }
