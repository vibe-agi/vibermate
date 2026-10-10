# ViberMate 当前 Goal 与恢复入口

核对日期：2026-10-11。此文件校准执行状态和顺序，不缩减原生产 Goal，也不抹掉历史失败证据。旧计划中的“当前”“下一步”“正在执行”只代表其当时状态；恢复时先读本文件和所选任务的最新账本，再核对实际 Git/agent/任务句柄。

## 最终目标（不变，补齐已批准的新增范围）

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
| VLESS 核心与自有 SOCKS 模块 | 已实现且对应缺陷已修复/审查 | production-readiness 的 VLESS functional Task1/2；不能等同于 Runtime/Host/UI 已连通 |
| VLESS 配置与 Runtime TCP | Task3A及启动/关闭修复已实现、验证、审查；其后Task3B也已关闭 | task-3a-profile-routing-report.md和shutdown-rereview/3hash；不重复原实现或核心Proof |
| VLESS Runtime UDP/监听/基础界面 | Task3B及关闭修复已复核关闭，Task4A界面实现/审查通过（2个Minor和字体证据限制保留） | vless-configuration-ui的report/review；后续TLS界面也已完成，现缺安装包/Host/用户连接交付，不重复TLS开发 |
| VLESS TLS／配置内自有CA核心 | 已实现、验证、独立审查通过，无Critical/Important；一个测试fixture Minor保留 | vless-tls-core/task-1-report.md与task-1-review.md。原生System信任成功证明仍待隔离runner |
| TLS私有配置／owner控制 | 已实现、18项定向测试通过、独立审查Approved无发现；包含真实磁盘重开及owner/member权限 | tls-profile-control/task-1-report.md与task-1-review.md，11个文件hash已核对，保持冻结 |
| TLS／CA编辑界面 | 已实现、验证、独立审查Approved无发现，正式关闭该任务 | tls-editor/task-1-report.md与task-1-review.md；13份hash已核对。114VM/86Chrome及最终受影响定向验证、分析/Web构建通过，390px实际查看；字体证据限制保留 |
| 内核生产与资格 | 原生补丁内核资格及可重复生产工具均已独立审查关闭；生产工具29项测试与5目标编译/静态证明已完成 | producer fix1 rereview无剩余发现；后续打包/Host也已完成对应单元，但SOCKS用户入口尚未交付 |
| 内核材料、真实构建和打包接入 | packaging Task1/2均已独立审查关闭；实际本地arm64候选构建/验证成功。Task2首审两项Important已修正：正式签名漏内核、签名前漏内核哈希核对；定向22项通过，复审I1/I2均关闭 | vless-kernel-packaging/task-2-report.md及task-2-fix1-review.md；本地候选仍是dirty0.1.23+25、ad-hoc/Xcode27，不是新发布或正式Universal资格。首失败、2项原有opt-in skip及工具链诊断保留 |
| Host启动接线 | 共享内核自动发现、Desktop认证回环SOCKS启动已实现、验证、独立审查通过 | vless-host-bootstrap Task1关闭；1个非阻断输出噪声Minor保留。实际地址/ManualCapture用户交付和完整打包网络验证仍是后续任务 |
| SOCKS用户入口 | 2026-10-11-socks-user-delivery Task1后端与Task2 Flutter/CLI均已实现、验证、独立审查关闭；不要重复实现 | Task2一次fix关闭scoped IPv6严格校验遗漏；17VM/12Chrome/12render/4既有manual/Go3包及fix5parser、clean analyze证据保留。已看390px实际可读画面，替代测试字体不冒充native字体。下一任务Task3真实控制接口→凭证→自有SOCKS→REALITY/Vision TCP/UDP |
| R1–R7、阅读器、MCP底层、全局出口 | 保留已有提交和审查；全量功能/客户端闭环未完成 | 2026-10-03主计划及已采用子计划；2026-10-05全局出口账本 |
| CA平台/devcontainer、完整MCP/46项客户端、持续运行与最终同源发布 | 尚未全部完成 | 原Goal范围继续有效，见下方完成条件 |

远端与公开字节核对：GitHub为v0.1.25，发布时间2026-10-10T10:48:48Z；Homebrew main30a170d7的cask为0.1.25，DMG SHA256 `0836cfd85c0f875dc6f698cc8d3c8edd9b8a114b2adff34fe65131e23f9409be`。实际Brew解析、CI安装/签名/Gatekeeper/卸载通过，原用户dirtytap与运行App未改。下一版本冻结前再查远端。

## 唯一恢复顺序

