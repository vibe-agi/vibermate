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

同一 30,000 活动 / 1,000 消息 fixture 另对“每次重新打开 SQLite 后的第一次读取”
采样 25 次。每次关闭并重新建立唯一连接，但**不驱逐操作系统页缓存**，因此这是
连接冷启动，不是假装测量物理冷盘：

| 连接冷启动环节 | p50 / p95 |
| --- | ---: |
| 打开并校验 Store | 2.22 / 2.86 ms |
| 第一次单会话列表 | 0.90 / 1.21 ms |
| 第一次身份元数据读取 | 0.11 / 0.14 ms |
| 第一次 1,000 消息完整投影 | 36.45 / 42.85 ms |

这补齐了可重复的冷 / 热读取分位数，但不支持“SSD 冷读”或跨机器 SLA 的说法。
测试报告显式输出 `osPageCacheEvicted:false`，防止以后把口径写错。

### 跨运行记录元数据检索（2026-09-26）

检索只读取已有 Activity 列与仍在保留期内的 Exchange manifest：工作区、Capture、
对话、流量策略、账号、请求/实际模型、工具名称、状态、错误和时间。它不会扫描或
解压消息块、原始 HTTP 正文、Header 或工具参数，也不建立一份额外的敏感全文索引。
模型与工具元数据随 Exchange content 到期而停止命中；账号、状态等 Activity 元数据
仍按其自身保留/删除边界返回。结果中的正文摘要仅对最终命中的少量 Exchange 调用
已有、受保留策略约束的 preview 投影。

分页按不可变 Activity sequence 倒序，游标绑定完整筛选条件；换条件复用旧游标会被
拒绝。当前 Server 只把该入口放在 Owner 工作台，成员与匿名会话不能读取团队检索。
点击结果先打开精确 Exchange 证据，也可再进入所属 Capture。

在上述 30,000 Activity / 1,000 消息的合成 SQLite 样本上，搜索一个位于较早历史的
Capture ID 子串，每组 25 次：无并发写入 warm p50/p95 为 **37.9/38.7 ms**；有
并发 Raw evidence 写入时为 **37.8/42.9 ms**。这是本机存储层样本，不是页面 SLA；
当前证据不支持增加 FTS、复制正文索引或更换数据库。

同一 release Web + Playwright 合成链路还验证了 Owner 接口与页面：账号 + 模型 +
成功状态的组合筛选返回 10 条；只存在于约 184 KiB 消息正文末尾的唯一标记返回 0 条；
页面在 Capture 目录收起时仍能打开“搜索全部记录”，键盘输入账号、选择结果并进入精确
证据详情。普通成员和匿名会话访问 Owner 检索接口均返回未授权。可用
`VIBERMATE_SEARCH_ONLY=1 node tool/performance/web-capture-latency.mjs` 复跑精简路径。

### 原生 macOS App：请求完成到证据可见（2026-09-25）

使用当前提交构建 `dist/ViberMate.app`，以标准 `CFFIXED_USER_HOME` 隔离 App 数据；
sidecar 继续使用当前登录会话的 macOS Keychain。仓库已有的打包 App 双启动、状态恢复、
偏好原子落盘、进程归属及优雅退出验收通过（14.33 秒）。随后在同一隔离 Runtime 中
创建唯一命名的合成 Endpoint、Account、Environment 和手动 Capture；Provider 只监听
本机回环地址。测试结束前撤销全部额外 Capture，并从管理接口确认合成 Environment、
Account、Endpoint 均已删除；临时目录和脚本移入废纸篓。

计时从合成客户端读完下游 HTTP 响应，到原生窗口可访问性树出现唯一请求标记。持续
选中最新 Exchange 时，20 条 warm 短请求的 p50/p95 为 **617 / 1,012 ms**；约
9 KiB 和 151 KiB 的折叠正文各一次为 745 ms 和 131 ms。切换到最早 Exchange
为 617 ms；点击展开 151 KiB 连续文本直到全文末尾进入可访问性树为 1.39 秒。
这是 release App 的真实 Runtime、代理、SQLite、HTTP 客户端和 Flutter 窗口路径，
但可访问性出现仍不等同于逐像素 GPU paint。

