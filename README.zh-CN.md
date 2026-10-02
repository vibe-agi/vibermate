# ViberMate

[English](README.md) · [简体中文](README.zh-CN.md) · [官网](https://vibe-agi.github.io/zh/products/vibermate/)

**看清并掌控你的 AI 编程助手。**

Claude Code 和 Codex 通过网络与 AI 服务对话，平时你既看不到这些消息，也决定不了它们发往哪里。
ViberMate 就站在两者之间，运行在你的 Mac 或你自己的服务器上。你照常使用编程助手，ViberMate
把每一段对话记录下来，并由你决定它该怎么走。

![ViberMate 中逐轮查看的一次 Claude Code 会话](https://vibe-agi.github.io/images/vibermate/conversation-zh-2400.webp)

## 它能做什么

- **看清**：先读用户输入与 Agent 回复，从对应消息旁查看工具调用、系统上下文或原始 HTTP，返回时保留阅读位置。
- **掌控去向**：把每个请求发往你选定的上游服务和账号，切换时不用改配置文件。
- **先审批**：Agent 要访问策略里尚未决定的网站时，先由你确认。
- **算清用量**：按项目、分支、模型、账号或人员统计请求数、Token 和估算费用，也能在对话里通过小字入口查看单次请求明细。
- **团队共用**：一台服务器给整个团队用。每人用自己的账号登录，所有者决定谁能使用哪些策略。

![使用概览：请求数、Token 与估算费用](https://vibe-agi.github.io/images/vibermate/usage-zh-2400.webp)

[官网](https://vibe-agi.github.io/zh/products/vibermate/)配有截图，一步步介绍以上每项能力。

## 开始使用

**macOS 14+（Apple 芯片与 Intel）**

```sh
brew install --cask vibe-agi/tap/vibermate
```

打开 ViberMate，进入 **设置 → 接入与启动 → 终端命令**，设置好 `vibermate` 命令。然后在项目目录执行：

```sh
vibermate run -- claude    # 或：vibermate run -- codex
```

**Linux 服务器（x86-64 与 ARM64）**

从[最新发布](https://github.com/vibe-agi/vibermate/releases/latest)下载对应架构的压缩包，解压后启动：

```sh
./vibermated server
```

打开 <http://127.0.0.1:9666>，用 `./vibermated server recovery-key` 输出的密钥创建所有者账号。
如需从其他设备访问，请看[部署指南](docs/deployment.zh-CN.md)。

**有自己的公网域名？** ViberMate 内置自动 HTTPS，可申请、续期并热加载证书，无需另装
Caddy。在 **设置 → 接入与启动 → 使用自有域名部署** 中生成原生命令或 Docker 配置，
或直接查看[公网 HTTPS](docs/deployment.zh-CN.md#自动公共-https)与[Docker 示例](docs/docker.zh-CN.md#公网域名自动-https)。
域名需解析到服务器，公网 TCP 443 需直达或四层透传；普通 HTTP 反向代理不能替代 Agent 的 CONNECT 通道。

## 了解更多

- [官网](https://vibe-agi.github.io/zh/products/vibermate/)：一步步了解 ViberMate
- [部署与 HTTPS](docs/deployment.zh-CN.md)、[Docker](docs/docker.zh-CN.md)、[备份与恢复](docs/backup-and-restore.zh-CN.md)
- [当前支持范围](docs/capability-support.md)
- [Paseo 集成](integrations/paseo/README.md)：每个对话独立选择流量策略，桌面与手机共用
- [安全策略](SECURITY.md) · [参与贡献](CONTRIBUTING.md)

安装或启动遇到问题时，运行 `vibermate doctor`。

## 许可证

ViberMate 使用 [GNU AGPLv3](LICENSE) 许可证。也可获得商业许可、咨询与支持，详见 [COMMERCIAL.md](COMMERCIAL.md)。
