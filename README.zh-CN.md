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

## macOS App：第一次捕获

安装并打开 ViberMate：

```sh
brew install --cask vibe-agi/tap/vibermate
```

进入 **设置 → 常规 → 终端命令**，点击 **设置终端命令**。然后在项目目录执行：

```sh
vibermate run -- claude
# 或者
vibermate run -- codex
```

回到 App，就能看到新的 Capture。第一次使用不需要先配置 Traffic Policy；
透明捕获会保留代理原来的服务商、账号和模型。

App 也会在同一台 Mac 上提供浏览器管理界面。地址可从
**设置 → 团队接入 → 网页与客户端接入**复制。

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

启动后输出的第一行 JSON 包含浏览器地址、TLS 指纹和
`adminAccessKeyPath`。打开地址，读取该文件中的所有者密钥，用它进入 Web
工作台。浏览器会提示自签名证书警告；继续前请核对页面显示的指纹。

多人长期使用时，建议换成大家已经信任的 TLS 证书：

```sh
./vibermated server \
  --listen 0.0.0.0:9666 \
  --transport tls_files \
  --tls-cert /绝对路径/fullchain.pem \
  --tls-key /绝对路径/private-key.pem
```

在 **设置 → 团队接入** 中，为每个人或设备创建 Runtime User。每台开发机只需
用对应的用户名和密码登录一次：

```sh
vibermate login --server 192.0.2.10:9666
vibermate run --server 192.0.2.10:9666 -- claude
# 或：vibermate run --server 192.0.2.10:9666 -- codex
```

所有者密钥只用于浏览器管理；Runtime User 密码供 `vibermate` 命令登录。
不要把所有者密钥发给代理用户。

![ViberMate 团队用量](https://vibe-agi.github.io/images/vibermate/team-insights-2400.webp)

## 证书，不再靠猜

- ViberMate 会把本地根证书直接交给它启动的 Claude、Codex 进程，因此 Linux
  不需要修改系统 CA。
- 在 macOS 上，只有其他客户端必须依赖系统信任时，才需要在
  **设置 → 常规 → 本机根证书**中安装。
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
