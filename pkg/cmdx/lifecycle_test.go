package cmdx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cnk3x/xunlei/pkg/utils"
)

func Test逐行输出长行与关闭同步(t *testing.T) {
	for name, factory := range map[string]func(func(string)) io.WriteCloser{"命令": LineWriter, "工具": utils.LineWriter} {
		t.Run(name, func(t *testing.T) {
			var got strings.Builder
			w := factory(func(s string) { got.WriteString(s) })
			text := strings.Repeat("长", 50000) + "\n尾行\n"
			done := make(chan error, 1)
			go func() { _, err := io.WriteString(w, text); done <- errors.Join(err, w.Close()) }()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				w.Close()
				t.Fatal("长行导致输出阻塞")
			}
			if got.String() != strings.ReplaceAll(text, "\n", "") {
				t.Fatal("长行或后续内容丢失")
			}
			entered, release := make(chan struct{}), make(chan struct{})
			defer close(release)
			w = factory(func(string) { close(entered); <-release })
			go io.WriteString(w, "等待\n")
			<-entered
			closed := make(chan struct{})
			go func() { w.Close(); close(closed) }()
			select {
			case <-closed:
				t.Fatal("关闭未等待消费回调")
			case <-time.After(30 * time.Millisecond):
			}
		})
	}
}

func testProcessGone(t *testing.T, pid int) {
	t.Helper()
	// 容器的第一号进程可能延迟回收孤儿，只要已退出且不再持有资源即可。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if os.IsNotExist(err) {
			return
		}
		if err == nil {
			_, tail, ok := strings.Cut(string(b), ") ")
			if ok && strings.HasPrefix(tail, "Z ") {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("进程 %d 仍在运行", pid)
}

func Test启动回调失败先回收再退出回调(t *testing.T) {
	want := errors.New("模拟启动回调失败")
	var child *Cmd
	err := Run(context.Background(), "/bin/sh", Args("-c", "sleep 30"),
		OnStarted(func(c *Cmd) error { child = c; return want }),
		OnExit(func(c *Cmd) error {
			if c.ProcessState == nil {
				t.Error("退出回调前未回收子进程")
			}
			return nil
		}))
	if child == nil {
		t.Fatal("未启动测试子进程")
	}
	defer syscall.Kill(-child.Process.Pid, syscall.SIGKILL)
	if !errors.Is(err, want) {
		t.Fatalf("启动回调错误丢失：%v", err)
	}
	testProcessGone(t, child.Process.Pid)
}

func Test父进程先退出后的取消与等待上限(t *testing.T) {
	for _, cancelAfterExit := range []bool{true, false} {
		t.Run(strconv.FormatBool(cancelAfterExit), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			childPID := make(chan int, 1)
			started := make(chan int, 1)
			done := make(chan error, 1)
			go func() {
				done <- Run(ctx, "/bin/sh", Args("-c", "sleep 30 & echo $!"),
					OnStarted(func(c *Cmd) error { started <- c.Process.Pid; return nil }),
					LineOut(func(line string) { pid, _ := strconv.Atoi(line); childPID <- pid }))
			}()
			pid := <-started
			defer syscall.Kill(-pid, syscall.SIGKILL)
			var descendant int
			select {
			case descendant = <-childPID:
			case <-time.After(2 * time.Second):
				t.Fatal("未收到后代进程编号")
			}
			testProcessGone(t, pid)
			if cancelAfterExit {
				cancel()
			}
			select {
			case <-done:
			case <-time.After(4 * time.Second):
				t.Fatal("继承输出管道导致等待无上限")
			}
			testProcessGone(t, descendant)
		})
	}
}

func Test长输出可以取消并回收(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, "/bin/sh", Args("-c", "head -c 150000 /dev/zero; echo; sleep 30"), LineOut(func(string) {
			select {
			case entered <- struct{}{}:
			default:
			}
		}))
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("长输出没有被消费")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("取消应可识别")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("取消后没有结束")
	}
}
