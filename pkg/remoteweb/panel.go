package remoteweb

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"strings"
)

const PanelPath = "/webman/3rdparty/pan-xunlei-com/index.cgi/"
const panelExtension = `<link rel="stylesheet" href="/remote/panel.css"><script id="xlp-remote-extension" src="/remote/panel.js" defer></script>`
const maxPanelSize = 4 << 20

// Panel 只扩展原版首页的 HTML，其他 CGI 接口和文件流直接交给原处理器。
func Panel(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || (r.URL.Path != PanelPath && r.URL.Path != strings.TrimSuffix(PanelPath, "/")) {
			next.ServeHTTP(w, r)
			return
		}
		request := r.Clone(r.Context())
		request.Header.Del("If-None-Match")
		request.Header.Del("If-Modified-Since")
		request.Header.Del("Range")
		request.Header.Set("Accept-Encoding", "identity")
		response := &panelResponse{ResponseWriter: w, header: make(http.Header)}
		next.ServeHTTP(response, request)
		if response.passed {
			return
		}
		body := response.body.Bytes()
		if response.status == http.StatusOK && strings.HasPrefix(strings.ToLower(response.header.Get("Content-Type")), "text/html") {
			decoded := body
			encoding := response.header.Get("Content-Encoding")
			if encoding == "gzip" {
				if reader, err := gzip.NewReader(bytes.NewReader(body)); err == nil {
					value, err := io.ReadAll(io.LimitReader(reader, maxPanelSize+1))
					reader.Close()
					if err == nil && len(value) <= maxPanelSize {
						decoded = value
						encoding = ""
					}
				}
			}
			if encoding == "" || encoding == "identity" {
				lower := bytes.ToLower(decoded)
				at := bytes.Index(lower, []byte("</head>"))
				if at < 0 {
					at = bytes.Index(lower, []byte("</body>"))
				}
				if at >= 0 && !bytes.Contains(decoded, []byte(`id="xlp-remote-extension"`)) {
					body = make([]byte, 0, len(decoded)+len(panelExtension))
					body = append(body, decoded[:at]...)
					body = append(body, panelExtension...)
					body = append(body, decoded[at:]...)
					response.header.Del("Content-Encoding")
					response.header.Del("Content-Length")
					response.header.Del("ETag")
					response.header.Del("Last-Modified")
					response.header.Set("Cache-Control", "no-store")
				}
			}
		}
		response.writeHeaders()
		_, _ = w.Write(body)
	})
}

type panelResponse struct {
	http.ResponseWriter
	header http.Header
	status int
	body   bytes.Buffer
	passed bool
}

func (w *panelResponse) Header() http.Header { return w.header }
func (w *panelResponse) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	if status != http.StatusOK || !strings.HasPrefix(strings.ToLower(w.header.Get("Content-Type")), "text/html") {
		w.passed = true
		w.writeHeaders()
	}
}
func (w *panelResponse) writeHeaders() {
	for key, values := range w.header {
		w.ResponseWriter.Header()[key] = values
	}
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *panelResponse) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.passed {
		return w.ResponseWriter.Write(b)
	}
	if w.body.Len()+len(b) > maxPanelSize {
		w.passed = true
		w.writeHeaders()
		if _, err := w.ResponseWriter.Write(w.body.Bytes()); err != nil {
			return 0, err
		}
		w.body.Reset()
		return w.ResponseWriter.Write(b)
	}
	return w.body.Write(b)
}
