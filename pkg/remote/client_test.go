package remote

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func mockClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	c, err := New(filepath.Join(t.TempDir(), "remote", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		handler(w, r)
		return w.Result(), nil
	})
	c.update(func(s *state) {
		s.AccessToken = "访问令牌"
		s.RefreshToken = "刷新令牌"
		s.Target = "设备一"
	})
	t.Cleanup(c.Close)
	return c
}
func respond(w http.ResponseWriter, d any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(d)
}
func bodyOf(t *testing.T, r *http.Request) object {
	t.Helper()
	var d object
	if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
		t.Fatal(err)
	}
	return d
}
func wantKind(t *testing.T, err error, kind string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Kind != kind {
		t.Fatalf("预期错误类型 %s，实际：%v", kind, err)
	}
}

func TestBusinessErrorSuccessDescription(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		kind string
	}{
		{"短信数字成功码", `{"errorCode":0,"errorDesc":"成功"}`, ""},
		{"短信字符串成功码", `{"errorCode":"0","errorDesc":"成功"}`, ""},
		{"通用数字成功码", `{"error_code":0,"error_description":"成功"}`, ""},
		{"通用字符串成功码", `{"error_code":"0","error":"success","error_description":"成功"}`, ""},
		{"成功标识省略数字码", `{"error":"success","error_description":"成功","errorDesc":"操作成功"}`, ""},
		{"成功标识包含空白及大写", `{"error":" SUCCESS ","errorDesc":"成功"}`, ""},
		{"另一种成功标识", `{"error":"OK","error_description":"成功"}`, ""},
		{"字符串零成功标识", `{"error":"0","errorDesc":"成功"}`, ""},
		{"数字零成功标识", `{"error":0,"errorDesc":"成功"}`, ""},
		{"无错误字段", `{}`, ""},
		{"短信非零错误码", `{"errorCode":100,"errorDesc":"成功"}`, "upstream"},
		{"通用非零错误码", `{"error_code":123,"error_description":"成功"}`, "upstream"},
		{"非零错误码优先于成功标识", `{"errorCode":1002,"error":"success","errorDesc":"成功"}`, "upstream"},
		{"未知错误标识仍需拒绝", `{"errorCode":0,"error":"unexpected_failure","errorDesc":"失败"}`, "upstream"},
		{"短信错误码优先于通用成功码", `{"error_code":0,"errorCode":13,"errorDesc":"成功"}`, "review_expired"},
		{"通用错误码优先于短信成功码", `{"error_code":9,"errorCode":0,"error_description":"成功"}`, "captcha_required"},
		{"零码仍携带风控错误", `{"errorCode":0,"error":"review_panel","errorDesc":"需要验证"}`, "review_required"},
		{"零码仍携带登录错误", `{"error_code":0,"error":"invalid_grant","error_description":"登录失效"}`, "login_required"},
		{"仅有短信错误说明", `{"errorDesc":"请求失败"}`, "upstream"},
		{"仅有通用错误说明", `{"error_description":"请求失败"}`, "upstream"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var d object
			dec := json.NewDecoder(strings.NewReader(tt.body))
			dec.UseNumber()
			if err := dec.Decode(&d); err != nil {
				t.Fatal(err)
			}
			err := businessError(d)
			if tt.kind != "" {
				wantKind(t, err, tt.kind)
			} else if err != nil {
				t.Fatalf("成功响应不应被说明文字误判：%v", err)
			}
		})
	}
}

