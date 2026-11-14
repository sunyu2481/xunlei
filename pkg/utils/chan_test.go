package utils

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func Test中断选择并发完成(t *testing.T) {
	const scenarioKey = "XUNLEI_TEST_CHANNEL_SCENARIO"
	if scenario := os.Getenv(scenarioKey); scenario != "" {
		for range 500 {
			data := make(chan int, 1)
			if strings.HasSuffix(scenario, "数据竞争") {
				data <- 7
			}
			interrupts := make([]<-chan struct{}, 8)
			for i := range interrupts {
				interrupt := make(chan struct{})
				close(interrupt)
				interrupts[i] = interrupt
			}
			if strings.HasPrefix(scenario, "选择回调") {
				calls := 0
				broken := SelectDo(data, func(v int, ok bool) {
					calls++
					if v != 7 || !ok {
						t.Fatalf("回调结果错误：值=%d，通道开放=%t", v, ok)
					}
				}, interrupts...)
				if broken && calls != 0 || !broken && calls != 1 {
					t.Fatalf("回调次数与完成原因不符：中断=%t，次数=%d", broken, calls)
				}
			} else {
				v, ok, broken := CSelect(data, interrupts...)
				if broken && (v != 0 || ok) || !broken && (v != 7 || !ok) {
					t.Fatalf("选择结果与完成原因不符：值=%d，通道开放=%t，中断=%t", v, ok, broken)
				}
			}
		}
		return
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"选择同时中断", "选择回调同时中断", "选择数据竞争", "选择回调数据竞争"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^Test中断选择并发完成$", "-test.timeout=12s")
			cmd.Env = append(os.Environ(), scenarioKey+"="+scenario)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("隔离进程未正常完成：%v\n%s", err, output)
			}
		})
	}
}

func Test中断选择正常完成并回收监听(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		interrupt := make(chan struct{})
		for range 100 {
			data := make(chan int, 1)
			data <- 7
			v, ok, broken := CSelect(data, interrupt, nil)
			if v != 7 || !ok || broken {
				t.Fatalf("正常数据选择错误：值=%d，通道开放=%t，中断=%t", v, ok, broken)
			}
			close(data)
			calls := 0
			broken = SelectDo(data, func(v int, ok bool) {
				calls++
				if v != 0 || ok {
					t.Fatalf("关闭通道的回调结果错误：值=%d，通道开放=%t", v, ok)
				}
			}, interrupt, nil)
			if broken || calls != 1 {
				t.Fatalf("关闭数据通道未正常回调：中断=%t，次数=%d", broken, calls)
			}
		}
		// 中断通道始终不关闭，测试结束时仍有监听协程会由 synctest 判为死锁。
	})
}

func Test选择回调异常仍回收监听(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		data := make(chan int)
		close(data)
		defer func() {
			if recovered := recover(); recovered != "模拟回调异常" {
				t.Fatalf("回调异常未按原样传播：%v", recovered)
			}
		}()
		SelectDo(data, func(int, bool) { panic("模拟回调异常") }, make(chan struct{}))
	})
}

func Test睡眠正常结束与多路中断(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		if Sleep(time.Second, make(chan struct{})) || time.Since(start) != time.Second {
			t.Fatal("无中断的睡眠未按计时器结束")
		}
		interrupt := make(chan struct{})
		go func() {
			time.Sleep(time.Second)
			close(interrupt)
		}()
		start = time.Now()
		if !Sleep(time.Hour, interrupt, interrupt, interrupt) || time.Since(start) != time.Second {
			t.Fatal("多个相同中断来源未及时结束睡眠")
		}
	})
}

func Test异步完成与中断回收(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		data := make(chan int, 1)
		data <- 7
		result := make(chan int, 1)
		After(data, func(v int) { result <- v }, make(chan struct{}))
		if v := <-result; v != 7 {
			t.Fatalf("异步回调值=%d，期望7", v)
		}
		interrupt := make(chan struct{})
		close(interrupt)
		After(make(chan int), func(v int) { result <- v }, interrupt, interrupt, interrupt)
		synctest.Wait()
		select {
		case <-result:
			t.Fatal("中断后仍调用异步回调")
		default:
		}
	})
}
