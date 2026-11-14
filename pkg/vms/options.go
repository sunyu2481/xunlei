package vms

import (
	"context"
	"fmt"
	"path/filepath"
	"syscall"

	"github.com/cnk3x/xunlei/pkg/utils"
	"github.com/cnk3x/xunlei/pkg/vms/sys"
)

func newOptions(execOpts ...Option) options {
	var opts options
	for _, option := range execOpts {
		option(&opts)
	}
	// 等所有选项确定后再解析挂载目标，确保始终采用最终的根目录。
	for i := range opts.mounts {
		opts.mounts[i].Target = filepath.Join(opts.root, opts.mounts[i].Target)
	}
	return opts
}

func Root(root string) Option { return func(ro *options) { ro.root = root } }

func Run(run func(ctx context.Context) error) Option { return func(ro *options) { ro.run = run } }

// User 配置业务子进程身份，回调应通过命令封装或上下文凭据启动子进程。
func User[U, G utils.IntT | utils.UintT](uid U, gid G) Option {
	return func(ro *options) {
		ro.userErr = ValidateUser(uid, gid)
		if ro.userErr == nil {
			ro.uid, ro.gid = int(uid), int(gid)
		}
	}
}

// ValidateUser 在整数转换和环境准备前拒绝负数、溢出及系统保留编号。
// 编号零明确表示使用 root 用户或主组。
func ValidateUser[U, G utils.IntT | utils.UintT](uid U, gid G) error {
	if uid < 0 || uint64(uid) >= 1<<32-1 {
		return fmt.Errorf("UID 必须在 0 到 4294967294 之间")
	}
	if gid < 0 || uint64(gid) >= 1<<32-1 {
		return fmt.Errorf("GID 必须在 0 到 4294967294 之间")
	}
	return nil
}

func Wait(wait ...bool) Option {
	return func(ro *options) { ro.wait = len(wait) == 0 || wait[0] }
}

// After 在运行阶段结束且确认可安全清理后、准备资源回滚前调用。
// 准备失败时不会调用；返回值替换普通运行错误，关键恢复错误仍保留并合并。
func After(after func(ctx context.Context, runErr error) (err error)) Option {
	return func(ro *options) { ro.after = after }
}

func Before(before func(ctx context.Context) (undo func(), err error)) Option {
	return func(ro *options) { ro.before = before }
}

func Basic(ro *options) {
	flags := uintptr(syscall.MS_NOSUID | syscall.MS_NODEV | syscall.MS_NOEXEC)
	ro.mounts = append(ro.mounts,
		// 进程与系统信息目录禁止提权位、设备节点和程序执行。
		sys.MountOptions{Target: "proc", Source: "proc", Fstype: "proc", Flags: flags},
		// 设备目录需要使用设备节点，仅禁止提权位和程序执行。
		sys.MountOptions{Target: "dev", Source: "devtmpfs", Fstype: "devtmpfs", Flags: syscall.MS_NOSUID | syscall.MS_NOEXEC, Data: "mode=0755"},
		sys.MountOptions{Target: "sys", Source: "sysfs", Fstype: "sysfs", Flags: flags, Optional: true},
		// 临时目录只存放数据，粘滞位防止不同身份删除彼此的文件。
		sys.MountOptions{Target: "tmp", Source: "tmpfs", Fstype: "tmpfs", Flags: flags, Data: "mode=01777,size=100m", Optional: true},
	)
}

func Links(files ...string) Option {
	return func(ro *options) {
		for _, file := range files {
			mOpts := sys.LinkOptions{
				Source:   file,
				Optional: true,
				DirMode:  0777,
				Copy:     true,
			}
			ro.links = append(ro.links, mOpts)
		}
	}
}

func Binds(dirs ...string) Option {
	return func(ro *options) {
		for _, dir := range dirs {
			mOpts := sys.BindOptions{
				Source:   dir,
				Optional: true,
			}
			ro.binds = append(ro.binds, mOpts)
		}
	}
}

// ReadOnlyBinds 将系统运行库等目录设为只读，禁止隔离内写入传回源目录。
func ReadOnlyBinds(dirs ...string) Option {
	return func(ro *options) {
		for _, dir := range dirs {
			ro.binds = append(ro.binds, sys.BindOptions{Source: dir, Optional: true, ReadOnly: true})
		}
	}
}

func Symlink(source, target string) Option {
	return func(ro *options) {
		ro.symbols = append(ro.symbols, sys.LinkOptions{
			Source:   source,
			Target:   target,
			Optional: true,
			DirMode:  0777,
		})
	}
}
