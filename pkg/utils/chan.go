package utils

import (
	"sync"
	"time"
)

// CSelect 等待数据或任一中断来源，并在返回前结束全部中断监听。
// 接收数据时返回其值和通道状态；中断来源可读或关闭时返回零值、false、true。
// 数据和中断同时就绪时，任一结果均可能被选择；nil 通道不触发完成。
func CSelect[T any](cSelect <-chan T, cBreaks ...<-chan struct{}) (v T, ok, breaked bool) {
	cBreaked := make(chan struct{})
	stop := sync.OnceFunc(func() { close(cBreaked) })
	var listeners sync.WaitGroup
	defer func() {
		stop()
		listeners.Wait()
	}()

	for _, c := range cBreaks {
		listeners.Go(func() {
			select {
			case <-cBreaked:
			case <-c:
				stop()
			}
		})
	}

	select {
	case <-cBreaked:
		breaked = true
	case v, ok = <-cSelect:
	}
	return
}

// SelectDo 等待一次选择，仅在数据通道可读或关闭时调用 f，并返回是否被中断。
// 调用 f 前已经回收全部中断监听，f 的异常按原样传播。
func SelectDo[T any](cSelect <-chan T, f func(T, bool), cBreaks ...<-chan struct{}) (breaked bool) {
	v, ok, breaked := CSelect(cSelect, cBreaks...)
	if !breaked {
		f(v, ok)
	}
	return breaked
}

// Sleep 等待指定时长或任一中断，并返回是否被中断。
func Sleep(d time.Duration, cBreaks ...<-chan struct{}) (breaked bool) {
	t := time.NewTimer(d)
	defer t.Stop()
	_, _, breaked = CSelect(t.C, cBreaks...)
	return
}

// After 异步等待 done 可读或关闭时调用 f；若先收到中断则结束等待，不调用 f。
func After[T any, F ft[T]](done <-chan T, f F, cBreaks ...<-chan struct{}) {
	go SelectDo(done, func(t T, ok bool) {
		af := any(f)
		if ft, ok := af.(func()); ok {
			ft()
			return
		}

		if ft, ok := af.(func() error); ok {
			_ = ft()
			return
		}

		if ft, ok := af.(func(T)); ok {
			ft(t)
			return
		}

		if ft, ok := af.(func(T) error); ok {
			_ = ft(t)
			return
		}
	}, cBreaks...)
}

type ft[T any] interface {
	~func() | ~func() error | ~func(T) | ~func(T) error
}

func BackExec(exec func(), onDone ...func()) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer Run(onDone...)
		exec()
	}()
	return done
}
