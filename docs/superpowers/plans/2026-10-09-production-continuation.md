# ViberMate 当前 Goal 与恢复入口

核对日期：2026-10-12。此文件校准执行状态和顺序，不缩减原生产 Goal，也不抹掉历史失败证据。旧计划中的“当前”“下一步”“正在执行”只代表其当时状态；恢复时先读本文件和所选任务的最新账本，再核对实际 Git/agent/任务句柄。

本次用户明确要求重新审视并编辑Goal：已更新当前Goal引用的`/Users/null/.codex/attachments/5333dda7-121c-46a7-9d24-77ee624ae66c/pasted-text-1.txt`，保留原九项完整范围，新增已确认的5分钟额度刷新/统一credit展示/交互终端体验要求，并明确下面A/B/C交付顺序。原正文和本文件修订前副本保存在合流SDD的`goal-before-revision.ui8umB/`。这不是重新创建Goal或把未完成目标标为complete。

2026-10-12复核：产品方向保持，近期执行队列固定为额度独立审查/提交 → 交互终端重连诊断修复 → 既有全局出口差距报告中的具体单元 → 阶段B冻结与发布。额度实施者已交还源码；主控核对七文件哈希、task-only diff哈希及修订后日志，Flutter87/Chrome31通过、analysis无问题，独立审查尚未完成，未发布。终端计划已准备而未实现。再次复核前副本位于合流SDD的`goal-audit-20261012.fShVj4/`，原十项最终范围未删减。

最新覆盖上段额度状态：独立审查发现的I1（首次账号列表失败/附属读取失败后定时器未启动）已用真实RED3→GREEN3修正，VM59/Chrome15及实际页面VM1/Chrome1回归、analysis通过，fix1独立复审Approved无新发现。该任务实现/验证/审查已完成，接续终端计划，不再重复额度实现。未发布，真实上游/最终安装资格仍按阶段完成。

额度任务已提交`1ab5a05c4adfa13f8079a0756b04c71d39e7e932`，包括七个已复审源码/测试文件及其计划/恢复状态。下一实施入口为`2026-10-11-interactive-reconnect-diagnostics.md`；精确启动BASE/agent以该计划SDD首行状态为准。当前Goal最终十项范围不变，未发布的新功能不能由此宣称已交付。

最新终端状态：`2026-10-11-interactive-reconnect-diagnostics.md`已完成实现、验证及独立审查Approved，无发现。真实PTY复现输入提示旁的重连文字后，只替换两处临时诊断写入并新增私有终端判断；文件/管道/缓冲日志、输入、进程与撤销不变。Darwin定向/完整包/race通过，Linux/Windows amd64包与CLI编译通过；未执行真实Codex opt-in或外平台原生验收。下一任务为已批准全局出口Task6的账号读取/重置真实选路与暂停恢复单元，详见`2026-10-12-owned-account-egress-recovery.md`，不重开已关闭额度/终端实现。

终端任务已提交`73ab8f995046a7d059d8223b2f5f7b8940d7cac3`。账号操作单元沿用真实Runtime/Reader/Manager、限定TLS/SOCKS测试网和严格原始TLS探测器；先记录实际排队恢复RED，再仅修正已有owner读取/重置的探测构造。captured账户读取仍是父计划的独立剩余项，不把本单元的origin-only规则泛化到它，也不宣称全部出口已完成。

## 最终目标（不变，补齐已批准的新增范围）

当前覆盖前述历史“下一步”：账号额度/历史/重置出口单元已实现、验证并独立审查Approved，无发现。四个Go文件只增加closed-owned探测构造及真实测试；54个producer+34个Runtime行为格、count5 race和相关既有消费者通过。真实ReadOwned/RedeemOwned保持AP/AD/G/B选路、暂停恢复、一个租约的详情/兑换、取消/失败无直连回退和无重复兑换。尚未发布；下一单元是`2026-10-12-account-aux-egress.md` Task1手动OAuth刷新，再按Task2/3模型列表和首次登录推进，详情/精确BASE以各SDD首部为准。

