package log

import (
	"cmp"
	"log/slog"
	"strings"
	"sync"
)

// levelState 由同源处理器共享，同时保留外部动态等级的读取能力。
type levelState struct {
	mu    sync.RWMutex
	value slog.Leveler
}

func (s *levelState) Level() slog.Level {
	s.mu.RLock()
	value := s.value
	s.mu.RUnlock()
	return value.Level()
}

func (s *levelState) set(level string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value = LevelFromString(level, s.value.Level())
}

// Level 设置记录器及其全部同源派生记录器的共享等级，替代原有等级来源。
func Level(logger *slog.Logger, level string) {
	if h, ok := logger.Handler().(*handler); ok {
		h.level.set(level)
	}
}

// LevelFromString 忽略大小写解析调试、信息、警告和错误等级，未知值返回 def。
// 支持 debug、info/information、warn/warning、error/err。
func LevelFromString(level string, def slog.Level) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "info", "information":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error", "err":
		return slog.LevelError
	default:
		return def
	}
}

// ForDefault 设置默认日志记录器，未知等级按信息等级处理。
// 仅当 addSource 中至少一个值为真时记录调用位置；省略时关闭。
func ForDefault(level string, addSource ...bool) {
	l := LevelFromString(level, slog.LevelInfo)
	lOpt := &Options{Level: l, AddSource: cmp.Or(addSource...)}
	slog.SetDefault(New(lOpt))
}
