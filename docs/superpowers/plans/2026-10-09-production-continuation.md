# ViberMate 当前 Goal 与恢复入口

核对日期：2026-10-12。此文件是当前状态的单一快照，不是追加式流水账。后续更新对应行或当前任务，不再追加相互覆盖的“下一步”。详细过程、首失败和源码哈希留在各任务SDD账本。

## 最终目标与本次校准

持续分阶段交付用户实际可用的ViberMate，最终完成全部已批准功能、生产验收与GitHub/Homebrew发布；功能正确性优先，性能优化单独衡量。阶段交付不等于最终Goal完成。

完整十项范围与执行约束见Goal引用文件：`/Users/null/.codex/attachments/5333dda7-121c-46a7-9d24-77ee624ae66c/pasted-text-1.txt`。本次保留十项原文，修正恢复入口中的过时待办和阶段边界，没有删减范围、重开已交付工作或创建新Goal。get_goal实际返回active。

已发现的执行偏移是：旧文档同时保留“额度待审查”“终端未实现”“合流尚未发生”“captured计划尚未实施”等互相覆盖的现在时段落，混合目标、历史与待办。本次将其归档并改为下面唯一快照。原文及目标修订前全文位于：

`.superpowers/sdd/2026-10-11-stable-production-integration/goal-realignment-20261012.0pdErB/`

归档只作历史证据，不作为当前调度入口。原有更早快照、日志、审查和未提交源码均保留。

## 当前检查点

- 开发目录：`/Users/null/Code/github/vibe-agi/vibermate/.worktrees/production-readiness`，分支`fix/production-readiness-20261003`。
- 最近源码单元提交`b429d1a0811ece54ec9252af6fb30951b2d5e3d4`关闭B3 Task1；B1及B2此前均已提交，不重开。所有本阶段新增功能仍未正式发布。
- B3 Task1已真实修复原生恢复探测误拒绝，以及已批准未发送调用取消时未同步关闭批准的问题。初审Important测试证据缺口已通过fix1补齐并复审关闭；最终Runtime正常5项/race25及未变Store/native/prober证据保留。测试临时目录生命周期和撤销合法竞态预期也已纠正，未扩大生产权限或超时。
- 唯一下一动作：执行B3 Task2原生关联回复/恢复及发送后故障验收。计划：`docs/superpowers/plans/2026-10-12-native-egress-closure.md`；账本：`.superpowers/sdd/2026-10-12-native-egress-closure/progress.md`，实际启动BASE、所有者和进度以账本与工具句柄为准。
- B4已准备待执行。B5仓库规则检查仍有实际失败：保留快照被误扫、两处私有内核连接与Unicode测试源码规则，已有只读诊断，未修复/未豁免。候选完整审查、当前源码验收、签名公证、安装和GitHub/Homebrew仍未完成。
- 原始失败、缺失的历史逐次映射和非阻断建议保留给最终审查；不把局部通过称为版本就绪。用户App、账号、信任、数据、原工作区和无关缓存未动；恢复只核对变化。

## 已完成基线：不得重开

