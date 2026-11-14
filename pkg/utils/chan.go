package utils

import (
	"reflect"
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
// 支持约束内的命名或匿名函数；忽略 f 返回的错误。
func After[T any, F ft[T]](done <-chan T, f F, cBreaks ...<-chan struct{}) {
	value := reflect.ValueOf(f)
	var callback func(T, bool)
	switch {
	case value.CanConvert(reflect.TypeFor[func()]()):
		apply := value.Convert(reflect.TypeFor[func()]()).Interface().(func())
		callback = func(T, bool) { apply() }
	case value.CanConvert(reflect.TypeFor[func() error]()):
		apply := value.Convert(reflect.TypeFor[func() error]()).Interface().(func() error)
		callback = func(T, bool) { _ = apply() }
	case value.CanConvert(reflect.TypeFor[func(T)]()):
		apply := value.Convert(reflect.TypeFor[func(T)]()).Interface().(func(T))
		callback = func(t T, _ bool) { apply(t) }
	case value.CanConvert(reflect.TypeFor[func(T) error]()):
		apply := value.Convert(reflect.TypeFor[func(T) error]()).Interface().(func(T) error)
		callback = func(t T, _ bool) { _ = apply(t) }
	default:
		panic("异步回调函数类型不满足已声明的约束")
	}
	go SelectDo(done, callback, cBreaks...)
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