另一条关联请求的阶段时间如下；所有时间使用同一台机器的壁钟，Provider 等待在客户端
响应完成之前，不计入后续显示延迟：

| 同一请求阶段 | 相对客户端响应完成 |
| --- | ---: |
| 终态 Activity 的 `occurredAt` | -2 ms（时间戳粒度内） |
| Activity 与 Conversation 目录 API 首次返回该记录 | +13 ms |
| 所选 Conversation 的 Activity API 返回 | +15 ms |
| 原生 App 可访问性树出现标记 | +822 ms |

由此可见该样本的证据提交、索引与本机 API 不是主要等待；剩余约 0.8 秒包含 1 秒轻量
探测相位、JSON 解码、状态更新和 Flutter 布局。为分开后两项，合成 Exchange JSON
在 Flutter VM debug 中对 151 KiB 正文采样 25 次：`jsonDecode` warm p50/p95
为 0.158/0.182 ms，Control 模型校验为 0.125/0.168 ms；随后独立 Widget
挂载 warm p50/p95 为 16.4/18.8 ms。Chrome debug 的微秒钟分辨率较粗，两个
解码环节 p95 均不超过约 0.1 ms，Widget 挂载 warm p50/p95 为 44.9/50.9 ms。
这些分段不相加成虚假的精确总和；真实 App 的 1 秒探测仍主导可见延迟。

测量末尾 macOS `footprint` 显示 GUI 当前/进程生命周期峰值约 207/562 MiB，daemon
约 283/812 MiB；daemon 同时有约 530 MiB 可回收 VM，数据库/WAL 为约
0.8/4.0 MiB。它们是整个进程在多次可访问性快照和长正文展开后的高水位，不是单次请求
分配量，也没有足够依据增加 SQLite 连接或设置产品内存 SLA。

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

2026-09-25 继续检查重建边界：原控制器在每次 5 秒 Dashboard 读取后通知一次，随后
相同的空审批列表再通知一次；即使所有修订不变，整棵 `WorkbenchShell` 也会重建至少
2 次。现在 Dashboard 按 Runtime/Offline 状态、Capture 更新时间、Environment 摘要、
Endpoint 修订及 Account/凭据/OAuth 修订逐项比较；空审批列表按记录修订比较。读取仍然
发生，以便账号修改和权限撤销按原周期生效，但事实不变时不发送 UI 通知。

Widget 回归中，一个完全相同的 5 秒周期从 2 次以上通知降为 0；下一周期把账号备注
修订推进后恰好通知 1 次并显示新备注。并发轮询仍保持单飞，切换 Capture 后的迟到响应
仍由选择代际丢弃；Web Session 失效仍只触发一次失效通知并返回登录页。release Web
在流量策略页稳定 2 秒后观察 11 秒：18 个 `/api/v1/` 响应、约 17.2 KiB，语义 DOM 的
非样式变更为 0。请求数受 5 秒采样相位影响，不能和不同起点的 12 秒样本直接相减；
这里验证的是“继续核对权限，但不因相同结果重建”。Playwright 工具在该值非零时失败。

### 本机 Web：请求完成到新 Exchange 可见（2026-09-25，探索性）

在 macOS/arm64、Apple M5 Max、Chrome 153、Flutter 3.41.5 release Web 构建中，
以隔离数据目录启动原生 Server，并把合成 Anthropic 请求经手动代理的 CONNECT/TLS
送往本机合成 Provider。Playwright 登录 Web 后保持同一运行中的手动 Capture 可见。
计时从客户端收到完整 HTTP 响应开始，到浏览器可访问性树出现该请求的唯一标记并
完成下一帧为止；**不包含 Provider 等待时间**。每组发送 24 条短请求、1 条约
9 KiB、1 条约 151 KiB 的连续文本、1 条约 151 KiB 的 Markdown 段落与 1 条约
184 KiB 的结构化 Markdown；请求前按
固定序列错峰，避免与 1 秒轮询持续同相。
短请求剔除首次样本后报告 23 次 warm p50/p95：

| Web API 路径 | 短请求 warm p50 / p95 | 9 KiB 单次 | 151 KiB 单次 |
| --- | ---: | ---: | ---: |
| 本机直连 | 786 / 1,289 ms | 794 ms | 786 ms |
| 浏览器每条 API 请求人为增加 80 ms 延迟 | 1,287 / 1,304 ms | 795 ms | 781 ms |

