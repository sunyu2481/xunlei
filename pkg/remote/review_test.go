package remote

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

func TestLoginDiscardsPreviousReview(t *testing.T) {
	for _, username := range []string{"上次账号", "其他账号"} {
		t.Run(username, func(t *testing.T) {
			c := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/xluser.core.login/v3/login" {
					t.Fatal("登录失败后不应请求验证接口")
				}
				respond(w, object{"error_code": 100, "error": "rejected"})
			})
			c.update(func(s *state) {
				s.Username = "上次账号"
				s.ReviewURL = accountURL + "/review?creditkey=上次验证"
				s.ReviewKey, s.ReviewToken = "上次信任键", "上次短信令牌"
				s.ReviewCookies = map[string]string{"token": "上次短信令牌"}
				s.ReviewSentAt = 123
				s.VerificationURL = accountURL + "/verify"
			})
			wantKind(t, c.Login(context.Background(), username, "密码", false), "upstream")
			saved, err := readState(c.path)
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range []state{c.snapshot(), saved} {
				if s.ReviewURL != "" || s.ReviewKey != "" || s.ReviewToken != "" || len(s.ReviewCookies) != 0 || s.ReviewSentAt != 0 || s.VerificationURL != "" {
					t.Fatal("重新登录失败后仍保留上次验证会话")
				}
			}
			if c.Status().ReviewRequired || c.Status().LoggedIn {
				t.Fatal("登录失败后不应显示已登录或短信验证状态")
			}
		})
	}
}

func TestReviewSMSSuccessMarkers(t *testing.T) {
	for _, tt := range []struct {
		name    string
		body    string
		cookies bool
	}{
		{"成功标识省略数字码", `{"error":"success","errorDesc":"成功","error_description":"Success","token":"短信令牌"}`, false},
		{"JSONP成功标识省略数字码", `callback({"error":"success","error_description":"Success","token":"短信令牌"});`, false},
		{"大写成功标识", `{"errorCode":"0","error":"OK","errorDesc":"成功","token":"短信令牌"}`, false},
		{"字符串零成功标识", `{"error":"0","errorDesc":"成功","token":"短信令牌"}`, false},
		{"数字零成功标识", `{"error":0,"errorDesc":"成功","token":"短信令牌"}`, false},
		{"Cookie成功标识省略数字码", "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sends, checks, reports, logins := 0, 0, 0, 0
			token := "短信令牌"
			if tt.cookies {
				token = "sms-token"
			}
			c := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/xluser.core.login/v3/sendsms":
					sends++
					if tt.cookies {
						http.SetCookie(w, &http.Cookie{Name: "error", Value: "success"})
						http.SetCookie(w, &http.Cookie{Name: "error_description", Value: "Success"})
						http.SetCookie(w, &http.Cookie{Name: "token", Value: token})
					}
					fmt.Fprint(w, tt.body)
				case "/xluser.core.login/v3/checksms":
					checks++
					if err := r.ParseMultipartForm(4096); err != nil {
						t.Fatal(err)
					}
					defer r.MultipartForm.RemoveAll()
					if r.FormValue("token") != token || r.FormValue("smsCode") != "123456" {
						t.Fatal("提交验证码时未沿用已下发的短信令牌")
					}
					if _, err := r.Cookie("error"); err == nil {
						t.Fatal("不应把上一次响应的结果 Cookie 带入下一次请求")
					}
					http.SetCookie(w, &http.Cookie{Name: "error", Value: "success"})
					http.SetCookie(w, &http.Cookie{Name: "blogresult", Value: "0"})
					http.SetCookie(w, &http.Cookie{Name: "securitytoken", Value: "verified-token"})
					respond(w, object{"error_description": "Success"})
				case "/credit/v1/report":
					reports++
					respond(w, object{"code": 200, "creditkey": "已验证信任键"})
				case "/xluser.core.login/v3/login":
					logins++
					if bodyOf(t, r)["creditkey"] != "已验证信任键" {
						t.Fatal("验证后的登录未携带信任键")
					}
					respond(w, object{"error": "success", "error_description": "Success", "sessionID": "会话", "userID": "用户一"})
				case "/v1/shield/captcha/init":
					respond(w, object{"captcha_token": "人机令牌"})
				case "/v1/auth/signin/token":
					respond(w, object{"access_token": "访问令牌", "refresh_token": "刷新令牌"})
				default:
					t.Fatalf("意外请求：%s", r.URL.Path)
				}
			})
			c.update(func(s *state) {
				s.AccessToken, s.RefreshToken = "", ""
				s.Username, s.Password = "13800000000", "密码"
				s.ReviewURL = accountURL + "/review?creditkey=验证信任键&userID=用户一&appid=40"
			})
			if err := c.SendSMS(context.Background()); err != nil {
				t.Fatalf("已下发短信的成功响应被误判：%v", err)
			}
			saved, err := readState(c.path)
			if err != nil {
				t.Fatal(err)
			}
			if saved.ReviewToken != token || saved.ReviewSentAt == 0 {
				t.Fatal("成功发送后未保存短信令牌及发送时间")
			}
			wantKind(t, c.SendSMS(context.Background()), "rate_limited")
			if err := c.CheckSMS(context.Background(), "123456"); err != nil {
				t.Fatalf("已获取验证码后应能继续验证登录：%v", err)
			}
			if sends != 1 || checks != 1 || reports != 1 || logins != 1 {
				t.Fatalf("短信验证请求次数错误：发送=%d，校验=%d，上报=%d，登录=%d", sends, checks, reports, logins)
			}
			if !c.Status().LoggedIn || c.Status().ReviewRequired || c.snapshot().ReviewToken != "" {
				t.Fatal("验证后未进入已登录状态或未清理短信会话")
			}
		})
	}
}

func TestReviewSMSUnknownResponseDiagnostics(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	c := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
		respond(w, object{"error": "不可泄露的错误原文", "errorDesc": "不可泄露的响应说明", "token": "不可泄露的令牌"})
	})
	c.update(func(s *state) { s.ReviewURL = accountURL + "/review?creditkey=不可泄露的信任键" })
	err := c.SendSMS(context.Background())
	wantKind(t, err, "upstream")
	if !strings.Contains(err.Error(), "短信发送返回了未识别的状态") || !strings.Contains(logs.String(), "短信验证响应摘要") {
		t.Fatal("未知响应应提示具体失败步骤并记录诊断摘要")
	}
	if !strings.Contains(logs.String(), `"包含短信令牌":true`) {
		t.Fatal("诊断摘要应记录是否收到了短信令牌")
	}
	if strings.Contains(err.Error()+logs.String(), "不可泄露") {
		t.Fatal("诊断信息不应泄露上游响应或凭据")
	}
}
