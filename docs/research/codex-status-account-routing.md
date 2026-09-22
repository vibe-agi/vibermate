# Codex 状态、账号身份与额度的数据来源

调研日期：2026-09-22。范围：官方文档与已有官方源码快照的只读核对；未读取真实凭据，也未调用真实账号或上游业务 API。

源码固定为 `ebc05da3bdb76f25861e7cb418bd06d28cadc609`，沿用[先前研究](2026-09-21-codex-oauth-account-management.md)；本地目录为 `/tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609`。主线程确认已安装 CLI 为 `0.155.1`；该源码快照没有核实为此版本，以下实现结论不能当作对本机 CLI 的实测。

## 官方文档确认的行为

| 入口 | 文档定义 |
| --- | --- |
| `/status` | 汇总当前模型、审批策略、可写目录和 token 使用情况；远程连接时还显示服务器信息。 |
| `/usage` | 当前文档明确存在，可查看 daily、weekly、cumulative token 活动，也提供兑换可用 reset 的入口；需要适用的 Codex 账号认证。 |
| `codex login status` | 显示当前认证方式；存在凭据时退出码为 0。它不是账号额度报表。 |

来源：[CLI developer commands](https://learn.chatgpt.com/docs/developer-commands?surface=cli)。旧 `developers.openai.com/codex/cli/reference` 和 `slash-commands` 链接现重定向到该页。

官方说明登录信息会缓存在本地文件或系统凭据库，也可以仅留在进程内存；ChatGPT token 可自动刷新。[Authentication](https://learn.chatgpt.com/docs/auth)

官方 app-server 协议分别定义 `account/read`、`account/rateLimits/read`、`account/usage/read`；另有独立的 `account/rateLimitResetCredit/consume`。因此“身份”“额度”“历史用量”“消费 reset”本身就是不同操作。[Codex App Server](https://learn.chatgpt.com/docs/app-server#auth-endpoints)

## 源码核对：身份显示与网络请求并非同一个来源

`/status` 的 Account 文案来自 TUI 持有的 `StatusAccountDisplay`。启动时，`account/read` 的 `Account::Chatgpt { email, plan_type }` 被转成显示值；provider 的 `account_state()` 读取 `auth_manager.auth_cached()`，普通 ChatGPT OAuth 模式的邮箱和套餐来自当前 ID token 的派生字段。显示时没有改用额度响应的账号字段。[TUI 账号投影][account-display]、[provider 账号状态][provider-account]、[邮箱与套餐读取][auth-fields]、[Account 渲染][account-render]

这不意味着整个 `account/read` 离线：该快照在路由缓存缺失时还会调用 `accounts/check`，确认当前 workspace 路由。应区分“显示字段来自缓存身份”与“读取账号流程绝不联网”。[workspace 路由发现][workspace-routing]

普通存储认证模式下，`codex login status` 的实现加载认证存储，再输出 ChatGPT/API key 等认证方式；ChatGPT 分支不读取额度，也不打印邮箱。workload identity 有独立分支，因此不能把所有认证模式概括成完全相同的离线行为。[login status 实现][login-status]

## 源码核对：额度与历史用量的网络入口

在 `requires_openai_auth && has_chatgpt_account` 时，`/status` 会先产生状态卡，再触发 `RefreshRateLimits`；TUI 的 `refresh_rate_limits()` 发起 `account/rateLimits/read`，后台用当前认证创建 `BackendClient`。[状态命令][status-command]、[触发条件][prefetch-condition]、[后台刷新][refresh-rates]、[app-server 读取额度][account-rates]

下表是该源码快照的 ChatGPT 路径，均相对配置的 backend base；它们是实现细节，官方产品文档未承诺这些 HTTP 路径稳定。采用 Codex API 路径风格时，前缀改为 `/api/codex`。

| 用途 | ChatGPT backend 请求 | 边界 |
| --- | --- | --- |
| 当前额度 | `GET /wham/usage` | 返回额度窗口、credits 和部分账号相关信息。 |
| `/usage` token 活动概要 | `GET /wham/profiles/me` | 分析界面读取账号 profile；`account/usage/read` 的账号概要也使用该路径。 |
| 可用 reset 列表 | `GET /wham/rate-limit-reset-credits` | 只读列表，与当前额度请求分开。 |
| 消费一次 reset | `POST /wham/rate-limit-reset-credits/consume` | 有副作用，不属于只读额度同步。 |

来源：[额度及 reset 请求][usage-http]、[profile 路径][profile-http]、[分析界面读取 profile][analytics-profile]。`/status` 还可按可用性查询 thread usage，因此这张表不是所有状态相关请求的穷举。[thread usage 条件][thread-usage]

`BackendClient::from_auth()` 通过认证适配器同时绑定 bearer、`ChatGPT-Account-ID` 及适用的 FedRAMP 标记。代理若要查询所选账号的用量，需要让对应只读请求进入相同的账号选择与认证绑定流程。[backend 认证][backend-auth]、[认证快照适配][auth-provider]、[请求头注入][auth-headers]

## 另一条额度来源：推理响应

HTTP SSE 响应入口读取响应头并生成 `ResponseEvent::RateLimits`；解析器识别 `x-codex-primary/secondary-{used-percent,window-minutes,reset-at}`、credits，以及其他 limit 家族。core 保存这些快照，并通过后续 token-count 更新传递状态。WebSocket 则另有 `codex.rate_limits` 事件解析入口。[SSE 响应头入口][sse-rates]、[额度解析器][rate-parser]、[core 接收更新][core-rates]、[WebSocket 事件][ws-rates]

因此，只修改额度查询的请求认证仍可能不完整；如果代理重建 SSE 响应并丢弃额度头，也会损失推理响应携带的更新。

## 本地账号 A、代理所选账号 B 的不一致

该快照的 `account/rateLimits/read` 比较响应 `account_id`、`user_id` 与当前认证；不匹配时过滤 `ordinary_usage_allowed` 和 `rate_limit_upsell`，但普通 `rate_limits` 与 `rate_limits_by_limit_id` 仍返回。TUI 在请求代次检查通过后转换快照并更新状态卡，这段转换没有以返回账号 ID 拒绝普通额度。[身份匹配检查][account-match]、[TUI 应用结果][tui-rates]、[快照转换][snapshot-conversion]

所以，仅把请求头替换为 B，可能出现“Account 显示 A、额度显示 B”；上述普通用量资格字段和 upsell 提示不会随 B 生效。不能承诺换头即可让全部账号状态一致，也不应伪造响应账号 ID 来绕过这些检查。完整身份同步需要独立的 Codex 认证状态集成设计。只读额度支持应限定具体操作，不能把 reset 消费或整个 `usage` 路径族一并纳入。

## 当前 ViberMate 的缺口与建议

本节核对的是 `feat/codex-oauth-accounts` 当前工作树，不代表已发布版本或真实账号端到端验收。

- 托管 OAuth 的推理请求已经同时替换 `Authorization`、`Chatgpt-Account-Id`，并处理 FedRAMP 标记；问题不是完全没有做账号 Header 替换。[认证绑定](../../internal/providertransport/auth.go#L150)
- 操作目录目前有 Codex responses、models 和部分 plugins 探针，没有 `/backend-api/wham/usage` 或 `/backend-api/wham/profiles/me`。被捕获的未登记操作会被拒绝，而不是自动带 B 的凭据查询；现有 opaque 探针又走保留客户端 Header 的 original transport。因此不能只把额度 URL 加成普通透传探针。[操作目录](../../internal/operationcatalog/catalog.go#L136)、[请求分派](../../internal/loopbackproxy/handler.go#L881)、[原目标转发](../../internal/loopbackproxy/handler.go#L1574)
- 普通托管 SSE 路径在取得上游响应前就提交客户端响应头；`managedResponseEnvelope` 仅设置 Content-Type 和 Cache-Control，没有传回额度头。修复需要考虑响应提交时机及现有等待/重试语义，不能在已提交 Header 后再补写。[流式提交](../../internal/exchange/pipeline.go#L2287)、[托管响应头](../../internal/exchange/contracts.go#L965)

建议将只读用量接口作为显式、受限的托管账号操作，复用实际选中的上游账号、刷新后的凭据和网络出口。固定账号可以直接对应；账号轮换时必须先定义查询对应的会话绑定或实际使用账号，不能重新随机选一个，也不能在查询失败时悄悄查询原登录账号。相关缓存需要按账号隔离。推理返回的额度信息也要通过客户端支持的方式保留。

纯代理模式应明确区分“客户端登录身份”和“实际使用的上游账号”，不覆盖用户的本地认证文件。完整 CLI 身份切换属于单独的托管启动/认证集成，不把它伪装成 Header 修补。本轮仅新增调研笔记，没有修改产品转发逻辑。

验证：以下现有测试通过，证明当前探针与未登记请求的边界行为；不是对真实 Codex 账号的验收。

```sh
go test ./internal/operationcatalog ./internal/loopbackproxy -run 'TestObservedCodexOperationsAreCatalogued|TestCataloguedControlProbeReachesTheOriginalOrigin|TestUncataloguedBodylessGETIsRejectedLocally' -count=1
```

## 源码位置

以下链接指向本次已读取的本地固定快照；临时目录清理后，应按上述 commit 重新取得源码。

[account-display]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/tui/src/app_server_session.rs:679
[provider-account]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/model-provider/src/provider.rs:503
[auth-fields]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/login/src/auth/manager.rs:626
[account-render]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/tui/src/status/card.rs:749
[workspace-routing]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/app-server/src/request_processors/account_processor/workspace_routing.rs:296
[login-status]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/cli/src/login.rs:443
[status-command]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/tui/src/chatwidget/slash_dispatch.rs:506
[prefetch-condition]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/tui/src/chatwidget/rate_limits.rs:415
[refresh-rates]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/tui/src/app/background_requests.rs:783
[account-rates]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/app-server/src/request_processors/account_processor.rs:1107
[usage-http]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/backend-client/src/client/rate_limit_resets.rs:70
[profile-http]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/backend-client/src/client.rs:393
[analytics-profile]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/tui/src/analytics/summary.rs:19
[thread-usage]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/tui/src/chatwidget/status_controls.rs:268
[backend-auth]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/backend-client/src/client.rs:215
[auth-provider]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/model-provider/src/auth.rs:306
[auth-headers]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/model-provider/src/bearer_auth_provider.rs:31
[sse-rates]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/codex-api/src/sse/responses.rs:43
[rate-parser]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/codex-api/src/rate_limits.rs:57
[core-rates]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/core/src/session/turn.rs:2815
[ws-rates]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/codex-api/src/endpoint/responses_websocket.rs:757
[account-match]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/app-server/src/request_processors/account_processor.rs:1180
[tui-rates]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/tui/src/app/event_dispatch.rs:1563
[snapshot-conversion]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/tui/src/app_server_session.rs:2521
