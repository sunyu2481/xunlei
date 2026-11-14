package remote

import (
	"bytes"
	"context"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"testing"
	"time"
)

// 使用临时目录阻止状态文件替换，原状态文件始终保持可读。
func blockStateSave(t *testing.T, c *Client) func() {
	t.Helper()
	path := c.path
	c.path = t.TempDir()
	return func() { c.path = path }
}

func copyState(s state) state {
	s.ReviewCookies = maps.Clone(s.ReviewCookies)
	s.Submissions = maps.Clone(s.Submissions)
	return s
}

func stateBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func Test登录发送前保存失败保留会话(t *testing.T) {
	for _, username := range []string{"原账号", "新账号"} {
		for _, remember := range []bool{false, true} {
			name := username + "/不记住密码"
			if remember {
				name = username + "/记住密码"
			}
			t.Run(name, func(t *testing.T) {
				calls := 0
				c := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
					calls++
					switch r.URL.Path {
					case "/xluser.core.login/v3/login":
						d := bodyOf(t, r)
						if d["userName"] != username || d["passWord"] != "新密码" {
							t.Fatal("恢复存储后的登录没有使用本次账号密码")
						}
						respond(w, object{"sessionID": "新会话", "userID": "新用户"})
					case "/v1/shield/captcha/init":
						respond(w, object{"captcha_token": "新人机令牌"})
					case "/v1/auth/signin/token":
						respond(w, object{"access_token": "新访问令牌", "refresh_token": "新刷新令牌"})
					default:
						t.Fatalf("意外请求：%s", r.URL.Path)
					}
				})
				c.update(func(s *state) {
					s.Username, s.Password, s.Remember = "原账号", "原密码", !remember
					s.TargetName, s.Directory = "原设备", "/downloads/原目录"
					s.UserID, s.CaptchaToken, s.CreditKey, s.ExpiresAt = "原用户", "原人机令牌", "原信任键", 123
					s.ReviewURL, s.ReviewKey, s.ReviewToken = accountURL+"/review", "原验证键", "原短信令牌"
					s.ReviewVerified, s.ReviewReported = true, true
					s.ReviewSecurityToken, s.ReviewSession = "原校验凭据", "原登录会话"
					s.ReviewCookies, s.ReviewSentAt = map[string]string{"token": "原短信令牌"}, 123
					s.VerificationURL = accountURL + "/verify"
					s.Submissions = map[string]submission{"已有记录": {TaskID: "已有任务"}}
				})
				c.relays["设备一"] = relay{Base: "https://relay.xunlei.com", Query: url.Values{"token": {"原中继令牌"}}}
				before, relays := copyState(c.snapshot()), maps.Clone(c.relays)
				if err := c.save(); err != nil {
					t.Fatal(err)
				}
				path, disk := c.path, stateBytes(t, c.path)
				restore := blockStateSave(t, c)
				defer restore()
				if err := c.Login(context.Background(), username, "新密码", remember); err == nil {
					t.Fatal("登录应返回发送前保存错误")
				}
				if calls != 0 || !reflect.DeepEqual(c.snapshot(), before) || !reflect.DeepEqual(c.relays, relays) {
					t.Fatal("发送前保存失败改变了原会话、验证进度或中继缓存")
				}
				if !bytes.Equal(stateBytes(t, path), disk) {
					t.Fatal("发送前保存失败改变了原状态文件")
				}
				restore()
				if err := c.Login(context.Background(), username, "新密码", remember); err != nil {
					t.Fatal(err)
				}
				saved, err := readState(c.path)
				if err != nil {
					t.Fatal(err)
				}
				if calls != 3 || saved.Username != username || saved.AccessToken != "新访问令牌" || saved.Remember != remember || saved.DeviceID != before.DeviceID {
					t.Fatal("存储恢复后未完成并保存本次登录")
				}
				if (remember && saved.Password != "新密码") || (!remember && saved.Password != "") {
					t.Fatal("恢复后的密码保存策略不正确")
				}
				if username != before.Username && (saved.Target != "" || len(saved.Submissions) != 0 || len(c.relays) != 0) {
					t.Fatal("切换账号成功后仍保留原账号设置或缓存")
				}
				if username == before.Username && (saved.Target != before.Target || !reflect.DeepEqual(saved.Submissions, before.Submissions) || !reflect.DeepEqual(c.relays, relays)) {
					t.Fatal("同账号登录丢失了设备设置或缓存")
				}
			})
		}
	}
}

