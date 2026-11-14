package remoteweb

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestPanel按HTML上下文保留原字节注入(t *testing.T) {
	for _, tc := range []struct {
		name, before, after string
	}{
		{"缩短字节的字符", `<html><head><title>İK</title>`, `</head><body>正文</body></html>`},
		{"增长字节的字符", `<html><head><title>` + strings.Repeat("Ⱥ", 100) + `</title>`, `</head><body>正文</body></html>`},
		{"大小写与空白", `<HTML><HEAD><title>中文</title>`, "</HeAd \t><BODY></BODY></HTML>"},
		{"脚本中的头标签", `<html><head><script>const marker="</head>";</script>`, `</head><body></body></html>`},
		{"脚本中的体标签", `<html><body><script>const marker="</body>";</script>`, `</body></html>`},
		{"脚本转义状态", `<html><head><script><!--<script></script>const marker="</head>";--></script>`, `</head><body></body></html>`},
		{"注释中的标签", `<!doctype html><!-- </head> --><html><head><!-- </head> -->`, `</head><body></body></html>`},
		{"属性中的标签", `<html><head><meta content="</head>" data-other='</body>'>`, `</head><body></body></html>`},
		{"标题中的标签", `<html><head><title>İK </head> 正文</title>`, `</head><body></body></html>`},
		{"样式中的标签", `<html><head><style>p::after { content: "</head>"; }</style>`, `</head><body></body></html>`},
		{"文本框中的标签", `<html><body><textarea></head></body></textarea>`, `</body></html>`},
		{"模板中的标签", `<html><body><template><template></head></template></body></template>`, `</body></html>`},
		{"禁用脚本替代内容", `<html><head><noscript></head></noscript>`, `</head><body></body></html>`},
		{"脚本中的扩展编号", `<html><head><script>const marker='id="xlp-remote-extension"';</script>`, `</head><body></body></html>`},
		{"注释中的扩展编号", `<html><head><!-- id="xlp-remote-extension" -->`, `</head><body></body></html>`},
		{"已有单引号扩展", `<html><head><script ID='xlp-remote-extension' src='/remote/panel.js'></script></head><body></body></html>`, ""},
		{"已有无引号扩展", `<html><head><script id=xlp-remote-extension src='/remote/panel.js'></script></head><body></body></html>`, ""},
		{"只有脚本伪标签", `<script>const marker="</head></body>";</script>`, ""},
		{"只有注释伪标签", `<!-- </head></body> -->`, ""},
		{"未闭合脚本", `<html><head><script>const marker="</head>";`, ""},
		{"纯文本模式", `<html><body><plaintext></head></body>`, ""},
	} {
		for _, encoding := range []string{"identity", "gzip"} {
			t.Run(tc.name+"/"+encoding, func(t *testing.T) {
				defer func() {
					if value := recover(); value != nil {
						t.Fatalf("合法页面处理发生异常：%v", value)
					}
				}()
				original := []byte(tc.before + tc.after)
				body := original
				if encoding == "gzip" {
					body = panelGzip(t, original)
				}
				w := panelHTMLResponse(body, encoding)
				want := body
				if tc.after != "" {
					want = []byte(tc.before + panelExtension + tc.after)
				}
				if !bytes.Equal(w.Body.Bytes(), want) {
					t.Fatalf("注入位置或原文字节改变：实际 %q，期望 %q", w.Body.Bytes(), want)
				}
				header := w.Result().Header
				if tc.after != "" {
					if header.Get("Content-Encoding") != "" || header.Get("Content-Length") != "" || header.Get("ETag") != "" || header.Get("Last-Modified") != "" || header.Get("Cache-Control") != "no-store" {
						t.Fatalf("扩展后的元信息未更新：%v", header)
					}
				} else if header.Get("Content-Encoding") != encoding || header.Get("Content-Length") != strconv.Itoa(len(body)) || header.Get("ETag") != `"original"` {
					t.Fatalf("未注入页面的元信息被改变：%v", header)
				}
			})
		}
	}
}

func TestPanel保留压缩和解压大小边界(t *testing.T) {
	original := []byte(`<html><head>` + strings.Repeat("x", maxPanelSize) + `</head><body></body></html>`)
	for _, tc := range []struct {
		name, encoding string
		body           []byte
	}{
		{"原文超限", "identity", original},
		{"解压超限", "gzip", panelGzip(t, original)},
		{"损坏压缩", "gzip", []byte(`<html><head></head></html>`)},
		{"未支持编码", "br", []byte(`<html><head></head></html>`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := panelHTMLResponse(tc.body, tc.encoding)
			if !bytes.Equal(w.Body.Bytes(), tc.body) || w.Result().Header.Get("Content-Encoding") != tc.encoding || w.Result().Header.Get("Content-Length") != strconv.Itoa(len(tc.body)) {
				t.Fatal("超限或不能解码的页面没有原样透传")
			}
		})
	}
	boundary := []byte(`<html><head>` + strings.Repeat("x", maxPanelSize-len(`<html><head></head></html>`)) + `</head></html>`)
	for _, encoding := range []string{"identity", "gzip"} {
		t.Run("恰好上限/"+encoding, func(t *testing.T) {
			body := boundary
			if encoding == "gzip" {
				body = panelGzip(t, body)
			}
			w := panelHTMLResponse(body, encoding)
			want := bytes.Replace(boundary, []byte("</head>"), []byte(panelExtension+"</head>"), 1)
			if !bytes.Equal(w.Body.Bytes(), want) {
				t.Fatal("大小恰好达到上限的页面未正确扩展")
			}
		})
	}
}

func panelGzip(t *testing.T, body []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	w := gzip.NewWriter(&compressed)
	if _, err := w.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return compressed.Bytes()
}

func panelHTMLResponse(body []byte, encoding string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	Panel(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		out.Header().Set("Content-Type", "text/html; charset=utf-8")
		out.Header().Set("Content-Encoding", encoding)
		out.Header().Set("Content-Length", strconv.Itoa(len(body)))
		out.Header().Set("ETag", `"original"`)
		out.Header().Set("Last-Modified", "Mon, 02 Jan 2006 15:04:05 GMT")
		_, _ = out.Write(body)
	})).ServeHTTP(w, httptest.NewRequest(http.MethodGet, PanelPath, nil))
	return w
}
