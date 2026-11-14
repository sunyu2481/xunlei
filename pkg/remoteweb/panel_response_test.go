package remoteweb

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPanel刷新输出全部缓冲并继续透传(t *testing.T) {
	for _, tc := range []struct {
		name, path, contentType, first string
		buffered                       bool
	}{
		{"非首页", PanelPath + "file", "application/octet-stream", "首块", false},
		{"非网页首页", PanelPath, "application/octet-stream", "首块", false},
		{"缓冲网页", PanelPath, "text/html", "<html><head>", true},
		{"超限网页", PanelPath, "text/html", strings.Repeat("x", maxPanelSize+1), false},
		{"正文前刷新", PanelPath, "text/html", "", true},
	} {
		for _, controller := range []bool{false, true} {
			name := "接口"
			if controller {
				name = "响应控制器"
			}
			t.Run(tc.name+"/"+name, func(t *testing.T) {
				w := httptest.NewRecorder()
				Panel(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
					out.Header().Set("Content-Type", tc.contentType)
					out.Header().Set("ETag", `"stream"`)
					if tc.first != "" {
						if _, err := io.WriteString(out, tc.first); err != nil {
							t.Fatal(err)
						}
					}
					if tc.buffered && w.Body.Len() != 0 {
						t.Fatal("刷新前提前输出了网页缓冲")
					}
					if controller {
						if err := http.NewResponseController(out).Flush(); err != nil {
							t.Fatalf("响应控制器无法刷新：%v", err)
						}
					} else {
						flusher, ok := out.(http.Flusher)
						if !ok {
							t.Fatal("包装隐藏了底层刷新接口")
						}
						flusher.Flush()
					}
					if !w.Flushed || w.Body.String() != tc.first {
						t.Fatal("刷新时未输出全部已有正文")
					}
					if _, err := io.WriteString(out, "</head><body>后续</body></html>"); err != nil {
						t.Fatal(err)
					}
					if w.Body.String() != tc.first+"</head><body>后续</body></html>" {
						t.Fatal("刷新后的正文仍被缓冲或修改")
					}
				})).ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
				if w.Body.String() != tc.first+"</head><body>后续</body></html>" || w.Result().Header.Get("ETag") != `"stream"` {
					t.Fatal("刷新后的正文被重复输出或响应头被修改")
				}
			})
		}
	}
}

type panelControllerWriter struct {
	*httptest.ResponseRecorder
	flushErr error
	writeErr error
	deadline time.Time
}

func (w *panelControllerWriter) FlushError() error { return w.flushErr }
func (w *panelControllerWriter) Write(body []byte) (int, error) {
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return w.ResponseRecorder.Write(body)
}
func (w *panelControllerWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadline = deadline
	return nil
}

func TestPanel保留响应控制与刷新错误(t *testing.T) {
	for _, failWrite := range []bool{false, true} {
		w := &panelControllerWriter{ResponseRecorder: httptest.NewRecorder()}
		want := errors.New("模拟刷新失败")
		w.flushErr = want
		if failWrite {
			want = errors.New("模拟正文写入失败")
			w.writeErr = want
		}
		Panel(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
			controller := http.NewResponseController(out)
			deadline := time.Now().Add(time.Minute)
			if err := controller.SetWriteDeadline(deadline); err != nil || !w.deadline.Equal(deadline) {
				t.Errorf("底层截止时间能力丢失：%v", err)
			}
			out.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(out, "<html><head>")
			if err := controller.Flush(); !errors.Is(err, want) {
				t.Errorf("刷新没有返回底层错误：实际 %v，期望 %v", err, want)
			}
		})).ServeHTTP(w, httptest.NewRequest(http.MethodGet, PanelPath, nil))
	}
}

