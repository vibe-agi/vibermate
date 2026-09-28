# 完整运行概览与 Web 验收

范围：VIBERMATE-44 本次运行/客户端会话概览。未替换 `/Applications/ViberMate.app` 或 `dist/ViberMate.app`，不代表整体候选包或发布门禁通过。

## 实现边界

`GET /api/v1/activities/summary` 接受单一 CaptureRun/ManualCapture，或一对明确的 client/session。闭合参数和响应，不支持缺字段旧响应。请求和会话范围互斥；会话包含同一 Runtime 内其他启动，但不能把其他客户端的同名会话混入。

请求统计复用 activity 列表的生命周期唯一胜出 SQL、现有索引和两个有界只读 WAL 连接，终态/开始事件不会双计。不依赖正文、不进行本地 Agent 日志扫描、不受列表分页或 100,000 上限影响。异常原因最多返回十类和其余合计。普通 Web member 与本机 CLI 不因新增接口得到全局读取权限。

用量仍经过现有 RuntimeUsage 过滤、汇总与明细分页路径：同一 Capture 或 client/session 的显式过滤，服务器时间确定覆盖最大留存的 UTC 窗口。用量和请求记录的留存、启用时间、失败及更新时间分开表达；不将采集缺口当作零。没有新增缓存、统计库、依赖或迁移链。

布局沿用现有主题、报表卡片、数字格式和可分页表格。上部总请求量及四种状态，下部独立采集覆盖、Token/等价费用、模型→调用者明细、异常类型。有正文也可进入概览；无正文默认概览。手动选择不会被请求进入 pending 或后台更新改写。最新独立请求 pending 时参考其他对话已完成的正文证据，保留原有请求视图。

## 可复核检查

| 检查 | 结果 |
| --- | --- |
| 240 次请求、加载页限制 100、终态唯一胜出 | 全量 240，200 成功/10 失败/10 取消/20 未终态 |
| 精确客户端会话跨启动与手动连接 | Codex 242、同名 Claude 1；独立手动连接 1 |
| 写连接被占用时读取汇总 | 只读连接正常完成 |
| 300,003 生命周期记录，100,001 个请求 | 无截断；单次约 1.47 秒 |
| 同一大库中的单请求会话 | 约 0.2 毫秒；不是生产延迟承诺 |
| owner/member/CLI 真实 HTTP 权限 | owner 200；其他身份拒绝 |
| 390/1440，中英、深浅主题，范围迟到、分页、后台暂停 | 自动检查通过 |
| 全仓 Go / Flutter | Go 通过；Flutter 670 通过、16 条既有条件跳过 |

功能 race：

```sh
go test -race -short ./internal/activity ./internal/runtimepersistence ./internal/desktopcontrol ./internal/desktophost ./internal/serverhost -run 'TestActivitySummary|TestEvidencePointReads|TestHostPublishesReadyGeneration|TestServerAdminCreatesRuntimeUser' -count=1
```

`-short` 仅使新增 300k 行规模夹具不参加这项功能 race。该夹具普通执行通过；最初在 race 下的 30 秒墙钟断言失败，原因是纯 Go SQLite 插桩使建库和扫描显著放大。已移除不适合跨机器/race 的耗时断言，保留完整数量断言与时长输出；没有宣称完整规模 race 已通过。

## 真实 Web 操作

隔离目录 `/tmp/vibermate-overview-web.xn7jVQ`。当前源码编译 daemon + Flutter release Web，loopback 独立数据库、合成 API-key 账号与上游；正文 off、用量显式开启。130 次真实 CONNECT/TLS 语义请求分属两个手动连接，共用协议声明的 Claude 会话；没有读取真实账号凭据或改写用户数据。

Chrome 原生 UI 实际确认：

1. 本次连接 A：125 请求，113 成功、12 失败，成功率 90.4%；完整 Token/模型汇总。
2. 切换客户端会话：130 请求，118 成功、12 失败，90.8%。
3. 模型下钻到调用者及选中范围小计；未证明身份的手动来源显示未报告。
4. 新增一条 A 请求，自动及手动刷新显示会话 131 请求、119 成功、12 失败；范围和模型下钻保持。
5. 请求计数与用量分别显示时间/覆盖；缺价格不伪造零金额，已知费用保留 ≥ 语义。

