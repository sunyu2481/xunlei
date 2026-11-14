package remote

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func prepareSMSReview(t *testing.T, c *Client, remember bool) {
	t.Helper()
	c.update(func(s *state) {
		s.AccessToken, s.RefreshToken = "", ""
		s.Username, s.Password, s.Remember = "短信测试账号", "测试密码", remember
		s.ReviewURL = accountURL + "/review?creditkey=old-key&userID=测试用户&appid=40"
		s.ReviewKey, s.ReviewToken = "old-key", "sms-token"
		s.ReviewSentAt = time.Now().Unix()
	})
	if err := c.save(); err != nil {
		t.Fatal(err)
	}
}

func restoreSMSClient(t *testing.T, c *Client) *Client {
	t.Helper()
	restored, err := New(c.path)
	if err != nil {
		t.Fatal(err)
	}
	restored.http.Transport = c.http.Transport
	t.Cleanup(restored.Close)
	return restored
}

func assertSMSPending(t *testing.T, c *Client) {
	t.Helper()
	saved, err := readState(c.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []state{c.snapshot(), saved} {
		if s.ReviewURL == "" || s.ReviewToken != "sms-token" || s.ReviewSentAt == 0 {
			t.Fatal("失败后未保留可继续的短信验证状态")
		}
	}
	if !c.Status().ReviewRequired || c.Status().LoggedIn {
		t.Fatal("失败后的登录及验证状态不正确")
	}
}

func assertSMSLoggedIn(t *testing.T, c *Client) {
	t.Helper()
	saved, err := readState(c.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []state{c.snapshot(), saved} {
		if s.AccessToken != "访问令牌" || s.RefreshToken != "刷新令牌" || s.ReviewURL != "" || s.ReviewToken != "" || s.ReviewKey != "" || s.ReviewSentAt != 0 || len(s.ReviewCookies) != 0 || s.ReviewVerified || s.ReviewReported || s.ReviewSecurityToken != "" || s.ReviewSession != "" {
			t.Fatal("成功后未保存登录令牌或清理短信验证状态")
		}
	}
	if !c.Status().LoggedIn || c.Status().ReviewRequired {
		t.Fatal("成功后未进入已登录状态")
	}
}

func Test短信响应冲突拒绝任一来源的失败(t *testing.T) {
	for _, tt := range []struct {
		name, body, field, cookie, kind string
		code                            int
	}{
		{"正文非零码", `{"errorCode":100,"blogresult":"0"}`, "errorCode", "0", "upstream", 100},
		{"正文另一非零码", `{"error_code":100,"blogresult":"0"}`, "error_code", "0", "upstream", 100},
		{"正文具名失败", `{"error":"invalid_grant","blogresult":"0"}`, "error", "success", "login_required", 0},
		{"正文校验失败", `{"blogresult":"1"}`, "blogresult", "0", "invalid_request", 0},
		{"正文错误码类型无效", `{"errorCode":false,"blogresult":"0"}`, "errorCode", "0", "upstream", 0},
		{"正文校验结果空值", `{"blogresult":null}`, "blogresult", "0", "invalid_request", 0},
		{"Cookie非零码", `{"errorCode":0,"blogresult":"0"}`, "errorCode", "100", "upstream", 100},
		{"Cookie具名失败", `{"error":"success","blogresult":"0"}`, "error", "invalid_grant", "login_required", 0},
		{"Cookie校验失败", `{"blogresult":"0"}`, "blogresult", "1", "invalid_request", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			checks, reports, logins := 0, 0, 0
			c := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/xluser.core.login/v3/checksms":
					checks++
					http.SetCookie(w, &http.Cookie{Name: tt.field, Value: tt.cookie})
					fmt.Fprint(w, tt.body)
				case "/credit/v1/report":
					reports++
					respond(w, object{"code": 200})
				case "/xluser.core.login/v3/login":
					logins++
					respond(w, object{"sessionID": "登录会话"})
				case "/v1/shield/captcha/init":
					respond(w, object{"captcha_token": "人机令牌"})
				case "/v1/auth/signin/token":
					respond(w, object{"access_token": "访问令牌", "refresh_token": "刷新令牌"})
				default:
					t.Fatalf("意外请求：%s", r.URL.Path)
				}
			})
			prepareSMSReview(t, c, false)
			err := c.CheckSMS(context.Background(), "123456")
			wantKind(t, err, tt.kind)
			var detail *Error
			if !errors.As(err, &detail) || detail.RemoteCode != tt.code {
				t.Fatalf("失败来源的错误码丢失：%v", err)
			}
			if checks != 1 || reports != 0 || logins != 0 {
				t.Fatalf("失败后继续了请求：校验=%d，上报=%d，登录=%d", checks, reports, logins)
			}
			assertSMSPending(t, c)
		})
	}
}