func TestLoginAndRestore(t *testing.T) {
	var refreshes atomic.Int32
	handler := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/xluser.core.login/v3/login":
			d := bodyOf(t, r)
			if d["userName"] != "13800000000" || d["passWord"] != "仅本次登录" {
				t.Fatal("登录参数错误")
			}
			respond(w, object{"sessionID": "会话", "userID": "用户一"})
		case "/v1/shield/captcha/init":
			d := bodyOf(t, r)
			if obj(d["meta"])["phone_number"] != "13800000000" {
				t.Fatal("登录前人机令牌参数错误")
			}
			respond(w, object{"captcha_token": "人机令牌"})
		case "/v1/auth/signin/token":
			respond(w, object{"access_token": "访问令牌", "refresh_token": "刷新令牌", "expires_in": 3600})
		case "/v1/auth/token":
			refreshes.Add(1)
			respond(w, object{"access_token": "新访问令牌", "refresh_token": "新刷新令牌", "expires_in": 3600})
		case "/drive/v1/tasks":
			respond(w, object{"tasks": []any{object{"name": "当前迅雷", "params": object{"target": "设备一"}}}})
		default:
			t.Errorf("意外请求：%s", r.URL.Path)
			w.WriteHeader(500)
		}
	}
	c := mockClient(t, handler)
	if err := c.Login(context.Background(), "13800000000", "仅本次登录", false); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(c.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "仅本次登录") {
		t.Fatal("未选择记住密码时不应持久化密码")
	}
	for _, p := range []string{c.path, filepath.Dir(c.path)} {
		info, e := os.Stat(p)
		if e != nil {
			t.Fatal(e)
		}
		if info.Mode().Perm()&0077 != 0 {
			t.Fatal("凭据目录或文件对其他用户开放")
		}
	}
	c.update(func(s *state) { s.AccessToken = "" })
	if err = c.save(); err != nil {
		t.Fatal(err)
	}
	restored, err := New(c.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restored.Close)
	restored.http.Transport = c.http.Transport
	if restored.snapshot().DeviceID != c.snapshot().DeviceID {
		t.Fatal("重启改变了设备标识")
	}
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := restored.Devices(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	if refreshes.Load() != 1 {
		t.Fatalf("并发请求应只刷新一次令牌，实际 %d 次", refreshes.Load())
	}
	if err = restored.Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(c.path)
	if strings.Contains(string(b), "刷新令牌") || restored.Status().LoggedIn {
		t.Fatal("退出后仍保留登录令牌")
	}
}

func TestCaptchaRenewalUsesBusinessAction(t *testing.T) {
	for _, userID := range []string{"用户一", ""} {
		t.Run("用户标识="+userID, func(t *testing.T) {
			calls := 0
			c := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/shield/captcha/init" {
					d := bodyOf(t, r)
					meta := obj(d["meta"])
					if d["action"] != "GET:/drive/v1/tasks" || meta["user_id"] != userID || !strings.HasPrefix(str(meta["captcha_sign"]), "1.") {
						t.Fatal("业务人机令牌缺少签名或完整路径")
					}
					respond(w, object{"captcha_token": "续期人机令牌"})
					return
				}
				calls++
				if calls == 1 {
					respond(w, object{"error_code": 9, "error": "captcha_invalid"})
					return
				}
				if r.Header.Get("X-Captcha-Token") != "续期人机令牌" {
					t.Fatal("请求未使用续期令牌")
				}
				respond(w, object{"tasks": []any{}})
			})
			c.update(func(s *state) { s.UserID = userID })
			if _, err := c.Devices(context.Background()); err != nil {
				t.Fatal(err)
			}
			if calls != 2 {
				t.Fatal("业务请求重试次数不正确")
			}
		})
	}
}

