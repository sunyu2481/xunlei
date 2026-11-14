package cmdx

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"reflect"
	"slices"
	"strings"
	"syscall"

	"github.com/cnk3x/xunlei/pkg/log"
	"github.com/cnk3x/xunlei/pkg/utils"
)

type Option = func(*Cmd) (Closer, error)

func Options(options ...Option) Option {
	return func(c *Cmd) (closer Closer, err error) {
		bq := utils.BackQueue(&closer, &err)
		defer bq.ErrDefer()
		for _, option := range options {
			fc, e := option(c)
			if err = e; err != nil {
				return
			}
			bq.Put(fc)
		}
		return
	}
}

// ProcAttr 确保系统进程属性存在，再按顺序应用选项。
func ProcAttr(options ...Option) Option {
	attrInit := May(func(c *Cmd) {
		if c.SysProcAttr == nil {
			c.SysProcAttr = &syscall.SysProcAttr{}
		}
	})
	return Options(slices.Insert(options, 0, attrInit)...)
}

// Credential 以另外的身份运行外部程序
func Credential(uid, gid uint32) Option {
	return ProcAttr(May(func(c *Cmd) error {
		if os.Geteuid() != 0 {
			return fmt.Errorf("%w：设置子进程身份需要根权限", os.ErrPermission)
		}
		c.SysProcAttr.Credential = &syscall.Credential{Uid: uid, Gid: gid}
		return nil
	}))
}

// Args 覆盖设置外部程序运行参数，替换掉 `exec.Command(name, args...)` 中所有的args
func Args(args ...string) Option {
	return May(func(c *Cmd) { c.Args = append(c.Args[:1], args...) })
}

const True = "___true"

// Flags 覆盖设置外部程序运行参数，以两个一对键值对分割
//   - 如果是无值参数，需添加[cmdx.True]来占位
//   - 如果提供的键或值为空，该方法会自动忽略。
//   - 有空值要求的，请使用 [cmdx.Args] 选项
func Flags(args ...string) Option {
	var selectedArgs []string
	for i := 0; i < len(args)-1; i += 2 {
		if k, v := args[i], args[i+1]; k != "" && v != "" {
			if v == True {
				selectedArgs = append(selectedArgs, k)
			} else {
				selectedArgs = append(selectedArgs, k, v)
			}
		}
	}
	return Args(selectedArgs...)
}

func Dir(dir string) Option   { return May(func(c *Cmd) { c.Dir = dir }) }
func Env(env []string) Option { return May(func(c *Cmd) { c.Env = env }) }

// Stderr 设置标准错误目标，其关闭由调用方负责。
func Stderr(w io.Writer) Option { return May(func(c *Cmd) { c.Stderr = w }) }

// Stdout 设置标准输出目标，其关闭由调用方负责。
func Stdout(w io.Writer) Option { return May(func(c *Cmd) { c.Stdout = w }) }

// Stdin 设置标准输入来源，其关闭由调用方负责。
func Stdin(r io.Reader) Option { return May(func(c *Cmd) { c.Stdin = r }) }

func PreStart(f func(*Cmd) error) Option  { return May(func(c *Cmd) { c.preStart = f }) }
func OnStarted(f func(*Cmd) error) Option { return May(func(c *Cmd) { c.onStarted = f }) }
func OnExit(f func(*Cmd) error) Option    { return May(func(c *Cmd) { c.onExit = f }) }

func LineOut(lineRecv func(string)) Option {
	return May(func(c *Cmd) Closer {
		w := LineWriter(lineRecv)
		c.Stdout = w
		return eClean(w)
	})
}

// LineCombined 显式将标准输出与标准错误交给同一个逐行接收器。
func LineCombined(lineRecv func(string)) Option {
	return May(func(c *Cmd) Closer {
		w := LineWriter(lineRecv)
		c.Stdout, c.Stderr = w, w
		return eClean(w)
	})
}

func LineErr(lineRecv func(string)) Option {
	return May(func(c *Cmd) Closer {
		w := LineWriter(lineRecv)
		c.Stderr = w
		return eClean(w)
	})
}

// LineIn 将各行以换行连接并追加末尾换行，作为标准输入。
func LineIn(lines ...string) Option {
	return Stdin(strings.NewReader(strings.Join(lines, "\n") + "\n"))
}

// Slog 按指定等级将两路输出逐行交给默认日志记录器，附带前缀和输出来源。
func Slog(prefix string, level slog.Level) Option {
	le := LineErr(func(s string) { slog.LogAttrs(context.Background(), level, "[stderr] "+s, log.PrefixAttr(prefix)) })
	lo := LineOut(func(s string) { slog.LogAttrs(context.Background(), level, "[stdout] "+s, log.PrefixAttr(prefix)) })
	return Options(le, lo)
}

// LogStd 让子进程继承当前进程的标准输出和标准错误。
func LogStd() Option {
	return May(func(c *Cmd) { c.Stderr, c.Stdout = os.Stderr, os.Stdout })
}

type iOption interface {
	~func(*Cmd) | ~func(*Cmd) error | ~func(*Cmd) Closer | ~func(*Cmd) (Closer, error)
}

// May 将约束内的命名或匿名函数适配为选项，保留其错误与清理函数。
func May[O iOption](option O) Option {
	// 仅在构造时转换函数类型，执行选项时直接调用已适配的函数。
	value := reflect.ValueOf(option)
	switch {
	case value.CanConvert(reflect.TypeFor[func(*Cmd) (Closer, error)]()):
		apply := value.Convert(reflect.TypeFor[func(*Cmd) (Closer, error)]()).Interface().(func(*Cmd) (Closer, error))
		return func(c *Cmd) (Closer, error) { return apply(c) }
	case value.CanConvert(reflect.TypeFor[func(*Cmd) Closer]()):
		apply := value.Convert(reflect.TypeFor[func(*Cmd) Closer]()).Interface().(func(*Cmd) Closer)
		return func(c *Cmd) (Closer, error) { return apply(c), nil }
	case value.CanConvert(reflect.TypeFor[func(*Cmd) error]()):
		apply := value.Convert(reflect.TypeFor[func(*Cmd) error]()).Interface().(func(*Cmd) error)
		return func(c *Cmd) (Closer, error) { return func() {}, apply(c) }
	case value.CanConvert(reflect.TypeFor[func(*Cmd)]()):
		apply := value.Convert(reflect.TypeFor[func(*Cmd)]()).Interface().(func(*Cmd))
		return func(c *Cmd) (Closer, error) {
			apply(c)
			return func() {}, nil
		}
	default:
		panic("命令选项函数类型不满足已声明的约束")
	}
}

type Closer = func()

func eClean(closer io.Closer) Closer {
	return func() {
		if closer != nil {
			_ = closer.Close()
		}
	}
}