func Test短信上报失败保留阶段并继续(t *testing.T) {
	for _, mode := range []string{"网络", "取消", "HTTP", "业务错误", "非成功码", "缺少状态码", "无效状态码", "正文与Cookie冲突", "Cookie与正文冲突", "保存失败"} {
		for _, restart := range []bool{false, true} {
			if mode == "保存失败" && restart {
				continue
			}
			t.Run(fmt.Sprintf("%s/重建客户端=%v", mode, restart), func(t *testing.T) {
				checks, reports, logins, sends := 0, 0, 0, 0
				broken := true
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var c *Client
				badPath := t.TempDir()
				c = mockClient(t, func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/xluser.core.login/v3/checksms":
						checks++
						respond(w, object{"blogresult": "0", "securitytoken": "verified-token"})
					case "/credit/v1/report":
						if r.URL.Query().Get("data") != "verified-token" {
							t.Fatal("继续上报未沿用已校验的令牌")
						}
						if broken {
							switch mode {
							case "HTTP":
								w.WriteHeader(http.StatusServiceUnavailable)
								fmt.Fprint(w, "不可泄露的服务原文")
								return
							case "业务错误":
								respond(w, object{"code": 200, "errorCode": 500, "errorDesc": "不可泄露的业务原文"})
								return
							case "非成功码":
								respond(w, object{"code": 503})
								return
							case "缺少状态码":
								respond(w, object{})
								return
							case "无效状态码":
								respond(w, object{"code": true})
								return
							case "正文与Cookie冲突":
								http.SetCookie(w, &http.Cookie{Name: "code", Value: "200"})
								respond(w, object{"code": 503})
								return
							case "Cookie与正文冲突":
								http.SetCookie(w, &http.Cookie{Name: "code", Value: "503"})
								respond(w, object{"code": 200})
								return
							case "保存失败":
								c.path = badPath
							}
						}
						respond(w, object{"code": "2e2", "errorDesc": "上报成功", "creditkey": "reported-key"})
					case "/xluser.core.login/v3/login":
						logins++
						if bodyOf(t, r)["creditkey"] != "reported-key" {
							t.Fatal("验证后的登录未使用上报取得的信任键")
						}
						respond(w, object{"sessionID": "登录会话"})
					case "/v1/shield/captcha/init":
						respond(w, object{"captcha_token": "人机令牌"})
					case "/v1/auth/signin/token":
						respond(w, object{"access_token": "访问令牌", "refresh_token": "刷新令牌"})
					case "/xluser.core.login/v3/sendsms":
						sends++
						respond(w, object{"token": "unexpected-token"})
					default:
						t.Fatalf("意外请求：%s", r.URL.Path)
					}
				})
				prepareSMSReview(t, c, restart)
				originalPath := c.path
				transport := c.http.Transport
				c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
					if r.URL.Path == "/credit/v1/report" {
						reports++
						if broken && (mode == "网络" || mode == "取消") {
							if mode == "取消" {
								cancel()
							}
							return nil, errors.New("不可泄露的网络原文")
						}
					}
					return transport.RoundTrip(r)
				})
				err := c.CheckSMS(ctx, "123456")
				c.path = originalPath
				if err == nil || !strings.Contains(err.Error(), "短信上报") || strings.Contains(err.Error(), "不可泄露") {
					t.Fatalf("未脱敏返回上报失败阶段：%v", err)
				}
				if mode == "取消" {
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("上报取消原因丢失：%v", err)
					}
				} else {
					kind := "upstream"
					if mode == "网络" {
						kind = "network"
					} else if mode == "保存失败" {
						kind = "internal"
					}
					wantKind(t, err, kind)
					var detail *Error
					if !errors.As(err, &detail) || !strings.Contains(detail.Message, "短信上报") {
						t.Fatal("供网页返回的错误未保留上报阶段")
					}
					if mode == "业务错误" && detail.RemoteCode != 500 || (mode == "非成功码" || strings.Contains(mode, "冲突")) && detail.RemoteCode != 503 {
						t.Fatalf("上报失败的错误码丢失：%v", err)
					}
				}
				if checks != 1 || reports != 1 || logins != 0 || sends != 0 {
					t.Fatalf("上报失败后继续或重复请求：校验=%d，上报=%d，登录=%d，发送=%d", checks, reports, logins, sends)
				}
				assertSMSPending(t, c)
				broken = false
				if restart {
					c = restoreSMSClient(t, c)
				}
				if err = c.CheckSMS(context.Background(), "123456"); err != nil {
					t.Fatalf("恢复后不能继续验证：%v", err)
				}
				wantReports := 2
				if mode == "保存失败" {
					wantReports = 1
				}
				if checks != 1 || reports != wantReports || logins != 1 || sends != 0 {
					t.Fatalf("恢复后重复了已完成步骤：校验=%d，上报=%d，登录=%d，发送=%d", checks, reports, logins, sends)
				}
				assertSMSLoggedIn(t, c)
			})
		}
	}
}