func TestReviewSMSWithCookieResult(t *testing.T) {
	verified := false
	sent := false
	reported := false
	c := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/xluser.core.login/v3/login":
			if !verified {
				respond(w, object{"error": "review_panel", "creditkey": "登录信任键", "reviewurl": accountURL + "/review?mobile=138****0000&creditkey=验证信任键&userID=用户一&appid=40"})
				return
			}
			if bodyOf(t, r)["creditkey"] != "已验证信任键" {
				t.Fatal("验证后重新登录未携带续期信任键")
			}
			respond(w, object{"sessionID": "会话", "userID": "用户一"})
		case "/xluser.core.login/v3/sendsms":
			if r.Method != "GET" || r.URL.Query().Get("smsEvent") != "34" || r.URL.Query().Get("csrf_token") == "" {
				t.Fatal("发送短信协议不正确")
			}
			sent = true
			http.SetCookie(w, &http.Cookie{Name: "deviceid", Value: "abcdefghijklmnopqrstuvwx12345678"})
			fmt.Fprint(w, `callback({"errorCode":"0","errorDesc":"成功","token":"短信令牌","creditkey":"续期验证键"});`)
		case "/xluser.core.login/v3/checksms":
			if err := r.ParseMultipartForm(4096); err != nil {
				t.Fatal(err)
			}
			defer r.MultipartForm.RemoveAll()
			if !sent || r.FormValue("smsCode") != "123456" || r.FormValue("token") != "短信令牌" || r.FormValue("creditkey") != "续期验证键" {
				t.Fatal("短信校验参数错误")
			}
			if _, err := r.Cookie("deviceid"); err != nil {
				t.Fatal("短信验证未沿用响应 Cookie")
			}
			verified = true
			http.SetCookie(w, &http.Cookie{Name: "blogresult", Value: "0"})
			http.SetCookie(w, &http.Cookie{Name: "securitytoken", Value: "验证成功令牌"})
		case "/credit/v1/report":
			reported = true
			respond(w, object{"code": 200, "creditkey": "已验证信任键"})
		case "/v1/shield/captcha/init":
			respond(w, object{"captcha_token": "人机令牌"})
		case "/v1/auth/signin/token":
			respond(w, object{"access_token": "访问令牌", "refresh_token": "刷新令牌"})
		default:
			t.Fatalf("意外的验证请求：%s", r.URL.Path)
		}
	})
	wantKind(t, c.Login(context.Background(), "13800000000", "密码", false), "review_required")
	if !c.Status().ReviewRequired || c.Status().Mobile != "138****0000" {
		t.Fatal("风控状态不正确")
	}
	if err := c.SendSMS(context.Background()); err != nil {
		t.Fatal(err)
	}
	saved, err := readState(c.path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ReviewToken != "短信令牌" || saved.ReviewSentAt == 0 {
		t.Fatal("短信发送成功后未保存验证令牌和发送时间")
	}
	wantKind(t, c.SendSMS(context.Background()), "rate_limited")
	if err := c.CheckSMS(context.Background(), "123456"); err != nil {
		t.Fatal(err)
	}
	if !reported || !c.Status().LoggedIn || c.Status().ReviewRequired || len(c.snapshot().ReviewCookies) > 0 {
		t.Fatal("验证后的会话状态未清理")
	}
}

func TestSendSMSRejectsFailureResponses(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		kind string
	}{
		{"非零错误码携带令牌", `{"errorCode":100,"errorDesc":"失败","token":"不可使用的令牌"}`, "upstream"},
		{"零码携带具名错误", `{"errorCode":0,"error":"invalid_grant","token":"不可使用的令牌"}`, "login_required"},
		{"成功响应缺少令牌", `{"errorCode":0,"errorDesc":"成功"}`, "upstream"},
		{"仅返回错误说明", `{"errorDesc":"失败","token":"不可使用的令牌"}`, "upstream"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sends := 0
			c := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/xluser.core.login/v3/sendsms" {
					t.Fatalf("短信发送失败后不应继续校验：%s", r.URL.Path)
				}
				sends++
				fmt.Fprint(w, tt.body)
			})
			c.update(func(s *state) { s.ReviewURL = accountURL + "/review?creditkey=验证信任键" })
			wantKind(t, c.SendSMS(context.Background()), tt.kind)
			if s := c.snapshot(); s.ReviewToken != "" || s.ReviewSentAt != 0 {
				t.Fatal("失败响应不应保存验证令牌或启动重发倒计时")
			}
			wantKind(t, c.CheckSMS(context.Background(), "123456"), "invalid_request")
			if sends != 1 {
				t.Fatal("发送失败不应自动重复发送短信")
			}
		})
	}
}

func TestRequestDoesNotExposeUpstreamSecrets(t *testing.T) {
	c := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
		respond(w, object{"error_code": 123, "error_description": "https://relay.xunlei.com?plugin_app_token=不可泄露"})
	})
	_, err := c.Devices(context.Background())
	if err == nil || strings.Contains(err.Error(), "不可泄露") {
		t.Fatal("错误响应泄露上游凭据")
	}
	for _, link := range []string{"http://api-pan.xunlei.com/", "https://xunlei.com.evil.invalid/", "https://127.0.0.1/", "https://user@api-pan.xunlei.com/"} {
		if trustedURL(link) {
			t.Fatalf("不应信任：%s", link)
		}
	}
}

