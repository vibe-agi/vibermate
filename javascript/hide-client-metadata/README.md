# 隐藏客户端元信息（请求头，v1）

独立于 `hide-local-identity/`，只增加 version、install id、User-Agent 的请求头替换。
本目录的 [request.js](request.js) 是 UI“替换客户端元信息”模板的唯一代码来源，也可直接粘贴；
**响应 JavaScript 留空**。
不使用请求／响应映射，不改写回答或工具参数，也不修改已有的本机身份脚本。

## 配置与使用

1. 修改 `request.js` 顶部 `replacement` 的三个值。`null` 表示关闭对应项，空字符串报错。
   默认 `0.0.0`、示例 UUID、`vibermate-client/0.0.0` 只是合成测试值，
   **不是已验证可用于真实上游的生产配置**。版本头与 User-Agent 中的版本应保持一致。
2. 在脚本库从“替换客户端元信息”新建可编辑副本，或将文件全文粘贴到“请求 JavaScript”；
   响应脚本留空。
3. 先用下方合成数据在测试面板验证，再绑定到需要使用的流量策略；可和“隐藏本机身份”组合。
4. 必须先部署包含本次 User-Agent 衔接修复的 Runtime。旧 Runtime 会覆盖脚本的 UA，
   或在托管转发中报头冲突；仅在测试面板中看到替换结果不足以证明上游发送成功。
5. 使用新会话检查最终 Provider Request 的头及真实上游兼容性。
   本次没有自动发布策略、修改数据库、重启容器或调用真实模型。

同一条策略使用固定替换值。示例 install id **不是每个用户／会话独立的随机 ID**，
也不是原 ID 的哈希、持久映射或认证凭据。需要区分安装实例时，为不同策略配置不同的合成 ID；
不要将依赖 install id 做会话关联的上游贸然统一为同一个值。脚本不生成随机数，也不存储原值。

## 精确范围

| 类型 | 处理的完整请求头名（大小写不敏感） | 行为 |
| --- | --- | --- |
| 客户端版本 | `version`、`x-client-version`、`x-app-version`、`x-codex-version` | 只替换已有头，不补不存在的头 |
| 安装 ID | `install-id`、`installation-id`、`x-install-id`、`x-installation-id`、`x-codex-install-id`、`x-codex-installation-id` | 只替换已有头，多个值收敛为一个 |
| 客户端标识 | `user-agent` | 替换整个值；缺失时补入配置值 |

引擎向 JS 提供小写头名。此白名单是本脚本明确支持的别名，不表示所有客户端实际都会发送它们。
若真实请求中的 install id 使用其他名字，需要确认语义后扩展白名单。
托管模式若原本已丢弃某个头，本脚本不会为了替换它而恢复原值。

不处理：

- `account id`、`chatgpt-account-id`、`session-id`、`thread-id`、`x-client-request-id`，及任何凭据。
- `anthropic-version` 等 API 协议版本，`sec-ch-ua` 等其他指纹头，未列出的 SDK 版本头。
- URL／query、正文中的同名字段、提示词、工具 JSON、模型名、schema、签名和加密字段。
- 模型列表／健康检查／控制接口等未进入 AI 消息转换链路的请求。

正文逐字节保持原样；没有响应脚本，因此流式与非流式响应都不做替换或反向恢复。
Runtime 仍按原规则解压、重算 framing、保护凭据及保存证据。原始证据可能包含真实客户端信息；
本脚本不是日志脱敏、TLS 指纹修改或完全匿名化工具。

## User-Agent 生效顺序

运行时默认线协议配置 → 请求 JS 的实际 UA 修改 → Account 显式设置／删除的头策略。

本次 Go 修复将实际变化的 UA 从普通头集合中提取，作为独立、经过校验的发送字段冻结。
没有改 UA 的脚本不会改变原有默认行为；与输入相同的值也不算覆盖。
单值、可打印 ASCII、最多 512 字节；删除或空值表示不发送，不允许 Go 自动补默认 UA。
Account 层显式 UA 设置／删除仍有最终优先级，任意认证驱动擅自改 UA 仍会被拒绝。
若账号配置了这类覆盖，应在配置中消除冲突，再验收最终发送值。

## 合成测试数据

请求头：

```json
{
  "Version": ["9.8.7"],
  "X-Install-Id": ["synthetic-install-a"],
  "User-Agent": ["test-client/9.8.7 (synthetic-device)"],
  "Anthropic-Version": ["2023-06-01"],
  "Session-Id": ["synthetic-session"]
}
```

正文保持不变：

```json
{"model":"test-model","input":"hello","metadata":{"version":"do-not-edit","account_id":"synthetic-account"}}
```

验证默认配置时，前三个头变成示例配置值，其他头和正文不变。
真正使用前必须把示例值改为已验证可兼容上游的值；替换版本或 UA 可能影响上游能力判断。

```sh
go test ./javascript/hide-client-metadata ./internal/exchange ./internal/providertransport -count=1
go test -race ./javascript/hide-client-metadata ./internal/exchange ./internal/providertransport -count=1
```

测试使用真实 Goja 引擎和本地 HTTP 测试服务，不读取账号、浏览器凭据或真实请求日志。
以上命令同时覆盖脚本行为、消息转换与竞态检查；生成型验收报告仅保留在本机，不纳入版本控制。
