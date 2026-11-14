package cmdx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

type Cmd struct {
	*cmd
	preStart  func(*Cmd) error
	onStarted func(*Cmd) error
	onExit    func(*Cmd) error
}

type cmd = exec.Cmd

// Run 管理整个进程组；主进程退出后最多等待两秒输出，随后清理残留后代。
func Run(ctx context.Context, name string, options ...Option) (err error) {
	c := &Cmd{cmd: exec.CommandContext(ctx, name)}
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Credential: ProcessCredential(ctx)}
	c.WaitDelay = 2 * time.Second
	c.Cancel = sync.OnceValue(func() error {
		err := syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	})
	clean, err := Options(options...)(c)
	if err != nil {
		return fmt.Errorf("cmdx options: %w", err)
	}
	clean = sync.OnceFunc(clean)
	defer clean()
	if c.preStart != nil {
		if err = c.preStart(c); err != nil {
			return fmt.Errorf("cmdx pre start: %w", err)
		}
	}
	if err = c.Start(); err != nil {
		err = fmt.Errorf("cmdx start: %w", err)
	} else {
		slog.DebugContext(ctx, "cmdx started", "command", c.String(), "dir", c.Dir, "pid", c.Process.Pid)
		// 标准库的取消监控在主进程退出后结束，此监控持续到输出回收完成。
		finished, watched := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(watched)
			select {
			case <-ctx.Done():
				c.Cancel()
			case <-finished:
			}
		}()
		if c.onStarted != nil {
			if e := c.onStarted(c); e != nil {
				err = fmt.Errorf("cmdx started: %w", e)
				c.Cancel()
			}
		}
		waitErr := c.Wait()
		killErr := c.Cancel()
		if errors.Is(killErr, os.ErrProcessDone) {
			killErr = nil
		}
		close(finished)
		<-watched
		err = errors.Join(err, waitErr, killErr, ctx.Err())
	}
	// 输出消费完成之后才通知退出，调用方可以安全关闭日志或清理文件。
	clean()
	if c.onExit != nil {
		err = errors.Join(err, c.onExit(c))
	}
	return err
}

// GetSignal 判断 cmd.Wait() 返回的错误是否为信号中断，返回-1代表不是。
func GetSignal(err error) syscall.Signal {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return -1
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return -1
	}
	return ws.Signal()
}
