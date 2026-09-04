# ViberMate

[English](README.md) | [简体中文](README.zh-CN.md)

**看清并控制 Claude Code 和 Codex CLI 如何连接 AI 服务。**

ViberMate 是一款在 macOS 本地运行的 AI 编程代理管理工具。通过
ViberMate 启动代理后，你可以查看它的请求、选择请求去向、应用简单的
JavaScript 规则，并在本机保留可审计的运行记录。

> **发布状态：**经过 Developer ID 签名和 Apple 公证的通用 macOS App
> [ViberMate 0.1.0](https://github.com/vibe-agi/vibermate/releases/tag/v0.1.0)
> 已经可以安装。这是早期 0.x 预览版，不代表已经达到 GA 稳定性承诺。

## 它能做什么？

- 正常运行 Claude Code 或 Codex CLI，同时查看每次捕获的对话。
- 保持原来的服务连接，或者把请求转发到指定的 Endpoint 和账号。
- 在请求离开 Mac 之前隐藏本机用户名和路径。
- 查看内置 JavaScript 示例，修改后用一个样例 Turn 测试，确认效果再发布。
- 用 JavaScript 选择 Endpoint 账号，包括根据登录 ViberMate 的用户名选择。
- 需要安全断网时，用“断网保护”暂停新的网络操作。

ViberMate 不是 AI 服务商，也不会替代 Claude Code 或 Codex。使用前仍需安装
相应的代理 CLI，并确保它本身能够登录服务商账号。

## 第一次使用

### 1. 安装 ViberMate

ViberMate 需要 macOS 14 或更高版本。使用 Homebrew 安装已签名的 App：

```sh
brew install --cask vibe-agi/tap/vibermate
```

安装完成后，从“应用程序”中打开 ViberMate。你也可以直接下载
[`ViberMate_0.1.0_universal.dmg`](https://github.com/vibe-agi/vibermate/releases/download/v0.1.0/ViberMate_0.1.0_universal.dmg)，
再把 App 拖入“应用程序”。

以后升级或卸载 App，可以执行：

```sh
brew upgrade --cask vibermate
brew uninstall --cask vibermate
```

卸载 App 时会保留 ViberMate 的设置和运行数据。

### 2. 安装终端命令

1. 打开 ViberMate。
2. 进入 **设置 → 常规 → 终端命令**。
3. 点击 **设置终端命令**。

ViberMate 会创建 `~/.local/bin/vibermate`，不会修改你的 Shell 配置文件，
也不会覆盖一个无法确认由自己管理的同名命令。

如果终端提示 `command not found`，可以把示例中的 `vibermate` 换成
`~/.local/bin/vibermate`，或者把 `~/.local/bin` 加入 Shell 的 `PATH`。

### 3. 启动代理

在项目目录中打开终端，执行下面任意一条命令：

```sh
vibermate run -- claude
```

```sh
vibermate run -- codex
```

第一次使用不需要先创建 Environment。内置的透明模式会保留代理原来的请求
地址、账号、凭据和模型。

### 4. 回到 ViberMate 查看

新运行会显示为一个 Capture。打开它即可查看对话、请求、响应、工具活动、
用量和连接结果。需要结束时，仍然在终端按 `Ctrl+C`。

准备好使用自己的路由或 JavaScript 规则后，再创建 Environment 并明确选择：

```sh
vibermate run --env work -- claude
```

## 我需要安装证书吗？

通常不需要。通过 `vibermate run` 启动的 Claude Code 和 Codex 进程会直接收到
本次运行需要的 ViberMate 证书。

只有其他应用必须依赖 macOS 系统信任时，才需要进入
**设置 → 常规 → 本机根证书**安装。ViberMate 会显示准确的 SHA-256 指纹，
并且只为当前 macOS 用户添加信任。私钥始终保留在 ViberMate 的本地数据目录。

存在正在运行的 Capture 时，ViberMate 不允许替换证书。删除和恢复说明始终
要求按指纹确认准确的 ViberMate 证书，不会让你误删其他证书。

## 界面中的五个常用词

| 名称 | 简单解释 |
| --- | --- |
| **Capture（捕获）** | 一次由 ViberMate 管理的代理运行，或一个手动连接的应用。 |
| **Environment（环境）** | 一组可复用的路由、账号、网络和 JavaScript 规则。 |
| **Endpoint（上游服务）** | AI 请求最终发送到的服务地址。 |
| **Account（账号）** | 只属于某一个 Endpoint 的凭据。 |
| **Message transform（消息变换）** | 上传前修改请求、显示前修改响应的 JavaScript。 |

Capture 启动时会冻结当时的 Environment。之后编辑或发布 Environment 只影响
新的 Capture，不会改变已经进行中的请求。

## 数据与隐私

- ViberMate 在本机运行，但 AI 请求仍会发送到你选择的服务商或 Endpoint。
- 运行证据保存在本地 SQLite 数据库中。数据库**没有做应用层静态加密**，
  依赖 macOS 用户账号和本地文件权限保护。
- 新 Environment 默认保存经过脱敏的完整内容，保留 30 天。你也可以只记录
  元数据，或者关闭内容记录。
- 服务商凭据不会进入 Environment 快照和运行证据。你在提示词中主动输入的
  文字仍然属于内容；除非确实希望发送给所选服务商，否则不要在提示词中粘贴
  密钥。
- 消息变换 JavaScript 不能访问网络、文件、时钟或随机源。脚本超时或失败会
  停止请求，不会悄悄绕过规则继续发送。

## 从源代码构建

这一部分面向开发者。需要 macOS 14 或更高版本、Xcode、Go 1.25.13，以及
仓库锁定的 Flutter 3.41.5 SDK。

```sh
git clone https://github.com/vibe-agi/vibermate.git
cd vibermate
make build-flutter-app
open dist/ViberMate.app
```

本机构建的 App 只是 ad-hoc 签名，不是可公开分发的安装包。主要验证命令是：

```sh
make check
make check-flutter-macos
```

想了解实现细节，可以查看[运行时模块地图](docs/module-map.md)和主要的
[架构决策记录](docs/adr)。

## 当前边界

- 当前发布目标是 macOS 14 或更高版本。
- 内置语义解析覆盖受支持 Claude 和 Codex 路径使用的 Anthropic Messages
  与 OpenAI Responses 流量。
- 目前不承诺公网 Server 加固、自动更新、插件，以及任意客户端的广泛兼容。
- Homebrew 与 GitHub Release 提供的安装包已经过 Developer ID 签名、Apple
  公证和 Gatekeeper 验证。本地从源代码构建的 App 仅使用 ad-hoc 签名，
  不是同一个分发产物。

发现疑似漏洞时，请通过[私密安全渠道](SECURITY.md)报告。
ViberMate 使用 [Apache License 2.0](LICENSE)。