func Test短信阶段保存失败可从内存继续(t *testing.T) {
	for _, stage := range []string{"校验结果", "登录会话"} {
		t.Run(stage, func(t *testing.T) {
			checks, reports, logins, signins := 0, 0, 0, 0
			broken := true
			badPath := t.TempDir()
			var c *Client
			c = mockClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/xluser.core.login/v3/checksms":
					checks++
					if broken && stage == "校验结果" {
						c.path = badPath
					}
					respond(w, object{"blogresult": "0", "securitytoken": "verified-token"})
				case "/credit/v1/report":
					reports++
					respond(w, object{"code": 200})
				case "/xluser.core.login/v3/login":
					logins++
					if broken && stage == "登录会话" {
						c.path = badPath
					}
					respond(w, object{"sessionID": "登录会话"})
				case "/v1/shield/captcha/init":
					respond(w, object{"captcha_token": "人机令牌"})
				case "/v1/auth/signin/token":
					signins++
					respond(w, object{"access_token": "访问令牌", "refresh_token": "刷新令牌"})
				default:
					t.Fatalf("意外请求：%s", r.URL.Path)
				}
			})
			prepareSMSReview(t, c, false)
			path := c.path
			err := c.CheckSMS(context.Background(), "123456")
			c.path = path
			wantKind(t, err, "internal")
			assertSMSPending(t, c)
			if checks != 1 || signins != 0 || stage == "校验结果" && reports != 0 {
				t.Fatal("保存验证进度失败后仍继续请求")
			}
			broken = false
			// 获取短信入口同样只能续走已完成的验证，不应重新发送短信。
			if err = c.SendSMS(context.Background()); err != nil {
				t.Fatalf("恢复存储后不能继续：%v", err)
			}
			if checks != 1 || reports != 1 || logins != 1 || signins != 1 {
				t.Fatalf("恢复后重复已完成步骤：校验=%d，上报=%d，登录=%d，换令牌=%d", checks, reports, logins, signins)
			}
			assertSMSLoggedIn(t, c)
		})
	}
}

func Test短信重复结果Cookie不能掩盖失败(t *testing.T) {
	for _, field := range []string{"errorCode", "blogresult"} {
		t.Run(field, func(t *testing.T) {
			c := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
				http.SetCookie(w, &http.Cookie{Name: field, Value: "100"})
				http.SetCookie(w, &http.Cookie{Name: field, Value: "0"})
				respond(w, object{"errorCode": 0, "blogresult": "0"})
			})
			prepareSMSReview(t, c, false)
			_, err := c.reviewCall(context.Background(), http.MethodPost, accountURL+"/xluser.core.login/v3/checksms", nil)
			kind := "upstream"
			if field == "blogresult" {
				kind = "invalid_request"
			}
			wantKind(t, err, kind)
		})
	}
}

