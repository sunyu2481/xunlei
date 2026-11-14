# SPK

网络来源仅支持通过系统证书校验的 HTTPS，拒绝降级重定向；本地套件使用绝对路径的 `file://` URL。
可在地址末尾附加 `#sha256=` 和可信的 64 位十六进制摘要，完整读取并验证后才安装。
来源规则、超时和缓存行为见[主文档](../README.md)。

完整解码和文件校验完成后才发布安装文件，单文件通过重命名原子替换，安装错误会恢复旧文件。
目标目录内的 `.xlp-install.json` 在所有文件成功安装后最后发布，记录架构、版本、文件大小和 SHA-256。
缓存复用还要求所有必要文件为独立普通文件，程序具有执行权限；旧缓存没有清单时重新安装。
发生进程崩溃或断电时不承诺整组文件同时切换；下一次启动会拒绝与清单不一致的内容并重新安装。
若操作系统同时拒绝恢复旧文件，会返回恢复错误并保留 `.spk-*/backup-*`，便于恢复。

下载地址:
<https://nas.xunlei.com>
<https://docs.qq.com/doc/DQVJpbEVGZXV0anNa>
<https://down.sandai.net/nas/nasxunlei-DSM7-armv8.spk>
<https://down.sandai.net/nas/nasxunlei-DSM6-armv8.spk>
<https://down.sandai.net/nas/nasxunlei-DSM6-x86_64.spk>
<https://down.sandai.net/nas/nasxunlei-DSM7-x86_64.spk>
