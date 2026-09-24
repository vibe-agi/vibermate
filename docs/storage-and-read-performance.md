# 数据位置与读取性能

## 在 App 中更改位置

入口：「设置 → 安全与数据 → 证据存储 → 更改位置」。当前目录和 SQLite 文件路径来自
正在连接的 Runtime，而不是界面猜测的默认值。

选择一个本机磁盘上的父文件夹，App 会展示将要新建的 `ViberMate` 子目录，确认后：

1. 检查没有运行中的 Capture 或上游操作，暂停新出站活动。
2. 停止本机 Runtime，等待进程实际退出；取得 App 代际锁和数据目录锁。
3. 复制整目录，包括数据库、可能尚未 checkpoint 的 WAL、CA、服务端配置。
   内核锁文件和可重建的 SQLite SHM 不作为数据复制。拒绝符号链接和特殊文件。
4. 校验每个文件的 SHA-256，运行 SQLite `integrity_check` 和 `foreign_key_check`。
5. 原子写入待确认的位置设置，启动新 Runtime；启动成功后提交新位置。
   首次启动失败则恢复原目录。旧目录不删除、不再同步后续写入。

不能覆盖已有目录，也不能把目标放在源目录内。复制失败时可能留下不完整副本，但
不会将它设置成当前目录。重新迁移前选择别的位置，或自行检查并处理失败的副本。

位置设置在默认 App 支持目录**旁边**的 `io.vibermate.desktop.storage.json` 中，不随数据
移动。已完成迁移后，如果磁盘未挂载，App 会明确报错，不回退到过时备份，也不新建空库。
macOS 钥匙串凭证仍属于这台 Mac；保留旧目录不等于导出钥匙串，也不是跨机器账号备份。
不要将 SQLite 活动目录放进网盘同步文件夹或网络共享目录。

原生 Web / 容器 Web 仍由部署者通过 `--data-dir` 和持久化卷选择服务器目录。浏览器不提供
移动服务器文件的按钮。迁移服务器前，应停止进程并备份整个目录；不能只搬一个打开的
`runtime.db`。外部提供的 TLS 文件仍由部署者管理。

## 已测量并修复的读取成本

原会话消息列表使用 `ROW_NUMBER()` 对整个活动表做生命周期去重，再按会话、游标筛选。
新查询先使用已有的会话 / Capture / sequence 索引选择候选，再通过 subject 索引排除
被后续生命周期记录取代的条目。排除逻辑不受游标、时间窗口或会话变化影响，避免已完成
请求的旧 `started` 记录在翻页时重新出现。

2026-09-22，本机 Apple M5 Max，临时 SQLite 数据库，读取同一会话、上限 100 条：

| 活动记录 | 旧全表窗口查询 | 新索引查询 |
| --- | ---: | ---: |
| 3,000 | 16.39 ms | 0.74 ms |
| 30,000 | 169.04 ms | 0.83 ms |

这是独立 SQL 基准，**不是**端到端页面延迟，也不是所有机器的保证。复现：

```sh
go test ./internal/runtimepersistence -run '^$' -bench BenchmarkConversationPageRead -benchtime=500ms
```

测试还验证新旧查询结果相同、完成优先、游标边界、时间窗口、会话变化，以及执行计划确实
使用 scope 和 subject 索引，不再为这条读取创建全表排序临时树。不需要改动数据库 schema。

### 会话身份补全不再读取全文

会话目录的身份补全只需要请求协议标识、响应 ID 和响应协议标识。原先从完整证据读取这些
字段，会遍历、解压并校验整段历史。现在通过独立的 `GetConversationEvidence` 读取已有
manifest 中的身份元数据，不访问正文块；完整正文仍走原有的内容读取和完整性校验。
没有增加持久化副本或缓存，过期、删除和损坏 manifest 的检查仍然生效。

2026-09-22，同一台机器、临时数据库，每条合成消息约 1 KiB：