| 交付/单元 | 真实状态 | 主要依据 |
| --- | --- | --- |
| 4096长会话修复 | 已发布v0.1.24(26) | 发布源码dbf9745；稳定线发布证据 |
| 默认HTTP(S)端口、Web同标签页续登、原账号重新OAuth | 已发布v0.1.25(27)，GitHub/Homebrew交付完成 | tag/source4e89f628；稳定线2026-10-10-login-fixes-release计划及public-delivery报告 |
| 多Runtime CLI登录 | 已有实现，按规范化server隔离，保留回归 | LoginStore；不是待新建功能 |
| 稳定修复合入生产线（阶段A） | 已合流、验证、独立复审关闭 | b5d5dbe，父提交d50e0d8/999d7b0；stable-production-integration账本 |
| VLESS/REALITY+Vision、自有SOCKS TCP/UDP、域名排除、配置内CA、界面/内核/Host入口 | 对应实现、单元验收和独立审查已关闭，尚未随新正式版本发布 | 2026-10-09-vless-udp-functional及TLS/packaging/Host子计划；2026-10-11-socks-user-delivery Tasks1–4 |
| 5分钟共享额度刷新与credit | 已实现、验证、独立复审、提交，未发布 | 1ab5a05；2026-10-11-account-quota-refresh，含首次读取失败后的定时器恢复fix1 |
| 交互终端重连诊断 | 已实现、真实PTY验证、独立审查、提交，未发布 | 73ab8f9；2026-10-11-interactive-reconnect-diagnostics |
| 独立账号额度/历史/重置出口及暂停恢复 | 已实现、真实选路/race验证、独立审查、提交，未发布 | 39ca501；2026-10-12-owned-account-egress-recovery |
| 手动OAuth刷新、账号模型列表 | 已补齐真实验收及独立审查、提交；无需改生产行为 | 1e3bcbb/86fc9ab；account-aux Tasks1/2 |
| 首次OAuth及Runtime关闭回收 | 真实RED确认遗漏清理注册，修复/验证/独立审查/提交完成，未发布 | 714b8f9；account-aux Task3；账号状态与真实自有回调监听关闭，无skip |
| 会话内账号额度/历史读取及暂停恢复 | 已实现、真实权限/选路/故障及race验证、独立审查和修正复审关闭、提交，未发布 | 87c4fd3；captured-original Task1；两个非阻断测试建议留给最终审查 |
| 原账号模型请求暂停/故障出口 | 已补齐实际Runtime与物理网络验收，normal/race/受影响回归及独立审查通过、提交；生产逻辑未改，未发布 | 055990b；captured-original Task2；实际原请求ID与持久Exchange关联，证据边界明确 |
| 原生工具Hold恢复与发送前取消收尾 | 已修复、真实Runtime/Store验证、独立审查和fix1复审关闭、提交，未发布 | b429d1a0；native-egress-closure Task1；恢复与未消耗批准原子关闭，保留首失败及历史证据边界 |
| 托管模型令牌轮换后代理断开及下一请求独立审计预期 | 已完成实际故障验收、测试修正、独立审查及提交，生产逻辑未改，未发布 | 31bb94de；managed-egress-tail Task1，保留原观察器失败及分源码覆盖边界 |
| 原始/原目标转换转发与流式出口 | 测试实现、保留验证、独立补充复审及提交已完成，未发布 | 6ed836b；captured-original Task3与task-3-fix1-review；真实父级/请求/审计关联缺口已关闭，T3-M1/M2非阻断建议保留 |

v0.1.25的已核验发布记录：2026-10-10T10:48:48Z，GitHub release408868448，源码4e89f628ba8eff3284e731b48475996908fa1c90，macOS/Linux六项资产；Homebrew PR20合并30a170d7。DMG SHA256为`0836cfd85c0f875dc6f698cc8d3c8edd9b8a114b2adff34fe65131e23f9409be`。这些是已保存的发布证据，不冒称本轮查询了远端；冻结新版本前再次核对。

稳定工作树`.worktrees/long-session-hotfix`的999d7b0产品树与该发布产品树已验证相同。主工作区未提交修改、旧发布工作区、dirty tap及正在运行的App/Codex均不动。

## 阶段B唯一剩余顺序

本阶段固定交付：已接通的VLESS/UDP/域名排除/配置内CA与分发、全局默认出口的现有请求族、额度/credit及终端体验。正常实现沿用已批准子计划；没有新需求，不重做整体架构规划。

| 顺序 | 剩余单元与完成条件 | 执行依据 |
| --- | --- | --- |
| B1 已关闭 | Task1/2/3均已验证、独立审查并提交；保留证据与非阻断建议，不重做实现或整套验证 | 2026-10-12-captured-original-egress计划及当前SDD |
| B2 已关闭 | 令牌轮换成功后、首个模型发送前真实代理断开及现存P3独立预期断言已验收、审查、提交；生产逻辑未改，不重开 | 31bb94de；2026-10-12-managed-egress-tail计划及SDD |
| B3 当前 | Task1已提交关闭；执行Task2实际FP/FD/未设置出口、关联回复/恢复、独立新调用与发送后不重复执行的故障验收。保留真实审批/once/receipt，不实现阶段C新续流机制 | 2026-10-12-native-egress-closure计划及当前SDD；全局差距报告native行及phase-b-separation-audit |
| B4 | 盲转发/普通HTTP的已列明缺口：真实默认直连、默认读取失败不外发、代理失效不回退，以及实际暂停/编辑/取消恢复；单元计划已准备并完成主控预检，尚未执行 | 2026-10-12-blind-cleartext-egress-closure计划；全局差距报告blind/cleartext行 |
| B5 发布收尾 | 冻结候选；执行实际变更相关的Go/Flutter/Chrome、race、安全、规则和CI等适用验证及全局单元/完整候选独立审查；核验实际共享数据库升级/恢复与安装；完成同源构建、签名公证、GitHub/Homebrew和公开下载验证 | 2026-10-05-global-default-egress Task6与生产/发布计划；冻结时填写真实版本/源码/产物 |

全局细项证据：`.superpowers/sdd/2026-10-05-global-default-egress/phase-b-remaining-audit.md`。该报告原表为历史pin；本快照及其顶部已关闭记录优先于旧表的E/I，不能重新派发owned/manual/models/first-login。metadata/prices、固定更新请求及既有UI覆盖继续复用；冻结时核验实际变化的消费者，不重建已完成界面。

