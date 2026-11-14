package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const accountURL = "https://xluser-ssl.xunlei.com"
const driveURL = "https://api-pan.xunlei.com/drive/v1"

// Error 使用稳定错误类型供页面处理，避免把上游凭据或完整响应写入日志。
type Error struct {
	Kind       string `json:"code"`
	Message    string `json:"message"`
	RemoteCode int    `json:"remote_code,omitempty"`
}

func (e *Error) Error() string           { return e.Message }
func failure(kind, message string) error { return &Error{Kind: kind, Message: message} }

type relay struct {
	Base  string
	Query url.Values
	At    time.Time
}

// Client 的云端操作按会话串行执行，避免令牌续期、验证及设备切换相互覆盖。
// 等待操作锁的请求可以随上下文取消；本地状态查询不等待网络操作。
type Client struct {
	mu                             sync.RWMutex
	gate                           chan struct{}
	closed                         bool
	state                          state
	path                           string
	files                          stateFiles
	http                           *http.Client
	relays                         map[string]relay
	clientID, clientSecret, appKey string
}

func New(path string) (*Client, error) {
	s, err := readState(path)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// 云端接口直连，避免继承容器中用于下载或构建的代理配置。
	transport.Proxy = nil
	c := &Client{state: s, path: path, files: localStateFiles(), gate: make(chan struct{}, 1), relays: make(map[string]relay),
		http: &http.Client{Transport: transport, Timeout: 20 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		clientID: "Xp6vsxz_7IYVw2BB", clientSecret: "Xp6vsy4tN9toTVdMSpomVdXpRmES", appKey: "34a062aaa22f906fca4fefe9fb3a3021"}
	if err = c.save(); err != nil {
		return nil, err
	}
	return c, nil
}

// Close 等待当前操作结束并拒绝后续操作，防止退出隔离环境后仍写入状态文件。
func (c *Client) Close() {
	c.gate <- struct{}{}
	defer c.unlock()
	c.closed = true
	c.http.CloseIdleConnections()
}
func (c *Client) lock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case c.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			c.unlock()
			return err
		}
		if c.closed {
			c.unlock()
			return failure("unavailable", "远程下载服务正在退出")
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (c *Client) unlock() { <-c.gate }

type object = map[string]any

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return ""
}
func obj(v any) object {
	if x, ok := v.(map[string]any); ok {
		return x
	}
	return object{}
}
func items(v any) []any {
	if x, ok := v.([]any); ok {
		return x
	}
	return nil
}
func first(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func businessError(d object) error {
	var code int64
	successCode := false
	for _, key := range []string{"error_code", "errorCode"} {
		if value, present := d[key]; present {
			n, err := integer(value)
			if err != nil {
				return failure("upstream", "迅雷返回了无效的错误码，请稍后重试")
			}
			if code == 0 {
				code = n
			}
			successCode = successCode || n == 0
		}
	}
	name := strings.ToLower(strings.TrimSpace(str(d["error"])))
	if value, present := d["error"]; present {
		switch value.(type) {
		case string:
		case json.Number, float64:
			n, err := integer(value)
			if err != nil {
				return failure("upstream", "迅雷返回了无效的错误标识，请稍后重试")
			}
			name = strconv.FormatInt(n, 10)
		default:
			return failure("upstream", "迅雷返回了无效的错误标识，请稍后重试")
		}
	}
	// 成功响应可能只带文字或零值标识，省略数字错误码；说明文字本身不代表失败。
	successName := name == "success" || name == "ok" || name == "0"
	hasDescription := str(d["error_description"]) != "" || str(d["errorDesc"]) != ""
	if code == 0 && (name == "" || successName) && (successCode || successName || !hasDescription) {
		return nil
	}
	e := &Error{Kind: "upstream", RemoteCode: int(code), Message: fmt.Sprintf("迅雷接口拒绝请求（错误码 %d），请稍后重试", code)}
	switch {
	case code == 4122 || code == 4121 || code == 10 || code == 16 || name == "invalid_grant" || name == "invalid_token":
		e.Kind, e.Message = "login_required", "登录已失效，请重新登录迅雷账号"
	case code == 9:
		e.Kind, e.Message = "captcha_required", "人机令牌校验失败，请重新登录；这与短信验证码是否正确无关"
	case name == "review_panel":
		e.Kind, e.Message = "review_required", "本次登录需要短信验证，请获取并填写验证码"
	case code == 13 || name == "userinfo_expired":
		e.Kind, e.Message = "review_expired", "验证会话已失效，请重新获取验证码"
	case code == 2 || name == "external_unknown_err":
		e.Kind, e.Message = "device_unavailable", "远程设备暂时没有响应，请检查设备网络和迅雷插件后重试"
	}
	return e
}

func (c *Client) raw(ctx context.Context, method, endpoint string, query url.Values, body any, headers http.Header) (object, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, failure("upstream", "迅雷接口地址无效")
	}
	q := u.Query()
	for k, v := range query {
		q[k] = v
	}
	u.RawQuery = q.Encode()
	var reader io.Reader
	if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			return nil, e
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return nil, failure("upstream", "无法创建迅雷请求")
	}
	req.Header = headers
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, failure("network", "无法连接迅雷服务，请检查网络和域名解析")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent && (method == http.MethodDelete || method == http.MethodPatch) {
		return object{}, nil
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, failure("network", "读取迅雷响应失败，请稍后重试")
	}
	if len(b) > 8<<20 {
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, failure("login_required", "迅雷登录已失效，请重新登录")
		}
		return nil, failure("upstream", "迅雷响应过大，已停止读取")
	}
	var d object
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	err = dec.Decode(&d)
	if resp.StatusCode == http.StatusUnauthorized {
		// 明确的验证流程优先；无效响应或其他业务错误不能覆盖认证失效。
		if err == nil && d != nil {
			if e, ok := businessError(d).(*Error); ok {
				switch e.Kind {
				case "login_required", "captcha_required", "review_required", "review_expired":
					return d, e
				}
			}
		}
		return d, failure("login_required", "迅雷登录已失效，请重新登录")
	}
	if err != nil || d == nil {
		return nil, failure("upstream", fmt.Sprintf("迅雷返回了无效响应（HTTP %d）", resp.StatusCode))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if e := businessError(d); e != nil {
			return d, e
		}
		return d, failure("upstream", fmt.Sprintf("迅雷服务暂时不可用（HTTP %d）", resp.StatusCode))
	}
	return d, businessError(d)
}