| 消息数 | 原完整证据读取 | 身份元数据读取 | 每次分配内存（原 → 新） |
| --- | ---: | ---: | ---: |
| 100 | 2.32 ms | 0.037 ms | 1.46 MB → 16.4 KB |
| 1,000 | 25.04 ms | 0.043 ms | 15.02 MB → 16.4 KB |

这也是独立存储读取基准，并非页面加载总耗时。复现：

```sh
go test ./internal/runtimepersistence -run TestConversationEvidence -bench BenchmarkConversationEvidenceRead -benchtime=300ms
```

回归测试覆盖请求到响应完成后的元数据变化、保留期限、删除和 manifest 损坏；还验证
正文损坏不会拖累身份读取，但打开正文时仍拒绝损坏的内容。Indexer 测试禁止调用全文
读取接口，防止以后无意恢复这一开销。

前端切换到账号、脚本或设置页后，周期性的管理信息刷新不再同时加载隐藏的会话目录和
时间线。重新查看运行记录时仍会更新证据，不依赖隐藏页面轮询。

## 尚未声称“最优”的部分

- 会话目录与 Exchange 列表现在先返回已有持久投影，客户端日志身份补全由最长 15 秒的
  单任务后台扫描执行；同一范围 5 秒内不重复启动。后续刷新可看到补全结果。回归测试
  阻塞补全器并验证目录与活动列表仍能返回、补全后目录更新。每个 Capture 范围在
  Runtime 启动后首次扫描仍检查最近最多 10,000 条终态 Exchange，修复可能在写入身份
  与更新目录之间中断的记录；扫描成功后，后续查询通过已有身份表主键排除已完成的
  本地身份记录，协议身份和未解析记录仍可继续补全。每轮最多处理 10,000 条候选，
  未到末尾时保存本进程的游标，下轮继续旧记录；出错或重启则重新校正。游标只保存在
  内存中，不改变 SQLite schema；超过 256 个不同范围反复交替访问时会安全地重新扫描
  被逐出的范围。大规模未解析记录的实际吞吐与首次完整校正时间仍需测量。
  身份写入还要求对应 Exchange 的 Activity 或内容仍然存在；Capture 删除事务提交后，
  迟到的补全不能重新插入孤儿身份。删除和迟到写入的回归检查已覆盖此边界。
- 消息预览已经是批量读取，而非逐条加载全文；完整证据按需展开。但超长上下文的首次
  解压、验证和 Flutter Markdown 布局，仍需端到端样本分段计时。
- SQLite 当前采用 WAL 和单连接。这降低写并发复杂度；只有观察到读写排队后，才有依据
  引入独立只读连接池。不能仅调大连接数便认定更快。
- 可见运行会话有轻量轮询，管理信息更新仍是周期性读取。后续可评估变更通知，避免空轮询。

当前没有更换存储引擎、删除数据、关闭完整性验证，也没有为了跑基准读取真实账号或正文。

## 分段性能基线（2026-09-23，初步）

新增两项显式启用的合成基线，默认不参与日常测试耗时。它们测量不同环节，**不是完整的
请求完成到真实 App 首帧指标**，也不能据此宣布端到端性能任务已完成。

```sh
VIBERMATE_PERFORMANCE=1 go test ./internal/runtimepersistence \
  -run '^TestEvidenceReadPerformanceBaseline$' -count=1 -v
cd ui/flutter_app
flutter test test/evidence_render_cost_test.dart \
  --dart-define=VIBERMATE_PERFORMANCE=true --reporter expanded
# 同一套前端合成场景也可在 Chrome 测试运行器中执行：
flutter test --platform chrome test/evidence_render_cost_test.dart \
  --dart-define=VIBERMATE_PERFORMANCE=true --reporter expanded
```

存储测试复用已有合成活动表和正文 fixture，每个环节 25 次读取。输出首次读取与后续
24 次的 p50/p95，以及连接池累计等待、正文写入、原始证据 flush、采样队列峰值和 WAL
文件大小。并发场景每毫秒尝试向已有 raw evidence writer 提交一条合成消息，使用其
正常批处理。首次读取不代表清空 OS 缓存后的冷磁盘读取；进程 heap 采样也不是单次请求
分配量或 RSS 峰值。列表计时为已有 SQL 的读取与扫描，不包括 HTTP/DTO/身份补全。

