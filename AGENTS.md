# Repository Guidelines

## 项目结构与模块划分

本项目使用 Go，为迅雷群晖套件提供 Linux 运行环境，支持 amd64、arm64。根目录的 `config.go`、`xlp.go` 负责配置与服务编排；`cmd/xlp/` 是主程序入口。`pkg/` 包含进程管理、文件操作、日志、网页服务和隔离环境，其中 `pkg/vms/sys/` 封装系统调用。`spk/` 负责套件下载与解包；`embed/authenticate_cgi/` 保存按架构嵌入的认证程序，对应源码位于 `cmd/authenticate_cgi_go/`、`cmd/authenticate_cgi_c/`。测试与源码同目录；构建产物写入已忽略的 `artifacts/`。

## 构建、测试与本地开发

使用 Linux 和 Go 1.26.11 或更新的受支持补丁版本，在仓库根目录执行；发布工作流按 `go.mod` 选择工具链：

- `make amd64`、`make arm64`：生成对应架构的程序和压缩包。
- `make build`：构建两个架构。
- `go test ./...`：运行全部 Go 测试。
- `go vet ./...`：执行 Go 静态检查。
- `make busybox`：构建并加载本地容器镜像；另有 `make ubuntu`、`make debian`。
- `sudo ./artifacts/xlp-amd64 --root ./local/xunlei --debug`：在 amd64 主机启动调试服务，默认端口为 2345；arm64 主机替换程序名。

运行服务需要根权限及挂载、命名空间能力。容器启动前调整 `docker-compose.yaml` 中的宿主机目录；已有配置提供 `SYS_ADMIN` 能力。

## 编码风格与命名约定

遵循 `.editorconfig`：使用 UTF-8、LF，删除行尾空格并保留末尾换行；Go 和 Makefile 使用制表符，其他文件默认两个空格。修改 Go 文件后执行 `gofmt -w 路径/文件.go`。包名使用简短小写，导出标识符使用大写开头的驼峰式名称，沿用相邻代码的命名。注释使用中文；新增命令行参数和 `XL_` 环境变量时同步更新 `config.go` 与 `README.md`。拼写词表位于 `cspell.config.yaml`。

## 测试规范

使用标准库 `testing`，文件命名为 `*_test.go`，测试函数使用明确的 `Test功能名`。可用 `go test ./pkg/remote -run '^TestLoginAndRestore$' -v` 定向运行测试。新增测试应使用 `t.TempDir()` 并断言结果，避免依赖已有测试中的固定宿主机路径。目前没有覆盖率门槛；工作流以普通用户执行 `go test`、双架构 `go vet`、漏洞扫描及编译，全部通过才允许发布。合并请求只验证，不发布。

普通 CI 不启用真实挂载、网络队列或跨设备测试，也不通过 `sudo` 运行测试。需要根权限的测试会跳过；特殊集成验证应在无发布凭据的一次性容器或虚拟机中单独运行，按需设置 `XL_TEST_MOUNT=1`、`XL_TEST_CROSS_DEVICE_DIR` 或 `XL_NETLIMIT_TEST_TARGET`，并提供对应的挂载或网络管理能力。

外部 GitHub Actions 固定到官方仓库的完整提交 SHA，并在行尾保留版本号；更新时核对上游版本标签、提交来源、动作入口及所需权限，再更新固定值。

## 提交与合并请求

历史记录常见 `feat(模块): 描述`、`fix(模块): 描述`、`refactor(模块): 描述`，也存在自由格式；新提交优先采用前述格式，例如 `fix(vms): 修复挂载失败回滚`。每次提交聚焦一项变更。合并请求说明问题、实现效果、测试命令与结果，并关联相关议题；界面变化附截图。涉及系统调用或嵌入程序时说明验证的架构，配置变化同步补充文档。

提交的代码和文档不写入特定开发机的主机名、目录、代理或浏览器服务配置。依赖这类本机环境的测试脚本只在本地保留，不提交到仓库，可写入 `.git/info/exclude` 忽略。

## 配置与数据管理

配置项以 `config.go` 为准。使用独立的开发数据目录，避免提交账号数据、面板密码、下载文件和构建产物；数据目录中的 `.drive` 包含登录状态。嵌入认证程序变更需同时考虑两个架构。
