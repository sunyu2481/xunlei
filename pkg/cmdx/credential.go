package cmdx

import (
	"context"
	"syscall"
)

type credentialKey struct{}

// WithCredential 指定此上下文中业务子进程的身份，不改变父进程身份。
func WithCredential(ctx context.Context, uid, gid uint32) context.Context {
	return context.WithValue(ctx, credentialKey{}, syscall.Credential{Uid: uid, Gid: gid})
}

// ProcessCredential 返回独立的凭据副本；空附加组会在启动时清除继承的组。
func ProcessCredential(ctx context.Context) *syscall.Credential {
	credential, ok := ctx.Value(credentialKey{}).(syscall.Credential)
	if !ok {
		return nil
	}
	return &credential
}