1. **稳定线登录补全版本已交付**：v0.1.25(27) GitHub/Homebrew完成，不重新构建或重复发布。稳定修复尚需有意识地合入保留WIP的主生产线；这是后续合流，不是再次发0.1.25。
2. **VLESS/UDP可用版本**：Task3A、Task3B及各自关闭修复，Task4A基础界面、TLS/core、TLS私有配置/owner控制、TLS编辑界面、补丁内核生产、打包Task1/2与Host/bootstrap均已审查关闭；不重复这些实现。当前按`2026-10-11-socks-user-delivery.md`接通Host/control/grant、Flutter/CLI和实际用户入口网络验证。使用REALITY+Vision；域名匹配须同时绕过代理与DoH，使用Runtime系统DNS+Direct；支持`*.github.com`且不扩大私有目标访问权限。保留配置不可变和不偷偷直连的边界。源码/二进制扫描保留两条未导入包的模块提示，不把部分资格等同完整生产或全平台发布。
3. **主生产线合流及功能闭环**：有意识地把已发布hotfix/稳定线新修复合入production-readiness，保护现有WIP；继续全局默认出口剩余请求族、原生MCP真实工具/恢复/一次执行、阅读器与CA/devcontainer闭环。TLS上游自有CA与REALITY身份校验严格区分，不修改系统信任、不把跳过CA映射为绕过REALITY。
4. **最终生产资格与发布**：在最终同源代码上完成原Goal所有适用平台/客户端/数据恢复/持续运行/安全与发布验收。分阶段版本可以更早交付，但不能据此删掉最终目标里的剩余项。

## 已发布修复合流的只读预检（2026-10-11）

本地稳定HEAD999d7b0产品树`89be12a87e5c5958e0686987e60803e00867c0d9`与已发布0.1.25的已验证产品树一致；公开合并SHA4e89f628当前尚未存在于本地对象库，首次直接Git比较因此报bad object，此后改用已有的等产品树稳定HEAD，没有擅自fetch/merge或修改运行状态。生产HEAD f79c35d与稳定线共同祖先仍为c61f8c6；`git cherry`显示92项不等价补丁、0项等价（含文档/测试，不代表92个未做功能），稳定线自共同祖先改227个文件。必须合入的不仅是0.1.25登录修复，也包括0.1.24长会话实现。

稳定修改与当前tracked WIP重叠19个路径，包括CI/签名工作流、第三方声明、desktopcontrol/desktophost/runtimecontrol、productruntime builders/options/runtime、macOS发布策略/测试，以及Flutter API/models/copy/account/workbench/preview与Desktop manifest测试。列表是当前时点的只读范围证据，不是冲突已解决或测试已通过。不得直接在dirty生产工作区merge/reset覆盖这些工作；功能单元冻结后先保留精确快照，再制定并执行有意识的合流与相称回归。该工作不需要重发0.1.24/0.1.25，也不重做它们已完成的签名公证。

## 原 Goal 必须保留的完成条件

- R1–R7以及用户文字/图片预览、工具参数与返回补查、额度布局、SSE保活/异常断流、记录与资源回收的正确性；不裁剪完整历史、不隐藏数据丢失。
- MCP按profile单独授权、默认关闭，使用客户端原登录账号，实际审批/发送凭证/幂等与恢复边界生效；不自动重发可能已经执行的工具POST。
- 出口优先级：账号显式 > 流量策略显式 > 全局默认 > 内置直连。显式直连是覆盖，代理失败不是改走直连的理由；覆盖模型、额度/历史、重置券、OAuth、目录、原始/原生/盲转发、探测与更新查询。
- Codex0.145.0/0.159.2/0.160.0、Claude2.1.220及原46项矩阵、取消/两profile隔离/异常恢复、Paseo/ACP约定回归；未验证手机实机能力如实标明。
- 客户端CA下载/平台安装验证与约定devcontainer路径检查；不改用户当前账号、系统信任或正在运行的任务。
- 安全备份/迁移/恢复故障验证，保留所有数据；至少60分钟真实两profile受控持续运行，另用可控时钟验证10天边界/隐藏窗口续期/唤醒，二者不能互相冒充。
- 最终适用Go/race/vet/native-secrets/跨平台/依赖/Flutter/Chrome/Paseo/规则/CI、完整分支审查、同源签名公证与安装、GitHub/Homebrew公开交付和独立核验。

## 防止中断后绕回起点

- 当前稳定工作区：`/Users/null/Code/github/vibe-agi/vibermate/.worktrees/long-session-hotfix`，本地HEAD999d7b0，远端已合并候选4e89f628，二者产品树相同。当前开发工作区：`/Users/null/Code/github/vibe-agi/vibermate/.worktrees/production-readiness`，SOCKS用户入口Task1 BASE e9099de（计划/恢复文档提交）与保留的已审查产品WIP；后续HEAD以实际Git为准。20d483c/1cbb36d只是更早UI阶段的历史定位，不是当前待恢复任务。两条工作线不可混作同一最终候选，不因仅文档提交而重新执行已完成实现。
- 从最新任务报告/源码哈希/真实agent状态恢复；完成任务不得因旧账本仍写“running”而重新派发。活跃句柄归原所有者，观察等待超时不构成重启理由。
- 同时只有一个源码实现者；独立只读审查/方案核查可以并行。本机重量级构建/全套测试串行排队，避免同时跑Go全量与Flutter全量造成资源竞争。
- 测试和审查只覆盖实际变更与风险；已通过且源码未变的单元不因恢复会话而重做。性能目标与功能正确性分开；实际安全/数据丢失/协议错误仍需解决，不用任意快速完成阈值冒充业务成功标准。
- 每次任务冻结，更新“已实现/已验证/已审查/已发布”四种状态、下一任务和剩余边界。旧段落留作历史，不继续把过去的待办当最新事实。
- Goal状态：用户已在本轮将卡片恢复为active（此前paused检查点为历史）。从当前检查点持续推进，不再等待恢复授权；现有工具不能代替用户恢复，勿把本次用户操作记成代理改了状态。
