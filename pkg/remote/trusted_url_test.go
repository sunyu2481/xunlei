package remote

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

var trustedHostTests = []struct {
	name, host string
	trusted    bool
}{
	{"迅雷域名及标准端口", "API-PAN.XUNLEI.COM:443", true},
	{"山岱域名", "xluser-ssl.sandai.net", true},
	{"扩展域名", "relay.xunleix.com", true},
	{"回环区域标识", "[::1%25a.xunlei.com]", false},
	{"映射地址区域标识", "[::ffff:127.0.0.1%25a.sandai.net]", false},
	{"链路地址区域标识", "[fe80::1%25a.xunleix.com]:443", false},
	{"普通四版地址", "127.0.0.1", false},
	{"普通六版地址", "[::1]", false},
	{"无效域名标签", "bad_name.xunlei.com", false},
}

func Test可信地址主机边界(t *testing.T) {
	for _, tt := range trustedHostTests {
		t.Run(tt.name, func(t *testing.T) {
			if got := trustedURL("https://" + tt.host + "/"); got != tt.trusted {
				t.Fatalf("主机信任结果为 %t，预期 %t", got, tt.trusted)
			}
		})
	}
	for _, raw := range []string{
		"https://xunlei.com/", "https://sandai.net/", "https://xunleix.com/",
		"HTTPS://api-pan.xunlei.com/path?next=example.invalid#section",
		"https://1.a-b.xunlei.com/", "https://xn--fiqs8s.xunlei.com/",
		"https://" + strings.Repeat("a", 63) + ".xunlei.com/",
		"https://" + strings.Repeat(strings.Repeat("a", 63)+".", 3) + strings.Repeat("a", 50) + ".xunlei.com/",
	} {
		if !trustedURL(raw) {
			t.Errorf("可信域名被拒绝：%s", raw)
		}
	}
	for _, raw := range []string{
		"http://xunlei.com/", "//xunlei.com/", "https:///xunlei.com",
		"https://evil-xunlei.com/", "https://xunlei.com.evil.invalid/",
		"https://xunlei.com@evil.invalid/", "https://user@xunlei.com/",
		"https://xunlei.com:444/", "https://xunlei.com./", "https://[xunlei.com]/",
		"https://[::ffff:127.0.0.1]/", "https://[::1%25xunlei.com]/",
		"https://[::1%25a%2exunlei.com]/", "https://[::1%a.xunlei.com]/",
		"https://127.0.0.1%25a.xunlei.com/", "https://.xunlei.com/",
		"https://a..xunlei.com/", "https://-a.xunlei.com/", "https://a-.xunlei.com/",
		"https://a!b.xunlei.com/", "https://a~b.xunlei.com/", "https://＊.xunlei.com/",
		"https://" + strings.Repeat("a", 64) + ".xunlei.com/",
		"https://" + strings.Repeat(strings.Repeat("a", 63)+".", 3) + strings.Repeat("a", 51) + ".xunlei.com/",
	} {
		if trustedURL(raw) {
			t.Errorf("非可信地址被接受：%s", raw)
		}
	}
}

func Test验证入口拒绝伪装域名(t *testing.T) {
	for _, tt := range trustedHostTests {
		for _, review := range []bool{true, false} {
			stage, kind := "人机验证", "verification_required"
			if review {
				stage, kind = "短信验证", "review_required"
			}
			t.Run(tt.name+"/"+stage, func(t *testing.T) {
				link := "https://" + tt.host + "/verify?creditkey=test-key"
				calls := 0
				c := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
					calls++
					if r.URL.Host != "xluser-ssl.xunlei.com" {
						t.Fatalf("意外请求验证地址：%s", r.URL.Host)
					}
					switch r.URL.Path {
					case "/xluser.core.login/v3/login":
						if review {
							respond(w, object{"error": "review_panel", "reviewurl": link})
						} else {
							respond(w, object{"sessionID": "测试会话"})
						}
					case "/v1/shield/captcha/init":
						respond(w, object{"url": link})
					default:
						t.Fatalf("意外请求路径：%s", r.URL.Path)
					}
				})
				wantKind(t, c.Login(context.Background(), "测试账号", "测试密码", false), kind)
				wantCalls := 2
				if review {
					wantCalls = 1
				}
				if calls != wantCalls {
					t.Fatalf("请求数为 %d，预期 %d", calls, wantCalls)
				}
				wantLink := ""
				if tt.trusted {
					wantLink = link
					if review {
						u, err := url.Parse(link)
						if err != nil {
							t.Fatal(err)
						}
						q := u.Query()
						q.Set("deviceid", c.deviceSign())
						u.RawQuery = q.Encode()
						wantLink = u.String()
					}
				}
				saved, err := readState(c.path)
				if err != nil {
					t.Fatal(err)
				}
				for _, s := range []state{c.snapshot(), saved} {
					got := s.VerificationURL
					if review {
						got = s.ReviewURL
					}
					if got != wantLink {
						t.Errorf("保存的验证入口为 %q，预期 %q", got, wantLink)
					}
				}
			})
		}
	}
}

