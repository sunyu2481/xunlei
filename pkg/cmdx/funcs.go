package cmdx

import (
	"context"
)

// MultiShell 将脚本行作为 sh 的标准输入，覆盖其他选项设置的标准输入。
func MultiShell(ctx context.Context, shellScript []string, options ...Option) error {
	return Run(ctx, "sh", append(options, LineIn(shellScript...))...)
}

// Shell 以 sh -c 运行脚本，覆盖其他选项设置的命令参数。
func Shell(ctx context.Context, shellScript string, options ...Option) error {
	return Run(ctx, "sh", append(options, Args("-c", shellScript))...)
}