本机 darwin/arm64，Go 1.25.13；30,000 条活动、1,000 条约 1 KiB 合成上下文的本次样本：

| 环节 | 无并发写入 p50 / p95 | 有 raw writer p50 / p95 |
| --- | ---: | ---: |
| 单会话消息列表 SQL | 0.82 / 0.94 ms | 0.85 / 7.77 ms |
| 身份元数据读取 | 0.10 / 0.17 ms | 0.11 / 0.20 ms |
| 完整正文投影 | 33.79 / 36.29 ms | 31.92 / 47.29 ms |

该并发样本有 9 次连接池等待，累计约 70.5 ms；这提示应继续定位排队和批处理影响，
尚不足以决定增加连接数或更换引擎。必须在同一机器和受控负载下重复验证。

前端测试预先加载合成证据，只测固定 1180×760 窗口中的首次挂载到测试帧稳定；7 次挂载
中后 6 次计算 warm p50/p95。Flutter 3.41.5 的 debug `flutter-tester` 本次结果：

| 单条合成正文 | warm p50 / p95 |
| --- | ---: |
| 37 B | 51 / 55 ms |
| 9,472 B | 320 / 545 ms |
| 151,552 B | 10,139 / 10,857 ms |

以上为优化前的基线：折叠态仍将完整 Markdown 交给排版器。现在超过 8 KiB 的
正文及 Thinking 在折叠态只构建最多 15 行 / 4,096 字符的预览，展开时才构建完整内容。
同一合成 fixture、同一 debug 测试渲染器的优化后独立复测：

| 单条合成正文 | warm p50 / p95 | 渲染文本长度 |
| --- | ---: | ---: |
| 37 B | 66.5 / 107.2 ms | 92 字符 |
| 9,472 B | 63.7 / 78.8 ms | 348 字符 |
| 151,552 B | 55.8 / 68.9 ms | 348 字符 |

151 KiB 折叠挂载的 warm p50 从约 10.1 秒降至约 56 毫秒；第二次优化后运行测得
47 毫秒。常规 Widget 回归检查正文、Thinking 的折叠预览、展开后全文和再折叠；原
`ExchangeDetail` 文本不截断。当时展开全文仍需排版全部 Markdown，不能把折叠态数字
当作全文加载数字。两组数值都包含 debug/test 开销，**不等于发布 App 或浏览器用户实际
等待时间**。Chrome 基线、真实 App/Server 的 HTTP 解码和渲染阶段串联、远程 RTT
以及更稳定的回归阈值尚待补齐；不改变正文保留或完整性校验。

工作台的完整证据详情缓存继续按 LRU 保存，但新增约 8 MiB 的正文预算（同时保留原有
64 条 / 完整视图 2 条上限）；单条证据超过预算时仍保留当前项供查看。预算依据每个内容块
报告的原始大小，属于近似的内容上限，不是进程 RSS 上限。删除证据和控制器销毁仍会清空
对应内存状态；合成回归验证两个大详情不会同时常驻缓存。
优化前的 Chrome 151 KiB 测试运行未完成，因此没有可比的浏览器优化前数字。优化后，
相同合成 fixture 在 Chrome debug 测试运行器的 warm p50/p95 分别为：37 B
62.2/68.5 ms，9,472 B 62.4/68.3 ms，151,552 B 65.2/67.2 ms（各 7 次挂载）。
正文 / Thinking 折叠、展开、再次折叠及缓存驱逐 Widget 测试也在 Chrome 通过。
这些是浏览器测试帧稳定耗时，不是远程 Web 的真实请求到可见帧延迟；不能把 Flutter
VM 数字直接当作 Web 数字，也不能把此处 debug 数字当作发布构建的性能保证。

