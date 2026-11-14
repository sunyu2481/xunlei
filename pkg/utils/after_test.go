package utils

import (
	"errors"
	"testing"
	"testing/synctest"
)

func Test异步回调支持命名与匿名函数(t *testing.T) {
	type plainCallback func()
	type errorCallback func() error
	type valueCallback func(int)
	type valueErrorCallback func(int) error

	wantErr := errors.New("模拟异步回调返回错误")
	tests := []struct {
		name      string
		after     func(<-chan int, func(int), ...<-chan struct{})
		withValue bool
	}{
		{"匿名无参数", func(done <-chan int, record func(int), interrupts ...<-chan struct{}) {
			After(done, func() { record(0) }, interrupts...)
		}, false},
		{"命名无参数", func(done <-chan int, record func(int), interrupts ...<-chan struct{}) {
			After(done, plainCallback(func() { record(0) }), interrupts...)
		}, false},
		{"匿名无参数返回错误", func(done <-chan int, record func(int), interrupts ...<-chan struct{}) {
			After(done, func() error { record(0); return wantErr }, interrupts...)
		}, false},
		{"命名无参数返回错误", func(done <-chan int, record func(int), interrupts ...<-chan struct{}) {
			After(done, errorCallback(func() error { record(0); return wantErr }), interrupts...)
		}, false},
		{"匿名带参数", func(done <-chan int, record func(int), interrupts ...<-chan struct{}) {
			After(done, record, interrupts...)
		}, true},
		{"命名带参数", func(done <-chan int, record func(int), interrupts ...<-chan struct{}) {
			After(done, valueCallback(record), interrupts...)
		}, true},
		{"匿名带参数返回错误", func(done <-chan int, record func(int), interrupts ...<-chan struct{}) {
			After(done, func(v int) error { record(v); return wantErr }, interrupts...)
		}, true},
		{"命名带参数返回错误", func(done <-chan int, record func(int), interrupts ...<-chan struct{}) {
			After(done, valueErrorCallback(func(v int) error { record(v); return wantErr }), interrupts...)
		}, true},
	}
	for _, tc := range tests {
		for _, scenario := range []string{"数据已就绪", "等待数据", "关闭通道", "中断"} {
			t.Run(tc.name+"/"+scenario, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					data := make(chan int, 2)
					interrupt := make(chan struct{})
					if scenario == "数据已就绪" {
						data <- 7
						data <- 8
					}
					var values []int
					tc.after(data, func(v int) { values = append(values, v) }, interrupt, nil)
					if scenario != "数据已就绪" {
						synctest.Wait()
						if len(values) != 0 {
							t.Fatal("数据完成前提前执行回调")
						}
						switch scenario {
						case "等待数据":
							data <- 7
							data <- 8
						case "关闭通道":
							close(data)
						case "中断":
							close(interrupt)
						}
					}
					synctest.Wait()
					if scenario == "中断" {
						if len(values) != 0 {
							t.Fatalf("中断后仍执行回调：%v", values)
						}
						return
					}
					want := 0
					if tc.withValue && scenario != "关闭通道" {
						want = 7
					}
					if len(values) != 1 || values[0] != want {
						t.Fatalf("回调结果=%v，期望仅一次且值为%d", values, want)
					}
					if scenario != "关闭通道" && len(data) != 1 {
						t.Fatal("异步等待消费了多个值")
					}
				})
			})
		}
	}
}

func Test异步回调保留空接口值(t *testing.T) {
	type valueCallback func(any)
	for _, named := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			data := make(chan any)
			close(data)
			calls := 0
			callback := func(value any) {
				calls++
				if value != nil {
					t.Errorf("关闭通道的值=%v，期望空值", value)
				}
			}
			if named {
				After(data, valueCallback(callback))
			} else {
				After(data, callback)
			}
			synctest.Wait()
			if calls != 1 {
				t.Fatalf("命名类型=%t，回调次数=%d，期望1", named, calls)
			}
		})
	}
}
