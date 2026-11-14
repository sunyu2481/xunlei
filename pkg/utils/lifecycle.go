package utils

import "errors"

// ErrUnsafeCleanup 表示仍有活动任务或根目录视角不可信，禁止继续依赖路径的清理。
var ErrUnsafeCleanup = errors.New("当前状态不允许安全清理文件系统")
