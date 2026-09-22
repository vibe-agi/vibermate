# Agent CLI 账号一致性与适配器实现

日期：2026-09-22。状态：**第一阶段已实施；见 [ADR 0013](../adr/0013-scope-account-reads-through-service-adapters.md)**。

按上下文将“xcode”理解为 Codex，先核对官方源码快照，再在现有 OAuth 工作树中实现。未更改用户认证文件、运行中的 CLI、ACP 分支或发布状态。

## 当前交付边界

- 唯一固定 Route/Account 的 Codex 额度查询跟随托管账号，使用同一个刷新与租约入口。账号历史需额外勾选流量策略中的授权并发布；修改只作用于新 Capture，已启动 Capture 仍使用其冻结修订。模型请求原有动态账号选择不变；无明确账号的控制查询拒绝，不回退到原登录账号。
- 上游账号页提供显式额度/历史查询，不必先关联 Profile。管理 API 是 `GET /api/v1/provider-accounts/{id}/account-facts?kind=quota|history`，需要管理员写权限，返回有来源的投影，不返回凭据或原生响应。界面分别标明采集时间、上游口径及刷新失败的旧值。
- 服务差异集中在 `upstreamservice`，操作在 `operationcatalog` 注册，两种入口共享 `accountoperation`；不添加第二套账号选择、刷新或网络执行。
- **本地邮箱/登录套餐仍由 Codex 自己管理**。透传模式不伪造它们；HTTP SSE 已提交的头不追加额度，改用后续显式查询。未接管模型目录缓存，也未启用实验性的原生身份控制或改变 ACP 观察模式。

### 验证记录

- 后端：固定 B 在首轮生成前可查，动态选择拒绝，关联撤销拒绝，新操作使用新 credential epoch，历史授权隔离；真实 CONNECT/TLS 代理验证 A 的 Cookie/认证不会到达 B，未授权历史和 reset 写操作不出站。
- 本机 Codex `0.155.1`：隔离 `CODEX_HOME`、合成凭据和本地测试 CA，经生产代理运行 app-server 的 `account/read`、`account/rateLimits/read`、`account/usage/read`。本地身份为 A、额度和历史为 B、原文件字节未变。不是对真实订阅服务的联网验证，也不是原生 TUI UI 的验收。
- Web：真实独立服务端和 owner 登录；浏览器侧仅替换上游事实响应。Playwright 验证查询不自动触发、账号独立管理、权限正常、历史单独读取、失败标旧以及桌面/390px 布局；无浏览器异常。未调用真实账号。截图保留在本次本地测试目录。
- 回归命令：`go test ./...`；`flutter analyze`、`flutter test`；本机 CLI 验收 `VIBERMATE_CODEX_ACCEPTANCE=/absolute/path/to/codex go test ./internal/productruntime -run TestInstalledCodexReadsManagedAccount -count=1`。未配置该变量时 native 验收明确跳过。
- 最终回归：全量 Go 通过；账号操作/服务适配、账号、Transport、Runtime、代理、控制 API、OAuth、Environment 和 SQLite 的 race 测试通过；Flutter analyzer 无问题，476 项通过、5 项既有跳过。持久化测试额外发现并修正了 `upstream_account_read` / `credential_refresh` 的 SQL 用途白名单缺项，新增全目录一致性检查。公开 schema 仍为 v1；已知本地开发形态在事务中保留账号、证据与审计 sequence 后收敛，未清空用户数据。
- 本地构建：`ui/flutter_app/tool/build_macos_app.sh live` 通过，产物 `dist/ViberMate.app`；包含本次 Web 与 Go sidecar，打包校验通过。没有发布，也没有启动它接管用户真实数据；原生 App 的真实账号联网验收留给本地操作。

以下保留设计推导；其中原生身份控制、模型缓存与不同流式提交策略是后续边界，不表示本阶段已支持。

## 1. 目标与已证实的限制

目标是让模型执行、账号相关只读查询和用户看到的实际执行身份保持可解释的一致性，并让后续 CLI 的差异集中在 Adapter，而不是散落到 HTTP handler、UI 和转发代码。

Codex 的本地身份、网络额度、历史用量、推理响应元信息是不同来源。透明 HTTP 代理不能修改全部本地登录状态；app-server 的外部 token 能力也不能直接等同于原生 TUI 支持。[数据来源核对](../research/codex-status-account-routing.md)、[托管身份接口核对](../research/codex-managed-identity-integration.md)

当前已有可复用的 Module：

| Module | 本次保留的职责 |
| --- | --- |
| `clientadapter`、`runlauncher` | 客户端识别与版本证据、启动配置、子进程生命周期。 |
| `operationcatalog`、`protocolspec`、`protocolpath` | 精确操作契约、编译后的能力与现有两侧消息 codec。 |
| `environment`、`exchange` | 冻结策略、路由与账号选择、请求执行及流式提交语义。 |
| `provideraccount`、`providerauth`、`codexoauth` | 账号关联权限、刷新所有权、按请求冻结的凭据租约、最终认证注入。 |
| `providertransport`、`offlinehold`、`egressaudit` | 唯一受控网络执行、等待与取消、无秘密审计。 |

