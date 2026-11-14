package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func Test业务错误字段数值与类型(t *testing.T) {
	for _, tt := range []struct {
		name, body, kind string
		code             int
	}{
		{"科学计数非零码", `{"error_code":1e2,"error":"success"}`, "upstream", 100},
		{"小数形式非零码", `{"errorCode":100.0,"error":"ok"}`, "upstream", 100},
		{"空白字符串非零码", `{"errorCode":" 9 ","error":"success"}`, "captcha_required", 9},
		{"科学计数登录错误", `{"error_code":"4.122e3"}`, "login_required", 4122},
		{"小数形式零码", `{"errorCode":0.0,"errorDesc":"成功"}`, "", 0},
		{"科学计数零码", `{"error_code":0e2,"errorDesc":"成功"}`, "", 0},
		{"空白字符串零码", `{"errorCode":" 0 ","errorDesc":"成功"}`, "", 0},
		{"布尔真错误标识", `{"error":true,"error_code":0}`, "upstream", 0},
		{"布尔假也不是支持的标识", `{"error":false,"error_code":0}`, "upstream", 0},
		{"对象错误标识", `{"error":{},"error_code":0}`, "upstream", 0},
		{"数组错误标识", `{"error":[],"error_code":0}`, "upstream", 0},
		{"空值错误标识", `{"error":null,"error_code":0}`, "upstream", 0},
		{"小数零错误标识", `{"error":0.0,"errorDesc":"成功"}`, "", 0},
		{"错误码含小数部分", `{"error_code":0.1,"error":"success"}`, "upstream", 0},
		{"错误码上溢", `{"error_code":9223372036854775808,"error":"success"}`, "upstream", 0},
		{"错误码下溢", `{"error_code":-9223372036854775809,"error":"success"}`, "upstream", 0},
		{"错误码无限值", `{"error_code":"Inf","error":"success"}`, "upstream", 0},
		{"错误码非数字", `{"error_code":"NaN","error":"success"}`, "upstream", 0},
		{"极大指数", `{"error_code":1e999999999,"error":"success"}`, "upstream", 0},
		{"极小非零数", `{"error_code":1e-999999999,"error":"success"}`, "upstream", 0},
		{"错误码为空值", `{"error_code":null,"error":"success"}`, "upstream", 0},
		{"错误码为空字符串", `{"error_code":"","error":"success"}`, "upstream", 0},
		{"错误码为布尔值", `{"error_code":false,"error":"success"}`, "upstream", 0},
		{"错误码为对象", `{"error_code":{},"error":"success"}`, "upstream", 0},
		{"错误码为数组", `{"error_code":[],"error":"success"}`, "upstream", 0},
		{"另一错误码无效不能被零掩盖", `{"error_code":0,"errorCode":[],"error":"success"}`, "upstream", 0},
		{"另一错误码无效不能被非零掩盖", `{"error_code":9,"errorCode":[],"error":"success"}`, "upstream", 0},
		{"二进制精度外非零码", `{"error_code":9007199254740993.0,"error":"success"}`, "upstream", 9007199254740993},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var d object
			dec := json.NewDecoder(strings.NewReader(tt.body))
			dec.UseNumber()
			if err := dec.Decode(&d); err != nil {
				t.Fatal(err)
			}
			err := businessError(d)
			if tt.kind == "" {
				if err != nil {
					t.Fatalf("合法零值应成功：%v", err)
				}
				return
			}
			wantKind(t, err, tt.kind)
			if got := err.(*Error).RemoteCode; got != tt.code {
				t.Fatalf("错误码应为 %d，实际 %d", tt.code, got)
			}
		})
	}
}