第二次独立运行的短请求本机 p50/p95 为 791/1,293 ms，注入延迟为
1,285/1,301 ms；表中大正文仍只是首次运行各一次的样本。

最新同一轮 release Web 验收中，151 KiB Markdown 段落的请求完成到折叠可见为
本机 787 ms / 注入延迟 796 ms，展开为 97 / 109 ms；184 KiB 结构化 Markdown
分别为 300 / 781 ms 和 325 / 102 ms。Playwright 真实点击展开，Owner API 同时
读取该 Exchange 并断言唯一末尾标记仍存在；浏览器离屏语义节点不被冒充为完整性来源。

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
索引和 Flutter 解码的耗时。此处的本机样本未覆盖真实 App 或远程 Web；
完整分段验收仍未完成。

### 真实远程 Web：合成 Capture（2026-09-25，探索性）

在一台隔离的 Ubuntu Linux x86_64 主机（约 7.3 GiB RAM）上，以提交
`dde1104` 的 Linux Server 和同一份 release Web 构建启动独立 Runtime；
Server 与合成 Anthropic Provider 都只监听远端回环地址。本机 Chrome 153
通过 SSH 隧道访问 Web 与手动代理，Playwright 登录后选中该 Capture，再经
CONNECT/TLS 发送合成请求。没有开放公网管理端口，也没有使用真实账号或正文。
本次 SSH 隧道启用了传输压缩，因此数值描述的是这条**真实远端测试链路**，不是
普通公网 HTTPS 的通用性能。计时边界仍是客户端收到完整响应，到浏览器可访问性树
出现唯一标记并完成下一帧；Provider 等待不计入。

同一次运行发送 24 条短请求（剔除首次后 23 次 warm 样本）、一条约 9 KiB 和
一条约 151 KiB 的请求正文：

| 场景 | 观察值 |
| --- | ---: |
| 短请求完成 → 可见，warm p50 / p95 | 1,288 / 1,799 ms |
| 所选会话 Activity API 完成 → 可见，warm p50 | 397 ms |
| 9 KiB / 151 KiB 正文完成 → 折叠可见，各一次 | 2,299 / 794 ms |
| 151 KiB 展开 → 下一帧；切到最早会话 → 可见，各一次 | 215 / 220 ms |
| 并发写入 10 条时，20 次会话目录读取 p50 / p95 | 235 / 507 ms |

并发写入期间，浏览器继续显示用户主动选择的旧会话，没有跳回最新项。完成后一次
`limit=100` Activity 响应为 36,939 B，一次 `limit=200` 会话目录响应为
40,110 B。该 Server 进程的 `/proc` 样本显示 RSS 约 189 MiB、启动以来
VmHWM 约 365 MiB，SQLite WAL 约 4.1 MiB；这些是整个进程及测试数据的
快照，**不是单次请求的内存峰值**。本机直连的短请求 warm p50 约 0.79 秒，
但两种路径的采样相位、链路与进程不同，不应相减得到“纯网络开销”。其他远程
运行曾出现短请求 p50 约 0.80 秒；1 秒轮询相位使小样本波动明显，当前没有
稳定的延迟 SLA。

新浏览器上下文的冷登录框在修复前 3 次分别于 25.2、25.1、22.8 秒可见。
资源计时显示未压缩的 `main.dart.js` 为 4,404,763 B，单独传输约
20.6–23.4 秒，是这条链路的主要冷启动成本。Server 现对浏览器请求的主 JS
包提供 gzip；未压缩请求保留 Range，gzip 请求的 Range 安全地从头重发，
HEAD 保持相同编码协商。浏览器实收约
1,291,679 B，解码后仍为 4,404,763 B。不压缩的 SSH 隧道上，同一资源的
单次 curl 传输从原文 79.2 秒变为 gzip 19.2 秒；这只是该带宽条件的
字节传输对照，不是页面首帧改善值。已经启用 SSH 压缩的验收隧道上，修复后
3 次冷登录框仍需 27.2、28.0、21.7 秒，因此不能宣称这条隧道的冷启动已
明显加速。远端回环读取 gzip 资源约 0.13 秒，排查重点仍在远程传输。

