package netlimit

import (
	"context"
	"errors"
	"math"
	"slices"
	"testing"
)

const linkJSON = `[{"ifname":"eth0","mtu":1500,"linkinfo":{"info_kind":"veth"}}]`

func TestApplyDisabled(t *testing.T) {
	undo, err := apply(context.Background(), 0, "", func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("不限速时不应调用系统工具")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := undo(); err != nil {
		t.Fatal(err)
	}
}

func TestApplyRejectUnsafeInterface(t *testing.T) {
	for _, info := range []string{
		`[{"ifname":"eth0","linkinfo":{"info_kind":"bond"}}]`,
		`[{"ifname":"eth0"}]`,
		`[{"ifname":"other","linkinfo":{"info_kind":"veth"}}]`,
		`[]`,
		`invalid`,
	} {
		t.Run(info, func(t *testing.T) {
			undo, err := apply(context.Background(), 500, "eth0", func(_ context.Context, name string, _ ...string) ([]byte, error) {
				if name != "ip" {
					t.Fatal("不符合要求的网卡不应修改队列")
				}
				return []byte(info), nil
			})
			if err == nil || undo != nil {
				t.Fatal("应拒绝不符合要求的网卡信息")
			}
		})
	}
}

func TestApplyRejectInvalidLimit(t *testing.T) {
	for _, limit := range []uint64{math.MaxUint64, 500} {
		if _, err := apply(context.Background(), limit, "", func(context.Context, string, ...string) ([]byte, error) {
			t.Fatal("错误配置不应调用系统工具")
			return nil, nil
		}); err == nil {
			t.Fatal("应拒绝溢出数值或空网卡")
		}
	}
}

func TestApplyRateAndCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deleted := false
	undo, err := apply(ctx, 500, "eth0", func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			t.Fatal("清理不应使用已取消的启动上下文")
		}
		if name == "ip" {
			return []byte(linkJSON), nil
		}
		switch {
		case len(args) > 1 && args[1] == "add":
			for _, pair := range [][2]string{{"rate", "4096000bit"}, {"burst", "16384"}, {"handle", handle}} {
				index := slices.Index(args, pair[0])
				if index < 0 || index+1 >= len(args) || args[index+1] != pair[1] {
					t.Fatalf("限速参数错误：%v", args)
				}
			}
		case args[0] == "-j":
			return []byte(`[{"kind":"tbf","handle":"f1a0:","root":true}]`), nil
		case len(args) > 1 && args[1] == "del":
			deleted = true
		default:
			t.Fatalf("出现非预期命令：%s %v", name, args)
		}
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := undo(); err != nil {
		t.Fatal(err)
	}
	if !deleted {
		t.Fatal("退出时没有移除限速队列")
	}
}

func TestCleanupPreservesOtherQueues(t *testing.T) {
	for _, queue := range []string{
		`[{"kind":"tbf","handle":"1234:","root":true}]`,
		`[{"kind":"htb","handle":"f1a0:","root":true}]`,
		`[{"kind":"tbf","handle":"f1a0:","parent":"1:1"}]`,
		`[]`,
	} {
		t.Run(queue, func(t *testing.T) {
			undo, err := apply(context.Background(), 500, "eth0", func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name == "ip" {
					return []byte(linkJSON), nil
				}
				if args[0] == "-j" {
					return []byte(queue), nil
				}
				if args[1] != "add" {
					t.Fatal("不应删除其他队列")
				}
				return nil, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := undo(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestApplyCommandFailure(t *testing.T) {
	failure := errors.New("权限不足或队列已存在")
	for _, failedCommand := range []string{"ip", "tc"} {
		t.Run(failedCommand, func(t *testing.T) {
			undo, err := apply(context.Background(), 500, "eth0", func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name == failedCommand {
					return nil, failure
				}
				return []byte(linkJSON), nil
			})
			if !errors.Is(err, failure) || undo != nil {
				t.Fatalf("应返回系统命令错误：%v", err)
			}
		})
	}
}
