package utils

import (
	"cmp"
	"io"
	"iter"
	"log"
)

// CompactUniq 保留非零元素首次出现的顺序；inplace 中任一值为真时复用原切片。
func CompactUniq[Slice ~[]T, T comparable](s Slice, inplace ...bool) Slice {
	result := s
	if !cmp.Or(inplace...) {
		result = make(Slice, len(s))
	}

	seen := make(map[T]struct{}, len(s))
	var zero T
	var x int
	for _, v := range s {
		if v == zero {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		result[x], x = v, x+1
	}
	return result[:x]
}

// Conv 依次转换元素；inplace 中任一值为真时覆盖原切片，否则分配新切片。
func Conv[Slice ~[]T, T any](s Slice, cleanFn func(T) T, inplace ...bool) (result Slice) {
	result = s
	if !cmp.Or(inplace...) {
		result = make(Slice, len(s))
	}
	for i, it := range s {
		result[i] = cleanFn(it)
	}
	return
}

func Replace[Slice ~[]T, T any](s Slice, replaceFn func(T) (T, error)) (result Slice, err error) {
	result = make(Slice, len(s))
	for i, it := range s {
		if result[i], err = replaceFn(it); err != nil {
			result = result[:0]
			return
		}
	}
	return
}

// Map 按顺序将元素及其索引转换到新切片。
func Map[T, R any](s []T, conv func(T, int) R) []R {
	result := make([]R, len(s))
	for i, v := range s {
		result[i] = conv(v, i)
	}
	return result
}

// Reduce 从 init 开始按切片顺序累积结果，空切片返回 init。
func Reduce[T, R any](s []T, walk func(agg R, item T, i int) R, init R) R {
	for i, v := range s {
		init = walk(init, v, i)
	}
	return init
}

// Flat 按原顺序将各子切片拼接到新切片。
func Flat[T any](s [][]T) []T {
	l := Reduce(s, func(agg int, item []T, _ int) int { return agg + len(item) }, 0)
	return Reduce(s, func(agg []T, item []T, _ int) []T { return append(agg, item...) }, make([]T, 0, l))
}

// ReduceSeq2 按迭代顺序累积结果，将迭代器提供的整数键作为索引传给 walk。
func ReduceSeq2[T, R any](seq iter.Seq2[int, T], walk func(agg R, item T, index int) R, init R) R {
	for i, item := range seq {
		init = walk(init, item, i)
	}
	return init
}

// ReduceSeq 从 init 开始按迭代顺序累积结果，空序列返回 init。
func ReduceSeq[T, R any](seq iter.Seq[T], walk func(agg R, item T) R, init R) R {
	for item := range seq {
		init = walk(init, item)
	}
	return init
}

func LogStd(w io.Writer) *log.Logger { return log.New(w, "", 0) }

func Array[T any](s ...T) []T { return s }