这些能力不在新 Adapter 中重做。[现有 Module Map](../module-map.md)、[OAuth 租约决策](../adr/0011-manage-codex-oauth-before-freezing-account-leases.md)

## 2. 两个正交的适配维度，共享同一运行时

| Adapter / Module | Interface 所表达的能力 | 不拥有 |
| --- | --- | --- |
| **Client Adapter**：Codex、Claude Code 等 | 已识别版本支持的启动方式、客户端原生身份/会话证据、可选的原生认证控制。 | 选择上游账号、刷新令牌、通用网络拨号。 |
| **Upstream Service Adapter**：ChatGPT Codex 服务、其他明确支持的服务 | 服务特有的操作契约、请求/响应映射、账号额度事实解析、可用认证能力。 | 路由权、秘密存储、重试决策、直接访问网络。 |
| **共享运行时 Module** | 从已授权作用域解析 Route/Account，取得租约，执行模型或账号操作，记录结果及安全投影。 | 猜测 CLI 私有状态、在 Handler/UI 中硬编码服务特例。 |

CLI 身份与上游服务不是一一对应：Codex 可以连接 ChatGPT 或 API 服务，同一个服务也可能被多个 CLI 使用。JSON/流式消息仍由现有 codec 处理，不能复制到每个 Client Adapter。OAuth/静态 Header 仍是现有认证 Driver，不与 CLI 生命周期合成一个巨大 Interface。

建议的 Seam 是“已声明的操作 + 已授权的请求作用域”，而不是通用的任意 URL/Header Hook。具体协议路径仍然需要维护，但新增路径只改变相应服务契约及 fixture，不应重写核心执行链。

## 3. 把操作用途与账号归属写入契约

复用现有 method/path/query/body/ReplayClass 契约；补上明确的操作用途与认证归属，不从 `GET`、`control` 或 `opaque` 推断“应该换号”。

| 操作用途 | 凭据与执行规则 |
| --- | --- |
| 模型生成 | 使用现有冻结 Route 与 Account Selection Policy。 |
| 上游账号只读操作：额度、用量、模型目录 | 只有明确支持且存在唯一授权账号时，跟随该账号，复用刷新与租约。 |
| 客户端自身操作：原登录、登出、本地设置及其网络查询 | 保持原有认证归属；不因模型换号而自动接管。 |
| 账号修改操作：消耗 reset、充值、撤销授权等 | 单独建模和授权；本次只读能力不开放。 |
| 未知操作或未知版本扩展 | 不获得托管账号的凭据；保留明确的 unsupported/原目标规则，不扩大匹配。 |

`operationcatalog` 保持唯一可执行操作目录，服务 Adapter 提供定义，由运行时显式组装并编译；不另建一份 Handler 路径表。版本支持来自可验证的客户端/协议证据，未知客户端仍可使用原有通用捕获能力，但不会因此自动获得原生身份控制。

**对原控制面归属约束的窄化调整**：只有“明确登记、账号相关、只读”的操作成为可托管类别，已记录于 ADR 0013。它不授权整站换号，也不改变登录/设置/账单操作的默认归属。

## 4. 查询究竟对应哪个账号

不能新增进程全局 `currentAccount`，也不能把最近完成的一次请求当作全局账号。现有 Account Selector 按 Turn 决策，Client Session 也不拥有 Environment。[当前词汇](../../CONTEXT.md)、[Account Selector 决策](../adr/0007-freeze-account-selector-with-route.md)

建议解析顺序：

1. 已授权的显式账号操作：使用权限检查后的确切账号引用，例如账号页面上的额度刷新。
2. 固定 Route：使用该 Route 冻结的固定账号，额度查询与模型请求获取各自的短期租约。
3. 存在精确关联证据的执行上下文：使用其已选择账号；关联必须受 Capture、策略修订及原生会话作用域约束，不能只相信 Header 中的 session ID。
4. 动态 selector 请求缺少 Turn、多个候选 Route、并发账号或没有明确关联：返回“无法确定对应账号”，不重新运行 selector，不选择全局最近账号，也不回退到原登录身份。

账号页面发起的操作校验当前操作者及显式账号权限；被捕获 CLI 的操作则必须携带该请求已冻结的 Environment/Route 授权，不能用一次当前配置查询替代。二者共享执行 Module，不共享或放宽各自的授权入口。

原生 CLI 往往只能展示一个账号的额度。动态选多账号时，ViberMate 应按账号展示实际使用记录和额度来源，不能把不同账号额度相加后冒充原生账号报表。第一阶段的原生查询同步只承诺明确固定账号场景；这不限制模型请求现有的动态账号选择。

账号身份绑定与凭据版本分开：不会把一次 Attempt 的 access token 永久固定到整个会话。每个新操作都经过同一刷新与租约 Interface；刷新不改变账号身份。在途请求继续使用其冻结版本，后续请求使用已提交的新版本。

