# 网页详情不可读：现场证据与新增审查发现

日期：2026-10-22。基线：ViberMate v0.1.35 / `b03ebd8be040087d623f7a1b12c340e99994f56f`。

**本轮新增确认 3 项缺陷：1 项 P1、2 项 P2。最直接的 P1 是前后端内容分页游标格式不一致：服务端返回正常详情，前端解码失败。** 另有现场 summary 409，触发源尚未确定，与这个 P1 分开处理。

## 现场证据

- 用户报告 Version 0.1.35 (37)。公开读取 `https://ai.appsflyme.com/version.json` 确认 Web 版本为 0.1.35、build_number 37。没有使用登录凭证；记录在 `public-web-probe.json`。
- Network 中基础目录、状态和 assignment 成功，而 `summary?captureRunId=…` 出现两次 409。用户提供的错误为 `{ "type": "urn:vibermate:error:inspection-changed", "title": "Conflict", "status": 409, "code": "inspection_changed" }`。
- 单条 `GET /api/v1/exchanges/…?contentView=reading&contentMode=paged` 的响应包含 `status: succeeded`、content、parentRefs、processingTrace 和 clientIdentity。
- 用户进一步确认：该详情的 `content.page.requestNextCursor` **含英文句点 `.`**。无需收集完整游标、对话正文或认证头。

## P1：合法的签名游标被 Flutter 当作无效响应

服务端 [content_inspection_reader.go:272](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/internal/runtimepersistence/content_inspection_reader.go#L272) 返回 `签名证明.内部游标`，用来绑定 source、Exchange 和控制侧可见性状态。这是现行合法格式。

Flutter [control_models.dart:9435](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/ui/flutter_app/lib/core/api/control_models.dart#L9435) 只接受 `^[A-Za-z0-9_-]+$`，因此拒绝 `.`；[control_api.dart:1689](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/ui/flutter_app/lib/core/api/control_api.dart#L1689) 对下一页请求也有同样限制。

用真实生产 Runtime/control 组合、临时控制/记录 SQLite、HostServer 模式生成的 HTTP 响应进行跨语言验证：

| 输入/操作 | 结果 |
| --- | --- |
| 3 个带保存内容的正常详情，HTTP 200、每个仅 2063 字节 | 全部在真实 Flutter 解码器报 `requestNextCursor content cursor is invalid` |
| 3 个 `not_recorded` 详情，不带分页游标 | 全部解码成功 |
| 6 个正常汇总响应 | 全部解码成功 |
| 将服务端发出的 408 字符游标原样交还服务端 | 下一页 HTTP 200，响应 215 字节 |
| 将同一游标交给公开 `HttpControlApi.exchangeContentPage` | HTTP 发送次数 **0**，先被客户端判为非法 |

这解释了“有内容的卡片读取失败，没有内容的卡片反而能显示”的差异；不需要超大响应或损坏数据库就能复现。用户确认的字段也满足这个明确的失败条件。Desktop 与 Web 共用相关 Flutter 代码，因此影响不限于 Web。

影响面包括初始页的 request/evidence 游标、延迟加载块、后续页的 next/canonical 游标。工具结果的顶层游标属于另一种格式，不应对所有 cursor 正则一并替换。详见 [服务端格式及消费者清单](2026-10-22-web-detail-server.md)。

修复必须统一该内容游标的有界语法并原样传递，保留长度限制和服务端签名、归属、可见性校验；不能删除签名外层。需要同时覆盖响应解码和发送续页请求。Git history 显示服务端封装在 `2834b65a1` 引入，前端仍保留较早的单段校验；现有分层测试未覆盖真实服务端 JSON → Flutter → 原游标回传这一完整边界。

## P2：缓存未跟随请求完成或内容到达更新

真实 controller/widget 测试已复现两种情形：

- 离开页面前缓存 pending；离开期间请求完成；返回时 Activity 已 succeeded，但仍复用 pending 详情，持续显示“正在等待最终响应”，且没有重试按钮。
- Activity 状态已是 succeeded，保存内容稍后从不可用变为可用；状态未变化，界面不重新读取，继续显示 `not_recorded`。

原因位于 [loadExchangeDetail:2789](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/ui/flutter_app/lib/features/workbench/workbench_controller.dart#L2789) 的无条件缓存复用，以及 [didUpdateWidget:1391](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/ui/flutter_app/lib/features/workbench/conversation_reader.dart#L1391) 仅比较 status。轮询比较还遗漏 `contentAvailable`。应让未完成/缺内容的快照随当前证据刷新，不能仅因 Capture 结束就捏造请求成功。

## P2：旧记录的迟到错误影响新选择

Capture A 的详情尚未返回时切到 B，B 已加载正常；A 随后返回 `503 inspection_unavailable`，[_fetchExchangeDetail:2847](https://github.com/vibe-agi/vibermate/blob/b03ebd8be040087d623f7a1b12c340e99994f56f/ui/flutter_app/lib/features/workbench/workbench_controller.dart#L2847) 缺少其他 inspection 读取已有的选择代次检查，错误地清空 B 的详情页。测试中 B 从 100 行变为 null，而必需的 assignment 保持完好。

应区分旧选择的暂时失败与真正的全局可见性变化，保留后者的失效处理。两项 P2 的完整 fixture、源码和结果见 [UI 专项审查](2026-10-22-web-detail-ui.md)。

## summary 409：已确认的行为与未确定的原因

用户提供的 `code` 字段能被正确解码，不是错误对象 schema 不兼容。summary 409 会触发全局历史快照失效，清空缓存；并行返回的合法详情也会因读取代次变化被丢弃。连续模拟三次这一交错，三次详情成功响应均返回 null；移除冲突交错后详情可读取。

这属于可见性隔离机制，不能直接忽略 409 或继续展示已经失效的历史。它与游标解码缺陷是两条独立路径。在当前代码中全局失效还会卸载对话 reader，因此仅凭这条交错不能解释所有稳定显示的卡片状态。

新建的真实 Desktop/HostServer 状态在内容写入前后重复读取 summary/detail 均为 200，尚未复现现场 409 的触发源。可能的入口已定位至 source/可见性代次、到期边界和 summary 特有的 Capture observation 再检查，但没有证据将其归因为清理、VLESS、账号、记录被删除或某个具体服务端异常。

## 验证及改动边界

证据目录：`/Users/null/Downloads/vibermate-web-detail-review-20261022`。主要文件：`server-cursor-probe.log`、`server-cursor-samples.json`、`summary-and-wire-repro.log`、`cursor-continuation-repro.log`、`reader-refresh-repro.log`。测试使用合成数据、临时目录及 Go overlay；Flutter 直接读取真实服务端生成的 JSON fixture。

这些是已复现的现有缺陷；期望正确行为的断言仍为 RED。本轮仅更新审查文档和仓库外诊断 fixture，没有修改生产代码、真实数据或部署，也未发布版本。