按已批准的 Superpowers 设计，分阶段交付可实际使用的 ViberMate：先完成用户受阻的修复及功能贯通，再推进性能优化和最终生产资格验证；保留全部账号、配置、权限与历史，完成适用的独立审查、签名、公证、安装、GitHub Release 与 Homebrew 交付。只有全部已批准范围实际完成后才标记整个 Goal complete；单个 hotfix 发布不等于整个 Goal 完成。

## 真实基线

| 工作 | 当前状态 | 恢复依据 |
| --- | --- | --- |
| 4096 长会话独立修复 | **已发布** v0.1.24(26)，不能重新排为待发布 | GitHub latest 查询、公开 Homebrew cask 和已保存的发布报告；发布源码 dbf9745b5391723103967889939a5ab4c7fbb7bc |
| 默认 HTTP(S) 端口 | **已发布 v0.1.25(27)** | stable worktree f90d972；省略端口与80/443显式形式共享登录/信任身份 |
| Web 同标签页刷新续登 | **已发布 v0.1.25(27)** | stable worktree b29fff8；Chrome40、相关VM33、构建/分析和真实后端+实际浏览器重载探针通过 |
| 上述修复整体验证 | 最终稳定候选999d7b0的PR与push各14/14通过；合并4e89f628后的main CI也14/14通过 | 38001191279、38001188502、38005724047；旧失败及退出诊断证据保留，不再重排已完成CI |
| 0.1.25稳定修复发布 | **GitHub与Homebrew均已交付**；tag/source4e89f628，六项资产，签名/公证/隔离安装/公开下载核验完成 | GitHub release408868448发布2026-10-10T10:48:48Z；tapPR20合并30a170d7，PR/main CI全通过；stable public-delivery报告保留首失败与边界。旧3s退出根因仍未知，不冒称修复 |
| 原账号重新 OAuth | **已发布 v0.1.25(27)**，后端dc71b24、界面930f4b1及刷新反馈修正79fb19c | stable worktree 的2026-10-09-provider-account-reauthorization计划/账本；保留原ID/设置/关联/历史，不误换上游身份 |
| 多 Runtime CLI 登录 | **已有功能，不重新开发** | LoginStore 按规范化 scheme+host+port 保存多条；A/B不互相覆盖，同server保留一份登录 |
| VLESS 核心与自有 SOCKS 模块 | 已实现且对应缺陷已修复/审查；后续Runtime/Host/UI入口也已有闭环证据 | production-readiness 的 VLESS functional Task1/2及后续SOCKS用户入口Tasks1–4；不等同正式版本已发布 |
| VLESS 配置与 Runtime TCP | Task3A及启动/关闭修复已实现、验证、审查；其后Task3B也已关闭 | task-3a-profile-routing-report.md和shutdown-rereview/3hash；不重复原实现或核心Proof |
| VLESS Runtime UDP/监听/基础界面 | Task3B及关闭修复已复核关闭，Task4A界面实现/审查通过（2个Minor和字体证据限制保留） | vless-configuration-ui的report/review；后续TLS界面、打包接线、Host和用户连接单元均已完成，不重复开发；新正式版本未发布 |
| VLESS TLS／配置内自有CA核心 | 已实现、验证、独立审查通过，无Critical/Important；一个测试fixture Minor保留 | vless-tls-core/task-1-report.md与task-1-review.md。原生System信任成功证明仍待隔离runner |
| TLS私有配置／owner控制 | 已实现、18项定向测试通过、独立审查Approved无发现；包含真实磁盘重开及owner/member权限 | tls-profile-control/task-1-report.md与task-1-review.md，11个文件hash已核对，保持冻结 |
| TLS／CA编辑界面 | 已实现、验证、独立审查Approved无发现，正式关闭该任务 | tls-editor/task-1-report.md与task-1-review.md；13份hash已核对。114VM/86Chrome及最终受影响定向验证、分析/Web构建通过，390px实际查看；字体证据限制保留 |
| 内核生产与资格 | 原生补丁内核资格及可重复生产工具均已独立审查关闭；生产工具29项测试与5目标编译/静态证明已完成 | producer fix1 rereview无剩余发现；后续打包/Host/SOCKS用户入口也已完成对应单元，新正式版本尚未交付 |
| 内核材料、真实构建和打包接入 | packaging Task1/2均已独立审查关闭；实际本地arm64候选构建/验证成功。Task2首审两项Important已修正：正式签名漏内核、签名前漏内核哈希核对；定向22项通过，复审I1/I2均关闭 | vless-kernel-packaging/task-2-report.md及task-2-fix1-review.md；本地候选仍是dirty0.1.23+25、ad-hoc/Xcode27，不是新发布或正式Universal资格。首失败、2项原有opt-in skip及工具链诊断保留 |
| Host启动接线 | 共享内核自动发现、Desktop认证回环SOCKS启动已实现、验证、独立审查通过 | vless-host-bootstrap Task1关闭；1个非阻断输出噪声Minor保留。实际地址/ManualCapture交付已由后续Tasks1–4关闭；最终正式安装包验证仍需完成 |
| SOCKS用户入口 | 2026-10-11-socks-user-delivery Tasks1/2/3均已实现、验证、独立审查关闭；不要重复实现 | 真实owner控制→签发凭证→自有SOCKS→打包REALITY/Vision TCP/3UDP、DNS/直连、坏节点不回退、撤销/关闭回收通过；Task3修正后的native9.36s/race10.51s及9反例通过，复审关闭。macOSarm64/currentHost+既有内核证明，不冒充最终安装包或其它平台 |
| SOCKS Windows编译 | Task4已实现、验证、独立审查Approved无发现，实际编译缺陷关闭 | Windows/Linux amd64两包测试编译+CLI/daemon共8项通过；实际socket完整/截断与race、macOS真实用户入口回归通过。使用现有平台常量、不改权限或包处理；Windows原生UDP尚未验证 |
| R1–R7、阅读器、MCP底层、全局出口 | 保留已有提交和审查；全量功能/客户端闭环未完成 | 2026-10-03主计划及已采用子计划；2026-10-05全局出口账本 |
| CA平台/devcontainer、完整MCP/46项客户端、持续运行与最终同源发布 | 尚未全部完成 | 原Goal范围继续有效，见下方完成条件 |

