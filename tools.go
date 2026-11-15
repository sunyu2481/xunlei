package xunlei

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/cnk3x/xunlei/pkg/utils"
	"github.com/cnk3x/xunlei/pkg/vms/sys"
)

// 工具目录由官方内核选择，既包含数据目录中的历史位置，也包含内核旁的本地插件。
func toolDirectories(cfg Config) []string {
	return []string{
		filepath.Join(cfg.DirData, ".drive", "plugins"),
		filepath.Join(cfg.DirData, ".drive", ".drive", "plugins"),
		// 启动器会将实际运行的内核复制到数据目录，仍需隔离其旁的本地插件。
		filepath.Join(cfg.DirData, ".drive", "bin", "plugin"),
		filepath.Join(cfg.Root, filepath.Dir(FILE_PAN_XUNLEI_CLI), "plugin"),
	}
}

// prepareTools 用只读空挂载遮蔽已有小工具；不删除插件、账号状态或下载数据。
// 挂载必须全部成功才能启动内核，退出时按逆序撤销。
func prepareTools(ctx context.Context, cfg Config, mount func(context.Context, sys.MountOptions) (sys.Undo, error)) (undo func(), err error) {
	if cfg.AllowTools {
		return nil, nil
	}
	dirs := toolDirectories(cfg)
	for _, dir := range dirs {
		check := cfg
		check.DirData, check.DirDownload = dir, nil
		if err := check.validatePaths(); err != nil {
			return nil, fmt.Errorf("检查小工具隔离目录失败：%w", err)
		}
		for _, download := range cfg.DirDownload {
			if download == dir || strings.HasPrefix(download, dir+string(filepath.Separator)) {
				return nil, fmt.Errorf("下载目录不能位于小工具隔离目录 %q 内", dir)
			}
		}
	}
	bq := utils.BackQueue(&undo, &err)
	defer bq.ErrDefer()
	for _, dir := range dirs {
		restore, e := mount(ctx, sys.MountOptions{
			Target: dir, Source: "tmpfs", Fstype: "tmpfs",
			Flags: syscall.MS_RDONLY | syscall.MS_NOSUID | syscall.MS_NODEV | syscall.MS_NOEXEC,
			Data:  "mode=0755,size=4k,nr_inodes=4",
		})
		if e != nil {
			return nil, fmt.Errorf("禁用小工具失败：%w", e)
		}
		bq.Put(restore)
	}
	slog.InfoContext(ctx, "小工具安装和运行已禁用，内核自动更新已禁用")
	return
}

// blockTools 拒绝本机面板的插件接口及小工具任务，保留普通下载任务和文件传输。
// 云端直接下发的任务不经过此处，仍由只读目录阻止安装和运行。
func blockTools(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "/webman/3rdparty/" + SYNOPKG_PKGNAME + "/index.cgi"
		p := path.Clean(r.URL.Path)
		if strings.HasPrefix(p, prefix+"/") {
			p = strings.TrimPrefix(p, prefix)
		}
		for _, route := range []string{"/drive/v1/apps", "/apps", "/assets/apps", "/assets/apps_static"} {
			if p == route || strings.HasPrefix(p, route+"/") {
				toolsForbidden(w)
				return
			}
		}
		if r.Method == http.MethodPost && (p == "/drive/v1/task" || p == "/drive/v1/tasks") {
			// 普通任务仍透传原始字节；不让未知或过大的任务绕过检查。
			const maxTaskSize = 1 << 20
			body, err := io.ReadAll(io.LimitReader(r.Body, maxTaskSize+1))
			r.Body.Close()
			if err != nil || len(body) > maxTaskSize {
				http.Error(w, "任务内容无效或过大", http.StatusBadRequest)
				return
			}
			var task *struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(body, &task); err != nil || task == nil {
				http.Error(w, "任务内容必须是有效的对象", http.StatusBadRequest)
				return
			}
			if task.Type == "user#app" {
				toolsForbidden(w)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		next.ServeHTTP(w, r)
	})
}

func toolsForbidden(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusForbidden)
	_, _ = io.WriteString(w, `{"error":"tools_disabled","error_description":"已禁用网心云等小工具的安装和运行"}`)
}
