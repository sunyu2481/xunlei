package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
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

func smsFailure(stage string, err error) error {
	if err == nil {
		return nil
	}
	var detail *Error
	if errors.As(err, &detail) {
		e := *detail
		e.Message = stage + "失败：" + e.Message
		return &e
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s失败：%w", stage, err)
	}
	return failure("internal", stage+"失败：无法保存验证进度，请检查数据目录权限和剩余空间后重试")
}

// 各来源的明确失败先于字段合并判定，说明文字仍可搭配另一来源的成功标识。
func reviewResultError(d object) error {
	markers := object{}
	for _, key := range []string{"errorCode", "error_code", "error"} {
		if value, present := d[key]; present {
			markers[key] = value
		}
	}
	if err := businessError(markers); err != nil {
		return err
	}
	if value, present := d["blogresult"]; present && str(value) != "0" {
		return failure("invalid_request", "短信验证码未通过，请检查验证码或重新获取")
	}
	if value, present := d["code"]; present {
		code, err := integer(value)
		if err != nil {
			return failure("upstream", "短信上报返回了无效状态码，请稍后重试")
		}
		if code != 200 {
			return &Error{Kind: "upstream", RemoteCode: int(code), Message: fmt.Sprintf("短信上报被拒绝（错误码 %d），请稍后重试", code)}
		}
	}
	return nil
}

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
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, failure("network", "无法连接短信验证服务，请稍后重试")
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
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
	resultErr := reviewResultError(d)
	for _, cookie := range resp.Cookies() {
		if cookie.MaxAge >= 0 && resultErr == nil {
			resultErr = reviewResultError(object{cookie.Name: cookie.Value})
		}
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
			if _, exists := d[cookie.Name]; !exists && cookie.MaxAge >= 0 {
				d[cookie.Name] = cookie.Value
			}
		}
		for _, key := range []string{"blogresult", "error", "errorCode", "error_code", "code", "errorDesc", "error_description"} {
			delete(s.ReviewCookies, key)
		}
		if key := str(d["creditkey"]); key != "" {
			s.ReviewKey = key
		}
	})
	return d, resultErr
}

func (c *Client) review(ctx context.Context, code string) (object, error) {
	params, base, err := c.reviewParams()
	if err != nil {
		return nil, err
	}
	method, path := "GET", "/xluser.core.login/v3/sendsms"
	if code != "" {
		method, path = "POST", "/xluser.core.login/v3/checksms"
		params.Set("token", c.snapshot().ReviewToken)
		params.Set("smsCode", code)
	}
	d, err := c.reviewCall(ctx, method, base+path, params)
	if d == nil {
		return nil, err
	}
	if err == nil {
		err = businessError(d)
	}
	var e *Error
	if errors.As(err, &e) && e.Kind == "upstream" && e.RemoteCode == 0 {
		stage := "短信发送"
		if code != "" {
			stage = "短信校验"
		}
		// 只记录字段类型及是否存在，不记录账号、验证码、令牌或上游原文。
		slog.WarnContext(ctx, "短信验证响应摘要", "阶段", stage,
			"errorCode类型", fmt.Sprintf("%T", d["errorCode"]), "error_code类型", fmt.Sprintf("%T", d["error_code"]),
			"error类型", fmt.Sprintf("%T", d["error"]), "错误标识非空", str(d["error"]) != "",
			"包含短信令牌", str(d["token"]) != "", "包含校验结果", str(d["blogresult"]) != "",
			"包含中文说明", str(d["errorDesc"]) != "", "包含通用说明", str(d["error_description"]) != "")
		e.Message = stage + "返回了未识别的状态，请查看服务日志中的「短信验证响应摘要」"
	}
	if !errors.As(err, &e) || e.Kind != "review_expired" {
		if err != nil {
			if saveErr := c.save(); saveErr != nil {
				return nil, smsFailure("短信验证状态保存", saveErr)
			}
		}
		return d, err
	}
	session, err := c.coreLogin(ctx)
	if err != nil {
		if !errors.As(err, &e) || e.Kind != "review_required" {
			return nil, smsFailure("验证会话恢复", err)
		}
		return nil, failure("review_expired", "验证会话已刷新，请重新获取短信验证码")
	}
	// 已取得正常登录会话时直接换取令牌，不再向旧地址发送或校验短信。
	c.update(func(s *state) { s.ReviewSession = session })
	return nil, c.finishReviewLogin(ctx)
}

