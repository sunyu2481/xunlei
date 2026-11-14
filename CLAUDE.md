# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

@AGENTS.md

上面导入的 AGENTS.md 包含基本构建命令、编码风格、测试与提交规范。本文件只补充其中没有的内容。

## 补充命令

- `go test -race ./pkg/remote/... ./pkg/remoteweb/...`：修改远程下载功能后运行。
- `go run ./cmd/xlp --help`：打印当前全部参数及对应环境变量，不需要 root。README 的参数表为手工维护，同步文档时与此输出核对。
- `make -C cmd/authenticate_cgi_go build`：重新生成两个架构的嵌入认证程序。CI 每次编译前都会执行这一步，覆盖仓库中提交的二进制。C 版本 `cmd/authenticate_cgi_c` 需要 `x86_64-linux-gnu-gcc`、`aarch64-linux-gnu-gcc`、对应的静态 C 运行库和 `readelf`，并在替换前校验目标架构。
- `make busybox`、`make ubuntu`、`make debian` 默认编译并加载宿主架构镜像；指定 `TARGETARCH=amd64` 或 `TARGETARCH=arm64` 时，程序依赖与 Docker 目标平台一起切换。
- `pkg/netlimit` 的集成测试只在设置 `XL_NETLIMIT_TEST_TARGET` 时运行，会修改 `eth0` 的根队列，只能在专用测试容器中执行。

## 启动流程

`cmd/xlp/main.go` 依次执行：

1. `xunlei.ConfigBind` 解析参数。`pkg/flags` 封装 pflag，命令行未指定时回退到环境变量。
2. 检查 root 权限，然后调用 `netlimit.Apply`，在进入隔离环境前用 `tc` 设置出站限速；退出时只删除自己添加的队列。
3. `vms.Execute` 在根目录下挂载 proc、devtmpfs、sysfs、tmpfs，绑定宿主机的 `/lib`、`/bin`、`/etc/ssl`，硬链接 `/etc` 下的时区、DNS 和账号文件（跨设备时改为复制），再运行 `xunlei.Before`。之后 chroot，用 `setegid`/`seteuid` 降权（只改有效 ID，结束后恢复 root），运行 `xunlei.Run`，最后逆序回滚。
4. `xunlei.Before`（`xlp.go`）仍在宿主机视角执行：写入伪造的 `/etc/synoinfo.conf` 和 `authenticate.cgi`，创建目录，下载并解包 SPK，按 UID/GID 修改属主。
5. `xunlei.Run` 在 chroot 内启动网页服务，并用 `pkg/cmdx` 运行 `xunlei-pan-cli-launcher`。任一方退出都会取消另一方。

`--debug` 模式下，运行出错后进程不会退出，挂载保持原状供人工检查，按 Ctrl+C 后才回滚。

## 关键约束

- **两种路径视角**：`xlp.go` 中的 `DIR_*`、`FILE_*`、`SOCK_*` 常量是 chroot 内的绝对路径。`Before` 阶段必须使用 `filepath.Join(cfg.Root, 常量)`，`Run` 阶段直接使用常量。`cfg.DirDownload`、`cfg.DirData` 是宿主机绝对路径，`Run` 通过 `fixPath` 转为 chroot 内路径，因此必须位于 `Root` 之下。容器内的 `Root` 为 `/xunlei`。
- **选项顺序**：`vms.Basic` 读取已设置的根目录来拼接挂载点，必须排在 `vms.Root` 之后，否则会挂载到宿主机的 `/proc`、`/dev`。
- **回滚约定**：`pkg/vms/sys` 的每个操作都返回撤销函数，只撤销本次实际创建的内容，已存在的目录和文件不会被删除。组合多步时使用 `utils.BackQueue(&undo, &err)` 加 `defer bq.ErrDefer()`：出错时逆序自动回滚，成功时把回滚函数交给调用方。
- **认证程序必须静态链接**：chroot 内只有绑定进来的 `/lib`，`embed/authenticate_cgi` 的测试会拒绝带动态链接器的 ELF。嵌入文件按 `_linux_amd64.go`、`_linux_arm64.go` 后缀随目标架构选择。程序固定输出 `admin`，供套件 CGI 完成认证。
- **监听地址一致**：`mockEnv` 中的 `DriveListen`、`LauncherListen` 必须与启动器命令行的 unix socket 一致，`xlp_test.go` 对此有断言。群晖平台和型号按 `GOARCH` 选择。
- **SPK 解包**：`spk.Extract` 只提取 `bin/bin/version`、`bin/bin/xunlei-pan-cli*` 和 `ui/index.cgi`。这些文件齐全时跳过下载，除非强制下载。

## 网页层

路由在 `xlp.go` 的 `webRun` 中注册，`pkg/web` 是基于 chi 的封装。未设置面板密码时 `BasicAuth` 直接放行。

- `/webman/3rdparty/pan-xunlei-com/index.cgi/` 由标准库 `cgi.Handler` 执行原版 `index.cgi`，外层套 `remoteweb.Panel`。`Panel` 只改写首页 HTML，在 `</head>` 前注入 `/remote/panel.css` 和 `/remote/panel.js`，其他请求原样透传。不修改原版套件文件。
- `panel.js` 在原侧栏添加「远程下载」菜单，在内容区用 iframe 加载 `/remote/view.html`（逻辑在 `app.js`）。页签状态保存在原路由的 `xlpRemote=1` 查询参数中。这两个脚本没有自动化测试，修改后的检查项见 `docs/远程下载实现说明.md`。
- `/remote/`、`/api/remote/` 由 `remoteweb.New` 处理，使用标准库 `ServeMux`。静态文件通过 `go:embed` 打包，修改前端后需重新编译。写请求必须是 JSON 并携带 `X-XLP-Request: 1`，同时经过 `http.NewCrossOriginProtection`；CSP 禁止内联脚本和样式。
- `remoteweb.Service` 接口让网页测试使用模拟服务，不依赖真实云端。

## 远程下载客户端

`pkg/remote` 的协议细节见 `docs/远程下载实现说明.md`。修改代码时需要遵守以下设计：

- 直接请求迅雷云端，不使用代理。状态保存在数据目录的 `remote/state.json`，通过临时文件原子替换，权限 `0600`，与套件的 `.drive` 分开。
- 云端操作通过 `Client.gate` 串行执行；`Status()` 只读本地状态，不等待网络。
- 错误统一使用 `*remote.Error`，页面按 `Kind` 处理，`remoteweb.writeError` 把 `Kind` 映射为 HTTP 状态码，新增类型时需同步映射。不要把上游原文、令牌或账号写入日志或响应。
- 创建任务、发送短信和任务操作不做通用重试。每次提交按请求编号记录，超时后通过任务列表核对，避免重复下发。
- 测试通过替换 `c.http.Transport` 模拟上游响应。

## 日志

`pkg/log` 提供自定义 slog 处理器。子模块用 `log.Prefix(ctx, "名称")` 添加前缀，日志统一使用 `slog.XxxContext(ctx, ...)`。

## 镜像与发布

- `Dockerfile` 先在 Ubuntu 阶段收集 glibc 运行库、`ip`、`tc`、证书和时区，最终镜像基于 `busybox:musl`。
- `.github/workflows/docker.yml` 在推送任意分支或标签时都会构建并推送 `ghcr.io` 镜像，默认分支同时更新 `latest`。推送即发布。
