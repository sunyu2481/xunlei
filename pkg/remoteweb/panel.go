package remoteweb

import (
	"bytes"
	"compress/gzip"
	"io"
	"maps"
	"net/http"
	"strings"

	"golang.org/x/net/html"
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
		response := &panelResponse{ResponseWriter: w}
		next.ServeHTTP(response, request)
		if response.status == 0 {
			response.WriteHeader(http.StatusOK)
		}
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
				if at := panelInsertionOffset(decoded); at >= 0 && at <= len(decoded) {
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

// 词法器只负责定位，按原始词元长度累加偏移，避免重写页面或改变 Unicode 字节。
func panelInsertionOffset(body []byte) int {
	tokens := html.NewTokenizer(bytes.NewReader(body))
	head, endBody, offset, templates := -1, -1, 0, 0
	for {
		kind := tokens.Next()
		at := offset
		offset += len(tokens.Raw())
		switch kind {
		case html.ErrorToken:
			if tokens.Err() != io.EOF {
				return -1
			}
			if head >= 0 {
				return head
			}
			return endBody
		case html.StartTagToken, html.SelfClosingTagToken:
			token := tokens.Token()
			if token.Data == "template" {
				templates++
			}
			if templates > 0 {
				continue
			}
			for _, attr := range token.Attr {
				if attr.Key == "id" && attr.Val == "xlp-remote-extension" {
					return -1
				}
			}
		case html.EndTagToken:
			name, _ := tokens.TagName()
			if templates > 0 {
				if string(name) == "template" {
					templates--
				}
				continue
			}
			switch string(name) {
			case "head":
				if head < 0 {
					head = at
				}
			case "body":
				if endBody < 0 {
					endBody = at
				}
			}
		}
	}
}

type panelResponse struct {
	http.ResponseWriter
	header http.Header
	status int
	body   bytes.Buffer
	passed bool
}

func (w *panelResponse) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	// 101 结束 HTTP 响应，其余信息响应不确定最终状态，也不冻结响应头。
	if status >= 100 && status < 200 && status != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	w.status = status
	if status != http.StatusOK || !strings.HasPrefix(strings.ToLower(w.Header().Get("Content-Type")), "text/html") {
		w.passed = true
		w.ResponseWriter.WriteHeader(status)
		return
	}
	w.header = w.Header().Clone()
}

func (w *panelResponse) writeHeaders() {
	// 延迟发送的最终头采用首次 WriteHeader 的快照，随后恢复实时头以保留尾部字段。
	header := w.Header()
	live := header.Clone()
	clear(header)
	maps.Copy(header, w.header)
	w.ResponseWriter.WriteHeader(w.status)
	clear(header)
	maps.Copy(header, live)
}

func (w *panelResponse) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.passed {
		return w.ResponseWriter.Write(b)
	}
	if w.body.Len()+len(b) > maxPanelSize {
		if err := w.passThrough(); err != nil {
			return 0, err
		}
		return w.ResponseWriter.Write(b)
	}
	return w.body.Write(b)
}

func (w *panelResponse) passThrough() error {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.passed {
		return nil
	}
	w.passed = true
	w.writeHeaders()
	_, err := w.body.WriteTo(w.ResponseWriter)
	return err
}

// 刷新意味着立即结束注入缓冲，此后正文和响应头按原处理器的行为透传。
func (w *panelResponse) FlushError() error {
	if err := w.passThrough(); err != nil {
		return err
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *panelResponse) Flush() { _ = w.FlushError() }

func (w *panelResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }
