# Codex 只读账号操作契约

日期：2026-09-22。根据已有官方源码快照 `ebc05da3bdb76f25861e7cb418bd06d28cadc609` 核对；未读取真实凭据、未调用真实账号接口。已安装 CLI `0.155.1` 与该快照没有建立构建对应关系。
下文是该版本客户端实际接受的 JSON 契约，不是对私有服务 API 稳定性或所有未来响应字段的承诺。所有示例均为合成数据。

## 1. 精确请求边界

| 用途 | 默认 ChatGPT 完整路径 | 请求契约 |
| --- | --- | --- |
| 当前账号额度 | `GET /backend-api/wham/usage` | 无 body；源码不添加 query。 |
| 账号历史/profile | `GET /backend-api/wham/profiles/me` | 无 body；源码不添加 query。daily/weekly/cumulative 是客户端视图，不是该请求的 query。 |

路径相对配置的 backend base；Codex API 风格另使用 `/api/codex/usage`、`/api/codex/profiles/me`，不能仅因后缀相同就扩大允许的源站/路径。[额度请求][usage-request]、[profile 请求][profile-request]

两者用同一认证快照的 bearer、`ChatGPT-Account-ID` 及适用的 FedRAMP 标记；账号操作必须取得自己冻结的租约。请求范围要求明确 Route + Account，不能借用全局最后账号。[认证绑定][auth-binding]

额度请求可额外携带 `x-openai-codex-luna-reserve: 1`。源码区分能够应用 Reserve 的客户端与被动账号查询者，后者不应 opt-in；这不是 query。`rate-limit-reset-credits` 列表及其 `/consume` POST 是不同操作，本契约不自动开放。[额度请求][usage-request]

## 2. `/wham/usage` JSON

记号：`?` 表示字段可缺失或为 `null`；未标 `?` 的字段在其所属对象存在时必须提供。源码中 `Option<Option<T>>` 可区分缺失与显式 null，映射成 UI 快照时常被 flatten；投影不能把任一情况补成 0。

| 字段 | 类型/结构 |
| --- | --- |
| `plan_type` | 必需 string enum；未来未知值被该客户端映射到 Unknown。 |
| `rate_limit?` | `Limit`。 |
| `additional_rate_limits?` | `AdditionalLimit[]`。 |
| `credits?` | `Credits`。 |
| `spend_control?` | `SpendControl`。 |
| `rate_limit_reached_type?` | `{ "type": string enum }`。 |
| `account_id?`, `user_id?` | string；由手写 wrapper 补充。 |
| `rate_limit_reset_credits?` | `{ "available_count": i64 }`；仅数量概要，不是 reset 列表。 |
| `rate_limit_upsell?` | 任意 JSON；后端 UI 契约，不能当作已验证的账号事实或安全文本。 |

| 嵌套类型 | 字段 |
| --- | --- |
| `Limit` | `allowed: bool`、`limit_reached: bool`；`primary_window?: Window`、`secondary_window?: Window`。 |
| `Window` | `used_percent: i32`、`limit_window_seconds: i32`、`reset_after_seconds: i32`、`reset_at: i32`。 |
| `AdditionalLimit` | `limit_name: string`、`metered_feature: string`、`rate_limit?: Limit`；wrapper 另支持 `normal_model_slug?: string`。 |
| `Credits` | `has_credits: bool`、`unlimited: bool`、`balance?: string`、`approx_local_messages?: JSON[]`、`approx_cloud_messages?: JSON[]`。 |
| `SpendControl` | `reached: bool`、`individual_limit?: IndividualLimit`。 |
| `IndividualLimit` | `source?: string`；`limit/used/remaining: string`；`used_percent/remaining_percent/reset_after_seconds/reset_at: i32`。 |

来源：[基础 payload][quota-payload]、[手写扩展][quota-wrapper]、[Limit][quota-limit]、[Window][quota-window]、[AdditionalLimit][additional-limit]、[Credits][credits]、[SpendControl][spend-control]、[IndividualLimit][individual-limit]。

`reset_at` 是 Unix 秒；窗口长度 JSON 用秒，而 SSE 头用分钟。源码将 `used_percent` 转 f64 并将 `reset_at` 转 i64；不要据此把 JSON 输入宣称为任意小数/任意精度。`balance` 及花费金额是字符串，不能浮点往返。`allowed` 是后端对普通用量的决定，不能由百分比反推。[额度映射][quota-mapping]、[额度请求][usage-request]

该快照识别的 plan 值包括 `guest/free/go/plus/pro/prolite/free_workspace/team/self_serve_business_prolite/self_serve_business_usage_based/business/ent26/enterprise_cbp_automation/enterprise_cbp_usage_based/education/quorum/k12/enterprise/edu/edu_plus/edu_pro/unknown`。reached type 识别 `rate_limit_reached`、`workspace_owner_credits_depleted`、`workspace_member_credits_depleted`、`workspace_owner_usage_limit_reached`、`workspace_member_usage_limit_reached` 和 unknown。[枚举][quota-payload]

普通额度主桶 ID 映射为 `codex`；附加桶 ID 来自 `metered_feature`，展示名来自 `limit_name`。它们不是账号 ID，附加桶也不等于额外账号，不能跨桶相加为一个百分比。[额度映射][quota-mapping]

### 合成额度 fixture

```json
{
  "plan_type": "pro",
  "account_id": "workspace-fixture-b",
  "user_id": "user-fixture-b",
  "rate_limit": {
    "allowed": true,
    "limit_reached": false,
    "primary_window": {
      "used_percent": 25,
      "limit_window_seconds": 18000,
      "reset_after_seconds": 3600,
      "reset_at": 1800000000
    },
    "secondary_window": null
  },
  "additional_rate_limits": [
    { "limit_name": "Fixture bucket", "metered_feature": "codex_fixture", "rate_limit": null }
  ],
  "credits": { "has_credits": true, "unlimited": false, "balance": "12.50" },
  "spend_control": null,
  "rate_limit_reached_type": null,
  "rate_limit_reset_credits": { "available_count": 0 }
}
```

缺失数据 fixture：`{"plan_type":"pro"}` 合法，但没有已知窗口/credits；与上述 25% 或明确 0% 都不同。源码仍能生成主桶的空快照。[基础 payload][quota-payload]、[额度映射][quota-mapping]

## 3. `/wham/profiles/me` JSON 与历史授权

必需顶层 `stats: object`；可选 `profile`、`metadata`。`{"stats":{}}` 合法：字段未知，不是全为零。[TokenUsageProfile][profile-types]、[AccountProfile][profile-full]、[上游 partial/zero 测试][profile-tests]

| 对象 | 接受的字段 |
| --- | --- |
| `stats` 的基础字段 | `lifetime_tokens?`, `peak_daily_tokens?`, `longest_running_turn_sec?`, `current_streak_days?`, `longest_streak_days?` 均为 i64；`daily_usage_buckets?: Bucket[]`。 |
| `Bucket` | 必需 `start_date: string`、`tokens: i64`；图表按 `YYYY-MM-DD` 解析日期。 |
| `stats` 的扩展字段 | `fast_mode_usage_percentage?`、`most_used_reasoning_effort_percentage?` 是 JSON number；`most_used_reasoning_effort?: string`；`unique_skills_used?`、`total_skills_used?`、`total_threads?` 为 u64；`top_invocations?: Invocation[]`。 |
| `Invocation` | 必需 `type: string`（plugin/skill，未来值保留为 Unknown）；`plugin_name?`, `skill_name?: string`、`usage_count?: u64`。 |
| `profile?` | `display_name?: string`、`username?: string`。 |
| `metadata?` | `stats_as_of?: string`、`stats_error?: string`。未在客户端解码层定义统一时区/时间格式。 |

来源：[基础统计][profile-types]、[完整 profile][profile-full]、[日期解释][profile-date]。原生 UI 可展示 profile 身份、长期用量、活跃天数、技能/插件名称及使用次数；这不是单次模型调用的 token usage。

```json
{
  "metadata": { "stats_as_of": "2026-09-22", "stats_error": null },
  "stats": {
    "lifetime_tokens": 1200,
    "peak_daily_tokens": 300,
    "longest_running_turn_sec": null,
    "current_streak_days": 0,
    "daily_usage_buckets": [{ "start_date": "2026-09-21", "tokens": 300 }]
  }
}
```

该 profile 契约没有 `account_id`、`user_id` 或 workspace 范围标识，源码不能证明这些历史指标严格限制在一个 workspace。授权时按账号敏感历史处理，独立于“可用该账号推理”的权限；来源标明所用凭据主体/工作区/源站及采集时间，但不能把请求绑定的工作区冒充响应已经证明的历史统计范围。没有历史读取授权时，不应通过捕获的 CLI 请求隐式代读。[完整 profile][profile-full]

额度响应则可提供账号和用户标识：Codex 比较它们与 `auth.get_account_id()`、`auth.get_chatgpt_user_id()`，不匹配时抑制 upsell 与普通用量恢复决定，但仍接收普通额度。代理必须保留真实标识，不能改为客户端原身份来绕过检查。缺失标识也不能宣称完成了响应身份校验。[账号匹配][account-match]

## 4. HTTP SSE 额度响应头

成功 SSE 的入口先从最终 HTTP 响应头解析额度，再发送内部 `RateLimits` 事件。HTTP SSE 响应体没有在此路径获得“任意插入 `codex.rate_limits` 事件即可更新”的保证。[SSE 入口][sse-entry]

基础额度白名单候选（大小写不敏感）：

