# Codex 托管身份集成能力与客户端边界

日期：2026-09-22。仅核对官方文档和已有源码；未读真实凭据、未运行登录、未请求真实账号 API。
源码固定为 `ebc05da3bdb76f25861e7cb418bd06d28cadc609`，本机 CLI 已知为 `0.155.1`，两者没有核实为同一构建。以下源码结论需按实际部署版本验证。
本地源码根目录：`/tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609`；状态与额度的数据路径沿用[上一份笔记](codex-status-account-routing.md)，此处不重复展开。

## 结论

Codex 已有不修改上游源码的外部身份集成入口：由宿主控制双向 app-server 协议，提供短期 access token 并承担刷新。它不是“透明 HTTP 代理自动改写原生 CLI 本地身份”的能力。纯代理、宿主控制 app-server、原生 TUI 远程附着，应分别声明能力和限制。

跨 CLI 的统一模型应表达账号绑定、刷新所有权、额度/用量读取和运行实例隔离；各 CLI 适配器将这些能力映射到自己的协议。ACP 可以是会话连接方式之一，但本次没有证据证明 ACP 本身统一定义了 OAuth 账号注入、账号更新或提供商额度语义，不能把这些当作 ACP 的保证。

建议保留两个独立边界：Client Adapter 负责客户端启动、协议及身份同步能力；Upstream Service Adapter 负责提供商认证与只读账号操作，复用既有凭据租约和传输。以下均为 proposed 集成能力，不意味着现有控制面透传策略已经改变。

## 官方集成契约