func TestNormalizeLinks(t *testing.T) {
	magnet := "magnet:?xt=urn:btih:" + strings.Repeat("a", 40)
	for _, link := range []string{magnet, "https://example.com/文件.zip?key=123", "ftp://example.com/file", "thunder://" + base64.StdEncoding.EncodeToString([]byte("AA"+magnet+"ZZ"))} {
		if _, err := NormalizeLink(link); err != nil {
			t.Errorf("合法链接失败：%v", err)
		}
	}
	for _, link := range []string{"", "file:///etc/passwd", "magnet:?xt=错误", "thunder://错误", "https://user:pw@example.com/a", "https://example.com/a\nhttps://example.com/b"} {
		if _, err := NormalizeLink(link); err == nil {
			t.Errorf("无效链接通过：%s", link)
		}
	}
}

func TestCancelledWaitAndCorruptState(t *testing.T) {
	c := mockClient(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("取消后不应发请求") })
	if err := c.lock(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := c.Devices(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if err = os.WriteFile(c.path, []byte("损坏的状态"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = New(c.path); err == nil {
		t.Fatal("损坏状态不能被静默覆盖")
	}
	b, _ := os.ReadFile(c.path)
	if string(b) != "损坏的状态" {
		t.Fatal("状态文件被覆盖")
	}
}

func TestHTTPFailureWithSuccessfulJSON(t *testing.T) {
	c := mockClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503); respond(w, object{"tasks": []any{}}) })
	if _, err := c.Devices(context.Background()); err == nil {
		t.Fatal("不能把 HTTP 失败视为成功")
	}
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) { return nil, io.ErrUnexpectedEOF })
	_, err := c.Devices(context.Background())
	wantKind(t, err, "network")
}

func TestCloseWaitsForActiveRequest(t *testing.T) {
	started := make(chan struct{})
	c := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		respond(w, object{"tasks": []any{}})
	})
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() { defer close(finished); _, _ = c.Devices(ctx) }()
	<-started
	closed := make(chan struct{})
	go func() { c.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("正在执行的操作尚未结束就关闭了客户端")
	case <-time.After(10 * time.Millisecond):
	}
	cancel()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("取消请求后客户端未退出")
	}
	<-finished
	_, err := c.Devices(context.Background())
	wantKind(t, err, "unavailable")
}

func TestExpiredRefreshTokenUsesRememberedPassword(t *testing.T) {
	for _, remember := range []bool{true, false} {
		t.Run(fmt.Sprint(remember), func(t *testing.T) {
			logins := 0
			c := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/auth/token":
					respond(w, object{"error": "invalid_grant"})
				case "/xluser.core.login/v3/login":
					logins++
					respond(w, object{"sessionID": "会话"})
				case "/v1/shield/captcha/init":
					respond(w, object{"captcha_token": "人机令牌"})
				case "/v1/auth/signin/token":
					respond(w, object{"access_token": "新访问令牌", "refresh_token": "新刷新令牌"})
				case "/drive/v1/tasks":
					respond(w, object{"tasks": []any{}})
				default:
					t.Fatalf("意外接口：%s", r.URL.Path)
				}
			})
			c.update(func(s *state) {
				s.Username = "测试账号"
				s.AccessToken = ""
				s.Remember = remember
				if remember {
					s.Password = "已记住的密码"
				}
			})
			_, err := c.Devices(context.Background())
			if remember {
				if err != nil || logins != 1 {
					t.Fatalf("自动重新登录失败：%v", err)
				}
			} else {
				wantKind(t, err, "login_required")
				if c.Status().LoggedIn || logins != 0 {
					t.Fatal("未记住密码时应提示重新登录")
				}
			}
		})
	}
}

func TestTaskMutationAcceptsEmptySuccessResponse(t *testing.T) {
	c := mockClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	for _, method := range []string{"PATCH", "DELETE"} {
		if _, err := c.raw(context.Background(), method, driveURL+"/task", nil, nil, c.headers(true)); err != nil {
			t.Fatalf("不应把无响应体的成功状态视为失败：%v", err)
		}
	}
}