```text
x-codex-primary-used-percent
x-codex-primary-window-minutes
x-codex-primary-reset-at
x-codex-secondary-used-percent
x-codex-secondary-window-minutes
x-codex-secondary-reset-at
x-codex-limit-name
x-codex-credits-has-credits
x-codex-credits-unlimited
x-codex-credits-balance
```

这些名称确实被源码读取；“可安全转发”仍以同一冻结账号/响应、字段值验证和长度上限为条件。percent 按有限 f64 解析，window/reset 按 i64，boolean 接受 true/false（不区分大小写）及 1/0；credits 两个 boolean 都有效才生成快照。缺失 used-percent 的窗口不会生成；明确 0%、非零窗口长度能生成。limit-name/balance 是文本，不能因名称在白名单就无限制记录值。[额度头解析][header-parser]

额外桶使用 `x-<limit-id中的下划线改为连字符>-{primary/secondary-used-percent,primary/secondary-window-minutes,primary/secondary-reset-at,limit-name}`；源码用 `*-primary-used-percent` 发现额外桶。扩展支持应注册/验证桶前缀及这七个确切后缀，而不是透传全部 `x-*` 或 `x-codex-*`。credits 仍取上述公共 `x-codex-credits-*`，不是每桶各自的 credits。[额度头解析][header-parser]

`x-codex-active-limit`、`x-codex-rate-limit-reached-type`、`x-codex-promo-message` 用于特定 429 `usage_limit_reached` 错误路径，不属于成功 SSE 普通额度快照；promo 是自由文本，应单独评估。`X-Models-Etag` 等虽被 SSE 代码消费，也不因此进入额度白名单。`Set-Cookie`、Authorization、账号绑定的 `x-codex-turn-state` 不属于这份白名单。[429 路径][error-headers]、[SSE 入口][sse-entry]

最终响应头一旦提交就不能事后补入；Hold 已提交时应保留 Hold 契约并依赖显式额度查询，不能把 WebSocket 的事件契约套给 HTTP SSE。

## 5. models 控制请求的取舍

Codex model catalog 是认证相关 GET：默认为 provider base 下 `models?client_version=...`，使用 provider 认证，可返回 ETag；配置还能提供其他 catalog URL/query。本仓库当前只识别 `/backend-api/codex/models` 与 `client_version`。[模型请求][models-request]、[当前 catalog](../../internal/operationcatalog/catalog.go)

Codex 的模型缓存身份包含 provider/base/query、account ID、user ID、email、plan、FedRAMP 等；对同一稳定账号，access token 旋转本身不会丢弃缓存。透明代理把 A 换成 B 不会自动改变客户端缓存键。[缓存身份][models-identity]

建议当前 usage/profile 切片**明确延期 models 的托管账号切换**，保留现有原认证控制面行为并记录局限。后续只有在明确固定 Route + Account、同服务 catalog 契约、ETag/账号变更缓存验收成立后，才将 models 注册为可托管只读操作；动态 selector 缺少 Turn 时不可选“最近账号”。不能因为 models 是 bodyless/control 就自动升级认证归属。该取舍是实施建议，不是说 models 在服务端与账号无关。

## 固定源码引用

[usage-request]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/backend-client/src/client/rate_limit_resets.rs:23
[profile-request]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/backend-client/src/client.rs:393
[auth-binding]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/model-provider/src/bearer_auth_provider.rs:31
[quota-payload]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/codex-backend-openapi-models/src/models/rate_limit_status_payload.rs:15
[quota-wrapper]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/backend-client/src/types.rs:55
[quota-limit]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/codex-backend-openapi-models/src/models/rate_limit_status_details.rs:15
[quota-window]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/codex-backend-openapi-models/src/models/rate_limit_window_snapshot.rs:13
[additional-limit]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/codex-backend-openapi-models/src/models/additional_rate_limit_details.rs:15
[credits]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/codex-backend-openapi-models/src/models/credit_status_details.rs:13
[spend-control]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/codex-backend-openapi-models/src/models/spend_control_status_details.rs:15
[individual-limit]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/codex-backend-openapi-models/src/models/spend_control_limit_details.rs:14
[quota-mapping]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/backend-client/src/client.rs:591
[profile-types]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/backend-client/src/types.rs:532
[profile-full]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/backend-client/src/client/profile.rs:11
[profile-tests]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/backend-client/src/client/profile_tests.rs:12
[profile-date]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/tui/src/analytics/activity_chart.rs:232
[account-match]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/app-server/src/request_processors/account_processor.rs:1180
[sse-entry]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/codex-api/src/sse/responses.rs:43
[header-parser]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/codex-api/src/rate_limits.rs:57
[error-headers]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/codex-api/src/api_bridge.rs:149
[models-request]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/codex-api/src/endpoint/models.rs:33
[models-identity]: /tmp/vibermate-codex-source.39pRrV/codex-ebc05da3bdb76f25861e7cb418bd06d28cadc609/codex-rs/model-provider/src/models_identity.rs:1