远端与公开字节核对：GitHub为v0.1.25，发布时间2026-10-10T10:48:48Z；Homebrew main30a170d7的cask为0.1.25，DMG SHA256 `0836cfd85c0f875dc6f698cc8d3c8edd9b8a114b2adff34fe65131e23f9409be`。实际Brew解析、CI安装/签名/Gatekeeper/卸载通过，原用户dirtytap与运行App未改。下一版本冻结前再查远端。

## 唯一恢复顺序

1. **稳定线登录补全版本已交付且已合流**：v0.1.25(27) GitHub/Homebrew完成，不重新构建或重复发布；稳定修复已通过下述合流提交进入主生产开发线，不再重做合流。
2. **VLESS/UDP功能已接通**：协议/Runtime/UI/TLS、补丁内核生产、打包与Host/bootstrap及`2026-10-11-socks-user-delivery.md` Tasks1–4均已独立审查关闭；不重复实现。真实控制/凭证/自有SOCKS/REALITY+Vision TCP/UDP与坏节点/撤销/回收已证明；源码/二进制扫描两条未导入模块提示、测试字体和其它平台原生资格限制仍保留。尚未构建/发布包含这些当前源码的新正式安装包。
3. **阶段A已完成：合流检查点b5d5dbe**：按`2026-10-11-stable-production-integration.md`完成两线合流，I1/I2真实RED→GREEN和独立复审关闭；完整loopbackproxy包已补验。两父提交为d50e0d8/999d7b0，不再重开合流或已关闭单元。这不是新版本发布或最终生产资格。
4. **阶段B：下一阶段功能版**：交付已接通的VLESS/REALITY+Vision、自有SOCKS UDP、域名排除/配置内CA与全局默认出口剩余真实请求族闭环；补齐5分钟额度自动刷新、现有缓存统一展示credit与不污染输入的重连提示，保留已发布登录修复。冻结本阶段清单，完成其实际代码范围对应的正确性/安全/数据/安装/审查、签名公证、GitHub/Homebrew交付，不让无关MCP后续功能和性能调优自动阻挡。未完成能力须证明安全分离并如实标注，不能只说默认关闭就跳过真实可达缺陷。
5. **阶段C：完整生产Goal**：继续原生MCP真实工具/恢复/一次执行、完整客户端矩阵、阅读器与CA/devcontainer、数据恢复/持续运行/10天边界等全部剩余范围，并在最终同源代码上完成全量适用验证、独立整体审查和正式发布。阶段B可以更早交付，但不替代或删减阶段C。