func TestPanel信息响应后仍发送最终状态和响应头(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		implicit bool
	}{
		{"显式成功", http.StatusOK, false},
		{"隐式成功", http.StatusOK, true},
		{"其他最终状态", http.StatusCreated, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "<html><head></head><body>正文</body></html>"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Outer", "kept")
				Panel(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
					out.Header().Set("X-Phase", "early")
					out.Header().Set("Link", "</first.css>; rel=preload")
					out.WriteHeader(http.StatusEarlyHints)
					out.Header().Set("X-Phase", "processing")
					out.Header().Del("Link")
					out.WriteHeader(http.StatusProcessing)
					out.Header().Set("X-Phase", "final")
					out.Header().Set("Content-Type", "text/html; charset=utf-8")
					out.Header().Add("Set-Cookie", "first=1")
					out.Header().Add("Set-Cookie", "second=2")
					if !tc.implicit {
						out.WriteHeader(tc.status)
					}
					_, _ = io.WriteString(out, body)
					out.Header().Set("X-Phase", "too-late")
					out.Header().Set("X-Too-Late", "ignored")
					out.WriteHeader(http.StatusBadGateway)
				})).ServeHTTP(w, r)
			}))
			defer server.Close()
			var codes []int
			var headers []http.Header
			trace := &httptrace.ClientTrace{Got1xxResponse: func(code int, header textproto.MIMEHeader) error {
				codes = append(codes, code)
				headers = append(headers, http.Header(header).Clone())
				return nil
			}}
			request, err := http.NewRequest(http.MethodGet, server.URL+PanelPath, nil)
			if err != nil {
				t.Fatal(err)
			}
			request = request.WithContext(httptrace.WithClientTrace(request.Context(), trace))
			client := server.Client()
			client.Timeout = 5 * time.Second
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			got, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(codes, []int{http.StatusEarlyHints, http.StatusProcessing}) {
				t.Errorf("信息响应丢失：%v", codes)
			} else if headers[0].Get("X-Phase") != "early" || headers[0].Get("Link") == "" || headers[1].Get("X-Phase") != "processing" || headers[1].Get("Link") != "" {
				t.Errorf("信息响应没有使用各阶段的响应头：%v", headers)
			}
			if response.StatusCode != tc.status || response.Header.Get("X-Phase") != "final" || response.Header.Get("X-Too-Late") != "" || response.Header.Get("Link") != "" || response.Header.Get("X-Outer") != "kept" || len(response.Cookies()) != 2 {
				t.Errorf("最终状态或响应头不正确：状态 %d，响应头 %v", response.StatusCode, response.Header)
			}
			want := body
			if tc.status == http.StatusOK {
				want = strings.Replace(body, "</head>", panelExtension+"</head>", 1)
			}
			if string(got) != want {
				t.Errorf("信息响应干扰最终正文：%q", got)
			}
		})
	}
}

func TestPanel压缩响应刷新后原样透传(t *testing.T) {
	body := panelGzip(t, []byte("<html><head></head><body>正文</body></html>"))
	w := httptest.NewRecorder()
	Panel(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		out.Header().Set("Content-Type", "text/html")
		out.Header().Set("Content-Encoding", "gzip")
		_, _ = out.Write(body[:5])
		if err := http.NewResponseController(out).Flush(); err != nil {
			t.Fatal(err)
		}
		if !w.Flushed || !bytes.Equal(w.Body.Bytes(), body[:5]) {
			t.Fatal("刷新没有先输出尚未完整的压缩数据")
		}
		_, _ = out.Write(body[5:])
	})).ServeHTTP(w, httptest.NewRequest(http.MethodGet, PanelPath, nil))
	if !bytes.Equal(w.Body.Bytes(), body) || w.Result().Header.Get("Content-Encoding") != "gzip" {
		t.Fatal("压缩流刷新后被解码或重复写入")
	}
}

func TestPanel最终响应保留尾部字段(t *testing.T) {
	for _, flushed := range []bool{false, true} {
		server := httptest.NewServer(Panel(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
			header := out.Header()
			header.Set("Content-Type", "text/html")
			header.Set("Trailer", "X-Result")
			_, _ = io.WriteString(out, "<html><head></head><body>正文</body></html>")
			if flushed {
				if err := http.NewResponseController(out).Flush(); err != nil {
					t.Errorf("刷新失败：%v", err)
				}
			}
			header.Set("X-Result", "complete")
			header.Set(http.TrailerPrefix+"X-Late", "kept")
		})))
		client := server.Client()
		client.Timeout = 5 * time.Second
		response, err := client.Get(server.URL + PanelPath)
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		_, err = io.ReadAll(response.Body)
		response.Body.Close()
		server.Close()
		if err != nil {
			t.Fatal(err)
		}
		if response.Trailer.Get("X-Result") != "complete" || response.Trailer.Get("X-Late") != "kept" || response.Header.Get("X-Result") != "" {
			t.Fatalf("刷新 %v 时尾部字段丢失或提前发送：头 %v，尾部 %v", flushed, response.Header, response.Trailer)
		}
	}
}

func TestPanel协议切换不再发送最终网页(t *testing.T) {
	w := httptest.NewRecorder()
	Panel(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		out.Header().Set("Content-Type", "text/html")
		out.WriteHeader(http.StatusSwitchingProtocols)
		out.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(out, "<html><head></head></html>")
	})).ServeHTTP(w, httptest.NewRequest(http.MethodGet, PanelPath, nil))
	if w.Code != http.StatusSwitchingProtocols || w.Body.String() != "<html><head></head></html>" {
		t.Fatal("协议切换被当成普通信息响应继续处理")
	}
}