2026-09-24 复跑同一合成测试：Chrome debug 的 37 B、9,472 B、151,552 B 折叠态
warm p50 分别为 41.8、42.1、42.6 ms，p95 为 42.2、43.3、43.2 ms；正文与 Thinking
展开、再折叠检查通过。30,000 活动 / 1,000 消息的 Go 存储测试里，单会话列表
warm p50/p95 为 0.53/0.59 ms；并发 raw writer 时为 0.53/0.63 ms，完整正文投影
20.99/25.24 ms，有 8 次连接池等待、累计 18.5 ms。与前次数字存在正常测试波动，
仍不能由这组小样本推出增加只读连接会改善真实页面；保持当前单连接，先完成
真实 App 与远程 Web 请求到可见帧的分段测量。

### Web 空闲 Capture 轮询

2026-09-23，在本机原生 HTTP Server、390×760 Playwright Chromium、隔离数据目录中保留
4 条尚无 Exchange 的合成手动 Capture，登录所有者并停留在空 Capture 页面。登录稳定后
记录连续 12 秒的 `/api/v1/` 响应，按路径汇总 `response.body()` 的字节数：

| 版本 | 请求总数 | 响应正文 | 空 Capture 的详情请求 |
| --- | ---: | ---: | ---: |
| 优化前 | 46 | 20,046 B | Assignment 14 次，Conversation 14 次 |
| 优化后 | 31 | 13,611 B | Assignment 1 次，Conversation 1 次，轻量 Activity 探测 11 次 |

空 Capture 现在每秒只探测最新一条 Activity，发现记录后才重新读取 Assignment、
Conversation 和时间线；5 秒总览刷新不再与运行中 Capture 的快轮询重复加载详情。
请求数下降约 33%，正文传输下降约 32%。Widget 回归用先空后出现证据的合成接口
验证新记录仍会进入目录。以上是**单次本机空闲样本**，不代表长对话、远程 RTT 或
浏览器隐藏状态；本次无头 Chromium 将后台标签仍报告为 `visible`，没有据此声称隐藏
页面的轮询已经优化。

控制器还在 Flutter 生命周期报告 `hidden`、`paused` 或 `detached` 时暂停两组定时
轮询，恢复 `resumed` 时立即读取最新状态。Widget 测试用生命周期事件验证隐藏期间
零新增请求、恢复后补拉；真实浏览器后台标签是否正确发出该事件仍待有头浏览器验收。

### 本机 Web：请求完成到新 Exchange 可见（2026-09-25，探索性）

在 macOS/arm64、Apple M5 Max、Chrome 153、Flutter 3.41.5 release Web 构建中，
以隔离数据目录启动原生 Server，并把合成 Anthropic 请求经手动代理的 CONNECT/TLS
送往本机合成 Provider。Playwright 登录 Web 后保持同一运行中的手动 Capture 可见。
计时从客户端收到完整 HTTP 响应开始，到浏览器可访问性树出现该请求的唯一标记并
完成下一帧为止；**不包含 Provider 等待时间**。每组发送 24 条短请求、1 条约
9 KiB、1 条约 151 KiB 的连续文本与 1 条约 151 KiB 的 Markdown 段落；请求前按
固定序列错峰，避免与 1 秒轮询持续同相。
短请求剔除首次样本后报告 23 次 warm p50/p95：

| Web API 路径 | 短请求 warm p50 / p95 | 9 KiB 单次 | 151 KiB 单次 |
| --- | ---: | ---: | ---: |
| 本机直连 | 786 / 1,289 ms | 794 ms | 786 ms |
| 浏览器每条 API 请求人为增加 80 ms 延迟 | 1,287 / 1,304 ms | 795 ms | 781 ms |

第二次独立运行的短请求本机 p50/p95 为 791/1,293 ms，注入延迟为
1,285/1,301 ms；表中大正文仍只是首次运行各一次的样本。

本机短请求的最新 Activity 探测响应完成时间 p50/p95 为 386/816 ms，随后
Conversation 目录为 391/818 ms，所选 Exchange 的 Activity 响应为 392/819 ms；
最后一个相关 API 响应到可访问性树出现并完成下一帧为 145/505 ms。这些是相对
客户端 HTTP 完成时刻的浏览器侧观察，不能据此独立推导数据库提交或索引耗时。

