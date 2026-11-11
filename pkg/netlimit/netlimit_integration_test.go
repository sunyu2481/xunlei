package netlimit

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"
)

// 仅在专用测试容器中显式指定接收端时修改队列。
func TestTrafficLimit(t *testing.T) {
	target := os.Getenv("XL_NETLIMIT_TEST_TARGET")
	if target == "" {
		t.Skip("需在独立测试容器中设置 XL_NETLIMIT_TEST_TARGET")
	}
	client := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{Proxy: nil}}
	t.Run("已有队列保护与清理", func(t *testing.T) {
		undo, err := Apply(context.Background(), 128, "eth0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := undo(); err != nil {
				t.Error(err)
			}
		})
		if _, err := Apply(context.Background(), 64, "eth0"); err == nil {
			t.Fatal("不应覆盖已有根队列")
		}
		if err := undo(); err != nil {
			t.Fatal(err)
		}
		output, err := run(context.Background(), "tc", "-j", "qdisc", "show", "dev", "eth0")
		if err != nil || bytes.Contains(output, []byte(handle)) {
			t.Fatalf("限速队列未清理：%s，错误：%v", output, err)
		}
	})
	for _, target := range []string{target, os.Getenv("XL_NETLIMIT_TEST_TARGET_V6")} {
		if target == "" {
			continue
		}
		t.Run(target, func(t *testing.T) {
			t.Run("流式上传", func(t *testing.T) {
				undo, err := Apply(context.Background(), 128, "eth0")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := undo(); err != nil {
						t.Error(err)
					}
				})
				payload := bytes.Repeat([]byte{1}, 512*1024)
				start := time.Now()
				resp, err := client.Post("http://"+target+"/upload", "application/octet-stream", bytes.NewReader(payload))
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				body, err := io.ReadAll(resp.Body)
				if err != nil || string(body) != strconv.Itoa(len(payload)) {
					t.Fatalf("接收字节不匹配：%s，错误：%v", body, err)
				}
				elapsed := time.Since(start)
				if elapsed < 3*time.Second || elapsed > 15*time.Second {
					t.Fatalf("512 KiB 上传耗时应接近四秒，实际为 %s", elapsed)
				}
				t.Logf("512 KiB 上传耗时 %s，平均速度 %.1f KiB/s", elapsed, 512/elapsed.Seconds())
			})
			t.Run("数据报上传", func(t *testing.T) {
				undo, err := Apply(context.Background(), 128, "eth0")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := undo(); err != nil {
						t.Error(err)
					}
				})
				request := func(path string) []byte {
					resp, err := client.Get("http://" + target + path)
					if err != nil {
						t.Fatal(err)
					}
					defer resp.Body.Close()
					body, err := io.ReadAll(resp.Body)
					if err != nil {
						t.Fatal(err)
					}
					return body
				}
				request("/reset")
				host, _, err := net.SplitHostPort(target)
				if err != nil {
					t.Fatal(err)
				}
				conn, err := net.Dial("udp", net.JoinHostPort(host, "9877"))
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				ticker := time.NewTicker(time.Millisecond)
				defer ticker.Stop()
				deadline := time.Now().Add(2 * time.Second)
				payload := make([]byte, 1000)
				for time.Now().Before(deadline) {
					<-ticker.C
					if _, err := conn.Write(payload); err != nil {
						t.Fatal(err)
					}
				}
				// 等待令牌桶中剩余的数据报发出，再读取接收计数。
				time.Sleep(300 * time.Millisecond)
				count, err := strconv.Atoi(string(request("/count")))
				if err != nil {
					t.Fatal(err)
				}
				if count < 128*1024 || count > 360*1024 {
					t.Fatalf("两秒过量发送后的接收字节应受到限制，实际为 %d", count)
				}
				t.Logf("两秒过量发送后接收 %d 字节", count)
			})
		})
	}
}
