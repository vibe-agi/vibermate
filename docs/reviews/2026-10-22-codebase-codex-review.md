# ViberMate 整体代码审查与 Codex 上游对照 — 2026-10-22

**结论：当前常用协议路径已有支持，但还不能宣称完整跟上 Codex 0.163，也不建议按现状直接发下一版。** 本次确认 7 项问题：3 项 P1、4 项 P2。另列出有源码依据、尚未完成现场复现的网络恢复风险和兼容性缺口，不将它们当作已证实的故障原因。

审查基线是已发布的 ViberMate **v0.1.35 / `b03ebd8be040087d623f7a1b12c340e99994f56f`**。PR #66 的头为 `6995ea291a13505fa14a68666cbfee07bf3accb9`；它只修改出口设置的展示、操作和测试，不包含本报告问题的修复。工作在独立 checkout `/Users/null/.codex/worktrees/fbde/vibermate`，本次仅新增审查文档；所有新复现 fixture、Go overlay 和日志均在仓库外。未改变用户 App、账号、网络、信任、真实 Runtime 数据或发布状态。

官方稳定版基线是 **[Codex rust-v0.163.0](https://github.com/openai/codex/releases/tag/rust-v0.163.0)**，发布于 2026-10-15，提交 `83086188ae0614bd882818ff1aa23f3194962012`。另对照 2026-10-22 的 main `342ed69a174c206c1de52a59491b131e0753cfa5`；当时最高预发布为 0.164.0-alpha.10。main 的变化单列，不能当作已发布要求。官方 API、Git 历史及完整源码快照保存在证据目录；浏览器工具不可用时使用的是官方 GitHub API/仓库下载。

## 已确认的问题，按优先级排序

### 1. P1：发布工具链存在新披露漏洞，当前 CI 已明确失败

位置：[go.mod:5](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/go.mod#L5)、[CI 编译器断言](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/.github/workflows/ci.yml#L261)、[VLESS kernel lock](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/tool/vless-kernel/kernel-lock.json#L20)。

当前使用 Go 1.26.9。2026-10-21 公布的 **GO-2026-6809 / CVE-2026-111943** 涉及受影响信任链中的已被攻陷中间证书，1.26 分支修复版本为 **1.26.10**。PR #66 的 vulnerability 和 native-release-build 两个 job 均因此失败；后者已完成编译和交叉构建，在漏洞扫描阶段失败。[官方固定版本公告](https://github.com/golang/vulndb/blob/e0bad56276e98711a598adbab976de7cd914013a/data/osv/GO-2026-6809.json)、[本次 CI](https://github.com/vibe-agi/vibermate/actions/runs/39614335458)。

应一起升级主程序、VLESS 内核、构建/验收脚本的编译器要求及制品证明，再重新构建验证。只改 `go.mod` 不会更新已安装的二进制。扫描中的可达符号不等于已经证明攻击成功；macOS 系统证书验证路径还依赖操作系统的信任更新，本次没有证明已发布 macOS App 可被利用。详见 [状态与交付审查 SD-1](2026-10-22-state-ui-delivery.md)。

### 2. P1：Codex 子命令后的 `-c` 能使注入的 provider 配置消失

位置：[codex_provider.go:90](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/internal/runlauncher/codex_provider.go#L90)、[launcher.go:338](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/internal/runlauncher/launcher.go#L338)。

ViberMate 把 managed provider、base URL、认证方式等作为根级 `--config` 放在用户参数前。Codex 0.163 的 Clap 处理会让子命令层的同类参数覆盖先前集合。例如在 `app-server --stdio` 后添加合法的 `-c 'model_reasoning_effort="high"'`，可能同时丢掉之前的 provider 设置。

使用实际安装的 **Codex 0.163**，临时 HOME/CODEX_HOME、合成凭证和操作系统级完全禁网，调用 `config/read` 得到：

| 情况 | 有效 model_provider | 注入 provider 定义 | reasoning effort |
| --- | --- | --- | --- |
| 只有根级注入 | `audit_fixture` | 存在 | 未设置 |
| 子命令后再带 `-c` | 默认/空 | **消失** | `high` |

这证明 managed provider/auth/base 约定会失效，可能退回原生配置或因没有原生账号而失败；没有执行模型请求，也没有证明凭证泄漏。上游已经在 **main 的 [#56022 / 38da85f](https://github.com/openai/codex/commit/38da85f49e0ab8247d1840f7437ce06fd12d7856)** 修复参数收集，但稳定版 0.163 没有该修复。ViberMate 仍需在支持的稳定版上保障注入语义，并增加真实 CLI 的参数作用域回归。不能仅通过“字符串中包含注入参数”的单元测试验收。

证据：`upstream/native/config-scope-probe.py`、`config-scope-result.json`。

### 3. P1：企业账号的工作区配置接口被代理拒绝，导致启动失败

位置：[operationcatalog/catalog.go:155](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/internal/operationcatalog/catalog.go#L155)、[upstreamservice/account_reads.go:89](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/internal/upstreamservice/account_reads.go#L89)。

稳定版 Codex 对符合条件的 Business / Education / Enterprise 账号，在没有有效身份匹配缓存时，需要读取 `GET /backend-api/wham/config/bundle`。当前目录允许 `accounts/check`，却不允许这个配置接口。[官方资格与启动逻辑](https://github.com/openai/codex/blob/83086188ae0614bd882818ff1aa23f3194962012/codex-rs/cloud-config/src/service.rs#L198-L228)、[官方接口](https://github.com/openai/codex/blob/83086188ae0614bd882818ff1aa23f3194962012/codex-rs/backend-client/src/client.rs#L526-L544)。

以现有真实 Runtime/proxy/CA/account acceptance fixture 为基础，实际 Codex 0.163、临时 HOME/CODEX_HOME、只允许 loopback 的操作系统沙箱，除合成账号 plan 外其他设置相同：

- **Plus 对照通过**：本地身份、托管额度和授权历史读取均通过，账号文件保持原样。
- **Enterprise 失败**：`account/read` 返回 `-32603: failed to load workspace requirements`。
- 独立经过同一生产 CONNECT 代理验证：`accounts/check` 返回 200；`config/bundle` 返回 **422 `path_unsupported / client_target_path_not_allowed`**；准备好返回 200 的测试 original transport 被调用 **0 次**。

现有 native account acceptance 使用 Plus，未覆盖这个场景。修复应明确原生工作区配置的身份和路由归属，保留企业强制要求；不能伪造空的成功配置，也不能不加区分地将其转换成托管推理账号的配置。原生复现仅覆盖 Enterprise；其他资格类型由官方源码支持。

证据：`root-repros/enterprise-native.log`、`plus-native.log`、`bundle-catalog-red.log` 及对应 overlay/fixture。

### 4. P2：当前稳定版合法的 Cyber 参数被启动器拒绝

位置：[codex_isolation.go:203](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/internal/runlauncher/codex_isolation.go#L203)。

Codex 0.163 的 `exec` 支持 `--cyber-access-program`，ViberMate 的参数分类表缺少它，尚未启动客户端就返回无法隔离 Capture。对实际产品 `classifyCodexInvocation` 执行 Go overlay 测试：普通 `exec --model` 控制正常；独立值和 `=value` 两种 Cyber 写法均失败。[官方参数定义](https://github.com/openai/codex/blob/83086188ae0614bd882818ff1aa23f3194962012/codex-rs/exec/src/cli.rs#L31-L34)。

应按客户端适配器及作用域补齐参数契约，覆盖 exec/resume 等合法组合，继续拒绝未知的不安全启动模式。不要为了兼容一个参数而放开整个解析器。证据：`root-repros/parser-red.log`。

### 5. P2：每分钟的记录清理即使没有过期数据，也重写所有归档

位置：[recording_cleanup.go:109](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/internal/productruntime/recording_cleanup.go#L109)、[routine 调度:158](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/internal/productruntime/recording_cleanup.go#L158)、[CompactRecordingSource:226](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/internal/runtimepersistence/recording_cleanup.go#L226)。

调度每分钟创建清理义务；处理时暂停记录/读取接纳，对当前及 legacy source 执行完整 `VACUUM`、检查和证明，不判断该 source 是否实际删除了记录。合成的 17.5 MB 旧版归档，两轮都删除 0 条，SQLite schema cookie 仍从 109 → 110 → 111，分别耗时约 684/627 ms，证明重复重写。

记录在此期间可以被丢弃，成本随历史数据规模增长。测试没有证明 Agent 执行阻塞。应让无过期数据的周期无需重写，按实际变更 source 做必要的物理清理；真实删除仍保留持久拒绝证明、迟到写入隔离与完成认证。详见 [SD-2](2026-10-22-state-ui-delivery.md)。

### 6. P2：一次记录维护失败会持续停用记录，却丢失具体错误

位置：[recording_maintenance.go:101](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/internal/productruntime/recording_maintenance.go#L101)、[recording_storage.go:222](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/internal/productruntime/recording_storage.go#L222)。

清理错误被压成布尔值；失败后 `unsafeMaintenance` 保持记录/详情关闭，后续 tick 不再尝试，而结构化 `RecordingFailure` 没有保存原因。用临时 SQLite reader 触发 checkpoint 失败再释放 reader，得到：Runtime initialized、storage healthy、recording degraded、cleanup pending、**specific failure=nil**；新记录被丢弃，控制面的 Capture 创建仍成功。

这能解释一种“客户端正常、详情一直不可读且不知道原因”的代码路径，但本次没有证明用户早先截图就是由此触发。隐私恢复闸门不应直接删除；应保留清理阶段、可脱敏原因及恢复状态，按同一持久清理义务完成验证后才恢复接纳。详见 [SD-3](2026-10-22-state-ui-delivery.md)。

### 7. P2：迟到的读取响应覆盖已保存的 IP allowlist 显示

位置：[workbench_controller.dart:2423](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/ui/flutter_app/lib/features/workbench/workbench_controller.dart#L2423)、[save:2442](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/ui/flutter_app/lib/features/workbench/workbench_controller.dart#L2442)。

安静刷新发出 revision 1 GET，随后保存 revision 2 成功，最后旧 GET 返回时 UI 无 generation/revision 检查，显示回 revision 1。真实 controller + 延迟响应 fixture 复现：API 测试替身中的权威状态仍是 revision 2 和新 CIDR，界面显示 revision 1 和空列表。**没有改变服务端权限**，但会误导用户并引起后续编辑冲突。

应对读取发布加版本/generation 约束，并测试保存后旧 GET、并行刷新乱序、冲突刷新和销毁。证据：`audit-allowlist-repro.log`；详见 [SD-4](2026-10-22-state-ui-delivery.md)。

## 网络与代理：已有保障及仍需验证的边界

| 边界 | 本次源码结论 | 处理方向 |
| --- | --- | --- |
| 代理和 DNS 选择 | `egressprofile.DefaultManager.Resolve` 按账号 > 流量策略 > 全局 > 内置直连确定一次请求的出口；保存 profile 新 revision 不等于更新引用它的默认设置/已运行 Capture。DoH direct/proxy 是独立字段。 | 展示实际生效的引用、revision 和 DoH 路径；PR #66 针对此 UX，不等于实现网络自动恢复。 |
| 本机控制/Agent 转发 | 本机控制与 launcher relay 使用独立 loopback 边界。provider transport 每次请求新建 HTTP transport，关闭响应时回收。 | 不能仅凭重连症状断言所有请求都在复用睡眠前的旧 TCP 连接。 |
| VLESS 生命周期 | 缓存按不可变代理凭证选择；已退出且清理完成的 helper 会被淘汰，新操作可启动新的实例；普通 dial 失败不重放模型请求。 | 不能把进程仍在运行当作远端路径健康。物理睡眠/换网及 kernel 远端握手取消需要专门验收。 |
| VLESS 节点域名 | `vlessprofile.Manager.start` 在启动时系统解析节点域名，固定第一个地址，保持原 SNI；存活 helper 不重新解析。 | 这是域名节点地址变动/单地址不可达的恢复限制。尚未做受控节点换址复现，不能判定用户当前节点命中了此条件。 |
| DoH 预算 | A、AAAA 串行查询；已有一个族的地址时，仍等另一个查询结束，且可能在 parent context 到期后返回已有地址。拨号随后检查剩余 deadline。 | 补单地址族阻塞、取消和预算分配测试；优化时保留完整答案的私网限制验证，不能直接跳过。未列入已确认根因。 |
| 模型能力发现 | 成功目录缓存 45 秒、最多 128 项；缓存身份包含账号/凭证和冻结出口范围，失败不存永久负缓存。非 Standard 模式缺可信目录时仍阻止模型请求，20 秒预算。 | 可达性与能力发现会影响调用开始。可评估同范围并发合并、短暂错误的有界 last-good 策略及可读错误，但不能擅自换账号/出口、忽略撤销或降级模式。 |
| Codex 系统代理回退 | 0.163 默认开启 `system_proxy_fallback`。特定 bootstrap GET 超时/连接失败会采用 OS 路由再试，OS Direct/Proxy 优先于环境代理；ViberMate 只注入环境变量，未约束此行为。 | **源码确认的路由缺口**，尚未执行 OS 双路径原生测试。范围是这些 bootstrap GET，不能扩大成“所有模型 POST 绕过代理”。 |

最后一项的上游依据：[fallback 实现](https://github.com/openai/codex/blob/83086188ae0614bd882818ff1aa23f3194962012/codex-rs/backend-client/src/client.rs#L343-L407)、[OS 与环境代理优先级](https://github.com/openai/codex/blob/83086188ae0614bd882818ff1aa23f3194962012/codex-rs/http-client/src/outbound_proxy.rs#L341-L375)。没有修改用户 VPN、系统代理或真实节点配置。

“记录副本可丢、Agent 能继续工作”已落实到独立 storage roles、非等待记录接纳及预算；清理故障复现中控制操作仍可用。账号、配置、权限、审批及 native 一次性交付事实仍须可靠保存。下一步应修复记录维护与错误呈现，并补网络恢复边界。现有证据不支持为整个 Runtime 引入外部消息队列：它不能直接修复原生 CLI 参数、缺失接口、引用旧 revision 或握手生命周期问题。

这些网络生命周期规则应继续放在共享 Runtime/transport；Codex 参数、bootstrap 和 Daybreak 放在对应客户端/上游适配器。不得让 Claude 依赖 Codex 特有状态。

## 是否跟上 Codex 最近变化

| 项目 | 0.163 稳定版适配状态 |
| --- | --- |
| 常用 Responses/SSE、原始同协议内容、incomplete terminal | 已有处理；本次相关协议包通过。并不等于逐项跑过全部原生 TUI 场景。 |
| 普通 OAuth/API key 选择、显式 managed auth | 接口层已有分离；显式 managed provider 受第 2 项参数丢失影响。 |
| 原生账号读取、模型目录、workspace routing GET | Plus 原生对照通过；Enterprise cloud bundle 缺失已复现。 |
| Responses WebSocket | 明确返回 426；官方仍支持回退 HTTP，不能因缺 WebSocket 实现直接判定不兼容。 |
| daemon/resume | 有 `--no-daemon` 隔离、前台命令分类和原生会话所有权；本次没有重新执行完整真实 resume/升级重连矩阵。 |
| Daybreak | ChatGPT 托管模式已有适配；`exec` 新参数缺失。API key Daybreak/原生模型目录是明确的能力差距，不能因此说普通 API key 推理不可用。 |
| 版本识别 | 精确 tuple 较旧，但 Darwin arm64 有发布者签名 fallback；不能把缺新 hash 等同于所有新 CLI 都无法识别。hoisted npm / 其他平台仍需矩阵。 |
| CI 原生客户端 | 当前主要 pin 为 0.145、0.159.2、0.161，未建立最新稳定 0.163 的完整验收。应保留旧回归并另加当前稳定版。 |

main-only 观察项包括：#56022 参数覆盖修复；#56055 的可信配置服务器要求；账号认证所有权及模型目录 transient failure/缓存处理变化。这些需要预发布跟踪，但不应和已发布 0.163 的缺口混在一起。#52702 系统代理回退、#53701/#53717 API key Daybreak、#53451 incomplete response 已在 0.163 中，不能延后当作未来功能。逐条证据见 [Codex 专项审查](2026-10-22-codex-upstream.md)。

## 覆盖范围与验证证据

| 范围 | 本次检查 |
| --- | --- |
| 架构/状态权威 | CONTEXT、module map、相关 ADR；Runtime composition、Capture/Environment/账号冻结、必需控制存储与可丢记录存储。 |
| 转发/协议/工具边界 | Responses、Anthropic、同协议保留、SSE、exchange commit/retry、工具输出判定、原生 MCP 生命周期；未确认新的核心编解码阻断。 |
| 网络/权限 | proxy/DoH、出口优先级、VLESS session、credential lease、bootstrap、保留连接有效性、remote scope 与真实 TCP peer 边界。部分为源码审查，并非攻击测试或全面安全认证。 |
| 持久化/隐私/交付 | schema cutover、清理义务/物理删除、备份恢复、状态错误呈现、Web session、Paseo/通用 Agent 绑定、CI/编译器/kernel 供应链。 |
| Flutter | controller 并发、记录/用量/存储读状态、设置版本展示；新增 allowlist 乱序复现。未重新执行完整视觉验收。 |

本次新执行结果：

- **14 个现有 Go 包全部通过**：`openairesponses`、`anthropicchat`、`responseschat`、`protocolcore`、`ssewire`、`exchange`、`egressnetwork`、`egressprofile`、`vlesscore`、`providertransport`、`originaltransport`、`runlauncher`、`clientadapter`、`captureadmission`。使用固定 Go 1.26.9；这些是功能验证，不抵消漏洞扫描失败。未设置 opt-in 真 kernel/真实供应商验收变量，包通过不能充当相应端到端验收。
- 两个记录维护 fixture 成功复现缺陷，使用临时 SQLite 和真实 Runtime。
- Cyber 参数、cloud-bundle 和 allowlist 的期望正确行为断言 **RED**，证明当前基线的问题；没有伪称修复后 GREEN。
- 实际 Codex 0.163 配置作用域对照完成；Plus 原生账号读取通过，Enterprise 原生读取失败。
- 保存了 PR #66 的确切 CI 日志及官方漏洞公告，未绕过检查。

未执行全仓 race、完整 Flutter suite、Windows/Linux 原生客户端、现场睡眠/VPN 切换、真实托管账号或企业服务请求。没有访问用户 auth.json、打印真实密钥、恢复用户备份、改动系统信任或发布二进制。本报告是主要模块和高风险路径的整体审查，不是每行代码/每个平台都经过动态测试的承诺。

证据根目录：`/Users/null/Downloads/vibermate-codebase-review-20261022`。核心日志为 `root-repros/protocol-baseline.log`、`network-launch-baseline.log`、`parser-red.log`、`enterprise-native.log`、`plus-native.log`、`bundle-catalog-red.log`、`audit-recording-repro.log`、`audit-allowlist-repro.log`。外部 overlay/fixture 保留了可重复运行的命令与源文件。上游快照、SHA、时间记录见 `upstream/snapshot.json`。

## 建议的修复与发布顺序

1. **先完成发布前必要修复**：工具链及 kernel 同步升级，修复 provider 参数保留和企业配置接口；补 Cyber 参数。保留这次 RED 场景，逐个修到 GREEN，并做当前稳定 CLI 验收。
2. **验证统一网络边界**：在受控环境验证系统代理回退、节点地址变化、单 DNS 族阻塞、睡眠/换网和 App 重启。增加明确的阶段诊断；共享层同时验证 Codex、Claude 及普通 HTTP 客户端。按证据修复，不通过无条件直连或重放已发送请求来掩盖问题。
3. **单独修记录与 UI**：无工作清理不重写、维护失败原因与安全恢复、allowlist 响应顺序。它们可以独立提交和递进交付。

每次发布仍需 exact source/artifact 的 CI、签名/公证及安装验收。当前报告没有执行修复或发布，也没有把上次版本的绿色结果当成本次发布证明。

后续网页现场审查又确认 1 项 P1 和 2 项 P2，包含正常 HTTP 200 内容详情被前端拒绝解析的游标格式缺陷。其现场证据、跨语言复现及与 summary 409 的区别见 [网页详情补充报告](2026-10-22-web-detail-followup.md)；它们不包含在本报告原始的 7 项计数中。
