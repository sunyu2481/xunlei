package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"testing"
	"time"
)

func 保存过期会话(t *testing.T, c *Client, remember bool) state {
	t.Helper()
	c.update(func(s *state) {
		s.Username = "续期测试账号"
		s.UserID = "续期测试用户"
		s.CaptchaToken = "原人机令牌"
		s.ExpiresAt = time.Now().Unix() - 1
		s.Remember = remember
		if remember {
			s.Password = "模拟记住密码"
		}
	})
	if err := c.save(); err != nil {
		t.Fatal(err)
	}
	return c.snapshot()
}

func 断言会话状态(t *testing.T, c *Client, want state) {
	t.Helper()
	saved, err := readState(c.path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.snapshot(), want) || !reflect.DeepEqual(saved, want) {
		t.Error("内存或状态文件中的会话与预期不符")
	}
	if c.Status().LoggedIn != (want.AccessToken != "" || want.RefreshToken != "") {
		t.Error("登录状态与保留的令牌不一致")
	}
}

func Test续期临时失败保留会话(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		body   string
		kind   string
		code   int
	}{
		{"设备暂不可用", 200, `{"error_code":2}`, "device_unavailable", 2},
		{"人机令牌失败", 200, `{"error_code":9}`, "captcha_required", 9},
		{"临时业务错误", 200, `{"error_code":500}`, "upstream", 500},
		{"临时服务错误", 503, `{"error_code":500}`, "upstream", 500},
		{"无效服务响应", 503, `<html>暂不可用</html>`, "upstream", 0},
		{"未知业务错误", 200, `{"error":"temporarily_unavailable"}`, "upstream", 0},
		{"短信验证要求", 200, `{"error":"review_panel"}`, "review_required", 0},
		{"短信验证过期", 200, `{"error_code":13}`, "review_expired", 13},
		{"未授权但明确人机失败", 401, `{"error_code":9}`, "captcha_required", 9},
		{"无效错误码", 200, `{"error_code":true}`, "upstream", 0},
		{"无效令牌有效期", 200, `{"access_token":"新访问令牌","expires_in":"0.1"}`, "upstream", 0},
		{"成功响应缺少访问令牌", 200, `{}`, "login_required", 0},
	} {
		for _, remember := range []bool{false, true} {
			for _, rejected := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/记住密码=%v/业务拒绝=%v", tt.name, remember, rejected), func(t *testing.T) {
					refreshes, logins, requests := 0, 0, 0
					c := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
						switch r.URL.Path {
						case "/v1/auth/token":
							refreshes++
							if bodyOf(t, r)["refresh_token"] != "刷新令牌" {
								t.Error("续期未沿用原刷新令牌")
							}
							if refreshes == 1 {
								w.WriteHeader(tt.status)
								fmt.Fprint(w, tt.body)
								return
							}
							respond(w, object{"access_token": "恢复访问令牌", "refresh_token": "恢复刷新令牌", "expires_in": 3600})
						case "/xluser.core.login/v3/login":
							logins++
							respond(w, object{"error": "invalid_grant"})
						case "/drive/v1/tasks":
							requests++
							if r.Header.Get("Authorization") != "Bearer 恢复访问令牌" {
								respond(w, object{"error": "invalid_token"})
								return
							}
							respond(w, object{"tasks": []any{}})
						default:
							t.Fatalf("不应发起其他认证请求：%s", r.URL.Path)
						}
					})
					保存过期会话(t, c, remember)
					if rejected {
						c.update(func(s *state) { s.ExpiresAt = time.Now().Unix() + 3600 })
						if err := c.save(); err != nil {
							t.Fatal(err)
						}
					}
					before := c.snapshot()
					fileBefore, err := os.ReadFile(c.path)
					if err != nil {
						t.Fatal(err)
					}
					_, err = c.Devices(context.Background())
					var remoteErr *Error
					if !errors.As(err, &remoteErr) || remoteErr.Kind != tt.kind || remoteErr.RemoteCode != tt.code {
						t.Errorf("应保留错误类型 %s 和错误码 %d，实际：%v", tt.kind, tt.code, err)
					}
					断言会话状态(t, c, before)
					fileAfter, err := os.ReadFile(c.path)
					if err != nil || !bytes.Equal(fileBefore, fileAfter) {
						t.Error("临时续期失败改写了状态文件")
					}
					initialRequests := 0
					if rejected {
						initialRequests = 1
					}
					if refreshes != 1 || logins != 0 || requests != initialRequests {
						t.Errorf("临时失败后应立即返回，续期=%d，登录=%d，业务请求=%d", refreshes, logins, requests)
					}
					if t.Failed() {
						return
					}
					restored, err := New(c.path)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(restored.Close)
					restored.http.Transport = c.http.Transport
					if _, err := restored.Devices(context.Background()); err != nil {
						t.Fatalf("重启后应能使用原刷新令牌恢复：%v", err)
					}
					if refreshes != 2 || logins != 0 || requests != initialRequests*2+1 {
						t.Errorf("恢复时不应额外登录或重试，续期=%d，登录=%d，业务请求=%d", refreshes, logins, requests)
					}
					want := before
					want.AccessToken, want.RefreshToken = "恢复访问令牌", "恢复刷新令牌"
					want.ExpiresAt = restored.snapshot().ExpiresAt
					if want.ExpiresAt <= time.Now().Unix() {
						t.Error("恢复后未更新令牌有效期")
					}
					断言会话状态(t, restored, want)
				})
			}
		}
	}
}