## 已发布修复合流的只读预检（2026-10-11）

**最新覆盖历史状态**：合流提交为`b5d5dbe191a4e62096e57b1b3eba599374e57c01`，父提交为175项源码检查点`d50e0d8539f8777c46fc04618cad712510fd5fbf`和稳定`999d7b0876b1c9c570e1d2192f9cc7e5a97805ee`，树`01aeb4b0f3ff1777a2bffeb0ce61c7c3b4fbad3f`。24个冲突及8个直接消费者修正已完成，原72份三方源码保留。组合Go编译、相关15包、Flutter185/Chrome52及真实SOCKS入口通过；审查I1/I2经HTTP1/HTTP2 Raw作用域12的实际RED、五项GREEN、完整loopbackproxy包和独立复审关闭。先前模型容量中断未导致丢失、重做或回滚已有解析。精确日志/源码/审查及资格边界见合流SDD；以下“没有merge/产品提交/仍待修复”段落仅保留历史。正式版本仍为0.1.25，不把本地合流说成已发版。

本地稳定HEAD999d7b0产品树`89be12a87e5c5958e0686987e60803e00867c0d9`与已发布0.1.25的已验证产品树一致；公开合并SHA4e89f628当前尚未存在于本地对象库，首次直接Git比较因此报bad object，此后改用已有的等产品树稳定HEAD，没有擅自fetch/merge或修改运行状态。生产HEAD f79c35d与稳定线共同祖先仍为c61f8c6；`git cherry`显示92项不等价补丁、0项等价（含文档/测试，不代表92个未做功能），稳定线自共同祖先改227个文件。必须合入的不仅是0.1.25登录修复，也包括0.1.24长会话实现。

后续一次精确公开源码获取：`git fetch --no-tags https://github.com/vibe-agi/vibermate.git 4e89f628ba8eff3284e731b48475996908fa1c90`返回128/CONNECT tunnel407，未重试、未改网络代理或凭证。生产HEAD35185fd和本地稳定产品树保持不变；已有稳定等产品树源仍可用于安全合流准备，不因此停止正在进行的功能验收。正式远端发布前仍须核对所选源与实际公开版本。

稳定修改与当前tracked WIP重叠19个路径，包括CI/签名工作流、第三方声明、desktopcontrol/desktophost/runtimecontrol、productruntime builders/options/runtime、macOS发布策略/测试，以及Flutter API/models/copy/account/workbench/preview与Desktop manifest测试。列表是当前时点的只读范围证据，不是冲突已解决或测试已通过。不得直接在dirty生产工作区merge/reset覆盖这些工作；功能单元冻结后先保留精确快照，再制定并执行有意识的合流与相称回归。该工作不需要重发0.1.24/0.1.25，也不重做它们已完成的签名公证。