func Test短信服务主机拒绝伪装域名(t *testing.T) {
	for _, tt := range trustedHostTests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			c := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if tt.trusted && (r.URL.Host != tt.host || r.URL.Path != "/xluser.core.login/v3/sendsms") {
					t.Fatalf("短信请求地址不正确：%s", r.URL)
				}
				respond(w, object{"token": "测试短信令牌"})
			})
			c.update(func(s *state) {
				s.ReviewURL = accountURL + "/review?" + url.Values{"mainHost": {"https://" + tt.host}, "creditkey": {"测试信任键"}}.Encode()
			})
			err := c.SendSMS(context.Background())
			if !tt.trusted {
				if calls != 0 || c.snapshot().ReviewToken != "" {
					t.Errorf("非可信短信地址仍被请求或保存了令牌：请求数=%d", calls)
				}
				wantKind(t, err, "upstream")
				return
			}
			if err != nil || calls != 1 || c.snapshot().ReviewToken != "测试短信令牌" {
				t.Fatalf("可信短信地址未正常发送：请求数=%d，错误=%v", calls, err)
			}
		})
	}
}

func Test设备中继拒绝伪装域名(t *testing.T) {
	for _, tt := range trustedHostTests {
		for _, field := range []string{"link", "links"} {
			t.Run(tt.name+"/"+field, func(t *testing.T) {
				discoveries, requests := 0, 0
				c := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/drive/v1/apps/INNER_API" {
						discoveries++
						if r.URL.Host != "api-pan.xunlei.com" || r.URL.Query().Get("space") != "设备一" {
							t.Fatal("设备发现请求的地址或目标不正确")
						}
						link := "https://" + tt.host + "/device/?plugin_app_id=test-app&plugin_app_token=test-token"
						if field == "link" {
							respond(w, object{"link": link})
						} else {
							respond(w, object{"links": []any{object{"url": link}}})
						}
						return
					}
					requests++
					q := r.URL.Query()
					if tt.trusted && (r.URL.Host != tt.host || r.URL.Path != "/device/drive/v1/files" || q.Get("plugin_app_id") != "test-app" || q.Get("plugin_app_token") != "test-token" || q.Get("space") != "设备一") {
						t.Fatal("中继请求的地址、插件凭据或目标不正确")
					}
					respond(w, object{"files": []any{object{"id": "测试目录", "name": "downloads", "kind": "drive#folder"}}})
				})
				folders, err := c.Folders(context.Background(), "设备一", "")
				if discoveries != 1 {
					t.Errorf("设备发现请求数为 %d，预期一次", discoveries)
				}
				if !tt.trusted {
					if requests != 0 || len(c.relays) != 0 || len(folders) != 0 {
						t.Errorf("非可信中继仍被请求、缓存或返回目录：请求数=%d，缓存数=%d，目录数=%d", requests, len(c.relays), len(folders))
					}
					wantKind(t, err, "device_unavailable")
					return
				}
				if err != nil || requests != 1 || len(c.relays) != 1 || len(folders) != 1 || folders[0].ID != "测试目录" {
					t.Fatalf("可信中继未正常返回目录：请求数=%d，目录=%v，错误=%v", requests, folders, err)
				}
			})
		}
	}
}