func Test续期明确失效清理或重新登录(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		body   string
	}{
		{"刷新令牌失效", 200, `{"error":"invalid_grant"}`},
		{"访问令牌失效", 200, `{"error":"invalid_token"}`},
		{"失效码4122", 200, `{"error_code":4122}`},
		{"失效码4121", 200, `{"error_code":4121}`},
		{"失效码10", 200, `{"error_code":10}`},
		{"失效码16", 200, `{"error_code":16}`},
		{"未授权空响应", 401, ""},
		{"未授权未知错误", 401, `{"error":"unrecognized"}`},
	} {
		for _, remember := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/记住密码=%v", tt.name, remember), func(t *testing.T) {
				refreshes, logins, captchas, signins, requests := 0, 0, 0, 0, 0
				var c *Client
				var cleared state
				c = mockClient(t, func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/v1/auth/token":
						refreshes++
						w.WriteHeader(tt.status)
						fmt.Fprint(w, tt.body)
					case "/xluser.core.login/v3/login":
						logins++
						断言会话状态(t, c, cleared)
						body := bodyOf(t, r)
						if body["userName"] != cleared.Username || body["passWord"] != cleared.Password {
							t.Error("自动登录未使用记住的账号密码")
						}
						respond(w, object{"sessionID": "重新登录会话", "userID": cleared.UserID})
					case "/v1/shield/captcha/init":
						captchas++
						respond(w, object{"captcha_token": "新登录人机令牌"})
					case "/v1/auth/signin/token":
						signins++
						respond(w, object{"access_token": "新登录访问令牌", "refresh_token": "新登录刷新令牌", "expires_in": 3600})
					case "/drive/v1/tasks":
						requests++
						if r.Header.Get("Authorization") != "Bearer 新登录访问令牌" {
							t.Error("业务请求未使用重新登录的令牌")
						}
						respond(w, object{"tasks": []any{}})
					default:
						t.Fatalf("意外请求：%s", r.URL.Path)
					}
				})
				cleared = 保存过期会话(t, c, remember)
				cleared.AccessToken, cleared.RefreshToken = "", ""
				_, err := c.Devices(context.Background())
				want := cleared
				loginCount := 0
				if remember {
					if err != nil {
						t.Fatalf("记住密码时应能重新登录：%v", err)
					}
					loginCount = 1
					want.AccessToken, want.RefreshToken = "新登录访问令牌", "新登录刷新令牌"
					want.CaptchaToken = "新登录人机令牌"
					want.ExpiresAt = c.snapshot().ExpiresAt
					if want.ExpiresAt <= time.Now().Unix() {
						t.Error("重新登录后未更新有效期")
					}
				} else {
					wantKind(t, err, "login_required")
				}
				断言会话状态(t, c, want)
				if refreshes != 1 || logins != loginCount || captchas != loginCount || signins != loginCount || requests != loginCount {
					t.Errorf("认证流程次数不符：续期=%d，登录=%d，人机=%d，换取令牌=%d，业务请求=%d", refreshes, logins, captchas, signins, requests)
				}
			})
		}
	}
}

func Test续期网络或取消保留会话(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		for _, remember := range []bool{false, true} {
			t.Run(fmt.Sprintf("取消=%v/记住密码=%v", canceled, remember), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				c := mockClient(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("不应经过默认模拟处理器") })
				before := 保存过期会话(t, c, remember)
				calls := 0
				c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					if r.URL.Path != "/v1/auth/token" {
						t.Fatalf("续期失败后不应继续请求：%s", r.URL.Path)
					}
					if canceled {
						cancel()
					}
					return nil, io.ErrUnexpectedEOF
				})
				_, err := c.Devices(ctx)
				if canceled {
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("续期未保留取消原因：%v", err)
					}
				} else {
					wantKind(t, err, "network")
				}
				断言会话状态(t, c, before)
				if calls != 1 {
					t.Errorf("续期失败不应重试或登录，实际请求 %d 次", calls)
				}
			})
		}
	}
}