官方将 `chatgptAuthTokens` 标为实验性：初始化需启用 `capabilities.experimentalApi`；宿主经 `account/login/start` 提供 `accessToken`、`chatgptAccountId`、可选 `chatgptPlanType`。服务器可在 401 后发送 `account/chatgptAuthTokens/refresh`，携带 `previousAccountId`；宿主回复新 token，服务器重试原请求，回调约十秒超时。[官方 app-server 认证说明](https://learn.chatgpt.com/docs/app-server#auth-endpoints)

同一协议提供 `account/read`、`account/updated`、`account/rateLimits/read`、`account/rateLimits/updated`、`account/usage/read`。这些可作为适配器的身份与额度接口；reset 消费是独立有副作用能力。外部 token 模式下，`account/read.refreshToken` 不负责刷新。[官方 app-server 认证说明](https://learn.chatgpt.com/docs/app-server#auth-endpoints)

官方支持启动独立 app-server，再用 `codex --remote` 连接原生终端界面；stdio 是默认宿主传输，WebSocket 被标为实验性且不支持生产用途。该连接能力不等于任意运行中 TUI 都能注入认证 RPC。[官方终端连接说明](https://learn.chatgpt.com/docs/app-server#connect-the-cli-terminal-ui)

## 外部 token 的具体身份行为

| 问题 | 固定源码快照中的行为 |
| --- | --- |
| 能传完整 OAuth 凭据吗？ | 登录处理器只接收 access token、account ID、可选 plan；没有独立 ID token 或 refresh token 参数。 |
| `id_token` 字段从何而来？ | `from_external_access_token()` 解析 access token 的 JWT claims，将解析结果放进内部 `TokenData.id_token`；不是收到了一份真正独立的 ID token。 |
| 邮箱显示 | 继续通过内部 token claims 的 `email` 读取；access token 没有邮箱时可为 `None`，不能保证保留 OAuth ID token 中的邮箱。 |
| 套餐显示 | 显式 `chatgptPlanType` 优先，否则使用 access token claim，再否则为 unknown。 |
| 谁刷新？ | 内部 `refresh_token` 为空；外部宿主负责取得新 token，`ExternalAuthBridge` 只发回调并接收结果。 |
| 是否写全局文件？ | 外部认证提交强制使用 Ephemeral 存储，并更新当前认证缓存；该提交路径不写磁盘 `auth.json`。 |

来源：[登录处理器][login]、[外部 token 构造][token-construction]、[邮箱/套餐读取][identity-fields]、[刷新桥][refresh-bridge]、[内存提交][ephemeral-commit]。

`account/updated` 只有认证模式、套餐等字段，当前 TUI 更新处理据此重建显示状态，没有重新取得邮箱；启动时的 `account/read` 才能带邮箱。即便使用官方协议，也应把“认证同步”和“原生 UI 完整显示账号资料”列成两个不同保证。[TUI 更新处理][tui-account-update]、[显示状态构造][display-construction]

## 与原生 TUI 的关系

| 模式 | 无 fork 能力与所需条件 |
| --- | --- |
| 透明网络代理 | 可选择上游凭据并适配请求/响应；没有协议能力直接改掉客户端缓存身份。代理所选账号与客户端显示账号可能不同，见上一份笔记。 |
| 宿主控制 stdio app-server | 已有直接入口。宿主控制初始化、登录、双向请求、刷新回调、账号通知及退出；可在自身 UI 显示准确的绑定账号。 |
| 托管启动原生 TUI，并连接外部 app-server | 有官方远程附着入口，但需要可靠的认证控制通道和刷新请求归属；不是单独设置代理地址就完成。可评估协议网关承担认证回调、转发原生 TUI 支持的请求，仍需原型验证。 |
| 已运行的普通 standalone CLI | 本次未发现可任意附着并注入外部认证的承诺。不能仅因为内部使用 app-server，就推断公开控制接口可用。 |

特别限制：该快照的 **in-process app-server client 会主动拒绝 `ChatgptAuthTokensRefresh`**；原生 TUI 的请求处理也将该请求归入未实现提示分支。[内嵌客户端拒绝][in-process]、[TUI 请求处理][tui-refresh]

`ExternalAuthBridge` 的刷新请求经 `OutgoingMessageSender.send_request()` 广播，没有绑定最初发起登录的连接。故“另外连一个控制客户端”仍需处理回调归属、其他连接收到请求以及断线恢复，不能承诺开箱即用。宿主独占 app-server RPC 通道是证据最明确的路径。[刷新桥][refresh-bridge]、[广播发送][broadcast]

## 隔离、所有权与会话绑定

Ephemeral 存储是进程内 map，按 Codex home 派生键区分。一个单独启动的 CLI 进程不能靠共享路径读取另一个进程的内存 token。外部认证模式因此应与宿主管理的 app-server 生命周期绑定。[内存存储][ephemeral-store]

官方允许 `CODEX_HOME` 改变文件凭据目录；独立目录配合明确存储模式，可以避免覆盖用户默认凭据。这个目录还承载其他 Codex 状态，托管启动需要同时规划配置与会话保存。[官方凭据存储说明](https://learn.chatgpt.com/docs/auth#credential-storage)

但把同一个 refresh token 复制到两个隔离目录，并不能消除两个刷新者之间的冲突。应选定一个所有者：Codex-managed 模式由 Codex 刷新；外部模式由宿主刷新、Codex 只持有短期 token。外部登录还受现有认证方式/workspace 策略约束。[登录处理器][login]、[外部 token 构造][token-construction]

架构建议是把身份绑定附着到受管运行实例/会话范围，并带上账号及凭据版本；同一会话内换账号需要明确的切换边界和旧请求失效规则。不要为原生 status 创建跨会话的全局 `currentAccount`。如果产品仍按 turn 选择不同上游账号，应承认固定原生认证身份与逐 turn 路由身份存在差异，或为需要身份一致的托管模式约束账号绑定。

账号查询应要求明确的 Route + Account 上下文；动态选择器缺少 Turn 时返回“无法确定账号”，不能借用最后一次全局选择或任意 fallback。只读上游账号查询也应与客户端登录、设置修改、账单或 reset 消费分别授权和适配。

## ACP 的证据边界

本次在固定 Codex 快照的 Rust、TypeScript、Markdown 和 Cargo 配置中检索 `ACP`、`Agent Client Protocol`、`agent-client-protocol`，没有找到可作为原生 ACP 身份/额度契约的定义。这里只能确认 Codex app-server 是其专有双向 JSON-RPC 接口，不能把“都是 JSON-RPC”当作协议兼容。

ViberMate 若保留现有 ACP 字节透传观察模式，未来托管认证应是用户明确选择的新能力或模式，不能默认改变编辑器的认证和权限所有权。ACP 标准或特定桥接实现是否有适用扩展，仍需对具体版本的规范/实现单独核对；此处不作支持或不支持的断言。

建议对每个 CLI 声明：可否绑定身份、可否外部刷新、可否读取额度和历史用量、可否附着原生 UI、是否需要重启、凭据隔离方式、已验证版本与实验状态。缺少能力时返回明确的“不支持”，而非在 HTTP 层伪造客户端身份资料。

## 本地固定源码引用

[login]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/app-server/src/request_processors/account_processor.rs:817
[token-construction]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/auth/manager.rs:1713
[identity-fields]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/auth/manager.rs:626
[refresh-bridge]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/app-server/src/external_auth.rs:18
[ephemeral-commit]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/auth/manager.rs:3006
[ephemeral-store]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/auth/storage.rs:473
[tui-account-update]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/tui/src/app/app_server_events.rs:251
[display-construction]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/tui/src/app_server_session.rs:1763
[in-process]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/app-server-client/src/lib.rs:406
[tui-refresh]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/tui/src/chatwidget/protocol_requests.rs:51
[broadcast]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/app-server/src/outgoing_message.rs:316
