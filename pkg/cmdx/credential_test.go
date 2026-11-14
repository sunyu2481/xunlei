package cmdx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
)

func Test子进程凭据独立且清空附加组(t *testing.T) {
	ctx := WithCredential(context.Background(), 60123, 60124)
	a, b := ProcessCredential(ctx), ProcessCredential(ctx)
	if a == b || a.Uid != 60123 || a.Gid != 60124 || a.NoSetGroups || len(a.Groups) != 0 {
		t.Fatalf("凭据未隔离或保留了附加组：%+v", a)
	}
	a.Uid = 0
	if b.Uid != 60123 || ProcessCredential(context.Background()) != nil {
		t.Fatal("凭据修改影响其他上下文")
	}
	// 显式设置为根身份也必须清空旧凭据及附加组。
	root := ProcessCredential(WithCredential(ctx, 0, 0))
	if root.Uid != 0 || root.Gid != 0 || root.NoSetGroups {
		t.Fatalf("根身份未显式配置：%+v", root)
	}
	for _, ids := range [][2]uint32{{0, 0}, {60123, 60124}} {
		c := &Cmd{cmd: exec.Command("占位程序")}
		cleanup, err := Credential(ids[0], ids[1])(c)
		if os.Geteuid() != 0 {
			if !errors.Is(err, os.ErrPermission) {
				t.Fatalf("普通用户设置凭据应返回权限错误：%v", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		cleanup()
		credential := c.SysProcAttr.Credential
		if credential == nil || credential.Uid != ids[0] || credential.Gid != ids[1] || credential.NoSetGroups || len(credential.Groups) != 0 {
			t.Fatalf("显式凭据选项未完整设置身份：%+v", credential)
		}
	}
}
