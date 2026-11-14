package remote

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const clientVersion = "8.31.0.9726"
const packageName = "com.xunlei.downloadprovider"

// 以下摘要参数是迅雷客户端协议所需的公开应用参数，不是用户凭据。
var captchaSalts = []string{
	"9uJNVj/wLmdwKrJaVj/omlQ", "Oz64Lp0GigmChHMf/6TNfxx7O9PyopcczMsnf", "Eb+L7Ce+Ej48u", "jKY0",
	"ASr0zCl6v8W4aidjPK5KHd1Lq3t+vBFf41dqv5+fnOd", "wQlozdg6r1qxh0eRmt3QgNXOvSZO6q/GXK",
	"gmirk+ciAvIgA/cxUUCema47jr/YToixTT+Q6O", "5IiCoM9B1/788ntB", "P07JH0h6qoM6TSUAK2aL9T5s2QBVeY9JWvalf", "+oK0AN",
}

func md5hex(s string) string { d := md5.Sum([]byte(s)); return hex.EncodeToString(d[:]) }
func (c *Client) deviceSign() string {
	did := c.snapshot().DeviceID
	d := sha1.Sum([]byte(did + packageName + "40" + c.appKey))
	return "div101." + did + md5hex(hex.EncodeToString(d[:]))
}

func (c *Client) headers(auth bool) http.Header {
	s := c.snapshot()
	h := make(http.Header)
	h.Set("Accept", "application/json;charset=UTF-8")
	h.Set("User-Agent", "ANDROID-com.xunlei.downloadprovider/8.31.0.9726 netWorkType/5G appid/40 deviceName/Xiaomi_M2004j7ac deviceModel/M2004J7AC OSVersion/12 protocolVersion/301 platformVersion/10 sdkVersion/512000 Oauth2Client/0.9 (Linux 4_14_186-perf-gddfs8vbb238b) (JAVA 0)")
	h.Set("X-Device-Id", s.DeviceID)
	h.Set("X-Client-Id", c.clientID)
	h.Set("X-Client-Version", clientVersion)
	if auth {
		if s.AccessToken != "" {
			h.Set("Authorization", "Bearer "+s.AccessToken)
		}
		if s.CaptchaToken != "" {
			h.Set("X-Captcha-Token", s.CaptchaToken)
		}
	}
	return h
}

// Login 的密码默认只保留在内存，用于本次登录和可能出现的短信验证。
func (c *Client) Login(ctx context.Context, username, password string, remember bool) error {
	if err := c.lock(ctx); err != nil {
		return err
	}
	defer c.unlock()
	username = strings.TrimSpace(username)
	if username == "" || password == "" || len(username) > 256 || len(password) > 1024 {
		return failure("invalid_request", "请填写迅雷账号和密码")
	}
	old := c.snapshot()
	err := c.updateAndSave(func(s *state) error {
		if s.Username != username {
			*s = state{DeviceID: s.DeviceID}
		}
		s.Username, s.Password, s.Remember = username, password, remember
		s.AccessToken, s.RefreshToken, s.CaptchaToken, s.UserID = "", "", "", ""
		// 手动重新登录时废弃上一次验证，避免失败后继续使用旧账号验证入口。
		s.clearReview()
		s.VerificationURL = ""
		return nil
	})
	// 目录同步失败也可能已切换账号，缓存必须跟随实际采用的状态。
	if old.Username != c.snapshot().Username {
		c.relays = make(map[string]relay)
	}
	if err != nil {
		return err
	}
	return c.login(ctx)
}

func (c *Client) coreLogin(ctx context.Context) (string, error) {
	s := c.snapshot()
	if s.Username == "" || s.Password == "" {
		return "", failure("login_required", "请重新输入迅雷账号密码登录")
	}
	body := object{"protocolVersion": "301", "sequenceNo": "1000012", "platformVersion": "10", "isCompressed": "0",
		"appid": "40", "clientVersion": clientVersion, "peerID": strings.Repeat("0", 32), "appName": "ANDROID-" + packageName,
		"sdkVersion": "512000", "devicesign": c.deviceSign(), "netWorkType": "WIFI", "providerName": "NONE",
		"deviceModel": "M2004J7AC", "deviceName": "Xiaomi_M2004j7ac", "OSVersion": "12", "creditkey": s.CreditKey,
		"hl": "zh-CN", "userName": s.Username, "passWord": s.Password, "verifyKey": "", "verifyCode": "", "isMd5Pwd": "0"}
	h := c.headers(false)
	h.Set("User-Agent", "android-ok-http-client/xl-acc-sdk/version-5.0.12.512000")
	d, err := c.raw(ctx, "POST", accountURL+"/xluser.core.login/v3/login", nil, body, h)
	if str(d["error"]) == "review_panel" {
		u, e := url.Parse(str(d["reviewurl"]))
		if e != nil || !trustedURL(u.String()) {
			return "", failure("review_required", "本次登录需要验证，但迅雷未返回有效的验证地址，请重新登录")
		}
		q := u.Query()
		q.Set("deviceid", c.deviceSign())
		u.RawQuery = q.Encode()
		c.update(func(s *state) {
			s.clearReview()
			s.CreditKey = str(d["creditkey"])
			s.ReviewURL = u.String()
			s.ReviewKey = first(q.Get("creditkey"), s.CreditKey)
		})
		if e = c.save(); e != nil {
			return "", e
		}
		return "", failure("review_required", "本次登录需要短信验证，请获取并填写验证码")
	}
	if err != nil {
		return "", err
	}
	session := str(d["sessionID"])
	if session == "" {
		return "", failure("upstream", "迅雷登录未返回有效会话，请重新登录")
	}
	c.update(func(s *state) {
		s.CreditKey = str(d["creditkey"])
		s.UserID = first(str(d["userID"]), str(d["userid"]), str(d["user_id"]))
	})
	return session, nil
}