## 5. 保留两种互不冒充的接入能力

### 透明代理 / `vibermate run`

支持模型与明确只读操作的路由、认证和额度一致性；不覆盖全局 `auth.json`，不伪造本地邮箱或返回账号 ID。Workbench 明确区分“客户端登录身份”和“实际使用账号”，未观察到的信息保持未知。

### 显式托管原生身份

仅在 Client Adapter 有经过验证的原生认证控制能力时启用。Codex 外部 token 模式是候选：ViberMate 保持 refresh token 的唯一所有权，受管进程仅获得所需的短期 access token 与账号资料，不共享可旋转 refresh token。

但是 app-server 的能力、原生 TUI 的能力和某个 ACP bridge 的能力必须分别验证；支持其中一项不能推导其他项。保留原生 TUI 还涉及刷新 RPC 归属，不能默认通过再连一个控制客户端解决。

现有 `feat/acp-integration` 分支明确是 byte-preserving observation，编辑器拥有认证和权限，且不注入 HTTP 策略。这个默认保持不变。未来托管身份必须是显式能力与授权，不能通过观察到 ACP `authenticate` 消息就取得账号控制权。

不把修改 `CODEX_HOME` 并复制完整 `auth.json` 当作默认方案：它同时影响配置/历史位置，并会引入双重刷新所有权。

## 6. 统一账号事实，不混淆统计口径

服务 Adapter 将允许公开的结果投影为有来源的账号事实；至少携带账号引用、源站/工作区、采集时间、适配器修订、适用窗口/模型范围，以及 `known / stale / unsupported / unavailable` 状态。

- 账号额度、账号历史活动、单次响应 token usage、ViberMate 自己的使用记录不是同一个指标。
- 缓存按真实账号、源站及权限范围隔离；刷新失败可以展示带时间的旧值，但不能标成最新或补成 0。
- Web、App 和托管控制通道读取同一投影，不各写一套查询器。
- 团队中允许使用某账号推理，不自动等于允许读取整个账号的历史活动；账号级查询需独立权限判定。
- HTTP 原生兼容只返回经支持矩阵认可的真实结果，不给不同后端编造 Codex 账号/套餐/额度。

## 7. 响应提交不能靠事后追加 Header

当前普通托管 SSE 在上游响应前提交 Hold envelope，之后无法再加入上游额度头。应让服务 Adapter 提供经过验证的响应事实、客户端呈现能力提供合法的承载方式；最终响应提交仍由 `exchange` 与 CommitLedger 独占。[现有提交](../../internal/exchange/pipeline.go#L2287)

响应兼容只开放明确安全的元信息；不因恢复额度信息就透传所有 Header。Cookie、账号绑定的 turn-state 和其他提供商私有状态继续遵守账号切换与保密规则。原始证据保持真实，与客户端显示投影分别记录。

对需要响应头的原生客户端，须验证在上游头到达后提交最终头的策略，并覆盖首包等待、Offline Hold、重试、断连和工具审批行为。若某条 Hold 路径已提前提交，只能使用该客户端确实支持的带内事件/控制通知，或者后续显式查询刷新；不能把 WebSocket 支持的额度事件假设成 HTTP SSE 也支持，更不能静默移除原有 Hold 能力。

这应是独立的响应提交契约与验收项，不埋在服务 Adapter 的 Header Hook 中。

## 8. 实施顺序与验收

1. 固化契约与能力测试：Codex/ChatGPT 与已支持 Claude/Anthropic 的现有行为作为真实差异；不声称实现尚未支持的 Claude OAuth/额度能力。
2. 在账号与现有 Transport 之间补一个受限的账号操作执行 Module，Interface 接收授权作用域和类型化操作，不接收任意 URL/凭据；内部复用已有权限、刷新、租约、Offline Hold 和审计。
3. 接入 Codex 固定账号的额度/用量只读操作，同时实现带来源的账号事实投影。HTTP 入口与账号页面共享执行路径；不把这些查询计成模型 Turn/token usage。
4. 独立验证响应元信息与提交策略，完成 native `/status`、`/usage` 的真实 CLI 验收。
5. 再接经过验证的可选原生身份控制；不要求第一阶段 fork CLI 或重新实现现有 ACP bridge。

测试跨同一 Interface 断言结果：原登录 A/托管 B 不串号；并发账号隔离；查询先于首轮生成；动态账号歧义拒绝；refresh 单次合并与旧版本请求；账号关联撤销；跨源/重定向不泄露；缺失能力；只读权限与 reset 写操作隔离；缓存过期；SSE 首包与 Hold；版本升级 fixture 差异。所有秘密不得进入日志、原始证据、UI 投影或测试报告。

新增 CLI 的验收标准是：注册其 Client Adapter 与 fixture 后，不修改核心账号执行 Module；新增上游服务同理只增加服务适配与相关认证 Driver/codec 的确切能力。只有出现已证实的共同语义缺口时才扩展公共 Interface，不按未来假设预造框架。