B5实际仓库规则失败的源码诊断见`.superpowers/sdd/2026-10-09-production-continuation/b5-repositorycheck-diagnosis-b4249020.md`，root已全文读取；仅准备了狭窄修正方案，未执行且检查仍失败。后续应保留历史快照，只排除准确证据目录；私有连接例外限定真实方法/接收者/调用形状，不能豁免整个文件或模块；Unicode转义须保持实际测试值。

B5现有发布命令/输入/产物/审批节点的只读准备已完成，见`.superpowers/sdd/2026-10-09-production-continuation/b5-release-preflight-b4249020.md`。root已全文读取；这是固定源码上的执行清单，不是候选验收、环境可用性检查或发布证明，不重复派发同一准备任务。B3/B4关闭后按真实候选源码核对执行；没有扩大当前版本门槛。

P3和现有两步Resume体验在相关独立审查中明确结论；只有确实影响候选功能/安全/数据/安装的问题阻挡发布，不凭未证实假设把性能或未来能力升级为门槛。非法配置在真实校验边界被拒绝是正确结果，不放宽产品权限来满足测试构造。

### 阶段B退出条件与真实耦合

B1–B4关闭后转B5，不自动增加新产品功能；候选范围冻结后新增需求进入下一版。新增阻断项须明确实际证据、用户影响与最小关闭条件；性能改善和非阻断测试建议单列，不混入本轮发布门槛。只有该候选的适用验证、独立审查、实际安装和公开交付都完成，阶段B才算结束。

已有原生工具可在profile授权后运行，原生Store恢复也在Runtime启动时执行。因此现有工具权限/出口与共享数据迁移属于当前候选验收，不能用“默认关闭”豁免。新的代理主动续流实现和完整46项客户端验收留在阶段C；发布说明不宣称这些已经完整支持。源码审计依据：`.superpowers/sdd/2026-10-11-stable-production-integration/phase-b-separation-audit.md`，它不是独立最终生产认证。

保留未覆盖边界：当前本地旧候选是dirty0.1.23+25/ad-hoc，不是新正式安装包；跨平台编译不等于Windows等平台的原生UDP行为；测试字体截图不等于原生字体安装资格；已有模块测试和本地内核证明不替代最终安装包验收。

## 阶段C：最终Goal剩余范围，不丢失

- R1–R7、完整阅读器/用户文字/图片预览、工具参数与结果、SSE/记录/资源回收的所有未闭环项。
- 原生MCP完整续流/恢复/一次执行，按profile授权、原客户端凭据、真实权限/审批；不重发可能已执行的POST。
- Codex0.145.0/0.159.2/0.160.0、Claude2.1.220及全部46项客户端矩阵，真实取消、两profile隔离、异常恢复、Paseo/ACP约定回归；手机实机未验部分明确标注。
- 客户端CA适用平台原生验收，以及`/Users/null/claude/workspace`内约定devcontainer中包装启动与直接Codex的登录/连接/证书对照。
- 全部数据的备份/升级/恢复故障验证；至少60分钟真实两profile持续运行，另用可控时钟验证10天会话/隐藏窗口/唤醒，不能混称。
- 最终同一源码上的全量适用Go/race/vet/native-secrets/跨平台/依赖/Flutter/Chrome/Paseo/规则/CI、完整分支独立审查及正式签名/公证/安装/GitHub/Homebrew交付。

完整定义以Goal十项原文、2026-10-03-production-readiness设计/计划、2026-10-05-global-default-egress设计/计划及各已批准子计划为准。阶段B后持续推进，不要求用户重新同意已有目标；只有全部完成才能将整个Goal标为complete。

## 恢复与验证纪律

1. 实时核对get_goal、Git和现有agent/测试句柄，再读当前任务账本；不因中断、观察超时或旧“running”文字重开工作。
2. 共享目录保持一名源码实现者，独立只读审查/准备可并行，重型测试避免资源争用；root管理Git，精确暂存，保留无关修改与生成缓存。
3. 分开记录已复现缺陷、缺证据、性能改善与未来功能。已有行为只缺证据就补验收；不制造产品缺陷来凑RED，不用任意3秒阈值判断业务失败。
4. 正确性/安全/数据/安装的实际缺陷必须解决；已通过且源码/覆盖未变的证据复用。冻结时验证真实候选，不把局部、跳过或仅编译说成全部通过。
5. 每项只维护一份当前状态及下一动作；阶段报告区分已实现/已验证/已审查/已发布，详细历史保留在SDD，不再把旧下一步追加到本入口。
