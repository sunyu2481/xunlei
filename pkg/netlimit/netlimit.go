// 为隔离容器的网卡设置出站限速。
package netlimit

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const handle = "f1a0:"

type command func(context.Context, string, ...string) ([]byte, error)

// Apply 设置网卡的总出站速度，单位为 KiB/s；零表示不限速。
// 只允许操作容器的虚拟网卡，返回的清理函数只删除本次设置的队列。
func Apply(ctx context.Context, limit uint64, iface string) (func() error, error) {
	return apply(ctx, limit, iface, run)
}

func apply(ctx context.Context, limit uint64, iface string, cmd command) (func() error, error) {
	noop := func() error { return nil }
	if limit == 0 {
		return noop, nil
	}
	if limit > math.MaxUint64/8192 {
		return nil, fmt.Errorf("上传限速数值过大：%d KiB/s", limit)
	}
	if iface == "" {
		return nil, fmt.Errorf("启用上传限速时必须指定网卡")
	}

	output, err := cmd(ctx, "ip", "-j", "-d", "link", "show", "dev", iface)
	if err != nil {
		return nil, fmt.Errorf("检查上传限速网卡失败：%w", err)
	}
	var links []struct {
		Name string `json:"ifname"`
		MTU  uint64 `json:"mtu"`
		Info struct {
			Kind string `json:"info_kind"`
		} `json:"linkinfo"`
	}
	if err := json.Unmarshal(output, &links); err != nil {
		return nil, fmt.Errorf("解析网卡信息失败：%w", err)
	}
	if len(links) != 1 || links[0].Name != iface || links[0].Info.Kind != "veth" {
		return nil, fmt.Errorf("上传限速只支持容器的 veth 网卡，%s 不符合要求；请使用桥接网络", iface)
	}

	// 允许约十毫秒的突发，至少能容纳一个完整数据包。
	burst := max(uint64(16*1024), links[0].MTU, limit*1024/100)
	_, err = cmd(ctx, "tc", "qdisc", "add", "dev", iface, "root", "handle", handle,
		"tbf", "rate", strconv.FormatUint(limit*8192, 10)+"bit",
		"burst", strconv.FormatUint(burst, 10), "latency", "200ms")
	if err != nil {
		return nil, fmt.Errorf("设置上传限速失败，请检查 NET_ADMIN 权限、内核队列支持及已有队列：%w", err)
	}

	return func() error {
		// 服务退出时原上下文通常已取消，清理使用独立的短超时。
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		output, err := cmd(ctx, "tc", "-j", "qdisc", "show", "dev", iface)
		if err != nil {
			return fmt.Errorf("检查上传限速队列失败：%w", err)
		}
		var queues []struct {
			Kind   string `json:"kind"`
			Handle string `json:"handle"`
			Root   bool   `json:"root"`
		}
		if err := json.Unmarshal(output, &queues); err != nil {
			return fmt.Errorf("解析上传限速队列失败：%w", err)
		}
		for _, queue := range queues {
			if queue.Root && queue.Kind == "tbf" && queue.Handle == handle {
				_, err := cmd(ctx, "tc", "qdisc", "del", "dev", iface, "root", "handle", handle)
				return err
			}
		}
		return nil
	}, nil
}

func run(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("执行 %s 失败：%w；%s", name, err, strings.TrimSpace(string(output)))
	}
	return output, nil
}