func Test未授权状态保留业务分类(t *testing.T) {
	for _, tt := range []struct{ name, body, kind string }{
		{"空体", "", "login_required"},
		{"网页", "<html>服务拒绝访问</html>", "login_required"},
		{"空值", "null", "login_required"},
		{"过大响应", strings.Repeat(" ", 8<<20+1), "login_required"},
		{"未知错误", `{"error":"unrecognized"}`, "login_required"},
		{"未知错误码", `{"error_code":123}`, "login_required"},
		{"无效错误字段", `{"error_code":true}`, "login_required"},
		{"零码", `{"error_code":0}`, "login_required"},
		{"令牌失效", `{"error":"invalid_token"}`, "login_required"},
		{"人机验证", `{"error_code":9}`, "captcha_required"},
		{"短信验证", `{"error":"review_panel"}`, "review_required"},
		{"短信过期", `{"error_code":13}`, "review_expired"},
		{"设备错误不能覆盖认证失效", `{"error_code":2}`, "login_required"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, tt.body)
			})
			_, err := c.raw(context.Background(), http.MethodGet, driveURL+"/tasks", nil, nil, c.headers(true))
			wantKind(t, err, tt.kind)
		})
	}
}

func Test未授权响应只续期一次(t *testing.T) {
	for _, body := range []string{"", "<html>未授权</html>", `{"error":"unrecognized"}`, `{"error":"invalid_token"}`} {
		for _, stillUnauthorized := range []bool{false, true} {
			t.Run(fmt.Sprintf("响应=%s/仍失效=%v", body, stillUnauthorized), func(t *testing.T) {
				calls, refreshes := 0, 0
				c := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/v1/auth/token":
						refreshes++
						respond(w, object{"access_token": "续期令牌", "refresh_token": "续期刷新令牌"})
					case "/drive/v1/tasks":
						calls++
						if calls == 1 || stillUnauthorized {
							w.WriteHeader(http.StatusUnauthorized)
							fmt.Fprint(w, body)
							return
						}
						if r.Header.Get("Authorization") != "Bearer 续期令牌" {
							t.Fatal("重试没有使用续期令牌")
						}
						respond(w, object{"tasks": []any{object{"name": "设备名称", "params": object{"target": "设备一"}}}})
					default:
						t.Fatalf("意外请求：%s", r.URL.Path)
					}
				})
				devices, err := c.Devices(context.Background())
				if stillUnauthorized {
					wantKind(t, err, "login_required")
				} else if err != nil || len(devices) != 1 {
					t.Errorf("续期后未返回设备：%v，%v", devices, err)
				}
				if calls != 2 || refreshes != 1 {
					t.Fatalf("应只续期并重试一次，业务请求=%d，续期=%d", calls, refreshes)
				}
			})
		}
	}
}

type responseReadFunc func([]byte) (int, error)

func (f responseReadFunc) Read(p []byte) (int, error) { return f(p) }

func Test请求失败保留取消原因(t *testing.T) {
	for _, sms := range []bool{false, true} {
		for _, reading := range []bool{false, true} {
			for _, cause := range []string{"取消", "截止", "网络故障"} {
				t.Run(fmt.Sprintf("短信=%v/读取=%v/%s", sms, reading, cause), func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					if cause == "截止" {
						var stop context.CancelFunc
						ctx, stop = context.WithTimeout(ctx, 10*time.Millisecond)
						defer stop()
					}
					fail := func() error {
						switch cause {
						case "取消":
							cancel()
						case "截止":
							<-ctx.Done()
						}
						return errors.New("不可泄露的网络原文")
					}
					c := mockClient(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("不应经过默认模拟处理器") })
					calls := 0
					c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
						calls++
						if !reading {
							return nil, fail()
						}
						return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(responseReadFunc(func([]byte) (int, error) {
							return 0, fail()
						}))}, nil
					})
					var err error
					if sms {
						_, err = c.reviewCall(ctx, http.MethodPost, accountURL+"/xluser.core.login/v3/checksms", url.Values{})
					} else {
						_, err = c.request(ctx, http.MethodPost, driveURL+"/task", nil, object{})
					}
					switch cause {
					case "取消":
						if !errors.Is(err, context.Canceled) {
							t.Fatalf("丢失取消原因：%v", err)
						}
					case "截止":
						if !errors.Is(err, context.DeadlineExceeded) {
							t.Fatalf("丢失截止原因：%v", err)
						}
					default:
						wantKind(t, err, "network")
						if strings.Contains(err.Error(), "不可泄露") {
							t.Fatal("网络错误泄露原文")
						}
					}
					if calls != 1 {
						t.Fatalf("取消或网络错误不应重复提交，实际 %d 次", calls)
					}
				})
			}
		}
	}
}
