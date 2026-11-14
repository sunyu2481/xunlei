# 迅雷远程下载服务(非官方)

[![GitHub Stars][1]][2] [![Docker Pulls][3]][5] [![Docker Version][4]][5]

[1]: https://img.shields.io/github/stars/cnk3x/xunlei?style=flat
[2]: https://star-history.com/#cnk3x/xunlei&Date
[3]: https://img.shields.io/docker/pulls/cnk3x/xunlei.svg
[4]: https://img.shields.io/docker/v/cnk3x/xunlei
[5]: https://hub.docker.com/r/cnk3x/xunlei

从迅雷群晖套件中提取出来用于其他设备的迅雷远程下载服务程序。仅供研究学习测试。 \
本程序仅提供 Linux 模拟和容器化运行环境，未对原版迅雷程序进行任何修改。

**当前为测试版本，版本号 [4.0.0-beta](https://hub.docker.com/layers/cnk3x/xunlei/4.0.0-beta)，且并未大规模验证。**

**3.20 版本介绍在此: (https://github.com/cnk3x/xunlei/tree/v3.20.2)**

## 特性

- 支持本地运行和容器化运行
- 重构了运行环境，有比较完善的回滚流程。
- 容器镜像基于busybox，不再内嵌SPK，改成从远程下载，减小镜像体积。
- 支持通过环境变量限制容器总出站流量。
- 原版面板侧栏内置「远程下载」页签，支持账号授权、短信验证、设备和目录选择、批量下发及任务管理。
- 不再内嵌SPK，不在受镜像包的luncher限制，理论上随时可以使用任何指定的版本。

## 使用

源码构建使用 Linux 和 Go 1.26.11 或更新的受支持补丁版本；最低工具链版本见 `go.mod`。发布工作流使用该版本，并在构建前运行 `govulncheck` 检查已知可达漏洞。

### Docker

#### 镜像

本仓库每次推送代码后，会自动构建 `linux/amd64` 和 `linux/arm64` 镜像并发布到 GitHub 容器仓库。工作流使用内置的 `GITHUB_TOKEN`，无需配置额外密钥，也可在 Actions 页面手动运行。

分支推送生成分支名标签，默认分支还会更新 `latest`；所有构建都会生成 `sha-<短提交号>` 标签，推送 Git 标签时生成对应的镜像标签。镜像地址跟随当前 GitHub 仓库，当前为：

```shell
docker pull ghcr.io/sunyu2481/xunlei:latest
```

首次发布的包默认是私有的；如需免登录拉取，可在 GitHub 包设置中将可见性改为公开。

上游镜像：

```plain
cnk3x/xunlei:beta
ghcr.io/cnk3x/xunlei:beta

# 或者指定版本
cnk3x/xunlei:4.0.0-beta
ghcr.io/cnk3x/xunlei:4.0.0-beta
```

#### 参数

命令行参数优先于环境变量；第一次指定下载目录会替换环境中的目录列表，后续重复参数按顺序追加。未被命令行覆盖的非法环境值会报错并停止启动。运行 `xlp --help` 可查看当前版本的完整参数。

| 命令行参数 | 环境变量 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--dashboard_port` | `XL_DASHBOARD_PORT`、`XL_PORT` | `2345` | 网页访问的端口 |
| `--dashboard_ip` | `XL_DASHBOARD_IP`、`XL_IP` | 空，绑定所有 IP | 网页访问绑定的 IP |
| `--dashboard_username` | `XL_DASHBOARD_USERNAME`、`XL_BA_USER` | 空 | 网页访问的用户名；只设置密码时为 `admin` |
| `--dashboard_password` | `XL_DASHBOARD_PASSWORD`、`XL_BA_PASSWORD` | 空，不启用访问认证 | 网页访问的密码 |
| `-r`、`--root` | `XL_ROOT`、`XL_CHROOT` | 当前目录下的 `xunlei` | 主目录，迅雷在此目录内隔离运行 |
| `-d`、`--dir_download` | `XL_DIR_DOWNLOAD` | `<主目录>/downloads` | 每个参数指定一个下载目录，可重复；环境变量用冒号 `:` 分隔，目录必须位于主目录内 |
| `-D`、`--dir_data` | `XL_DIR_DATA` | `<主目录>/data` | 程序数据目录，必须位于主目录内且可写；其下 `.drive` 保存登录账号和下载进度 |
| `-u`、`--uid` | `XL_UID`、`UID` | `0` | 运行迅雷的用户 ID |
| `-g`、`--gid` | `XL_GID`、`GID` | `0` | 运行迅雷的用户组 ID |
| `--prevent_update` | `XL_PREVENT_UPDATE` | `false` | 阻止迅雷自动更新 |
| `--spk` | `XL_SPK` | 按架构使用官方下载地址 | SPK 来源，支持 `https://` 和本地 `file://`，可附加 `#sha256=摘要` |
| `-F`、`--spk_force_download` | `XL_SPK_FORCE_DOWNLOAD` | `false` | 已有套件文件时仍重新下载 |
| `--upload_limit` | `XL_UPLOAD_LIMIT` | `0` | 总出站限速，单位 KiB/s，`0` 表示不限速，见[上传限速](#上传限速) |
| `--upload_interface` | `XL_UPLOAD_INTERFACE` | `eth0` | 上传限速使用的 veth 网卡 |
| `--launcher_log_file` | `XL_LAUNCHER_LOG_FILE` | 空 | 迅雷启动器日志文件 |
| `--debug` | `XL_DEBUG` | `false` | 调试模式：日志等级改为 `debug`，运行出错后不自动退出，按 Ctrl+C 退出 |
| `--log` | `XL_LOG` | `info` | 日志等级，可选 `debug`、`info`、`warn`、`error` |

`--chroot` 已弃用，请改用 `--root`。布尔类型的环境变量可使用 `true`、`false`、`1`、`0`。UID/GID 只接受 `0` 到 `4294967294`，其中 `0` 明确表示 root 用户或组；负数、系统保留编号和超范围数值会在准备环境前被拒绝。

数据和下载目录必须是主目录的子目录，不能使用主目录本身或越界路径；允许 `.data` 等隐藏目录。路径中的已有组件必须是真实目录，不接受符号链接。检查在限速、挂载、创建目录、清理运行状态及修改属主之前完成。

下载目录中的逗号和空白按原字符保留，例如 `-d '/xunlei/甲,乙' -d '/xunlei/另一个目录'`。套件使用冒号拼接下载目录列表，因此单个下载目录名不能含冒号；命令行中的冒号不会被拆分，会直接报错。环境变量仍使用 `XL_DIR_DOWNLOAD=/xunlei/甲,乙:/xunlei/另一个目录` 表示多个目录，不能包含空列表项。

#### 容器内的默认值

镜像以 `/xunlei` 为主目录，下载目录默认为 `/xunlei/downloads`，数据目录默认为 `/xunlei/data`。容器内不要修改数据目录，应将其挂载到宿主机以保留登录状态。

- `XL_DIR_DOWNLOAD` 指定多个目录时用冒号 `:` 隔开。每个目录都必须以 `/xunlei/` 开头，目录名可以使用中文，再在 `volumes` 中绑定宿主机的实际目录。迅雷面板显示保存路径时会去掉 `/xunlei` 前缀。迅雷云盘的缓存使用第一个下载目录。
- `XL_SPK` 默认指向官方下载地址。网络来源必须使用通过系统证书校验的 HTTPS，拒绝 HTTP 和降级重定向。离线套件使用绝对路径，例如 `file:///opt/packages/xunlei.spk`；协议名不区分大小写，主机部分仅接受空值或 `localhost`，文件名中的空格、`#`、`%` 分别编码为 `%20`、`%23`、`%25`，不支持查询参数或相对路径。
- 可在套件地址末尾附加 `#sha256=` 和 64 位十六进制摘要，例如 `https://example.com/xunlei.spk#sha256=<可信摘要>`。摘要须从可信渠道获得；程序校验完整 SPK 后才安装。指定摘要时每次启动都会重新读取来源并校验，已有安装也不会跳过；未指定摘要且未强制下载时，仍复用已有套件。默认下载依赖 HTTPS 证书认证，未内置固定摘要或发行方签名。
- 套件读取和解包总时限为 10 分钟，连接、TLS 握手和等待响应头分别限制为 15、10、30 秒；退出信号会取消下载与解包。套件必须包含当前架构的启动器、版本对应的主程序、版本文件及网页 CGI；路径穿越、已有安装路径中的链接和异常文件类型会被拒绝。
- `XL_UID`、`XL_GID` 默认为 `0`，即 root。建议设置为宿主机普通账号的 ID（通常为 `1000`），并确保该账号可以读写下载目录，以免下载后的文件无法由普通账号处理。
- 启动器和网页 CGI 使用配置的用户、主组运行，并清空继承的附加组。封装程序保留 root 身份，用于准备环境、管理远程状态和退出清理。
- 启用上传限速需要 `NET_ADMIN` 权限和桥接网络。

#### 远程下载页签

更新程序或镜像后，打开原来的迅雷面板，在左侧「下载列表」下方点击「远程下载」即可使用。首页仍为 `/webman/3rdparty/pan-xunlei-com/index.cgi/#/home`，远程下载在原面板的内容区显示，可以随时切换回下载列表、云盘等原有页签。

本功能直接随面板提供，无需启用开关，无需另设面板用户名或密码；原来配置过面板访问认证的部署继续沿用原配置。远程下载首次使用时需要在页签内授权迅雷云端账号，这与面板访问认证无关。

首次使用：

1. 在原版迅雷面板登录账号，并在迅雷客户端确认当前部署已作为远程设备绑定。
2. 点击侧栏「远程下载」，在页签内点击「登录迅雷」，输入同一个迅雷账号及密码。云端登录态会自动保存和续期。
3. 如果触发风控，在登录弹窗中获取短信验证码并完成验证；额外的人机验证通过弹窗提供的迅雷验证页面完成。
4. 在页签顶部选择当前部署对应的设备，再点击「保存目录」选择保存位置。设备不可用时不会自动切换到其他设备。
5. 点击「新建下载」，每行粘贴一个链接，每批最多 50 条。支持磁力链接、HTTP/HTTPS/FTP 直链以及 `thunder://` 链接。可在列表查看进度，暂停、继续或删除任务。

下载目录以设备接口返回的路径为准。默认容器下载位置在面板内通常显示为 `/downloads`，它对应容器外配置的 `/xunlei/downloads` 挂载。目录选择器仅浏览已有目录；需要新目录时，先在原版面板或实际下载目录中创建。目录无法读取或无效时会停止派发，不会改用其他目录。

远程账号状态保存在 `XL_DIR_DATA/remote/state.json`，与原版套件的 `.drive` 分开。封装程序以 root 身份写入，目录权限为 `0700`、文件权限为 `0600`；数据目录必须可写。访问令牌和刷新令牌会自动保存及续期。默认不保存迅雷密码；勾选「记住密码」后才允许在刷新令牌失效时自动重新登录。未记住密码且登录完全失效时，需要重新输入密码。退出远程登录会清除本功能保存的账号及会话，不会停止已下发的下载任务。

提交时若网络超时，页面会标记结果待确认。再次检查同一条提交会先查询任务，避免重复下发；服务重启后也保留提交记录。对于无法确认的任务，应到任务列表或迅雷客户端核对。批量提交中的成功项不会在重试时重复创建，浏览器关闭后重新粘贴链接属于新的提交。

本功能通过迅雷云端下发任务，仍由所选设备下载文件。远程下载的任务配额、设备兼容性和账号风控由迅雷服务决定；不同账号及套件版本需要实际验证。

#### 上传限速

封装程序支持对容器网卡的总出站流量限速，覆盖迅雷内核、插件、TCP、UDP 和 IPv6。无需调整迅雷内部配置。默认关闭，设置 `XL_UPLOAD_LIMIT=500` 可将平均出站速度限制为约 500 KiB/s，即 4.1 Mbps；允许短暂突发。

在部署配置中加入以下内容，并重新创建使用本次修改构建的镜像的容器：

```yaml
cap_add: [SYS_ADMIN, NET_ADMIN]
network_mode: bridge
environment:
  - XL_UPLOAD_LIMIT=500
  - XL_UPLOAD_INTERFACE=eth0
```

此功能只支持隔离网络环境中的 `veth` 网卡，适用于默认桥接网络和通常使用桥接驱动的自定义网络，不应在 `network_mode: host` 下启用。程序会拒绝物理网卡和其他类型的网卡。多网卡容器只限制 `XL_UPLOAD_INTERFACE` 指定的网卡；出站流量需经过该网卡。

限速包括面板文件传输、局域网发送和下载确认报文，因此较低的上传限制也可能影响下载速度。程序在准备运行环境前设置限速，正常退出时移除自己添加的队列；已有自定义根队列不会被覆盖。启用限速后，如果权限不足、工具缺失或宿主机内核不支持令牌桶队列，程序会报错并停止启动。

直接运行可使用 `--upload_limit 500 --upload_interface eth0`，但同样要求在独立网络环境中使用 `veth` 网卡，并预先安装 `iproute2`。

#### 示例: docker-compose

```yaml
services:
  xunlei:
    container_name: xunlei
    image: cnk3x/xunlei:beta
    restart: unless-stopped

    # 宿主机名，迅雷远程控制的名称与此相关，会显示 `群晖-r66s`
    hostname: r66s

    # 必须, cap_add: [SYS_ADMIN] 和 privileged: true 二选一
    cap_add: [SYS_ADMIN]
    # 启用上传限速时改为 cap_add: [SYS_ADMIN, NET_ADMIN]

    # 面板访问端口，如需更改访问端口到5432，替换前面的2345为5432即可
    ports: [2345:2345/tcp]
    network_mode: bridge
    # 可以通过环境变量 XL_DASHBOARD_PORT=5432 来更改内部端口, 不过bridge网络模式没有必要更改默认端口
    # 如果设置 network_mode: host, 将忽略上面的端口映射配置(ports), 但可以通过环境变量 XL_DASHBOARD_PORT=5432 来更改端口
    # network_mode: host

    environment:
      # 可选，总出站限速，单位 KiB/s；需要 NET_ADMIN 和桥接网络
      # - XL_UPLOAD_LIMIT=500
      # - XL_UPLOAD_INTERFACE=eth0

      ##如果需要指定多个下载目录，手动指定XL_DIR_DOWNLOAD
      ##多个以冒号`:`隔开，都必须以 /xunlei 开头，迅雷面板选择保存路径显示会去掉/xunlei前缀
      ##指定后可以在 volumes 中绑定宿主机实际目录
      ##迅雷云盘的缓存会使用第一个目录会缓存
      ##/xunlei/后面可以用中文
      ##不设置默认一个目录 /xunlei/downloads
      #- XL_DIR_DOWNLOAD=/xunlei/下载:/xunlei/影音:/xunlei/大人

      # 设置用户身份，请确保该用户对 XL_DIR_DOWNLOAD 指定的目录或者默认的 /xunlei/downloads 有读写权限
      - XL_UID=1000 # 用户ID
      - XL_GID=1000 # 用户组ID
    volumes:
      ## 二选一必须，对应 XL_DIR_DOWNLOAD 指定的目录, 请替换冒号前面的路径为实际路径
      #- /vol1/1000/下载:/xunlei/下载
      #- /vol1/1000/影音/下载:/xunlei/影音
      #- /vol1/1000/大人/下载:/xunlei/大人

      ## 二选一必须，如果没有通过 XL_DIR_DOWNLOAD 指定下载目录，请将下面这行代码替换为上面代码
      - /vol1/1000/下载:/xunlei/downloads

      # 必须，数据目录，迅雷运行时，插件，升级，包括登录数据都在这
      - ./data:/xunlei/data

      # 可选，首次初始化，会从远程下载迅雷套件到此处，如果不配置每次重新创建都会重新从远程下载
      - ./cache:/xunlei/var/packages/pan-xunlei-com
```
