// Package remoteweb 提供远程下载管理页及同源接口。
package remoteweb

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/cnk3x/xunlei/pkg/remote"
)

//go:embed static/*
var assets embed.FS

// Service 允许网页层通过模拟服务独立验证路由、认证边界及交互。
type Service interface {
	Status() remote.Status
	Login(context.Context, string, string, bool) error
	Logout(context.Context) error
	SendSMS(context.Context) error
	CheckSMS(context.Context, string) error
	Devices(context.Context) ([]remote.Device, error)
	Configure(context.Context, remote.Settings) error
	Folders(context.Context, string, string) ([]remote.Folder, error)
	Tasks(context.Context, string, string) (remote.TaskPage, error)
	Create(context.Context, remote.CreateRequest) (remote.CreateResult, error)
	TaskAction(context.Context, string, string, string) error
}

type endpoint func(http.ResponseWriter, *http.Request) (any, error)

func New(service Service) http.Handler {
	mux := http.NewServeMux()
	register := func(pattern string, fn endpoint) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			if r.Method != "GET" && (r.Header.Get("X-XLP-Request") != "1" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json")) {
				writeError(w, &remote.Error{Kind: "forbidden", Message: "请求来源无效，请从管理页面重试"})
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
			defer cancel()
			value, err := fn(w, r.WithContext(ctx))
			if err != nil {
				writeError(w, err)
				return
			}
			if value == nil {
				value = map[string]bool{"ok": true}
			}
			writeJSON(w, http.StatusOK, value)
		})
	}
	register("GET /api/remote/status", func(w http.ResponseWriter, r *http.Request) (any, error) { return service.Status(), nil })
	register("POST /api/remote/login", func(w http.ResponseWriter, r *http.Request) (any, error) {
		var body struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Remember bool   `json:"remember_password"`
		}
		if err := decode(w, r, &body); err != nil {
			return nil, err
		}
		err := service.Login(r.Context(), body.Username, body.Password, body.Remember)
		return service.Status(), err
	})
	register("POST /api/remote/logout", func(w http.ResponseWriter, r *http.Request) (any, error) { return nil, service.Logout(r.Context()) })
	register("POST /api/remote/review/send", func(w http.ResponseWriter, r *http.Request) (any, error) { return nil, service.SendSMS(r.Context()) })
	register("POST /api/remote/review/check", func(w http.ResponseWriter, r *http.Request) (any, error) {
		var body struct {
			Code string `json:"code"`
		}
		if err := decode(w, r, &body); err != nil {
			return nil, err
		}
		return nil, service.CheckSMS(r.Context(), body.Code)
	})
	register("GET /api/remote/devices", func(w http.ResponseWriter, r *http.Request) (any, error) { return service.Devices(r.Context()) })
	register("PUT /api/remote/settings", func(w http.ResponseWriter, r *http.Request) (any, error) {
		var body remote.Settings
		if err := decode(w, r, &body); err != nil {
			return nil, err
		}
		return nil, service.Configure(r.Context(), body)
	})
	register("GET /api/remote/folders", func(w http.ResponseWriter, r *http.Request) (any, error) {
		return service.Folders(r.Context(), r.URL.Query().Get("target"), r.URL.Query().Get("path"))
	})
	register("GET /api/remote/tasks", func(w http.ResponseWriter, r *http.Request) (any, error) {
		return service.Tasks(r.Context(), r.URL.Query().Get("target"), r.URL.Query().Get("page_token"))
	})
	register("POST /api/remote/tasks", func(w http.ResponseWriter, r *http.Request) (any, error) {
		var body remote.CreateRequest
		if err := decode(w, r, &body); err != nil {
			return nil, err
		}
		return service.Create(r.Context(), body)
	})
	register("POST /api/remote/tasks/{id}/{action}", func(w http.ResponseWriter, r *http.Request) (any, error) {
		var body struct {
			Target string `json:"target"`
		}
		if err := decode(w, r, &body); err != nil {
			return nil, err
		}
		return nil, service.TaskAction(r.Context(), body.Target, r.PathValue("id"), r.PathValue("action"))
	})
	static, _ := fs.Sub(assets, "static")
	mux.HandleFunc("GET /remote/{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, PanelPath+"#/home?xlpRemote=1", http.StatusTemporaryRedirect)
	})
	mux.Handle("GET /remote/", http.StripPrefix("/remote/", http.FileServer(http.FS(static))))
	protected := http.NewCrossOriginProtection().Handler(mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'self'; form-action 'self'")
		protected.ServeHTTP(w, r)
	})
}

func decode(w http.ResponseWriter, r *http.Request, out any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return &remote.Error{Kind: "invalid_request", Message: "请求内容无效或过大"}
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return &remote.Error{Kind: "invalid_request", Message: "请求只能包含一份数据"}
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, err error) {
	e := &remote.Error{Kind: "internal", Message: "操作失败，请稍后重试"}
	status := http.StatusBadGateway
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		e = &remote.Error{Kind: "timeout", Message: "请求超时，请稍后重试"}
		status = http.StatusGatewayTimeout
	} else {
		var detail *remote.Error
		if errors.As(err, &detail) {
			e = detail
		}
	}
	switch e.Kind {
	case "invalid_request", "directory_required", "device_required":
		status = 400
	case "forbidden":
		status = 403
	case "not_found":
		status = 404
	case "settings_changed", "submission_unknown":
		status = 409
	case "login_required", "review_required", "review_expired", "captcha_required", "verification_required":
		status = 422
	case "rate_limited":
		status = 429
	}
	writeJSON(w, status, e)
}
