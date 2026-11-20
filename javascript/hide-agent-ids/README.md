# 隐藏 Agent 标识（请求，v1）

替换 Claude Code 与 Codex 为自己生成、随请求发送的标识。本目录的 [request.js](request.js) 是 UI
“隐藏 Agent 标识”模板的唯一代码来源，也可直接粘贴；**响应 JavaScript 留空**。
可与“隐藏本机身份”“替换客户端元信息”组合使用。

## 处理范围

| 客户端 | 标识 | 位置 |
| --- | --- | --- |
| Claude Code | `cc_prompt_id`，每个提示一个 UUID | 系统提示词开头的计费行 `x-anthropic-billing-header: …` |
| Claude Code | `device_id`，同一设备固定不变 | `metadata.user_id` 中的 JSON；也识别旧版 `user_<device>_account_…` 格式 |
| Codex | `installation_id`，同一次安装固定不变 | 正文 `client_metadata` 的 `x-codex-installation-id` 与 `x-codex-turn-metadata`；请求头 `X-Codex-Turn-Metadata`、`X-Codex-Installation-Id` |

只替换上述字段里的值。同一个值若出现在别处（例如用户粘贴进消息），保持原样。

## 替换方式

替换值由脚本顶部的 `key` 经 HMAC-SHA-256 派生：

- 同一个原值总是得到同一个替换值，所以同一设备、同一次安装、同一个提示在服务方看来仍然一致；
- 不知道 `key`，就无法由替换值推回原值，也无法把两者对应起来；
- UUID 换成第 4 版格式的 UUID，`device_id` 换成等长的十六进制串。

## 配置与使用

1. 把 `key` 改为你自己的文本，16 到 256 个可打印 ASCII 字符。保留默认值时，知道原值的人可以算出替换值。
   更换 `key` 相当于换了一台设备、一次安装。
2. 将 `hide` 中某项设为 `false` 可单独关闭。非布尔值或不合格的 `key` 会拒绝请求，错误信息不回显任何标识。
3. 在脚本库从“隐藏 Agent 标识”新建可编辑副本，或把全文粘贴到“请求 JavaScript”，响应脚本留空；
   用测试面板验证后，再绑定到流量策略。
4. 先在一个新会话里确认上游接受修改后的请求。本脚本尚未用真实的 Anthropic、OpenAI 账号验证；
   计费行中 `cch=` 等字段的含义未公开，脚本不修改它们。

## 不处理

- **会话、线程、轮次 ID**：Claude 的 `session_id`、`X-Claude-Code-Session-Id`，Codex 的 `Session-Id`、
  `Thread-Id`、`prompt_cache_key` 等。它们会出现在模型要使用的文件路径里，替换后必须在回复中还原；
  Codex 的提示缓存以它为键；ViberMate 的会话分组也读取它们。它们每个会话随机生成，不跨会话关联身份。
- **`account_uuid`**：服务方已从登录凭据得知账号，修改它没有隐私收益，还可能被拒绝。
- 计费行中的 `cch=`，以及消息 ID、工具调用 ID、签名与加密续接字段。
- **不经过消息转换的请求**：Claude Code 的遥测（如 `/api/event_logging/v2/batch`）、特性开关等仍带真实标识。
  需要时在 Claude Code 中关闭非必要流量（`CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`），或用连接策略阻止这些请求。
- 托管模式的 Codex 转发本就不发送 `X-Codex-Turn-Metadata`；正文 `client_metadata` 中的副本由本脚本替换。

## 性能

脚本只解析标识所在的几个短字段，正文最多重建一次。在高负载的测试机上：1 MiB 请求比空脚本多约 0.08 秒；
接近 16 MiB 上限的请求约占用 2 秒脚本时限中的 0.8 秒，几乎全部是任何改写正文的脚本都要付出的重建开销。

## 测试

```sh
go test ./javascript/hide-agent-ids -count=1
go test ./internal/exchange -run TestAgentIDsScriptReachesProviderWire -count=1
```

第一组用真实 Goja 引擎运行脚本，并用 Go 的 `crypto/hmac` 独立计算期望的替换值；
第二组经过真实 Exchange、最终鉴权与本地 HTTP 服务，检查自带凭据的 Claude、Codex 与托管 Codex
三条路径上服务方实际收到的请求。样例形状取自 Claude Code 2.1.329 与 Codex 0.171.0，取值均为合成数据。
