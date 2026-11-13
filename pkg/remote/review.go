package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var jsonpPattern = regexp.MustCompile(`(?s)^[\w.$]+\s*\((\{.*\})\)\s*;?\s*$`)
var smsPattern = regexp.MustCompile(`^[0-9]{6}$`)

func (c *Client) reviewParams() (url.Values, string, error) {
	s := c.snapshot()
	if s.ReviewURL == "" {
		return nil, "", failure("review_required", "当前没有待处理的短信验证，请先登录")
	}
	u, err := url.Parse(s.ReviewURL)
	if err != nil {
		return nil, "", failure("review_expired", "验证会话无效，请重新登录")
	}
	p := url.Values{"provideNname": {"NONE"}, "providerName": {"NONE"}, "netWorkType": {"NONE"},
		"protocolVersion": {"301"}, "devicesign": {c.deviceSign()}, "platformVersion": {"1"}, "fromPlatformVersion": {"1"},
		"timestamp": {strconv.FormatInt(time.Now().UnixMilli(), 10)}, "sdkVersion": {"1.0"}, "deviceModel": {"M2004J7AC"},
		"deviceName": {"Xiaomi_M2004j7ac"}, "OSVersion": {"12"}}
	for k, v := range u.Query() {
		p[k] = v
	}
	if p.Get("deviceid") != "" {
		p.Set("devicesign", p.Get("deviceid"))
		p.Del("deviceid")
	}
	p.Set("smsEvent", "34")
	p.Set("creditkey", first(s.ReviewKey, u.Query().Get("creditkey"), s.CreditKey))
	base := accountURL
	if host := strings.Split(p.Get("mainHost"), ",")[0]; host != "" {
		host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
		base = "https://" + strings.TrimRight(host, "/")
		parsed, e := url.Parse(base)
		if e != nil || !trustedURL(base) || parsed.Path != "" {
			return nil, "", failure("upstream", "迅雷验证服务地址无效")
		}
	}
	return p, base, nil
}

func (c *Client) reviewCall(ctx context.Context, method, endpoint string, params url.Values) (object, error) {
	s := c.snapshot()
	var body io.Reader
	contentType := ""
	if method == "GET" {
		seed := first(s.ReviewCookies["deviceid"], s.DeviceID)
		if len(seed) > 32 {
			seed = seed[:32]
		}
		params.Set("csrf_token", md5hex(seed))
		endpoint += "?" + params.Encode()
	} else {
		var b bytes.Buffer
		w := multipart.NewWriter(&b)
		for k := range params {
			if err := w.WriteField(k, params.Get(k)); err != nil {
				return nil, err
			}
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
		contentType = w.FormDataContentType()
		body = &b
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, failure("upstream", "无法创建短信验证请求")
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range s.ReviewCookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, failure("network", "无法连接短信验证服务，请稍后重试")
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, failure("network", "读取短信验证响应失败")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, failure("upstream", "短信验证服务暂时不可用")
	}
	d := object{}
	if m := jsonpPattern.FindSubmatch(bytes.TrimSpace(b)); len(m) > 1 {
		b = m[1]
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	// 校验接口可能只通过响应 Cookie 返回结果，不要求响应体必须是 JSON。
	_ = dec.Decode(&d)
	if d == nil {
		d = object{}
	}
	c.update(func(s *state) {
		if s.ReviewCookies == nil {
			s.ReviewCookies = make(map[string]string)
		}
		for _, cookie := range resp.Cookies() {
			if cookie.Value == "" || cookie.MaxAge < 0 {
				delete(s.ReviewCookies, cookie.Name)
			} else {
				s.ReviewCookies[cookie.Name] = cookie.Value
			}
			if _, exists := d[cookie.Name]; !exists || method == "POST" {
				d[cookie.Name] = cookie.Value
			}
		}
		delete(s.ReviewCookies, "blogresult")
		delete(s.ReviewCookies, "error")
		if key := str(d["creditkey"]); key != "" {
			s.ReviewKey = key
		}
	})
	if err = c.save(); err != nil {
		return nil, err
	}
	return d, nil
}

func (c *Client) review(ctx context.Context, code string) (object, error) {
	for attempt := 0; attempt < 3; attempt++ {
		params, base, err := c.reviewParams()
		if err != nil {
			return nil, err
		}
		key := params.Get("creditkey")
		method, path := "GET", "/xluser.core.login/v3/sendsms"
		if code != "" {
			method, path = "POST", "/xluser.core.login/v3/checksms"
			params.Set("token", c.snapshot().ReviewToken)
			params.Set("smsCode", code)
		}
		d, err := c.reviewCall(ctx, method, base+path, params)
		if err != nil {
			return nil, err
		}
		err = businessError(d)
		var e *Error
		if !errors.As(err, &e) || e.Kind != "review_expired" {
			return d, err
		}
		if key != c.snapshot().ReviewKey && attempt == 0 {
			continue
		}
		c.update(func(s *state) { s.ReviewToken = "" })
		if _, err = c.coreLogin(ctx); err != nil {
			if !errors.As(err, &e) || e.Kind != "review_required" {
				return nil, err
			}
		}
		if code != "" {
			return nil, failure("review_expired", "验证会话已刷新，请重新获取短信验证码")
		}
	}
	return nil, failure("review_expired", "新的验证会话仍被拒绝，请稍后重新登录")
}

func (c *Client) SendSMS(ctx context.Context) error {
	if err := c.lock(ctx); err != nil {
		return err
	}
	defer c.unlock()
	if time.Now().Unix()-c.snapshot().ReviewSentAt < 60 {
		return failure("rate_limited", "验证码已发送，请等待一分钟再试")
	}
	d, err := c.review(ctx, "")
	if err != nil {
		return err
	}
	if str(d["token"]) == "" {
		return failure("upstream", "短信服务未返回验证会话，请稍后重新登录")
	}
	c.update(func(s *state) { s.ReviewToken = str(d["token"]); s.ReviewSentAt = time.Now().Unix() })
	return c.save()
}

func (c *Client) CheckSMS(ctx context.Context, code string) error {
	if err := c.lock(ctx); err != nil {
		return err
	}
	defer c.unlock()
	if !smsPattern.MatchString(code) {
		return failure("invalid_request", "请输入六位数字验证码")
	}
	if c.snapshot().ReviewToken == "" {
		return failure("invalid_request", "请先获取短信验证码")
	}
	d, err := c.review(ctx, code)
	if err != nil {
		return err
	}
	if str(d["blogresult"]) != "0" {
		return failure("invalid_request", "短信验证码未通过，请检查验证码或重新获取")
	}
	p, base, err := c.reviewParams()
	if err != nil {
		return err
	}
	s := c.snapshot()
	q := url.Values{"type": {"sms"}, "scene": {"34"}, "userid": {p.Get("userID")}, "data": {str(d["securitytoken"])},
		"result": {str(d["blogresult"])}, "appid": {p.Get("appid")}, "deviceid": {first(s.ReviewCookies["deviceid"], s.DeviceID)},
		"creditkey": {first(s.ReviewCookies["creditkey"], s.ReviewKey)}}
	report, reportErr := c.reviewCall(ctx, "GET", base+"/credit/v1/report", q)
	key := c.snapshot().ReviewKey
	if reportErr == nil && str(report["code"]) == "200" {
		key = first(str(report["creditkey"]), key)
	}
	c.update(func(s *state) { s.CreditKey = key; s.ReviewToken = "" })
	if err = c.save(); err != nil {
		return err
	}
	return c.login(ctx)
}