35185fd冻结时点的工作区快照已保存到`/private/tmp/vibermate-integration-snapshot.lNiS4P`（约1.3MiB、目录私有）：HEAD、NUL状态与未跟踪清单、tracked二进制patch、79个未跟踪文件tar与逐文件SHA、产物SHA。79项源码校验通过，patch/清单在捕获后再次与当前Git输出一致，全部6项产物哈希通过。它保留生成的`tool/vless-kernel/__pycache__/build_kernel.cpython-314.pyc`以防误丢；该缓存不是待发布源码，后续按精确源文件清单提交，不可git add全部。快照不构成数据库备份/迁移或发布验收；后续代码如再变化，合流前必须使用新的冻结输入。当前没有merge、reset、stash或产品提交。

## 原 Goal 必须保留的完成条件

- R1–R7以及用户文字/图片预览、工具参数与返回补查、额度布局、SSE保活/异常断流、记录与资源回收的正确性；不裁剪完整历史、不隐藏数据丢失。
- MCP按profile单独授权、默认关闭，使用客户端原登录账号，实际审批/发送凭证/幂等与恢复边界生效；不自动重发可能已经执行的工具POST。
- 出口优先级：账号显式 > 流量策略显式 > 全局默认 > 内置直连。显式直连是覆盖，代理失败不是改走直连的理由；覆盖模型、额度/历史、重置券、OAuth、目录、原始/原生/盲转发、探测与更新查询。
- Codex0.145.0/0.159.2/0.160.0、Claude2.1.220及原46项矩阵、取消/两profile隔离/异常恢复、Paseo/ACP约定回归；未验证手机实机能力如实标明。
- 客户端CA下载/平台安装验证与约定devcontainer路径检查；不改用户当前账号、系统信任或正在运行的任务。
- 安全备份/迁移/恢复故障验证，保留所有数据；至少60分钟真实两profile受控持续运行，另用可控时钟验证10天边界/隐藏窗口续期/唤醒，二者不能互相冒充。
- 最终适用Go/race/vet/native-secrets/跨平台/依赖/Flutter/Chrome/Paseo/规则/CI、完整分支审查、同源签名公证与安装、GitHub/Homebrew公开交付和独立核验。

## 新增的用户可见闭环（2026-10-11）

以下来自实际使用截图，纳入 Goal 的后续功能正确性范围，不以临时提示或让用户删除数据规避：

- **Runtime 断线恢复**：连接短暂中断时，状态提示必须是可恢复的非阻塞状态；自动重试成功后清除或更新提示，不能在输入栏持续留下误导性错误，也不能吞掉正在输入的内容、重复提交或伪造已完成的请求。首个断线、重连、重连失败和用户可继续输入的行为都要有回归证据。
- **原账号 OAuth 续授权**：该路径已随 v0.1.25 发布，后续保留并验证其可发现性和完整行为，不重复开发。refresh token 失效、过期或无法刷新时，在原账号卡片提供明确的“重新授权”路径；重新 OAuth 必须保留账号 ID、名称、额度关联、流量策略及历史。禁止要求用户先删除账号再导入，也禁止静默新建替代账号。需覆盖 refresh 失败、重新授权成功、取消及旧凭证失效后的状态。
- **额度缓存单一来源**：上游账号额度缓存是账号卡片、流量策略和额度展示的统一来源，额度和 credit 余额都应一致展示。账号刷新成功后，所有消费者读取同一份已校验缓存；缓存仍新鲜时不得为同一账号重复请求。用户已确认默认每5分钟后台刷新一次，手动刷新可立即触发，各页面共用结果并合并重复请求；提供过期/刷新中/失败状态。刷新必须遵循账号配置的网络出口和权限，不把缓存缺失伪装成零额度。需要验证跨页面一致性、刷新去重、失败保留上次有效值及账号切换隔离。