浏览器 UI 的元素编号会随 15 秒刷新失效，最终使用同次最新 AX 观察定位精确测试页的刷新控件完成验证；未操作其他页面。测试标签已关闭、临时 Runtime 已停止。最终正文默认视图小修复发生在此次 Web 编译后；其全量 Widget 回归已通过，最终候选包仍需重新构建与交叉验收。

## 包内 Web 与团队项目复验

候选 `dist/candidates/local-kueeBmxJ/ViberMate.app` 内的 daemon 和 Web 资源，隔离目录 `/private/tmp/vibermate-web-final.KVYhdT`。同样使用真实 loopback CONNECT/TLS、合成凭据、关闭正文与开启用量；没有操作真实账号或用户 App。两次手动连接产生 131 请求，再经真实 Server 登录／Capture 创建接口为 Alice 和 Bob 上报同一个远端 Git 项目，分别发送 10 和 5 请求。

实际 Chrome 验证：

- 总量 146 请求，134 成功、12 次夹具预设失败；项目中 131 条未报告、共享项目 15 条，不强行归属旧记录。
- `github.com/example/shared-library` → Alice 10／Bob 5；Alice → 两个模型各 5，小计不重复累计。切换分支维度后 `main` 10／`feature/accounts` 5，刷新保留当前有效下钻和数量。
- Bob 的普通成员页面只显示其自己的 5 次请求和项目小计，不能混入 Alice 或访问 owner 配置。
- 合成 API-key 账号由“跟随流量策略”改成已发布的“直连／系统 DNS”，保存与读回一致；非 OAuth 账号不出现不支持的刷新开关。
- 英文／深色与中文／浅色，桌面及 Chrome 的 iPhone 16 393×852 响应式视口：六个指标窄屏两列、项目长名称换行、表格及小计可访问。390px 是既有 widget 覆盖，实际浏览器使用的是 393px，不混写。
- 未观察到应用 JavaScript 异常；开发者工具仅显示 favicon 404。只关闭本次测试标签，隔离 daemon 与合成上游均已停止。

发现并修复一个真实后端与预览数据的差异：会话目录没有返回最新请求的正文可用性，UI 却把未知值当成有正文，导致无正文 Capture 首次仍打开请求记录。后端复用已有批量 `AvailableBodies` 元数据查询，每页最多 200 条，不读取正文；UI 只有明确的 `true` 才选择正文请求视图，不覆盖用户手动选择，也不把查询失败伪装成“未记录”。已先复现 Go 目录契约和 Flutter 未知值两个失败检查，修复后相关 135 项、Flutter 全量 671 项及 Go 全仓通过；16 条 Flutter 检查仍按原平台／集成条件跳过，analyzer 与结构检查通过。

该默认页签修复发生在上述候选编译之后，后续复验如下；上述结果不等于整体发布通过。

## 重建候选复验与账号样式反馈

候选 `dist/candidates/local-BChEs00i/ViberMate.app` 的真实 daemon 与包内 Web，隔离目录 `/private/tmp/vibermate-web-verify.q4kSFl`。正文 off、统计 on；两次手动连接产生 188 请求，真实登录／启动接口增加同远端 Git 项目的 15 请求，总计 203。所有凭据和请求均为合成数据。

- 冷启动进入无正文的统计概览；本次运行 183 请求（166 成功／17 失败），切到客户端会话为 188（171／17）。
- 59 个模型分为 50＋9 页，翻页后完整小计仍为 183，末页下一页禁用；手动选请求记录后全局刷新不改页签。
- 导入合成 OAuth 默认关闭自动刷新；UI 开启、全局刷新及真实 API 读回一致，再从 UI 关闭。账号出口保存读回通过；API-key 账号不显示不支持的刷新开关。
- OAuth 额度请求通过明确选中的本地拒绝 SOCKS 代理，失败如实显示；20 次连接到达拒绝代理，不静默直连，Runtime 仍 ready。此项不代替额度成功／排序验收。
- 共享项目 15 请求 = Alice 10／Bob 5；Alice 两个模型各 5，小计及刷新下钻保持。
- 测试标签关闭，隔离 daemon 与代理均正常停止，没有操作用户 App／真实凭据。