func Test设置保存失败保留原设置(t *testing.T) {
	for _, settings := range []Settings{
		{Target: "设备二", Directory: "/downloads/电影"},
		{Target: "设备一", Directory: "/downloads/电影"},
		{Target: "设备一"},
	} {
		t.Run(settings.Target+settings.Directory, func(t *testing.T) {
			c := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/drive/v1/tasks":
					respond(w, object{"tasks": []any{object{"name": "所选设备", "params": object{"target": settings.Target}}}})
				case "/drive/v1/apps/INNER_API":
					respond(w, object{"link": "https://relay.xunlei.com/device/"})
				case "/device/drive/v1/files":
					respond(w, object{"files": []any{object{"id": "所选目录", "kind": "drive#folder", "params": object{"RealPath": settings.Directory}}}})
				default:
					t.Fatalf("意外请求：%s", r.URL.Path)
				}
			})
			c.update(func(s *state) { s.TargetName, s.Directory = "原设备", "/downloads/原目录" })
			before := copyState(c.snapshot())
			if err := c.save(); err != nil {
				t.Fatal(err)
			}
			path, disk := c.path, stateBytes(t, c.path)
			restore := blockStateSave(t, c)
			defer restore()
			if err := c.Configure(context.Background(), settings); err == nil {
				t.Fatal("设置应返回保存错误")
			}
			if !reflect.DeepEqual(c.snapshot(), before) || !bytes.Equal(stateBytes(t, path), disk) {
				t.Fatal("设置保存失败后内存或磁盘仍切换了设备或目录")
			}
			restore()
			if err := c.Configure(context.Background(), settings); err != nil {
				t.Fatal(err)
			}
			saved, err := readState(c.path)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Target != settings.Target || saved.TargetName != "所选设备" || saved.Directory != settings.Directory || !reflect.DeepEqual(c.snapshot(), saved) {
				t.Fatal("存储恢复后未保存并采用所选设置")
			}
		})
	}
}

func Test创建发送前保存失败允许原编号重试(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "空记录"
		if existing {
			name = "已有记录"
		}
		t.Run(name, func(t *testing.T) {
			posted := 0
			var c *Client
			c = mockClient(t, driveFixture(t, func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path == "/drive/v1/task" {
					posted++
					saved, err := readState(c.path)
					if err != nil || saved.Submissions[sampleRequest().RequestID].Fingerprint == "" {
						t.Fatal("创建请求发出前未保存提交记录")
					}
				}
				return false
			}))
			if existing {
				c.update(func(s *state) {
					s.Submissions = map[string]submission{
						"待确认记录": {Fingerprint: "原指纹", At: time.Now().Unix()},
						"过期记录":  {TaskID: "原任务", At: time.Now().Add(-8 * 24 * time.Hour).Unix()},
					}
				})
			}
			before := copyState(c.snapshot())
			if err := c.save(); err != nil {
				t.Fatal(err)
			}
			path, disk := c.path, stateBytes(t, c.path)
			restore := blockStateSave(t, c)
			defer restore()
			if _, err := c.Create(context.Background(), sampleRequest()); err == nil {
				t.Fatal("创建应返回发送前保存错误")
			}
			if posted != 0 || !reflect.DeepEqual(c.snapshot(), before) || !bytes.Equal(stateBytes(t, path), disk) {
				t.Fatal("创建发送前保存失败仍留下新记录或改动了原记录")
			}
			restore()
			result, err := c.Create(context.Background(), sampleRequest())
			if err != nil || result.ID != "task-id" || result.Reused || posted != 1 {
				t.Fatalf("存储恢复后同编号不能正常提交：%v", err)
			}
			saved, err := readState(c.path)
			if err != nil || saved.Submissions[sampleRequest().RequestID].TaskID != result.ID {
				t.Fatal("恢复后没有保存任务编号")
			}
			if existing && (saved.Submissions["待确认记录"] != before.Submissions["待确认记录"] || saved.Submissions["过期记录"].TaskID != "") {
				t.Fatal("成功提交后未保留有效记录或未清理过期记录")
			}
		})
	}
}

func Test创建发送后保存故障保留提交证据(t *testing.T) {
	for _, outcome := range []string{"响应丢失", "缺少编号", "已取得编号"} {
		for _, restart := range []bool{false, true} {
			name := outcome + "/当前客户端"
			if restart {
				name = outcome + "/重建客户端"
			}
			t.Run(name, func(t *testing.T) {
				posted := 0
				c := mockClient(t, driveFixture(t, func(w http.ResponseWriter, r *http.Request) bool {
					if r.URL.Path != "/drive/v1/task" {
						return false
					}
					if outcome == "已取得编号" {
						respond(w, object{"task": object{"id": "已创建任务"}})
					} else {
						respond(w, object{})
					}
					return true
				}))
				path := c.path
				transport := c.http.Transport
				restore := func() {}
				defer func() { restore() }()
				c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
					if r.URL.Path == "/drive/v1/task" {
						posted++
						restore = blockStateSave(t, c)
						if outcome == "响应丢失" {
							return nil, io.ErrUnexpectedEOF
						}
					}
					return transport.RoundTrip(r)
				})
				_, err := c.Create(context.Background(), sampleRequest())
				wantKind(t, err, "submission_unknown")
				saved, err := readState(path)
				if err != nil {
					t.Fatal(err)
				}
				for _, s := range []state{c.snapshot(), saved} {
					if s.Submissions[sampleRequest().RequestID].Fingerprint == "" {
						t.Fatal("发送后的故障丢失了提交证据")
					}
				}
				if outcome == "已取得编号" && c.snapshot().Submissions[sampleRequest().RequestID].TaskID != "已创建任务" {
					t.Fatal("保存失败丢失了内存中已取得的任务编号")
				}
				restore()
				if restart {
					restored, err := New(c.path)
					if err != nil {
						t.Fatal(err)
					}
					restored.http.Transport = c.http.Transport
					t.Cleanup(restored.Close)
					c = restored
				}
				result, err := c.Create(context.Background(), sampleRequest())
				if outcome == "已取得编号" && !restart {
					if err != nil || result.ID != "已创建任务" || !result.Reused {
						t.Fatalf("没有复用内存中已确认的结果：%v", err)
					}
				} else {
					wantKind(t, err, "submission_unknown")
				}
				if posted != 1 {
					t.Fatal("结果未确认或已取得编号时重复发送创建请求")
				}
			})
		}
	}
}

