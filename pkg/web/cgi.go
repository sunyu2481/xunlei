// 本文件改编自 Go 标准库的网页通用网关处理器，版权归原作者所有。
// 原始许可见 LICENSE.go-cgi；扩展部分用于子进程身份与取消回收。
package web

import (
	"bufio"
	"context"
	"fmt"
	"github.com/cnk3x/xunlei/pkg/cmdx"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cgi"
	"net/textproto"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// CGIHandler 保留标准库的请求和响应语义，在执行网关程序前设置完整凭据。
type CGIHandler struct {
	cgi.Handler
	Credential *syscall.Credential
	Timeout    time.Duration // 单次网关请求期限，默认九十秒
}

var trailingPort = regexp.MustCompile(`:([0-9]+)$`)

func (h *CGIHandler) stderr() io.Writer {
	if h.Stderr != nil {
		return h.Stderr
	}
	return os.Stderr
}

func removeLeadingDuplicates(env []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(env))
	for i := len(env) - 1; i >= 0; i-- {
		key, _, _ := strings.Cut(env[i], "=")
		if !seen[key] {
			result = append(result, env[i])
			seen[key] = true
		}
	}
	return result
}

func validCGIHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c) {
			continue
		}
		return false
	}
	return true
}

func (h *CGIHandler) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	if len(req.TransferEncoding) > 0 && req.TransferEncoding[0] == "chunked" {
		rw.WriteHeader(http.StatusBadRequest)
		rw.Write([]byte("Chunked request bodies are not supported by CGI."))
		return
	}

	root := strings.TrimRight(h.Root, "/")
	pathInfo := strings.TrimPrefix(req.URL.Path, root)

	port := "80"
	if req.TLS != nil {
		port = "443"
	}
	if matches := trailingPort.FindStringSubmatch(req.Host); len(matches) != 0 {
		port = matches[1]
	}

	env := []string{
		"SERVER_SOFTWARE=go",
		"SERVER_PROTOCOL=HTTP/1.1",
		"HTTP_HOST=" + req.Host,
		"GATEWAY_INTERFACE=CGI/1.1",
		"REQUEST_METHOD=" + req.Method,
		"QUERY_STRING=" + req.URL.RawQuery,
		"REQUEST_URI=" + req.URL.RequestURI(),
		"PATH_INFO=" + pathInfo,
		"SCRIPT_NAME=" + root,
		"SCRIPT_FILENAME=" + h.Path,
		"SERVER_PORT=" + port,
	}

	if remoteIP, remotePort, err := net.SplitHostPort(req.RemoteAddr); err == nil {
		env = append(env, "REMOTE_ADDR="+remoteIP, "REMOTE_HOST="+remoteIP, "REMOTE_PORT="+remotePort)
	} else {
		env = append(env, "REMOTE_ADDR="+req.RemoteAddr, "REMOTE_HOST="+req.RemoteAddr)
	}

	if hostDomain, _, err := net.SplitHostPort(req.Host); err == nil {
		env = append(env, "SERVER_NAME="+hostDomain)
	} else {
		env = append(env, "SERVER_NAME="+req.Host)
	}

	if req.TLS != nil {
		env = append(env, "HTTPS=on")
	}

	for k, v := range req.Header {
		k = strings.Map(upperCaseAndUnderscore, k)
		if k == "PROXY" {
			continue
		}
		joinStr := ", "
		if k == "COOKIE" {
			joinStr = "; "
		}
		env = append(env, "HTTP_"+k+"="+strings.Join(v, joinStr))
	}

	if req.ContentLength > 0 {
		env = append(env, fmt.Sprintf("CONTENT_LENGTH=%d", req.ContentLength))
	}
	if ctype := req.Header.Get("Content-Type"); ctype != "" {
		env = append(env, "CONTENT_TYPE="+ctype)
	}

	envPath := os.Getenv("PATH")
	if envPath == "" {
		envPath = "/bin:/usr/bin:/usr/ucb:/usr/bsd:/usr/local/bin"
	}
	env = append(env, "PATH="+envPath)

	for _, e := range h.InheritEnv {
		if v := os.Getenv(e); v != "" {
			env = append(env, e+"="+v)
		}
	}

	for _, e := range []string{"LD_LIBRARY_PATH"} {
		if v := os.Getenv(e); v != "" {
			env = append(env, e+"="+v)
		}
	}

	if h.Env != nil {
		env = append(env, h.Env...)
	}

	env = removeLeadingDuplicates(env)

	var cwd, path string
	if h.Dir != "" {
		path = h.Path
		cwd = h.Dir
	} else {
		cwd, path = filepath.Split(h.Path)
	}
	if cwd == "" {
		cwd = "."
	}

	timeout := h.Timeout
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	ctx, cancel := context.WithTimeout(req.Context(), timeout)
	defer cancel()
	stdoutRead, stdoutWrite := io.Pipe()
	finished, watched := make(chan struct{}), make(chan struct{})
	var stdin io.Reader
	if req.ContentLength != 0 {
		stdin = req.Body
	}
	go func() {
		defer close(finished)
		err := cmdx.Run(ctx, path, cmdx.Args(h.Args...), cmdx.Dir(cwd), cmdx.Env(env),
			cmdx.Stdin(stdin), cmdx.Stdout(stdoutWrite), cmdx.Stderr(h.stderr()),
			cmdx.May(func(c *cmdx.Cmd) {
				// 网关路径按指定工作目录解释，沿用标准库语义，不通过 PATH 查找。
				c.Path = path
				c.Err = nil
				c.Args[0] = h.Path
				c.SysProcAttr.Credential = h.Credential
			}))
		stdoutWrite.CloseWithError(err)
	}()
	stopWatch := context.AfterFunc(ctx, func() {
		defer close(watched)
		// 解除管道及慢速请求正文的等待，使进程取消可以完成输出回收。
		controller := http.NewResponseController(rw)
		controller.SetReadDeadline(time.Now())
		controller.SetWriteDeadline(time.Now())
		stdoutRead.CloseWithError(ctx.Err())
		if req.Body != nil {
			req.Body.Close()
		}
	})
	defer func() {
		// 正常完成时先停止监控，避免取消清理误伤已经生成的响应。
		stopped := stopWatch()
		cancel()
		stdoutRead.Close()
		<-finished
		if !stopped {
			<-watched
		}
	}()

	linebody := bufio.NewReaderSize(stdoutRead, 1024)
	headers := make(http.Header)
	statusCode := 0
	headerLines := 0
	sawBlankLine := false
	for {
		line, isPrefix, err := linebody.ReadLine()
		if isPrefix {
			rw.WriteHeader(http.StatusInternalServerError)
			h.printf("cgi: long header line from subprocess.")
			return
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			rw.WriteHeader(http.StatusInternalServerError)
			h.printf("cgi: error reading headers: %v", err)
			return
		}
		if len(line) == 0 {
			sawBlankLine = true
			break
		}
		headerLines++
		header, val, ok := strings.Cut(string(line), ":")
		if !ok {
			h.printf("cgi: bogus header line: %s", line)
			continue
		}
		if !validCGIHeaderName(header) {
			h.printf("cgi: invalid header name: %q", header)
			continue
		}
		val = textproto.TrimString(val)
		switch {
		case header == "Status":
			if len(val) < 3 {
				h.printf("cgi: bogus status (short): %q", val)
				return
			}
			code, err := strconv.Atoi(val[0:3])
			if err != nil {
				h.printf("cgi: bogus status: %q", val)
				h.printf("cgi: line was %q", line)
				return
			}
			statusCode = code
		default:
			headers.Add(header, val)
		}
	}
	if headerLines == 0 || !sawBlankLine {
		rw.WriteHeader(http.StatusInternalServerError)
		h.printf("cgi: no headers")
		return
	}

	if loc := headers.Get("Location"); loc != "" {
		if strings.HasPrefix(loc, "/") && h.PathLocationHandler != nil {
			h.handleInternalRedirect(rw, req, loc)
			return
		}
		if statusCode == 0 {
			statusCode = http.StatusFound
		}
	}

	if statusCode == 0 && headers.Get("Content-Type") == "" {
		rw.WriteHeader(http.StatusInternalServerError)
		h.printf("cgi: missing required Content-Type in headers")
		return
	}

	if statusCode == 0 {
		statusCode = http.StatusOK
	}

	for k, vv := range headers {
		for _, v := range vv {
			rw.Header().Add(k, v)
		}
	}

	rw.WriteHeader(statusCode)

	_, err := io.Copy(rw, linebody)
	if err != nil {
		h.printf("cgi: copy error: %v", err)
		cancel()
	}
}

func (h *CGIHandler) printf(format string, v ...any) {
	if h.Logger != nil {
		h.Logger.Printf(format, v...)
	} else {
		log.Printf(format, v...)
	}
}

func (h *CGIHandler) handleInternalRedirect(rw http.ResponseWriter, req *http.Request, path string) {
	url, err := req.URL.Parse(path)
	if err != nil {
		rw.WriteHeader(http.StatusInternalServerError)
		h.printf("cgi: error resolving local URI path %q: %v", path, err)
		return
	}
	newReq := &http.Request{
		Method:     "GET",
		URL:        url,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     make(http.Header),
		Host:       url.Host,
		RemoteAddr: req.RemoteAddr,
		TLS:        req.TLS,
	}
	h.PathLocationHandler.ServeHTTP(rw, newReq)
}

func upperCaseAndUnderscore(r rune) rune {
	switch {
	case r >= 'a' && r <= 'z':
		return r - ('a' - 'A')
	case r == '-':
		return '_'
	case r == '=':
		return '_'
	}
	return r
}
