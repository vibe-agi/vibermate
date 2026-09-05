# ViberMate

[English](README.md) · [简体中文](README.zh-CN.md) · [官网](https://vibe-agi.github.io/zh/products/vibermate/)

**看清并控制 Claude Code 和 Codex CLI 的网络边界。**

ViberMate 可以捕获代理对话、控制请求去向、执行小型 JavaScript 规则，并保留
可审计的运行记录。它不会替代你的代理或 AI 服务商。

![ViberMate 对话捕获界面](https://vibe-agi.github.io/images/vibermate/capture-timeline-2400.webp)

## 选择使用方式

| 方式 | 适合场景 | 支持平台 |
| --- | --- | --- |
| **macOS App** | 一套完整的本机工作台，已经包含 Runtime | macOS 14+（Apple 芯片与 Intel） |
| **Runtime Server + Web** | 通过浏览器管理、由一个或多个人共同使用 | Linux x86-64 与 ARM64 |
| **`vibermate` 命令** | 通过本机或远程 Runtime 启动 Claude、Codex | macOS 与 Linux |

ViberMate 没有独立的“团队版”。同一个 Runtime 天然支持多个 Runtime User、
彼此独立的登录会话、按用户记录的 Capture，以及共享的管理视图。一个人可直接
使用，多人时为每个人或设备创建账号即可。

| 使用者 | 登录体验 |
| --- | --- |
| 本机 macOS App | 无需登录；App 直接管理自己的本机 Runtime |
| 浏览器中的 Server 所有者 | 个人用户名和密码；完整工作台 |
| 浏览器中的团队成员 | 个人用户名和密码；仅自己的用量和密码 |
| 终端中的 Claude 或 Codex | 使用同一组个人用户名和密码，只需输入一次 |

ViberMate 不提供共享或默认的 `admin/admin`。短期登录 token 只是内部实现，
普通用户不需要复制或理解它。

## macOS App：第一次捕获

安装并打开 ViberMate：

```sh
brew install --cask vibe-agi/tap/vibermate
```

进入 **设置 → 接入与启动 → 终端命令**，点击 **设置终端命令**。然后在项目目录执行：

```sh
vibermate run -- claude
# 或者
vibermate run -- codex
```

回到 App，就能看到新的 Capture。第一次使用不需要先配置 Traffic Policy；
透明捕获会保留代理原来的服务商、账号和模型。

正常使用 App 不需要创建账号。需要从浏览器打开或与团队共享时，进入
**设置 → 接入与启动**，点击 **创建所有者**，再复制网页工作台地址。第一个账号
是所有者，之后创建的是成员。

## Linux Server + Web

从[最新版本](https://github.com/vibe-agi/vibermate/releases/latest)下载
`linux_x86_64` 或 `linux_arm64` 压缩包，使用 `SHA256SUMS-linux` 校验并解压。
压缩包内已经包含 `vibermated`、`vibermate` 和相邻的 `vibermate-web` 网页界面。

在局域网启动加密的 Runtime：

```sh
./vibermated server \
  --listen 0.0.0.0:9666 \
  --transport self_signed_tls
```

启动后输出的第一行 JSON 包含浏览器地址和 TLS 指纹。请在 Server 机器上打印
一次性初始化/恢复密钥：

```sh
./vibermated server recovery-key
```

打开浏览器地址，输入该密钥，并创建你的个人所有者用户名和密码。浏览器会提示
自签名证书警告；继续前请核对页面显示的指纹。如果启动 Server 时指定了
`--data-dir`，这里也要传入同一个绝对目录。

多人长期使用时，建议换成大家已经信任的 TLS 证书：

```sh
./vibermated server \
  --listen 0.0.0.0:9666 \
  --transport tls_files \
  --tls-cert /绝对路径/fullchain.pem \
  --tls-key /绝对路径/private-key.pem
```

所有者可在 **设置 → 接入与启动** 中为每个人创建账号。同一个账号既能登录网页，
也能用于 CLI。每台开发机只需登录一次：

```sh
vibermate login --server https://your-server.example:9666
vibermate run --server https://your-server.example:9666 -- claude
# 或：vibermate run --server https://your-server.example:9666 -- codex
```

请将示例地址替换为你在浏览器中打开的 HTTPS 地址。

每个人都能从网页右上角修改自己的密码；所有者可以重置成员密码。本机 App
还可在 **设置 → 接入与启动** 中重置自己的所有者密码。如果是无界面的 Server，
请在 Server 本机运行 `vibermated server recovery-key`，再点击
**忘记所有者密码？**。恢复成功后，该密钥会自动轮换。

![ViberMate 团队用量](https://vibe-agi.github.io/images/vibermate/team-insights-2400.webp)

## 证书，不再靠猜

- ViberMate 会把本地根证书直接交给它启动的 Claude、Codex 进程，因此 Linux
  不需要修改系统 CA。
- 在 macOS 上，只有其他客户端必须依赖系统信任时，才需要在
  **设置 → 安全与数据 → 本机根证书**中安装。
- 用于检查代理流量的 Runtime 根证书，与浏览器访问远程 Server 时使用的 TLS
  证书，是两件不同的东西。
- 有 Capture 正在运行时不能替换根证书。安装、替换和删除时，界面都会显示
  需要核对的准确 SHA-256 指纹。

## 可以控制什么

- 查看对话、请求、响应、工具活动、Token 证据和网络决策。
- 保持代理原来的请求去向，或改用另一个上游服务和账号。
- 先查看、修改和测试内置 JavaScript 变换，确认效果后再发布。
- 根据已经登录的 ViberMate 用户名选择上游账号。
- 断开设备或 Runtime 前，先暂停新的外部网络操作。

![ViberMate 脚本库](https://vibe-agi.github.io/images/vibermate/script-library-2400.webp)

## 数据与当前边界

- AI 流量仍会发往你选择的服务商或上游服务。
- ViberMate 不会加密证据数据库；请保护主机账号和文件系统。记录范围和保留
  时间可以配置。
- 服务商凭据不会进入策略快照和证据，但主动写进提示词的文字仍然属于内容。
- 变换 JavaScript 无法访问网络、文件、时钟或随机源；执行失败会停止请求，
  不会静默绕过规则。
- 当前仍是早期 `0.x` 版本，暂不承诺公网加固部署、自动更新、插件和任意客户端
  兼容。

遇到安装问题可运行 `vibermate doctor`。实现细节见[运行时模块地图](docs/module-map.md)
和[架构决策](docs/adr)。疑似漏洞请通过 [SECURITY.md](SECURITY.md) 私密报告。

使用 Apache-2.0 许可证。