func (c *Client) captcha(ctx context.Context, action string, signingIn bool) error {
	s := c.snapshot()
	meta := object{}
	if signingIn {
		key := "username"
		if strings.Contains(s.Username, "@") {
			key = "email"
		} else if len(s.Username) >= 11 && len(s.Username) <= 18 {
			key = "phone_number"
		}
		meta[key] = s.Username
	} else {
		ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
		sign := c.clientID + clientVersion + packageName + s.DeviceID + ts
		for _, salt := range captchaSalts {
			sign = md5hex(sign + salt)
		}
		meta = object{"client_version": clientVersion, "package_name": packageName, "user_id": s.UserID, "timestamp": ts, "captcha_sign": "1." + sign}
	}
	d, err := c.raw(ctx, "POST", accountURL+"/v1/shield/captcha/init", nil, object{
		"action": action, "captcha_token": s.CaptchaToken, "client_id": c.clientID, "device_id": s.DeviceID,
		"meta": meta, "redirect_uri": "xlaccsdk01://xunlei.com/callback?state=harbor"}, c.headers(false))
	if link := str(d["url"]); link != "" {
		if !trustedURL(link) {
			return failure("verification_required", "迅雷要求额外验证，请在迅雷客户端完成验证后重新登录")
		}
		c.update(func(s *state) { s.VerificationURL = link })
		if e := c.save(); e != nil {
			return e
		}
		return failure("verification_required", "请打开验证页面，完成验证后重试")
	}
	if err != nil {
		return err
	}
	if str(d["captcha_token"]) == "" {
		return failure("upstream", "迅雷未返回人机令牌")
	}
	c.update(func(s *state) { s.CaptchaToken = str(d["captcha_token"]); s.VerificationURL = "" })
	return c.save()
}

func (c *Client) login(ctx context.Context) error {
	session, err := c.coreLogin(ctx)
	if err != nil {
		return err
	}
	return c.loginSession(ctx, session)
}

func (c *Client) loginSession(ctx context.Context, session string) error {
	if err := c.captcha(ctx, "POST:/v1/auth/signin/token", true); err != nil {
		return err
	}
	d, err := c.raw(ctx, "POST", accountURL+"/v1/auth/signin/token", nil,
		object{"client_id": c.clientID, "client_secret": c.clientSecret, "provider": "access_end_point_token", "signin_token": session}, c.headers(false))
	if err != nil {
		return err
	}
	if err = c.applyToken(d); err != nil {
		return err
	}
	c.update(func(s *state) {
		s.CreditKey, s.VerificationURL = "", ""
		s.clearReview()
		if !s.Remember {
			s.Password = ""
		}
	})
	return c.save()
}

func (c *Client) applyToken(d object) error {
	if str(d["access_token"]) == "" {
		return failure("login_required", "迅雷未返回访问令牌，请重新登录")
	}
	expiresIn, _, err := integerField(d, "expires_in")
	now := time.Now().Unix()
	if err != nil || expiresIn > 1<<63-1-now {
		return failure("upstream", "迅雷返回了无效的令牌有效期，请稍后重试")
	}
	c.update(func(s *state) {
		s.AccessToken = str(d["access_token"])
		s.RefreshToken = first(str(d["refresh_token"]), s.RefreshToken)
		s.UserID = first(str(d["user_id"]), str(d["sub"]), s.UserID)
		s.ExpiresAt = 0
		if expiresIn > 0 {
			s.ExpiresAt = now + expiresIn
		}
	})
	return nil
}

func (c *Client) ensureToken(ctx context.Context) error {
	s := c.snapshot()
	if s.AccessToken != "" && (s.ExpiresAt == 0 || s.ExpiresAt > time.Now().Unix()+30) {
		return nil
	}
	return c.refresh(ctx)
}

func (c *Client) refresh(ctx context.Context) error {
	s := c.snapshot()
	if s.RefreshToken != "" {
		d, err := c.raw(ctx, "POST", accountURL+"/v1/auth/token", nil, object{"grant_type": "refresh_token",
			"refresh_token": s.RefreshToken, "client_id": c.clientID, "client_secret": c.clientSecret}, c.headers(false))
		if err == nil {
			if err = c.applyToken(d); err != nil {
				return err
			}
			return c.save()
		}
		// 只有上游明确拒绝认证才清理会话，其他续期失败保留原令牌供稍后重试。
		var e *Error
		if !errors.As(err, &e) || e.Kind != "login_required" {
			return err
		}
	}
	c.update(func(s *state) { s.AccessToken = ""; s.RefreshToken = "" })
	if err := c.save(); err != nil {
		return err
	}
	return c.login(ctx)
}

func (c *Client) Logout(ctx context.Context) error {
	if err := c.lock(ctx); err != nil {
		return err
	}
	defer c.unlock()
	c.update(func(s *state) { *s = state{DeviceID: randomID()} })
	c.relays = make(map[string]relay)
	return c.save()
}