只读诊断已确认：CLI心跳协程直接写入与前台Codex共用的stderr，没有终端显示协调；重连成功追加另一行不会消除旧行。额度两处已有相同controller缓存及祖先订阅，不能依据截图声称另有一份缓存或订阅缺陷；真正已定位的是环境小卡片未渲染credit，以及已有分钟计时器仅重绘倒计时、没有定期额度请求。实际上游断线/refresh拒绝原因尚未证明。诊断依据保存在当前合流SDD的quota-oauth-runtime-diagnosis.md；初版误把历史发布文档当现状的报告已明确更正，不作为结论。

后续设计保留的边界：定期查询额度不等于开启OAuth凭证自动轮换，不能擅自改变账号现有AutomaticRefresh选择。终端修复只调整前台交互时诊断的呈现位置/时机，不放宽租约校验、重试或撤销后的停止规则；重定向日志仍需可诊断，不能靠操纵Codex输入或发送重绘按键掩盖问题。

这三项属于后续功能闭环，不改变当前稳定合流的已冻结源码边界；合流完成后建立对应实施任务，并在最终同源候选上通过用户路径验证。

合流完成后的当前实施入口：`2026-10-11-account-quota-refresh.md`，按已确认的5分钟方案补齐同一controller内的定时/手动合并请求与小卡片credit。该计划不重建后端缓存、不更改OAuth开关或网络权限；终端诊断与全局请求族余项接续，不作为重开已完成合流的理由。

2026-10-12并行只读全局出口核查已完成，报告位于全局计划SDD的`phase-b-remaining-audit.md`。主控完整阅读并独立核对了已知源码契约缺口：owned/captured账号读取和重置操作仍生成带语义/运行时plan、账号targetRef的Auxiliary探测目标，而实际Runtime选择的严格原始TLS prober要求origin-only/plan0，Hold恢复因此存在不匹配；本轮尚未执行新的实际网络RED，不能声称已复现或修复。后续在当前额度与终端任务检查点后，用既有真实Store/Manager/Reader及受限TLS/SOCKS fixture处理这一个明确单元。既有托管六层/token、公共目录/价格、更新与UI历史通过证据不重做；其它矩阵缺证据的格子和最终同源检查按报告逐项收敛，不把“缺证据”写成已确认产品失败。

## 防止中断后绕回起点

- 当前稳定工作区：`/Users/null/Code/github/vibe-agi/vibermate/.worktrees/long-session-hotfix`，本地HEAD999d7b0，远端已合并候选4e89f628，二者产品树相同。当前开发工作区：`/Users/null/Code/github/vibe-agi/vibermate/.worktrees/production-readiness`，本次核对HEAD为d9c0941，已包含合流b5d5dbe；七文件额度任务尚未提交，主控文档修改和生成缓存分别保留。后续HEAD以实际Git为准。e9099de/20d483c/1cbb36d只是更早任务的历史定位，不是当前待恢复任务。两条工作线不可混作同一最终候选，不因仅文档提交而重新执行已完成实现。
- 从最新任务报告/源码哈希/真实agent状态恢复；完成任务不得因旧账本仍写“running”而重新派发。活跃句柄归原所有者，观察等待超时不构成重启理由。
- 同时只有一个源码实现者；独立只读审查/方案核查可以并行。本机重量级构建/全套测试串行排队，避免同时跑Go全量与Flutter全量造成资源竞争。
- 测试和审查只覆盖实际变更与风险；已通过且源码未变的单元不因恢复会话而重做。性能目标与功能正确性分开；实际安全/数据丢失/协议错误仍需解决，不用任意快速完成阈值冒充业务成功标准。
- 每次任务冻结，更新“已实现/已验证/已审查/已发布”四种状态、下一任务和剩余边界。旧段落留作历史，不继续把过去的待办当最新事实。
- Goal状态必须实时读取，不能照抄本文件。2026-10-11用户已将此前blocked的卡片恢复active，最新get_goal确认active，目标仍未完成；此前blocked/paused记录属于历史。现有update_goal接口不能把卡片恢复active，此次是用户操作，不能冒称由代理恢复。不能因为模型临时容量或等待超时重复启动仍存活任务。
