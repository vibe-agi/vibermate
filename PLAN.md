# ViberMate 当前实施计划

任务状态以 official-plane 的 ViberMate 项目（`779ab6f7-24dd-49ca-9b01-d8be8ef20a51`）为准：总任务 **VIBERMATE-48**，子任务 49–58。本文件是仓库内的实施索引。

## 目标

在没有外部用户的前提下，把 v0.1.16 之后的代码收敛为干净、正确的单一实现：修复独立审查确认的缺陷，删除兼容与历史机制，文档与代码一致，然后重新发布。

## 已定的架构决定

- **协议边界**：每个进入编解码器的 JSON 文档（请求体、响应体、每个 SSE 事件）先按 `encoding/json` 的大小写折叠规则拒绝重名键，保证代理与客户端看到同一组字段。
- **同方言转发**：原始报文是权威；投影只观察它能建模的部分。未建模的历史、工具定义与字段不阻断请求。上游未建模的输出项是"未证实动作"（`provider_action`），由 Environment 工具策略决定：观察放行、审查询问、严格拒绝。在上游已执行完的托管工具结果按不透明数据透传。
- **审计可用性**：保留"无审计不外发"。终态审计写失败时保留并重试，期间拒绝新外发，写入后自动恢复；关停时仍有未写终态则停止失败。
- **写事务有界**：共享写连接上的清理、维护按 100 条分批提交。
- **Schema**：只有一份 `schema.sql` 和整数修订号，不做迁移与离线转换（[ADR 0021](docs/adr/0021-one-schema-revision-without-migrations.md)）。其他修订创建的数据目录会被拒绝打开且不被修改。
- **Server 授权**：Runtime User 只能启动 Owner 显式授予的 Environment，新成员默认没有授权；"全部 Environment"是显式标志；Owner 自己的会话拥有全部。代理只接受 ASCII 规范主机，策略、审计与拨号使用同一个主机。

## 本分支已完成（`fix/post-016-review`）

- 协议：大小写折叠重名键拒绝；Responses 与 Anthropic 未建模输出走工具策略；Responses 字符串 input、无 type 消息、未知历史/工具/tool_choice/include/input_file 不再被拒；incomplete 保留原因；Anthropic ping 计为存活；转换脚本不能以任何大小写形式改 model。
- 安全：Unicode 主机名绕过 deny 规则；成员默认授权；OAuth 凭据替换不再丢弃新修订上的轮换；私有 PKI 只发中间证书链时固定最上层 CA；远程 Server 下发的启动环境变量只接受 Agent 行为类允许列表（S2）。
- 可靠性：审计终态熔断与幂等写入；手动清理与维护分批；单个 Exchange 的本地身份与线上身份冲突不再中止整轮会话索引（L7）。
- 界面：Route 账号集合只做结构校验并显示真实不可用原因；报表导航原子提交，翻页不跳页、下钻失败保留原视图、错误可重试；额度倒计时在窗口失焦时继续更新；报表单元格带列名供读屏，短报表不再占 480px。
- 结构：Schema 单修订号；删除离线转换工具、开发库归档函数与历史文档；CI 检查 `dart format`。
- 发布：撤下 v0.1.14–v0.1.16 的 Release、tag 与 Homebrew cask；`golang.org/x/crypto` 升级到 v0.56.0。

## 复核后不改

- **盲隧道出口（S4）**：盲隧道承载的是 Environment 未声明的目的地，本来就没有 Environment 出口配置；审计按"网络默认（直连）"记录。Server 成员的 CONNECT 与绝对 URI 请求在入口标记为仅公网，拦截后的内层请求继承该上下文，私网目标被拒。
- **Server 进程拨号默认仅公网**：成员流量全部经入口标记；把进程级默认改为仅公网只会破坏 Owner 自己配置的私网上游，不增加安全性。
- **B7 启动全表校验出口记录**：有意检测被绕过约束写坏的终态记录，20 万行约 0.6 秒，仅启动时一次。

## 已发布

- **v0.1.21**（2026-10-01，`9b7eeaa`）：落地正文优先的对话阅读、就近工具/上下文面板与紧凑 Token/API 等价费用；修复原生 Codex 心跳被审批屏障误缓存造成的重复 SSE 超时。构建验证与下载渠道见 [docs/releases/v0.1.21.md](docs/releases/v0.1.21.md)。
- **v0.1.20**（2026-09-30，`c1eece3`）：修复请求预览与 Codex 协议边界；完善重置券、账号范围/原始账号、用量默认值及刷新交互；增加自有域名部署向导。macOS 签名公证与安装验收、Linux 双架构包、Homebrew 和官网更新见 [docs/releases/v0.1.20.md](docs/releases/v0.1.20.md)。
- **v0.1.19**（2026-09-29，`07bd4e7`）：Server IP 白名单（含 `--trusted-proxies` 与 PROXY protocol）；macOS 签名公证、Linux 包、Homebrew、官网均已更新，见 [docs/releases/v0.1.19.md](docs/releases/v0.1.19.md)。
- **v0.1.18**（2026-09-29，`25091f8`）：Web 工作台自包含（CanvasKit 与字体随包、严格 CSP）、中英文指南与官网改版；macOS 签名公证、Linux 包、Homebrew、官网均已更新，见 [docs/releases/v0.1.18.md](docs/releases/v0.1.18.md)。
- **v0.1.17**（2026-09-29，`7ac39f0`）：macOS 签名公证与安装证据、Linux x86-64/ARM64 包、Homebrew cask、vibe-agi.github.io 均已更新。构建证据与发布说明见 [docs/releases/v0.1.17.md](docs/releases/v0.1.17.md)。
- 发布前验收：Go 全量与 race、govulncheck（Go 1.26.8）、Flutter 全量、Linux 包构建校验、打包 Server 上的无头 Playwright 界面走查。

## 发布之后已完成

- Web 工作台自包含：CanvasKit 与全部 725 个回退字体随构建发布（SHA-256 锁定清单、OFL 声明），Server 下发严格 CSP，打包校验拒绝依赖 CDN 的构建（PR #28）。
- 产品说明：README 只讲大类与重点并引导到官网；官网产品页按“一句话说明 → 适合谁 → 能做什么 → 三步开始 → 值得了解”循序渐进；截图由 `tool/product-screenshots` 从 Preview 构建按中英文生成；部署、Docker、备份指南提供中英文。

- Server IP 白名单：所有者在 Web 编辑 CIDR 白名单，作用于整个 Server 端口，保存即生效并断开不再允许的连接；本机始终允许，保存不会把自己锁在外面，`vibermated server ip-allowlist clear` 用于应急。只认 TCP 对端；四层负载均衡通过 `--trusted-proxies` 与 PROXY protocol v1/v2 告知真实客户端，缺头按负载均衡地址严格判断；从不信任 HTTP 转发头（ADR 0022）。

## 发布之后

- 大 Capture 删除改为异步分批，删除期间显示"删除中"。
- 读请求（正文、raw、Capture 列表、搜索、`dbstat`）迁到只读连接池（B6）。
- 账号切换后加密推理内容的处理（P11），需要先设计上游状态范围。