这轮验证覆盖真实远程 Web 的列表、切换、展开和并发读写，但仍缺 App 原生
端到端首帧，以及证据提交、索引、HTTP 解码、Flutter 布局各阶段在同一条
请求上的关联计时；大历史数据的远端冷/热 p50/p95 也未形成稳定基线。
Task 1 继续进行，不据此增加 SQLite 只读连接或调整正文保留策略。

### 超大正文展开（2026-09-25）

上面的折叠态基线掩盖了全文展开的另一处瓶颈。同一 Chrome debug Widget 测试中，
151,565 B 连续文本原需 33.63 秒展开；151,565 B / 4,096 段 Markdown 原需
17.36 秒。151,577 B 的围栏代码块原已能在 0.26 秒展开。先比较原生
`SelectableText`（同样的连续文本挂载约 0.96 秒），再按内容形态做窄修复：

| 正文形态 | 修改前展开 | 修改后展开 |
| --- | ---: | ---: |
| 151 KiB 连续纯文本 | 33.63 秒 | 0.55 秒 |
| 151 KiB 独立 Markdown 段落 | 17.36 秒 | 0.09 秒 |
| 184 KiB 标题、列表、链接、引用混排 | 43.72 秒 | 0.11 秒 |
| 151 KiB 围栏代码块 | — | 0.52 秒（原路径，无需改动） |

超过 32 KiB 的非单一围栏正文在有界区域内显示完整、可选中的 Markdown 源码；
这避免把列表、引用、表格、引用定义或 HTML 切段后改变语义，也避免把首次展开的
成本延后到滚动过程中。小正文与单一围栏代码块继续使用格式化 Markdown。正文、
复制内容与持久化证据均不截断；390px 回归使用键盘 Enter 展开结构化正文，并检查
完整末尾、精确原文与可选中文本。

同一原生 Server + release Web + Playwright 合成路径中，修改前连续文本点击展开
在本机/注入延迟两组各约 33–36 秒；修改后重复观测为约 0.20–0.36 秒。
这轮最终 Chrome debug 样本中，独立段落与结构化 Markdown 分别约 0.09 秒和
0.11 秒；Flutter VM debug 分别约 0.042 秒和 0.044 秒。这是点击到测试帧稳定的
单次样本，不是 p95 或 GPU paint 指标。测试运行器同时检查全量原文仍在证据中。
复现展开 Widget 基线：

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

### 存储容量快照与保留期清理（2026-09-26）

“设置 → 安全与存储”现在按用户打开或手动刷新时读取一次容量快照，不加入轮询。
快照分别报告主数据库、WAL、证据相关表实际分配的 SQLite 页面、freelist 中可供
SQLite 后续写入复用的页面，以及 Runtime 所在卷的可用空间。证据页统计读取
`dbstat` 页面元数据，不解压正文；它不是解压后的正文总量。freelist 页面仍位于
数据库文件内，因此逻辑删除后文件未必缩小，也不据此声称 SSD 安全擦除。

普通快照只借助到期索引统计已经超过保留期的语义证据和 Raw HTTP 信封。完整存档
行数只在用户点击“清空证据存档”后按需读取，避免每次打开设置扫描所有证据索引。
手动清理使用单个事务同时处理两种到期证据及不可达的内容块；未过期证据、运行中
Capture、配置和正文之外的长期元数据不受影响。清空全存档仍经过原有运行中 Capture
屏障和幂等控制。

在 Apple M5 Max、本机临时 SQLite、30,000 个各 1 KiB 的合成证据块上，20 次容量
快照 p50 / p95 为 10.8 / 11.5 ms。2,000 条语义证据的 Go benchmark 为约
1.32 ms/op。两者均为单机合成样本，不是跨设备 SLA；回归上限只用于显式性能测试。
复现：

```sh
VIBERMATE_PERFORMANCE=1 go test ./internal/runtimepersistence \
  -run '^TestStorageStatisticsLargeDatabasePerformance$' -count=1 -v
go test ./internal/runtimepersistence -run '^$' \
  -bench '^BenchmarkStorageStatistics$' -benchtime=20x -count=1
```
