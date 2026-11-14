// Package web provides a simple HTTP multiplexer with middleware support.
package web

import (
	"cmp"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net"
	"net/http"

	"github.com/cnk3x/xunlei/pkg/utils"
	"github.com/go-chi/chi/v5"
)

// Middleware 定义中间件处理器函数类型，接收下一个处理器并返回包装后的处理器
type Middleware = func(http.Handler) http.Handler

// Mux 表示一个HTTP请求多路复用器，支持中间件处理链
type Mux struct {
	chi.Router                   // 当前多路复用器
	Timeouts   Timeouts          // 连接与服务关闭期限
	onShutdown func()            // 关闭时执行的回调函数
	onStarted  func(addr string) // 监听启动的回调
}

// Router 接口定义路由的基本操作
type Router interface {
	Handle(pattern string, handler http.Handler)
}

// NewMux 创建一个新的Mux实例，默认包含Recoverer中间件
func NewMux() *Mux {
	return &Mux{Router: chi.NewMux()}
}

// OnShutDown 设置服务器关闭时的回调函数
//   - fn: 服务器关闭时执行的函数
func (mux *Mux) OnShutDown(fn func())           { mux.onShutdown = fn }
func (mux *Mux) OnStarted(fn func(addr string)) { mux.onStarted = fn }

func (mux *Mux) BasicAuth(user, pwd string) *Mux { mux.Use(BasicAuth(user, pwd)); return mux }

func (mux *Mux) Recoverer() *Mux { mux.Use(Recoverer); return mux }

// Redirect 创建重定向处理器
//   - to: 重定向目标URL
//   - permanent: 是否永久重定向，默认临时重定向
//
// 返回HTTP处理器函数
func Redirect(to string, permanent ...bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, to, utils.Iif(cmp.Or(permanent...), http.StatusPermanentRedirect, http.StatusTemporaryRedirect))
	}
}

// Blob 创建返回字节切片或字符串内容的处理器
//   - body: 响应体内容
//   - contentType: 内容类型
//   - status: HTTP状态码
//
// 返回HTTP处理器函数
func Blob[T ~[]byte | ~string](body T, contentType string, status int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		w.Write([]byte(body))
	}
}

func JSON(body any, status int) http.HandlerFunc {
	data, err := json.Marshal(body)
	if err != nil {
		return func(w http.ResponseWriter, r *http.Request) { internalError(w, r, err) }
	}
	return Blob(data, "application/json", status)
}

func FBlob[T ~[]byte | ~string](body func() (T, error), contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, err := body()
		if err != nil {
			internalError(w, r, err)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(data))
	}
}

func FJSON[T any](body func() (T, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		val, err := body()
		if err != nil {
			internalError(w, r, err)
			return
		}

		data, err := json.Marshal(val)
		if err != nil {
			internalError(w, r, err)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write(data)
	}
}

// BasicAuth 创建基础认证中间件
//   - username: 用户名
//   - password: 密码
//
// 返回认证处理器
func BasicAuth(username, password string) Middleware {
	if password != "" {
		if username == "" {
			username = "admin"
		}
		usernameHash := sha256.Sum256([]byte(username))
		passwordHash := sha256.Sum256([]byte(password))
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				user, pass, ok := r.BasicAuth()
				userHash := sha256.Sum256([]byte(user))
				passHash := sha256.Sum256([]byte(pass))
				// 两项均比较固定长度摘要，避免用户名不匹配时跳过密码校验。
				matches := subtle.ConstantTimeCompare(userHash[:], usernameHash[:]) &
					subtle.ConstantTimeCompare(passHash[:], passwordHash[:])
				if !ok || matches != 1 {
					w.Header().Add("WWW-Authenticate", fmt.Sprintf(`Basic realm="%s"`, "xlp"))
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				next.ServeHTTP(w, r)
			})
		}
	}
	return func(next http.Handler) http.Handler { return next }
}

// Address 组合IP地址和端口为网络地址字符串
//   - ip: IP地址
//   - port: 端口号
//
// 返回组合的网络地址字符串
func Address[T utils.UintT | utils.IntT](ip net.IP, port T) string {
	sIp := ip.String()
	return net.JoinHostPort(utils.Iif(sIp == "<nil>", "", sIp), utils.String(port))
}
