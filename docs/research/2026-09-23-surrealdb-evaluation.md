# SurrealDB 接入可行性评估

日期：2026-09-23。状态：研究建议，尚未采纳为数据库迁移决定。
本次仅核对官方文档、发布源码和现有存储边界；未安装数据库、添加依赖、运行性能对比，
也未读取真实凭据或个人会话。

后续明确的产品目标是把团队对话整理成可审核、可检索的长期知识，同时支持第二种运行时
主存储和外部知识目的地；完整方案见[团队知识与存储计划](../plans/2026-09-23-team-knowledge-and-storage.md)。
本文仍作为 SurrealDB 这一候选引擎的专项技术核对，不代表产品只需要更快的抓包界面。

## 结论

**可以接入，但当前不建议直接替换 SQLite，也不建议只为了改善页面加载就引入第二个数据库。**

先完成现有性能基线与读取链路优化。只有明确需要跨会话全文检索、语义检索或多跳关系查询，
而较简单的实现达不到目标时，才验证 **SQLite 保留事实数据，SurrealDB 作为可关闭、可重建的派生检索层**。
这不是推荐现在就建设双写平台；没有通过验证门槛，就不新增服务和适配器。

需要区分两种“文档”：SurrealDB 的文档模型指带有嵌套字段的记录，不是自动生成可读的
Markdown、知识库或项目说明书。[官方文档模型](https://surrealdb.com/docs/learn/data-models/document/overview)
如果目标是把会话沉淀为知识文档，应先用现有 SQLite 做“选取记录 → 生成草稿 → 人工确认 →
带来源引用导出”，验证价值；换数据库不是这项能力的前提。这条流程是产品建议，尚未实现。

## 1. 核对基线与资料边界

| 对象 | 本次固定的发布版本 | 发布日期 / 源码修订 |
| --- | --- | --- |
| SurrealDB 核心 | [v3.2.4](https://github.com/surrealdb/surrealdb/releases/tag/v3.2.4) | 2026-08-17；`93ab219d69f09d8f999851b0359c80ebe6726102` |
| 网络 Go SDK `surrealdb.go` | [v1.7.0](https://github.com/surrealdb/surrealdb.go/releases/tag/v1.7.0) | 2026-09-07；`4e6b74f157f70c787782e91902bbebc53154b8b4` |
| 内嵌 Go 绑定 `surrealdb.c.go` | [v0.1.0](https://github.com/surrealdb/surrealdb.c.go/releases/tag/v0.1.0) | 2026-03-06；`bada31230021f2dec201cf0cdffff5f9c0e8245a` |

这些是调研时查询到的发布基线，不代表三者已经在 ViberMate 中通过兼容性测试。
官方网站是滚动文档，部分内容已经描述 **3.3.0**；下文明确标注此类差异。
实现时必须锁定核心、SDK、存储引擎及构建依赖，不能把 `main` 或 `latest` 当作固定发布依据。

## 2. 官方能力，以及它没有替我们解决的事

| 能力 | 已核对的依据 | 对 ViberMate 的意义与限制 |
| --- | --- | --- |
| 嵌套记录、Record Link、图关系 | [文档结构](https://surrealdb.com/docs/learn/data-models/document/nested-objects-and-arrays)、[RELATE](https://surrealdb.com/docs/reference/query-language/statements/relate)、[v3.2.4 关系测试](https://github.com/surrealdb/surrealdb/blob/v3.2.4/language-tests/tests/language/statements/relate/with_parameters.surql) | 可表达会话、请求、工具、文件之间的关系；识别关系、消歧和来源追踪仍是我们的工作。 |
| 全文索引、BM25、高亮 | [v3.2.4 全文测试](https://github.com/surrealdb/surrealdb/blob/v3.2.4/language-tests/tests/language/indexes/full_text/fulltext.surql) | 适合搜索历史内容；中文、代码标识符、长日志的分词和排序质量需要真实任务集验证。 |
| 向量近邻检索 | [v3.2.4 HNSW 测试](https://github.com/surrealdb/surrealdb/blob/v3.2.4/language-tests/tests/language/indexes/knn/hnsw_knn.surql)、[向量索引说明](https://surrealdb.com/docs/learn/data-models/vector-search/vector-indexes) | 可以检索相似片段；向量模型、分块、权限过滤、内存预算和重建仍需设计，近似搜索不是准确率保证。 |
| 多语句事务 | [事务语义](https://surrealdb.com/docs/learn/querying/concepts-and-guides/transactions)、[Go 事务 API](https://surrealdb.com/docs/reference/golang/concepts/transactions) | 可原子提交库内变更；不覆盖 SQLite 与 SurrealDB 之间的原子双写，也不自动保持现有业务不变量。 |
| 实时变更通知 | [LIVE SELECT](https://surrealdb.com/docs/reference/query-language/statements/live-select) | 可作为刷新提示；不能据此取消断线补拉、授权检查和数据版本校验。 |

上述源码测试证明发布树包含相关实现和测试，不代表我们已运行这些测试。
本评估没有采用官网吞吐量或低延迟宣传作为本项目的实测结果。

### Go 可以内嵌，但不是直接替换连接串

当前 [Go quickstart](https://surrealdb.com/docs/languages/golang) 把 `mem://` 列为内嵌协议。
但固定版本 `surrealdb.go v1.7.0` 的 `FromEndpointURLString` 对
`memory`、`mem`、`surrealkv` 实际返回 `embedded database not enabled`；该发布的常规入口
使用 HTTP(S) 或 WebSocket。[发布源码](https://github.com/surrealdb/surrealdb.go/blob/v1.7.0/db.go#L96)

真正的官方 Go 内嵌路径是另一个模块 `surrealdb.c.go`，通过 C FFI 和 CGO 链接
`libsurrealdb_c.a`。官方说明要求先构建 C 库并配置链接路径，不能把 Rust 的内嵌能力
直接视为纯 Go 的即插即用能力。[内嵌指南](https://surrealdb.com/docs/reference/golang/embedding)、
[v0.1.0 说明](https://github.com/surrealdb/surrealdb.c.go/blob/v0.1.0/README.md)

该绑定的发布文档说明默认构建包含内存和 SurrealKV 后端，RocksDB 需额外启用构建特性。
其示例构建默认引用上游 `main`；产品构建需要另行固定 C 库和核心修订。
[v0.1.0 存储后端说明](https://github.com/surrealdb/surrealdb.c.go/blob/v0.1.0/docs/rocksdb.md)
**项目推论：** 内嵌会增加 macOS Universal、Linux 多架构、原生工具链和发布验证的工作量，
但尚未测得安装包、常驻内存或构建时间增量。

### Live Query 不等于可靠的事件日志

当前官方文档明确 `LIVE SELECT` 仅支持单节点；通知只来自已提交事务，同一客户端提交顺序有保证，
跨客户端的全局顺序是尽力而为。会话失效、TTL 到期等情况会终止订阅。
文档还指出 **3.3.0 之前的内嵌引擎切换身份时，旧订阅可能继续使用之前身份的权限**。
因此不能把滚动文档中的修复当作 v3.2.4 已具备的安全保证。
[通知与权限语义](https://surrealdb.com/docs/reference/query-language/statements/live-select)

网络 SDK 的 `contrib/rews` 可以重连并恢复会话、重新订阅；官方将其排除在核心 SDK 的兼容性保证之外，
过期认证也需要应用处理。文档承诺的是恢复订阅，不是补发断线期间全部事件。
[可靠连接说明](https://surrealdb.com/docs/reference/golang/concepts/reliable-connections)、
[v1.7.0 实现说明](https://github.com/surrealdb/surrealdb.go/blob/v1.7.0/contrib/rews/doc.go)

**接入要求：** 重连后重新读取权威数据或通过已验证的增量游标补齐；通知只提示“可能变化”。
不能把订阅恢复等同于恰好一次投递，也不能仅凭 Live Query 做证据归档或跨库同步。
若采用旧版内嵌引擎，不复用变更身份的订阅会话；仍需专项越权测试和版本安全复核。

### 事务、迁移、备份不是零成本

官方说明使用快照隔离、提交时检查写冲突，不提供通用可串行化隔离；当前文档中的
`SELECT ... FOR UPDATE` 标为 **3.3.0 起提供**，不能用于证明 v3.2.4 的只读依赖已受到冲突保护。
[事务与隔离说明](https://surrealdb.com/docs/learn/querying/concepts-and-guides/transactions)
Go SDK 的交互式 `Begin/Commit` 要求 WebSocket 和核心 v3+；同一请求中的文本式事务是另一条路径，
不能将此限制误写成“HTTP 完全没有事务”。[v1.7.0 事务实现](https://github.com/surrealdb/surrealdb.go/blob/v1.7.0/transaction.go)

官方提供 SurrealQL 格式的导出与导入；它不是 SQLite 文件迁移工具，也不等于整个应用的备份。
恢复后仍需校验记录、关键查询和应用行为；单个导出不提供任意时间点恢复。
[导出](https://surrealdb.com/docs/reference/cli/surrealdb-cli/commands/export)、
[自托管恢复要求](https://surrealdb.com/docs/manage/self-hosted/backups-and-recovery)
核心大版本升级也有语法和索引变化，例如 2.x → 3.x 需要专门的兼容导出与诊断。
[官方迁移指南](https://surrealdb.com/docs/build/migrating/from-old-surrealdb-versions/2x-to-3x)

**项目要求：** 若直接替换，必须重新验证唯一约束、策略发布 CAS、提交结果不明后的恢复、
删除与并发写入、迁移失败回滚。不能因两端都支持事务，就认定这些语义自动等价。

## 3. 当前项目的约束

- SQLite 使用外键、WAL、`synchronous(NORMAL)` 和单连接；现有优化基线尚未证明引擎本身是瓶颈。
  [连接设置](../../internal/runtimepersistence/connector.go)、[连接池](../../internal/runtimepersistence/store.go)
- 策略发布依赖事务、修订检查和提交结果协调；Capture 删除在事务内清理证据引用图。
  这不是改一个驱动名称即可迁移的普通 CRUD。
  [策略持久化](../../internal/runtimepersistence/environment_repository.go)、
  [删除实现](../../internal/runtimepersistence/capture_deletion.go)
- 当前数据迁移会停止 Runtime，复制数据库、WAL、CA 和配置，检查哈希、SQLite 完整性及外键；
  Keychain 凭据不随目录转移。增加独立引擎后必须重新定义这套完整迁移和恢复流程。
  [数据位置与性能说明](../storage-and-read-performance.md)
- 同步身份补全、轮询、长内容解压和 Flutter 排版是不同阶段的成本；换数据库不会自动改善这些阶段。
  当前分段测试既不是完整页面延迟，也不是 SQLite 与 SurrealDB 的对照实验。
  [已测量与待测量边界](../storage-and-read-performance.md)

## 4. 三条路线比较

下表是基于上述事实和本仓库约束的工程判断，不是上游性能承诺。

| 路线 | 收益 | 新增成本与风险 | 当前建议 |
| --- | --- | --- | --- |
| 保留 SQLite，优化查询与展示 | 延续本机离线、单目录、已有事务与恢复；直接针对已测成本 | 高级检索需要按实际需求补充；不能预先宣称无容量上限 | **当前主线**。基础筛选、检索和文档导出先复用已有数据 |
| SurrealDB 直接替换事实库 | 一个引擎提供文档、关系、向量与实时查询 | 重写仓储查询和 schema；重做事务不变量、迁移、备份、发布和升级验证 | **暂不推进**。仅在真实负载证明必要且通过全部迁移门槛后重评 |
| SQLite + 可选派生检索层 | 不移动权威证据，可独立验证全文/语义/关系检索 | 两份敏感数据、索引滞后、删除一致性、额外服务或原生依赖 | **有明确检索需求时再验证**。必须可关闭、可清空重建，故障不阻断 Capture |

如果选择第三条路线，第一步只做离线合成语料对比，不先建设通用数据库插件系统、分布式集群、
自动双写或新的 Agent 编排。若 SQLite 的现有能力或小范围扩展满足目标，就停在更简单的方案。

## 5. 对四种部署方式的影响

服务端单节点持久化可以使用 RocksDB 或 SurrealKV；当前部署文档将 SurrealKV 标为 beta，
保守的服务端持久化建议 RocksDB。这里是上游当前建议，不是本项目已经验证的组合。
[文件存储](https://surrealdb.com/docs/running/file-backed)、
[部署与引擎选择](https://surrealdb.com/docs/manage/self-hosted/deployment-models)

| 场景 | 最小可能接入方式 | 必须增加的产品责任 |
| --- | --- | --- |
| 个人 App | 本机 sidecar 服务，或单独验证 CGO 内嵌 | 启停、升级、崩溃恢复、体积与内存；不能要求普通用户自行管理数据库 |
| 个人原生 Web | Runtime 连接本机服务 | 安装与版本检查、数据目录、认证、端口冲突；保留不启用时的完整功能 |
| 个人容器 Web | 独立容器、内部网络、持久化卷 | 固定镜像、健康检查、启动顺序、卷备份；无需让用户先有域名或公开数据库端口 |
| 团队原生 / 容器 Web | Runtime 连接受控服务 | 最小数据库权限、TLS、容量、备份、审计与租户隔离；不能把数据库管理凭据交给浏览器 |

这些是部署设计推论。若要做最小试验，优先隔离的单节点服务加网络 Go SDK，避免先改变 App
构建链；试验完成不表示默认产品必须携带该服务。多节点和托管云不是此次验证前提。

## 6. 权限、隐私与删除一致性

SurrealDB 支持表和字段权限，但这些规则适用于 record users；system users 由角色约束，
**不会自动受这些行级规则限制**。因此不能用一个高权限数据库连接，就宣称已经实现用户隔离。
[官方权限说明](https://surrealdb.com/docs/learn/security/authorization/permissions-and-row-level-security)

以下为拟议接入必须满足的边界，不是当前已实现能力：

1. **Runtime 仍是入口。** 浏览器、Agent 不直接访问派生库；检索候选必须回到权威数据校验用户、
   Capture 归属、保留期限和删除状态，校验通过后才返回片段、标题与正文。
2. **默认不复制全部正文。** 先用合成内容和必要元数据验证；正文索引另需明确范围与用户选择。
   不索引 OAuth token、认证 Header、Keychain 内容；正文和路径也可能敏感，不能把向量当作匿名数据。
3. **外部向量化单独授权。** 不因开启检索就把会话发送到第三方模型；提供本地路径或明确说明
   外发数据、服务商及费用，不能擅自借用用户的上游登录凭据。
4. **过期和删除立即影响可见性。** 即使派生库离线或未删完，权威库删除后也不能继续展示旧片段。
   重建必须遵守删除屏障，不能从旧快照或备份把内容重新放回可见索引。
5. **生产增量同步再设计可靠进度。** 需要时使用持久化检查点或同事务待同步记录、幂等写入与
   删除确认；不能依赖内存通知做唯一依据。初期离线验证无需提前建设这套机制。
6. **诚实展示故障。** 标明未建立索引、索引时间、重建中或不可用；派生库故障不伪装成“没有记录”，
   不拖住对话转发、OAuth 刷新或证据落库。备份与导出也必须纳入保留和删除策略。

## 7. 许可：核心与 SDK 必须分开判断

固定核心 **v3.2.4** 的许可证是 **Business Source License 1.1**，许可证自己声明不是
Open Source license。附加使用许可限制作为 Database Service 使用；其定义涉及向第三方提供
能够创建、管理或控制 schema / table 的数据库功能，不能简化成“只禁止卖托管数据库”。
该版本写明转换日期 `2030-01-01`、转换许可 Apache-2.0，正文还约定与首次公开分发四周年取较早日期。
[v3.2.4 LICENSE](https://github.com/surrealdb/surrealdb/blob/v3.2.4/LICENSE)

网络 Go SDK **v1.7.0** 则是 Apache-2.0。这不使随产品链接或分发的核心自动变为 Apache-2.0。
[网络 SDK 许可证](https://github.com/surrealdb/surrealdb.go/blob/v1.7.0/LICENSE)
本次检查 `surrealdb.c.go v0.1.0` 发布树未找到独立的 `LICENSE` / `COPYING` 文件；不能按其他 SDK 的
许可猜测它的授权，若选择嵌入应向维护者确认，并核对 C 库及完整原生依赖链。
[内嵌绑定发布树](https://github.com/surrealdb/surrealdb.c.go/tree/v0.1.0)

**工程判断，不是法律意见：** ViberMate 是否开源，不会自动豁免依赖许可。固定表结构的产品内部
存储与向用户开放任意数据库管理能力是不同使用形态；未来提供托管服务、任意 SurrealQL / schema
控制，或者分发嵌入核心之前，应按实际形态复核条款并在不确定时取得授权方确认。

## 8. 最小验证与决策门槛

本节是后续可执行的验证范围，**本次没有运行 POC**。先确定用户任务和通过阈值，再比较引擎，
不以“接上数据库成功”作为产品能力完成。

| 验证项 | 最小实验 | 继续推进的门槛 |
| --- | --- | --- |
| 功能价值 | 合成中英混合会话、工具结果和相似故障；人工标注查询相关性 | 全文或语义检索确实解决基础筛选难以解决的任务；若只是导出文档，先验证 SQLite 路径 |
| 性能与成本 | 同机同语料对比冷/热查询、并发 Capture 写入、索引建立与删除 | 记录 p50/p95/p99、CPU、峰值 RSS、磁盘增量和查询计划；端到端可感知收益超过新增成本，无 Capture 写入回退 |
| 正确性 | 更新、重复投递、超时后重试、事务提交响应丢失 | 幂等、无丢失、无重复事实；提交结果不明能明确恢复，不能盲目重放 |
| 权限与删除 | 跨用户猜 ID、改归属、撤销权限、删除时索引离线、旧快照重建 | 零未授权结果；过期/删除后标题与片段也不可见；最终删除可确认 |
| 可靠性 | 杀进程、断网、重连、磁盘满、库重启、重新建索引 | Capture 与事实库不被派生层阻塞；所有状态能解释、能恢复 |
| 发布与恢复 | 锁定版本；目标架构构建、升级、备份恢复、失败回退 | 每个支持的平台可重复验证；包含许可清单、数据恢复证据与明确运维负责人 |

先以项目现有 [性能测试与边界](../storage-and-read-performance.md) 为基线，再补真实页面计时。
阈值应在实验前结合支持设备和语料规模确定，不能测试完后为候选方案倒填标准。

### 尚未确定，不能据此承诺的事项

- SurrealDB 在本项目上的性能、中文检索质量、向量召回率、内存和安装包成本。
- 网络 SDK、内嵌绑定、C 库和核心的具体发布组合，以及相关安全修复是否已进入拟采用版本。
- 内嵌绑定授权、最终产品形态的核心许可适配性。
- 是否存在足够高频的高级检索需求，足以承担双份数据和删除一致性成本。

**建议顺序：现有链路测量与优化 → 验证检索 / 文档生产价值 → 必要时做隔离的派生层实验 →
满足门槛后决定是否接入。当前不暂停已有性能工作来换库。**