func (c *Client) request(ctx context.Context, method, endpoint string, query url.Values, body any) (object, error) {
	if err := c.ensureToken(ctx); err != nil {
		return nil, err
	}
	refreshed, captcha, retried := false, false, false
	for {
		d, err := c.raw(ctx, method, endpoint, query, body, c.headers(true))
		var e *Error
		if !errors.As(err, &e) {
			return d, err
		}
		switch {
		case e.Kind == "login_required" && !refreshed:
			refreshed = true
			if err = c.refresh(ctx); err != nil {
				return nil, err
			}
		case e.Kind == "captcha_required" && !captcha:
			captcha = true
			u, _ := url.Parse(endpoint)
			if err = c.captcha(ctx, method+":"+u.Path, false); err != nil {
				return nil, err
			}
		case method == http.MethodGet && !retried && (e.Kind == "device_unavailable" || e.Kind == "network"):
			retried = true
			select {
			case <-time.After(300 * time.Millisecond):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		default:
			return d, err
		}
	}
}

func trustedURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	host := u.Hostname()
	// 区域标识属于数值地址，不能把其中的文字当作可信域名。
	if _, err := netip.ParseAddr(host); err == nil || len(host) > 253 {
		return false
	}
	// 只接受合法的主机标签，避免无效主机仅凭域名后缀通过校验。
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-') {
				return false
			}
		}
	}
	host = strings.ToLower(host)
	for _, domain := range []string{"xunlei.com", "sandai.net", "xunleix.com"} {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

func maskedAccount(s string) string {
	r := []rune(s)
	if len(r) < 7 {
		return "已绑定手机号"
	}
	return string(r[:3]) + "****" + string(r[len(r)-4:])
}