func (c *Client) SendSMS(ctx context.Context) error {
	if err := c.lock(ctx); err != nil {
		return err
	}
	defer c.unlock()
	if s := c.snapshot(); s.ReviewVerified || s.ReviewSession != "" {
		return c.continueReview(ctx)
	}
	if time.Now().Unix()-c.snapshot().ReviewSentAt < 60 {
		return failure("rate_limited", "验证码已发送，请等待一分钟再试")
	}
	d, err := c.review(ctx, "")
	if err != nil {
		return smsFailure("短信发送", err)
	}
	if d == nil {
		return nil
	}
	if str(d["token"]) == "" {
		return failure("upstream", "短信服务未返回验证会话，请稍后重新登录")
	}
	c.update(func(s *state) {
		s.ReviewToken = str(d["token"])
		s.ReviewSentAt = time.Now().Unix()
	})
	return smsFailure("短信发送状态保存", c.save())
}

func (c *Client) CheckSMS(ctx context.Context, code string) error {
	if err := c.lock(ctx); err != nil {
		return err
	}
	defer c.unlock()
	if !smsPattern.MatchString(code) {
		return failure("invalid_request", "请输入六位数字验证码")
	}
	if s := c.snapshot(); s.ReviewVerified || s.ReviewSession != "" {
		return c.continueReview(ctx)
	}
	if c.snapshot().ReviewToken == "" {
		return failure("invalid_request", "请先获取短信验证码")
	}
	d, err := c.review(ctx, code)
	if err != nil {
		return smsFailure("短信校验", err)
	}
	if d == nil {
		return nil
	}
	if str(d["blogresult"]) != "0" {
		return failure("invalid_request", "短信验证码未通过，请检查验证码或重新获取")
	}
	c.update(func(s *state) {
		s.ReviewVerified = true
		s.ReviewSecurityToken = str(d["securitytoken"])
	})
	return c.continueReview(ctx)
}

func (c *Client) continueReview(ctx context.Context) error {
	s := c.snapshot()
	if s.ReviewSession != "" {
		return c.finishReviewLogin(ctx)
	}
	// 在后续请求前保存已完成的步骤；保存失败仍保留内存进度供本次进程继续。
	if err := c.save(); err != nil {
		return smsFailure("短信验证进度保存", err)
	}
	if !s.ReviewReported {
		p, base, err := c.reviewParams()
		if err != nil {
			return smsFailure("短信上报", err)
		}
		q := url.Values{"type": {"sms"}, "scene": {"34"}, "userid": {p.Get("userID")}, "data": {s.ReviewSecurityToken},
			"result": {"0"}, "appid": {p.Get("appid")}, "deviceid": {first(s.ReviewCookies["deviceid"], s.DeviceID)},
			"creditkey": {first(s.ReviewCookies["creditkey"], s.ReviewKey)}}
		report, reportErr := c.reviewCall(ctx, "GET", base+"/credit/v1/report", q)
		if reportErr != nil {
			return smsFailure("短信上报", reportErr)
		}
		if _, present := report["code"]; !present {
			return failure("upstream", "短信上报失败：服务未返回确认状态，请稍后重试")
		}
		c.update(func(s *state) {
			s.CreditKey = first(str(report["creditkey"]), s.ReviewKey)
			s.ReviewReported = true
		})
		if err = c.save(); err != nil {
			return smsFailure("短信上报", err)
		}
	}
	session, err := c.coreLogin(ctx)
	if err != nil {
		return smsFailure("验证后登录", err)
	}
	c.update(func(s *state) { s.ReviewSession = session })
	return c.finishReviewLogin(ctx)
}

func (c *Client) finishReviewLogin(ctx context.Context) error {
	if err := c.save(); err != nil {
		return smsFailure("验证后登录会话保存", err)
	}
	return smsFailure("验证后登录", c.loginSession(ctx, c.snapshot().ReviewSession))
}
