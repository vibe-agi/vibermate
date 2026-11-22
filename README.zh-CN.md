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

1. 安装 ViberMate：

   ```sh
   brew install --cask vibe-agi/tap/vibermate
   ```

2. 打开 ViberMate，「运行记录」会显示设置指南。
3. 在指南中或 **设置 → 接入与启动** 中设置 `vibermate` 命令。如果终端随后提示
   `command not found: vibermate`，请新开一个终端窗口；仍然找不到时，App 会给出把
   `~/.local/bin` 加入 PATH 的命令，以及暂时可用的完整路径命令。ViberMate 不会自己修改
   你的 shell 配置。
4. 仅 Codex：在 ViberMate 中打开 **设置 → 安全与数据 → ViberMate 证书**，点击
   **安装并信任**。Claude Code 不需要这一步。
5. 在项目目录运行 `vibermate run -- claude`（或 `codex`）。使用期间请保持 ViberMate 打开。
6. 不需要添加账号或流量策略：Agent 会继续使用自己的登录，请求经过内置的
   **原样转发** 策略，只做记录。

在 Intel Mac 上，只有本版本测试过的 Claude Code 版本（2.1.220）能通过 ViberMate 连接。

ViberMate 会在你的 Mac 上保存每段对话的完整内容 30 天，数据未加密。流量策略可以设置保存得更少，
**设置 → 安全与数据** 可以删除已保存的内容。

**Linux 服务器（x86-64 与 ARM64）**

从[最新发布](https://github.com/vibe-agi/vibermate/releases/latest)下载对应架构的压缩包，解压后启动：

```sh
./vibermated server
```

打开 <http://127.0.0.1:9666>，用 `./vibermated server recovery-key` 输出的密钥创建所有者账号。
如需从其他设备访问，请看[部署指南](docs/deployment.zh-CN.md)。

**有自己的公网域名？** ViberMate 内置自动 HTTPS，可申请、续期并热加载证书，无需另装
Caddy。在 **设置 → 接入与启动 → 其他接入方式 → 使用自有域名在服务器上运行 ViberMate** 中生成原生命令或 Docker 配置，
或直接查看[公网 HTTPS](docs/deployment.zh-CN.md#自动公共-https)与[Docker 示例](docs/docker.zh-CN.md#公网域名自动-https)。
域名需解析到服务器，公网 TCP 443 需直达或四层透传；普通 HTTP 反向代理不能替代 Agent 的 CONNECT 通道。

## 进阶

### 透明捕获（实验性）

**透明捕获** 让 Agent 及其启动的所有进程不依赖代理变量、连同 TCP 与 UDP 一起经过
ViberMate。普通的 `vibermate run` 不需要它。在 macOS 上，请在
**设置 → 接入与启动 → 透明捕获（实验性）** 中启用，并批准已签名的网络扩展与网络配置；
在 Linux 上需要允许非特权用户命名空间。然后使用
`vibermate run --transparent --env work -- claude`（也支持 Codex）。在 macOS 上，
指定 `--server` 时同样需要本机 App。它尚未完成发布验收。详见
[设置、签名与验收说明](docs/transparent-capture.md)。

### 模型服务认证

如需在不登录原生客户端的情况下使用流量策略中的模型服务账号，请在每次启动时显式开启。
同一选项同时支持 Codex 与 Claude：

```sh
vibermate run --env work -- codex                  # 使用原生客户端自己的登录
vibermate run --env work --managed-auth -- codex   # 无需 OpenAI 登录
vibermate run --env work --managed-auth -- claude
```

`--managed-auth` 需要一个托管该客户端目标地址的流量策略（`--env`）；只做记录的策略
无法提供认证。对于 Claude，准备好的客户端目标地址必须保持在该流量策略的托管范围内。
如果常规启动准备把 Claude 改回它的默认服务，只配置了自定义 Base URL 的目标可能会被拒绝；
这种情况下请使用托管的 `api.anthropic.com` 流向。

Codex 使用内部的 `vibermate` 提供商，同时保留同一个 `CODEX_HOME` 和原生历史。
用此选项创建的会话，恢复时请带上同一选项；较早的 `openai` 会话可直接用 UUID 打开：

```sh
vibermate run --env work --managed-auth -- codex resume
vibermate run --env work --managed-auth -- codex resume <uuid>
vibermate run --env work --managed-auth -- codex resume <uuid> --model <模型>
```

原生会话列表按提供商筛选，因此较早的 `openai` 会话可能不会出现；`--all` 只去掉工作目录筛选。
需要指定恢复后的模型时，请显式使用 `--model`。正在使用的会话归 Codex 管理：等待释放后按
**R** 重试，按 **F** 从已保存的历史分叉，或按 **Esc/Q** 退出。分叉会生成新 ID，可能不包含
尚未保存的内容。如果没有 **F**，请退出后保留同样的 ViberMate run 选项，改用 `codex fork`。
ViberMate 不会迁移已保存的历史，也不会解除其他应用的会话锁。Codex 可能会更新被恢复会话的
当前设置。托管模型服务认证不提供 Codex Cloud 或连接器 OAuth 访问权限。

## 了解更多

- [官网](https://vibe-agi.github.io/zh/products/vibermate/)：一步步了解 ViberMate
- [部署与 HTTPS](docs/deployment.zh-CN.md)、[Docker](docs/docker.zh-CN.md)、[备份与恢复](docs/backup-and-restore.zh-CN.md)
- [当前支持范围](docs/capability-support.md)
- [Paseo 集成](integrations/paseo/README.md)：每个对话独立选择流量策略，桌面与手机共用
- [安全策略](SECURITY.md) · [参与贡献](CONTRIBUTING.md)

安装或启动遇到问题时，运行 `vibermate doctor`。 远程 Runtime 登录后可运行
`vibermate doctor --server <同一服务端> [--env <流量策略 ID>]`。
它验证控制/会话/机器以及不带凭据的 CONNECT 入口认证拒绝；可选 `--env` 检查已授权的
有效目录。它不创建 Capture，并明确标记已授权转发、原生客户端登录/启动、模型推理
未检查。本地 doctor 检查运行时、存储及支持平台上的根证书信任，并说明相同范围限制。

## 许可证

ViberMate 使用 [GNU AGPLv3](LICENSE) 许可证。也可获得商业许可、咨询与支持，详见 [COMMERCIAL.md](COMMERCIAL.md)。