func Test设置保存失败保留已续期会话(t *testing.T) {
	refreshes, requests := 0, 0
	var c *Client
	restore := func() {}
	defer func() { restore() }()
	c = mockClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/token":
			refreshes++
			respond(w, object{"access_token": "续期访问令牌", "refresh_token": "续期刷新令牌", "expires_in": 3600})
		case "/drive/v1/tasks":
			requests++
			if requests == 1 {
				restore = blockStateSave(t, c)
			}
			respond(w, object{"tasks": []any{object{"name": "新设备", "params": object{"target": "设备二"}}}})
		default:
			t.Fatalf("意外请求：%s", r.URL.Path)
		}
	})
	c.update(func(s *state) {
		s.ExpiresAt = time.Now().Add(-time.Minute).Unix()
		s.TargetName, s.Directory = "原设备", "/downloads/原目录"
	})
	path := c.path
	if err := c.Configure(context.Background(), Settings{Target: "设备二"}); err == nil {
		t.Fatal("设置应返回保存错误")
	}
	saved, err := readState(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []state{c.snapshot(), saved} {
		if s.AccessToken != "续期访问令牌" || s.RefreshToken != "续期刷新令牌" || s.Target != "设备一" || s.TargetName != "原设备" || s.Directory != "/downloads/原目录" {
			t.Fatal("设置失败后未保留原设置与刚续期的会话")
		}
	}
	restore()
	if err := c.Configure(context.Background(), Settings{Target: "设备二"}); err != nil {
		t.Fatal(err)
	}
	if refreshes != 1 || requests != 2 || c.Status().Target != "设备二" {
		t.Fatal("恢复存储后未沿用已续期会话保存设置")
	}
}

func Test登录发送后保存失败保留新进度(t *testing.T) {
	for _, outcome := range []string{"短信入口", "登录令牌"} {
		t.Run(outcome, func(t *testing.T) {
			var c *Client
			restore := func() {}
			defer func() { restore() }()
			c = mockClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/xluser.core.login/v3/login":
					if outcome == "短信入口" {
						restore = blockStateSave(t, c)
						respond(w, object{"error": "review_panel", "reviewurl": accountURL + "/review?creditkey=new-key", "creditkey": "新信任键"})
					} else {
						respond(w, object{"sessionID": "新会话"})
					}
				case "/v1/shield/captcha/init":
					respond(w, object{"captcha_token": "新人机令牌"})
				case "/v1/auth/signin/token":
					restore = blockStateSave(t, c)
					respond(w, object{"access_token": "新访问令牌", "refresh_token": "新刷新令牌"})
				default:
					t.Fatalf("意外请求：%s", r.URL.Path)
				}
			})
			c.update(func(s *state) { s.Username = "原账号" })
			if err := c.Login(context.Background(), "新账号", "新密码", false); err == nil {
				t.Fatal("登录应返回取得新进度后的保存错误")
			}
			s := c.snapshot()
			if s.Username != "新账号" {
				t.Fatal("发送后的保存失败不应恢复原账号")
			}
			if outcome == "短信入口" && (s.ReviewURL == "" || s.ReviewKey != "new-key" || s.CreditKey != "新信任键") {
				t.Fatal("保存失败丢失了刚取得的短信验证入口")
			}
			if outcome == "登录令牌" && (s.AccessToken != "新访问令牌" || s.RefreshToken != "新刷新令牌") {
				t.Fatal("保存失败丢失了刚取得的登录令牌")
			}
		})
	}
}

func Test退出保存失败不恢复凭据(t *testing.T) {
	c := mockClient(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("本地退出不应请求云端") })
	deviceID := c.snapshot().DeviceID
	c.relays["设备一"] = relay{Base: "https://relay.xunlei.com"}
	restore := blockStateSave(t, c)
	defer restore()
	if err := c.Logout(context.Background()); err == nil {
		t.Fatal("退出应返回保存错误")
	}
	if c.Status().LoggedIn || c.snapshot().DeviceID == deviceID || len(c.relays) != 0 {
		t.Fatal("退出保存失败后不应恢复原会话或中继缓存")
	}
}