复现：先运行 `flutter build web --release`，安装可供 Node 加载的 Playwright 与
Chromium，再从仓库根目录运行以下命令。`VIBERMATE_PLAYWRIGHT_MODULE` 填本机
`node_modules/playwright` 的绝对路径；若已在 Node 模块搜索路径中安装，可省略。
脚本自行编译 Go Server、建立隔离数据目录和合成 Provider，结束后清理这些临时
资源；不读取真实账号或证据。

```sh
VIBERMATE_PLAYWRIGHT_MODULE=/absolute/path/to/node_modules/playwright \
  VIBERMATE_BROWSER_CHANNEL=chrome \
  node tool/performance/web-capture-latency.mjs
```

如使用 Playwright 自带的 Chromium，可省略 `VIBERMATE_BROWSER_CHANNEL`。
脚本输出首次样本、warm 分位数及每个可观察 API 边界的 JSON，另测两种大正文
展开。末尾关闭合成上游，断言真实 Server 记录的 `provider_transport_failed` /
`connection_failed` 与浏览器打开失败 Exchange 后的提示一致。小样本主要受
1 秒可见证据轮询周期和采样相位影响；80 ms 是浏览器侧逐请求注入的延迟，
**不等于实测远程部署**，表中大正文也各只有一次观测。
这里没有把浏览器可访问性树出现冒称为 GPU paint 时间，也没有分离证据提交、
索引和 Flutter 解码的耗时，因此 Task 1 的真实 App、远程 Web 与完整分段验收
仍未完成。

### 超大正文展开（2026-09-25）

上面的折叠态基线掩盖了全文展开的另一处瓶颈。同一 Chrome debug Widget 测试中，
151,565 B 连续文本原需 33.63 秒展开；151,565 B / 4,096 段 Markdown 原需
17.36 秒。151,577 B 的围栏代码块原已能在 0.26 秒展开。先比较原生
`SelectableText`（同样的连续文本挂载约 0.96 秒），再按内容形态做窄修复：

| 正文形态 | 修改前展开 | 修改后展开 |
| --- | ---: | ---: |
| 151 KiB 连续纯文本 | 33.63 秒 | 0.43 秒 |
| 151 KiB 独立 Markdown 段落 | 17.36 秒 | 0.43 秒 |
| 151 KiB 围栏代码块 | — | 0.26 秒（原路径，无需改动） |

超长、没有 Markdown 结构的 ASCII 单串（例如 Base64/JWT）用原生可选中文本显示，
源文与复制内容保持完整。超长独立段落仍保留内联 Markdown，但在一个有界的滚动
区域按约 8 KiB 的段落边界逐段构建；列表、引用、围栏等跨段落结构仍走原路径，
避免错误重排。常规回归检查完整末尾可滚动到达、文本可选中、普通 Markdown 仍
按原样渲染。它不是对任意超长结构化 Markdown 的性能保证。

同一原生 Server + release Web + Playwright 合成路径中，修改前连续文本点击展开
在本机/注入延迟两组各约 33–36 秒；修改后重复观测为约 0.20–0.36 秒。
多段 Markdown 修改后在两组浏览器样本中约 0.14–0.35 秒；这是点击到下一帧的
单次样本，不是 p95 或 GPU paint 指标。用户滚动后可见合成正文末尾；测试运行器
同时检查全量原文仍在证据中。复现展开 Widget 基线：

```sh
cd ui/flutter_app
flutter test --platform chrome test/evidence_render_cost_test.dart \
  --dart-define=VIBERMATE_PERFORMANCE=true --reporter expanded
```

本次浏览器路径还发现一个实际可见性错误：手动 Capture 有首条 Exchange 后只轮询
已选中的 Conversation，后续独立 Exchange 在持久层已存在，却要手动刷新才显示。
现在每秒轻量探测该 Capture 的最新 Activity；仅在顶端记录变化时更新目录。正在
查看最新项的用户会跟随新 Exchange，查看旧项的用户保留原选择。Widget 回归固定
这两种行为，并检查无变化时不会重新读取 Conversation 目录。