用户随后指出每行 Auto refresh 重复且开关太大：改为桌面列头统一标注、紧凑原生开关、独立凭据健康提示；窄屏保留局部标签，固定混合账号操作列宽。增加多账号、点击范围、独立读屏语义与列对齐检查；Flutter 全量 671 项通过、16 条既有条件跳过，analyze 与 diff 检查通过。

已重建 `dist/candidates/local-OaOh98uf/ViberMate.app` 并通过包校验，复用上述隔离数据、Server 端口 49540；三个合成 OAuth（Pro/Plus）和一个 API-key 的真实 Chrome 验证：

- 英文深色、中文浅色的桌面表格：一个列标题、三个独立小开关，API-key 显示 `—`；图标下套餐标签、凭据状态和操作列对齐。
- 切换 `Style review pro`，真实保存后刷新保持，其他账号不变；AX 独立 switch 包含账号名和 on/off，整行不再误成开关。
- iPhone 16 的 393×852 视口，中英／深浅卡片布局无溢出；英文操作区空间不足时换行，局部 Auto refresh 标签保留。
- 控制台仅出现预设拒绝代理导致的额度 502 及 favicon 404，无观察到的界面脚本异常。此夹具验证失败展示，不代表额度成功读取通过。
- 已关闭本测试标签及响应式工具，SIGTERM 正常停止独立 daemon。原 App 和 dist App 的 daemon 哈希未变；没有切换真实数据、安装或发布。

## 额度成功与策略账号卡片 Web 验收

隔离夹具 `/private/tmp/vibermate-quota-web.C6RC5j`，复用独立合成数据库，Server 端口 50702。Web 资源来自 `local-OaOh98uf`；后端使用同一生产 `serverdaemon.ProductionOptions`／`Run` 组合，额外在测试进程中信任一次性合成 TLS 根。证书验证仍开启，SOCKS 仅将流量送到固定 loopback 模拟上游；只接受 ChatGPT 额度 GET 和三个合成账号。未修改候选包、系统信任、Keychain 或真实凭据，因此此项不是对真实 ChatGPT 服务可用性的声明。

- 真实 HTTP 额度读回：Plus 70%、Pro 25%、导入账号 95%；同一 7 天主窗口重置分别为 2 小时、126 小时、24 小时。
- 实际 Chrome 账号表格选择“7-day reset · soonest first”后顺序为 Plus → 导入账号 → Pro，未知额度 API-key 最后；70% 黄色、95% 红色，25% 为正常颜色。
- 将模拟额度改为 94%，点击 Plus 的单账号刷新，UI 从 70% 更新为 94%，顺序不变。
- 策略可用账号卡片沿用相同正序，倒计时显示 `1h50m`／`23h50m`／`5d5h` 等紧凑表达；主窗口与 5 小时窗口分别标注，当前启用 Pro 不被排序自动改选。
- 卡片组右上角刷新真实重读三个账号，将 Plus 从 94% 更新回 70%；截图确认阈值颜色、邮箱下用量条和等尺寸的 Activate／Active 按钮。
- 显式启用 Plus 发布策略 r2；全局刷新后启用状态保持。未触发真实模型调用。

测试标签已关闭，独立 Server 正常退出，测试专用 Go 包已移到私有临时目录，不进入正式源码或候选包。

剩余门禁：最终原生切换、签名和发布。

## 发布 CI 的规模与竞态分工

首次全量远端 `-race` 暴露测试配置问题：12k 生命周期查询的 2 秒性能预算超时，300k 生命周期规模检查耗尽该包的默认 10 分钟预算；日志没有数据竞争报告。相同本机 12k 查询普通执行 43.2 ms、竞态插桩执行 1.74 s，100,001 请求普通汇总 1.27 s，单请求会话 0.20 ms。纯 Go SQLite 的每次内存访问也被插桩，不能将该耗时当作发布二进制性能。

CI race 改用已有的 `-short` 约定；仅三个批量规模／计时夹具（12k 生命周期、300k 生命周期、100k 用量）不参与插桩。普通 `unit` 和 `contracts` 仍不带 `-short`，保留原规模、数量、分页和性能断言。读取不占写连接、并发写入与快照失效、会话权限及金额正确性等功能检查继续参加 race，未改生产查询或放宽普通性能门限。定向功能 race 本机通过；最终远端完整 race 结果仍需等待。