func Test短信验证后登录失败可继续(t *testing.T) {
	for _, stage := range []string{"核心登录", "人机令牌", "登录令牌"} {
		for _, restart := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/重建客户端=%v", stage, restart), func(t *testing.T) {
				checks, reports, logins := 0, 0, 0
				broken := true
				c := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/xluser.core.login/v3/checksms":
						checks++
						respond(w, object{"blogresult": "0", "securitytoken": "verified-token"})
					case "/credit/v1/report":
						reports++
						respond(w, object{"code": 200, "creditkey": "reported-key"})
					case "/xluser.core.login/v3/login":
						logins++
						if broken && stage == "核心登录" {
							respond(w, object{"errorCode": 500})
							return
						}
						respond(w, object{"sessionID": "无需重复登录的会话"})
					case "/v1/shield/captcha/init":
						if broken && stage == "人机令牌" {
							w.WriteHeader(http.StatusServiceUnavailable)
						}
						respond(w, object{"captcha_token": "人机令牌"})
					case "/v1/auth/signin/token":
						if bodyOf(t, r)["signin_token"] != "无需重复登录的会话" {
							t.Fatal("恢复时未沿用已取得的登录会话")
						}
						if broken && stage == "登录令牌" {
							w.WriteHeader(http.StatusServiceUnavailable)
						}
						respond(w, object{"access_token": "访问令牌", "refresh_token": "刷新令牌"})
					default:
						t.Fatalf("意外请求：%s", r.URL.Path)
					}
				})
				prepareSMSReview(t, c, restart)
				err := c.CheckSMS(context.Background(), "123456")
				wantKind(t, err, "upstream")
				if !strings.Contains(err.Error(), "登录") {
					t.Fatalf("未返回验证后的登录失败阶段：%v", err)
				}
				assertSMSPending(t, c)
				broken = false
				if restart {
					c = restoreSMSClient(t, c)
				}
				if err = c.CheckSMS(context.Background(), "123456"); err != nil {
					t.Fatalf("恢复后不能继续登录：%v", err)
				}
				wantLogins := 1
				if stage == "核心登录" {
					wantLogins = 2
				}
				if checks != 1 || reports != 1 || logins != wantLogins {
					t.Fatalf("继续登录重复了已完成步骤：校验=%d，上报=%d，登录=%d", checks, reports, logins)
				}
				assertSMSLoggedIn(t, c)
			})
		}
	}
}

func Test短信过期恢复不重复请求(t *testing.T) {
	for _, checking := range []bool{false, true} {
		for _, newReview := range []bool{false, true} {
			t.Run(fmt.Sprintf("校验=%v/仍需短信=%v", checking, newReview), func(t *testing.T) {
				sends, checks, logins, signins := 0, 0, 0, 0
				c := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/xluser.core.login/v3/sendsms":
						sends++
						respond(w, object{"errorCode": 13})
					case "/xluser.core.login/v3/checksms":
						checks++
						respond(w, object{"errorCode": 13})
					case "/xluser.core.login/v3/login":
						logins++
						if newReview {
							respond(w, object{"error": "review_panel", "reviewurl": accountURL + "/review?creditkey=new-key"})
						} else {
							respond(w, object{"sessionID": "已恢复的登录会话", "creditkey": "login-key"})
						}
					case "/v1/shield/captcha/init":
						respond(w, object{"captcha_token": "人机令牌"})
					case "/v1/auth/signin/token":
						signins++
						if bodyOf(t, r)["signin_token"] != "已恢复的登录会话" {
							t.Fatal("恢复登录丢弃了正常会话")
						}
						respond(w, object{"access_token": "访问令牌", "refresh_token": "刷新令牌"})
					default:
						t.Fatalf("意外请求：%s", r.URL.Path)
					}
				})
				prepareSMSReview(t, c, false)
				c.update(func(s *state) { s.ReviewSentAt = time.Now().Add(-time.Minute).Unix() })
				var err error
				if checking {
					err = c.CheckSMS(context.Background(), "123456")
				} else {
					err = c.SendSMS(context.Background())
				}
				if newReview {
					wantKind(t, err, "review_expired")
					saved, readErr := readState(c.path)
					if readErr != nil || saved.ReviewKey != "new-key" || saved.ReviewToken != "" || saved.ReviewSentAt != 0 {
						t.Fatalf("未保留新的待发送验证会话：%v", readErr)
					}
				} else {
					if err != nil {
						t.Fatalf("恢复到正常会话后未完成登录：%v", err)
					}
					assertSMSLoggedIn(t, c)
					if signins != 1 {
						t.Fatal("没有恰好换取一次登录令牌")
					}
				}
				if sends+checks != 1 || logins != 1 {
					t.Fatalf("恢复流程重复了短信或登录请求：发送=%d，校验=%d，登录=%d", sends, checks, logins)
				}
			})
		}
	}
}

func Test短信校验状态文件不保存验证码(t *testing.T) {
	c := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/xluser.core.login/v3/checksms" {
			respond(w, object{"blogresult": "0", "securitytoken": "verified-token"})
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	prepareSMSReview(t, c, false)
	_ = c.CheckSMS(context.Background(), "654321")
	b, err := os.ReadFile(c.path)
	if err != nil || strings.Contains(string(b), "654321") || strings.Contains(string(b), "测试密码") {
		t.Fatalf("状态文件泄露验证码或未记住的密码：%v", err)
	}
}
