package cmdx

import (
	"errors"
	"testing"
)

func Test命令选项支持命名与匿名函数(t *testing.T) {
	type plainOption func(*Cmd)
	type errorOption func(*Cmd) error
	type closerOption func(*Cmd) Closer
	type fullOption func(*Cmd) (Closer, error)

	tests := []struct {
		name       string
		factory    func(func(*Cmd), Closer, error) Option
		withCloser bool
		withError  bool
	}{
		{"匿名无返回值", func(apply func(*Cmd), _ Closer, _ error) Option {
			return May(apply)
		}, false, false},
		{"命名无返回值", func(apply func(*Cmd), _ Closer, _ error) Option {
			return May(plainOption(apply))
		}, false, false},
		{"匿名错误", func(apply func(*Cmd), _ Closer, err error) Option {
			return May(func(c *Cmd) error { apply(c); return err })
		}, false, true},
		{"命名错误", func(apply func(*Cmd), _ Closer, err error) Option {
			return May(errorOption(func(c *Cmd) error { apply(c); return err }))
		}, false, true},
		{"匿名清理", func(apply func(*Cmd), clean Closer, _ error) Option {
			return May(func(c *Cmd) Closer { apply(c); return clean })
		}, true, false},
		{"命名清理", func(apply func(*Cmd), clean Closer, _ error) Option {
			return May(closerOption(func(c *Cmd) Closer { apply(c); return clean }))
		}, true, false},
		{"匿名清理与错误", func(apply func(*Cmd), clean Closer, err error) Option {
			return May(func(c *Cmd) (Closer, error) { apply(c); return clean, err })
		}, true, true},
		{"命名清理与错误", func(apply func(*Cmd), clean Closer, err error) Option {
			return May(fullOption(func(c *Cmd) (Closer, error) { apply(c); return clean, err }))
		}, true, true},
	}
	for _, tc := range tests {
		for _, wantErr := range []error{nil, errors.New("模拟选项失败")} {
			if wantErr != nil && !tc.withError {
				continue
			}
			for _, nilCloser := range []bool{false, true} {
				if nilCloser && !tc.withCloser {
					continue
				}
				name := tc.name
				if wantErr != nil {
					name += "/失败"
				}
				if nilCloser {
					name += "/空清理"
				}
				t.Run(name, func(t *testing.T) {
					command := &Cmd{}
					calls, cleanups := 0, 0
					var cleanup Closer
					if !nilCloser {
						cleanup = func() { cleanups++ }
					}
					option := tc.factory(func(c *Cmd) {
						if c != command {
							t.Error("选项没有收到原命令")
						}
						calls++
					}, cleanup, wantErr)
					if calls != 0 || cleanups != 0 {
						t.Fatal("构造选项时提前执行回调")
					}
					for invocation := 1; invocation <= 2; invocation++ {
						clean, err := option(command)
						if err != wantErr || calls != invocation {
							t.Fatalf("回调次数=%d，期望%d；错误=%v，期望%v", calls, invocation, err, wantErr)
						}
						if nilCloser {
							if clean != nil {
								t.Fatal("回调返回的空清理未被保留")
							}
							continue
						}
						if clean == nil {
							t.Fatal("缺少可调用的清理函数")
						}
						if tc.withCloser && cleanups != invocation-1 || !tc.withCloser && cleanups != 0 {
							t.Fatal("选项提前执行清理")
						}
						clean()
						if tc.withCloser && cleanups != invocation || !tc.withCloser && cleanups != 0 {
							t.Fatalf("清理次数错误：%d", cleanups)
						}
					}
				})
			}
		}
	}
}
