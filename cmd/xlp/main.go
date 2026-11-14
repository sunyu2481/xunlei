package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/cnk3x/xunlei"
	"github.com/cnk3x/xunlei/pkg/flags"
	"github.com/cnk3x/xunlei/pkg/log"
	"github.com/cnk3x/xunlei/pkg/netlimit"
	"github.com/cnk3x/xunlei/pkg/utils"
	"github.com/cnk3x/xunlei/pkg/vms"
)

var BuildTime string

func init() {
	flags.SetVersion(xunlei.Version)
	if BuildTime != "" {
		if bt, err := time.Parse(time.RFC3339, BuildTime); err == nil {
			flags.SetBuildTime(bt)
		}
	}
}

func main() { os.Exit(exitCode(run)) }

// exitCode 在业务入口和全部可行的延迟清理结束后决定进程退出码。
func exitCode(run func() error) int {
	if err := run(); err != nil {
		slog.Error("exit", "err", err)
		return 1
	}
	slog.Info("exit")
	return 0
}

func run() error {
	var cfg xunlei.Config
	if err := xunlei.ConfigBind(&cfg); err != nil {
		return err
	}

	if uid := os.Getuid(); uid != 0 {
		return fmt.Errorf("必须以根用户运行，当前 UID=%d", uid)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	ctx = log.Prefix(ctx, "main")
	logConfiguration(ctx, cfg)
	return runEnvironment(ctx, cfg, netlimit.Apply, vms.Execute)
}

func logConfiguration(ctx context.Context, cfg xunlei.Config) {
	slog.InfoContext(ctx, `_  _ _  _ _  _ _    ____  _`)
	slog.InfoContext(ctx, ` \/  |  | |\ | |    |___  |`)
	slog.InfoContext(ctx, `_/\_ |__| | \| |___ |___  |`)
	slog.InfoContext(ctx, fmt.Sprintf(`daemon version: %s`, xunlei.Version))
	slog.InfoContext(ctx, fmt.Sprintf(`build time: %s`, flags.GetBuildTime().In(time.Local).Format(time.RFC3339)))
	slog.InfoContext(ctx, fmt.Sprintf("port: %d", cfg.Port))
	slog.InfoContext(ctx, fmt.Sprintf("ip: %s", cfg.Ip))
	slog.InfoContext(ctx, fmt.Sprintf("dashboard username: %s", cfg.DashboardUsername))
	slog.InfoContext(ctx, "面板访问认证", "enabled", cfg.DashboardPassword != "")
	for i, dir := range cfg.DirDownload {
		slog.InfoContext(ctx, fmt.Sprintf("dir download.%d: %s", i+1, dir))
	}
	slog.InfoContext(ctx, fmt.Sprintf("dir data: %s", cfg.DirData))
	slog.InfoContext(ctx, fmt.Sprintf("uid: %d", cfg.Uid))
	slog.InfoContext(ctx, fmt.Sprintf("gid: %d", cfg.Gid))
	slog.InfoContext(ctx, fmt.Sprintf("root: %s", cfg.Root))
	slog.InfoContext(ctx, fmt.Sprintf("spk_force_download: %t", cfg.SpkForceDownload))
	slog.InfoContext(ctx, fmt.Sprintf("prevent update: %t", cfg.PreventUpdate))
	slog.InfoContext(ctx, fmt.Sprintf("log: %s", cfg.Log))
	slog.InfoContext(ctx, fmt.Sprintf("debug: %t", cfg.Debug))
	slog.InfoContext(ctx, "上传限速配置", "limit_kib", cfg.UploadLimit, "interface", cfg.UploadInterface)
}

func runEnvironment(ctx context.Context, cfg xunlei.Config,
	apply func(context.Context, uint64, string) (func() error, error),
	execute func(context.Context, ...vms.Option) error) (err error) {
	undoLimit, err := apply(ctx, cfg.UploadLimit, cfg.UploadInterface)
	if err != nil {
		return fmt.Errorf("上传限速初始化失败：%w", err)
	}
	defer func() {
		if errors.Is(err, utils.ErrUnsafeCleanup) {
			slog.WarnContext(ctx, "根目录或活动任务状态不可信，跳过依赖宿主机路径的限速清理")
			return
		}
		if cleanupErr := undoLimit(); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("清理上传限速失败：%w", cleanupErr))
		}
	}()
	if cfg.UploadLimit > 0 {
		slog.InfoContext(ctx, "上传限速已生效", "limit_kib", cfg.UploadLimit, "interface", cfg.UploadInterface)
	}
	return execute(
		log.Prefix(ctx, "vms"),
		vms.Before(xunlei.Before(cfg)),
		vms.Run(xunlei.Run(cfg)),
		vms.Wait(cfg.Debug),
		vms.User(cfg.Uid, cfg.Gid),
		vms.Root(cfg.Root),
		vms.ReadOnlyBinds("/lib", "/bin", "/etc/ssl"),
		vms.Links("/etc/timezone", "/etc/localtime", "/etc/resolv.conf"),
		vms.Links("/etc/passwd", "/etc/group", "/etc/shadow"),
		vms.Symlink("lib", filepath.Join(cfg.Root, "lib64")),
		vms.Basic,
	)
}
