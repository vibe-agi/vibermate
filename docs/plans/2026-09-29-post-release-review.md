# v0.1.16 独立 review 复核与修复

状态：当前修复集已冻结，交付 `fix/post-016-review` 供下一轮独立 review；整个审查矩阵仍未完成，不是发布候选。审查基线 `7eb2c196a9383790dc9f02800c6d4c3ee3ec2ac1`，已经发布为 v0.1.16；不改写标签和资产。本轮为新增复核，不能以此前绿灯否定新发现，也不能将外部 review 当作全部已证实。

Plane：official-plane 项目 `779ab6f7-24dd-49ca-9b01-d8be8ef20a51`，父项 **VIBERMATE-48**，子项 **49–56**；L9 另在 53 下以低优先级 **VIBERMATE-57** 跟踪。前轮交付与数据转换记录保留，不反复转换或操作用户当前 App。

## 本轮审查交接

按用户要求先收口、提交并推送审查分支，再由 Claude Code 独立 review；本轮不合并 main、不发 Release、不更新 Homebrew、不安装或启动 App。分支对齐 `origin/main` 的 `4fc9624`（仅合入此前发布记录，与原工作分支产品源码相同）。后续比较范围为 `origin/main...origin/fix/post-016-review`。

优先复查四条边界：Server 公共目标限制是否贯穿 HTTP/CONNECT、DNS 与实际拨号；同方言保真是否仍严格保护客户端工具审批；正文增量回收与保留期即时生效是否保住共享引用和删除权限；OAuth 待保存轮换、账号租约及私有 CA 信任是否在并发/取消/故障时保持正确。报表与 Profile 交互则从公开 Widget/真实 Web 操作验证，不以截图代替保存和读回。

尚未关闭、发布前需要继续裁决的重点如下；完整逐项状态见下一节，不能把本次提交当作全部修复：

- **Server（49）**：S2 远程启动环境变量权限、S3 新成员默认授权、S4 盲隧道出口选择仍待复核。
- **协议（51）**：P6–P11 的活性、Responses 输入/输出/终态、账号切换私有状态，以及 A1/A4 未建模工具和审批屏障仍待复核。未承诺新增 Chat Completions；Anthropic 仅完成矩阵中明确列出的兼容范围。
- **持久化（52）**：D3/D4 已消除已复现的保留期维护长事务和 Activity 失败连带跳过用量，但全库写锁下的终态补记/覆盖缺口仍未解决；D5/D7/D8 的跨环境规模预算、Capture 查询和 WAL 行为也未关闭。
- **生命周期（53）**：L4 证书过期冷启动/长期不重启、L7 子代理索引待复核；L5 暂保留 App 退出撤销 Capture 的既有授权语义，不能靠自动重建伪装恢复。L1 待保存轮换只保证进程内恢复，物理 SecretStore 不可用期间进程丢失仍可能需重新登录。
- **明确不扩范围**：L9 合盖换网已按用户要求在 57 低优先级跟踪；UTC 切日保持既有明确口径；纯改名、拆包、连接池/JS 运行时/重复解析优化先测量，不为交付 review 分支增加重构或历史兼容链。

当前 schema 已变化：评审测试只用临时新库/合成凭据。已有 0.1.15/0.1.16 数据只能通过 [显式离线转换](../../tool/convert-v1/README.md) 生成独立目标，不可直接用审查分支打开用户在用的数据目录。没有运行时双读或自动迁移。

### 冻结前最终回归

本机最终日志位于 `/private/tmp/vibermate-review-closeout.8rDh0m/`；测试代码随提交保留，临时日志和浏览器截图不作为仓库资产。此轮最终检查与下文历史 red/green 证据分开记录：

| 检查 | 结果 |
| --- | --- |
| `go test ./... -count=1` | 108 个有测试的包通过；包含非 race 的规模、31 秒响应头、真实合成代理和离线转换回归 |
| `go test -race -short ./... -count=1` | 全量通过；规模计时与条件性真实客户端检查不混入 race 预算 |
| `go vet ./...`、格式、模块 tidy diff、结构、workflow/action pins | 通过 |
| 发布工具测试 | 115 通过、2 条既有条件跳过 |
| `make check-release-build` | 本机原生 SecretStore 构建/vet、Windows amd64 与 Linux amd64 交叉编译通过；不是签名/公证/安装验收 |
| `go generate ./...` | 前后 diff SHA-256 一致，无生成物漂移 |
| Flutter analyzer、全量 Widget/unit | 无分析问题；681 通过、16 条既有条件跳过 |
| `flutter build web --release` | 通过，包含 Wasm dry run；不覆盖原生 App 的签名/公证 |
| 安装的 Codex 隔离 HTTP 重试对照 | 通过（21.30s）：直连与 Launcher 都在六次合成 502 后恢复，同一提示词完成且无无效配置警告；仅覆盖 HTTP fallback，不冒称真实合盖根因已修复 |

此前同一组 UI 产品代码已完成隔离生产 Web 的刷新、真实 409、下钻分页、Profile 发布读回及 390/1440px 深浅主题验收（见下方 U3/U5/U6/U7/R1 证据）。本轮不会把过去的 Web 验收冒称为新签名 App 验收，也不以本机测试代替推送后的远端 CI。

## 方法与顺序

用户已确认四类 test-first 入口：真实 HTTP/CONNECT 代理到合成上游、账号/登录控制接口、持久库公开读写接口、App/Web 用户操作。逐个纵向 red → green，不一次堆积所有假想测试，不用私有函数断言替代产品行为。只使用隔离数据、合成凭据和 loopback 上游。

先处理 Server 出站权限、托管多模态兼容、全局证据回收、OpenSSL 握手；再处理恢复/状态错误和其余性能与契约项。同方言容错不能绕过账户、路由、工具审批和变换的安全要求；多条 SQLite 写连接不等于并行写。保持当前 v1，不擅自增加历史格式兼容、启动迁移链或新统计数据库。

## 完整核对矩阵

“待复核”不是否定，也不是已经采纳；每项须有代码/协议依据及足够范围的复现，记录采纳、部分成立、明确限制或不采纳的原因。

| ID | Review 主张 | 跟踪 | 当前证据/状态 |
| --- | --- | --- | --- |
| T1 | Original Destination 无法编译 OpenSSL 扩展 22 | 50 | 已用本机 Node 25.8.1 / OpenSSL 3.6.4 默认 ClientHello 复现并修复；不支持扩展只触发有证据的严格标准 TLS 模板，不重复拨号或绕过证书/ALPN。相关五包通过，最终全代理复验仍须执行 |
| P1 | Anthropic 图片/PDF/嵌套工具结果被拒 | 51 | 已通过真实代理复现四种输入均本地 422；同方言历史以有界 opaque block 保留，工具结果附件与可读文本分离。两轮历史回放均送达合成上游且内容保真；跨方言仍明确拒绝不能表达的附件，不伪装成文本 |
| P2 | 带 `type` 的工具被拒 | 51 | 部分完成：真实代理先复现 custom / web_search 声明 422、服务端结果 JSON 502 / SSE error；现支持 custom 和有名称的原生定义，服务端搜索结果/引用/增量参数及后续历史保真，公开正文/用量读回通过。原生声明不伪造 schema、不授予执行许可；custom / bash / web_search 返回客户端 tool_use 的 JSON/SSE 六种严格策略反例均拦截。无名称 browser/computer toolset 及未来未知输出的安全投影另并入 A1，未宣称全协议覆盖 |
| P3 | 新 stop reason、工具被 max_tokens 截断失败并丢用量 | 51 | 已复现并修复 refusal/pause_turn/model_context_window_exceeded 及带完整工具参数的 max_tokens：8 种 JSON/SSE 回归保留原始响应且公开用量查询仍有 4 input/2 output；真正截断、无法解析的工具参数须与 A1/A4 的审批边界继续核对，不将其自动批准 |
| P4 | 新嵌套字段被严格解码拒绝 | 51 | 请求侧已复现并修复：同方言投影允许消息/内容/工具/配置的未知字段，原 JSON 保真；已知字段类型和语义仍验证，重复 JSON 名仍拒绝。真实代理嵌套扩展回归通过；响应侧与新枚举值另按 P3/P8 继续核对 |
| P5 | Anthropic 原始错误、retry-after/request-id 丢失 | 51 | HTTP 400/429 与 SSE error 均已先红再绿；同方言保留原始错误及顶层 request_id/扩展字段，Retry-After/Retry-After-Ms/Request-Id/X-Should-Retry 通过受限头列表传递，Cookie/Authorization/未绑定 turn state 不泄漏；原文仅暂存用于客户端交付，不进入诊断 |
| P6 | ping 不延长流空闲期限 | 51 | 待核对活性与语义进展的不同预算，不能推断真实上游时长 |
| P7 | Responses 字符串 input、引用、工具及对象 tool_choice 不支持 | 51 | 待复核官方契约和实际路径 |
| P8 | 新 Responses 输出中止整流 | 51 | 待复核 |
| P9 | incomplete 的合法原因被改写 | 51 | 待复核 |
| P10 | 不支持 Chat Completions | 51 | 待核对既有支持承诺，不因其他协议缺陷自动增加跨方言能力 |
| P11 | Account Switch 继续发送私有 encrypted reasoning | 51 | 待核对 Upstream State Scope 与 CONTEXT 的可移植延续承诺 |
| P12 | Endpoint base path 为 /v1 时请求重复拼接 | 55 | 真实 Desktop 代理→托管 Anthropic 路由已复现 /v1/v1/messages；共享路径解析只消除明确重复的 v1 段，五种基路径回归通过，保留 ChatGPT 专用路径与 Original Destination 原路径 |
| P13 | 本轮新增：Anthropic error 与先前文本同批读取时丢失安全前缀 | 51 | 真实代理先收到仅 error、丢掉前面的 message_start/text；Feed 现同时返回已解码的安全前缀和失败，工具审批屏障后的字节仍扣留。完整事件序列保真回归通过 |
| S1 | Server 默认成员可经透明代理访问回环/内网/metadata | 49 | 已通过真实 Server 登录/Capture/HTTP/CONNECT 复现并修复：公共目标约束传至实际拨号、解析结果全量校验并固定 IP；去除透明策略 bypass。Owner 显式目标可放行。六包通过，待全量/race/远端 Web 门禁 |
| S2 | Owner 可向远程客户端注入 LD_PRELOAD/NODE_OPTIONS | 49 | 待核对本机同意与 Launch Environment 信任边界 |
| S3 | 新成员默认全部 Environment 与显式授权冲突 | 49 | ADR 0018 明确授权集合，待控制接口复现 |
| S4 | 盲隧道忽略出口策略并直连 | 49 | 待复核冻结出口选择和真实拨号 |
| D1 | 少量过期正文使每次写入触发全保留量 GC | 52 | 已复现并修复：10 万保留+单条过期旧实现 500ms 超时回滚，增量引用回收约 0.3–0.6ms；录制与维护解耦，仍拒读过期正文。持久库全包及 race、离线转换 0.1.15/0.1.16 回归通过 |
| D2 | raw 每批写入同样触发全局 GC | 52 | 已复现并修复：10 万保留+单条过期旧实现 500ms 超时回滚，新实现约 0.2–0.4ms；AppendBatch 不再承担清理，启动回收分批。正文/原始报文共享与删除回归通过 |
| D3 | 审计与大写事务竞争导致 Runtime 停机 | 52 | D6 分批清理后的 20 万条真实持久库竞争验证通过：并发用量写入最慢 35ms，核心审计 Append+Complete 最慢 107ms（预算 500ms），重开库与续清不丢记录；这只覆盖后台保留期维护，其他长事务/全库写锁超时仍待复核，不移除核心审计 |
| D4 | Activity/usage 终态超时后少算且覆盖提示不足 | 52 | 部分修复：真实代理已先复现终态 Activity 写失败连带跳过 usage；现在独立记录用量并提前冻结观察时间，Activity/身份失败不再抑制用量，usage 失败仍保留 Activity。三种故障各重复 10 次、重开库、Go 全量/vet/相关 race 通过。现有 recording warning 有效，故“完全静默”不准确；全库竞争时的补记、报表覆盖缺口仍未解决 |
| D5 | 百万用量聚合可能超过查询预算 | 52 | D6 后本机百万观察/100003 种 token 组合：冷汇总 2.92s、75MiB Go 分配，复用快照 606ms、分组页 1.77s，精确计价通过；不是对容器 11s 的反证，跨环境 8s 查询预算与持续增量聚合仍待处理 |
| D6 | 保存用量策略无条件重写全部行 | 52 | 已先红后绿：不变/停采/延长不触碰历史，缩短原子发布有界保留限制、查询立即执行，复用每批 1000 条维护物化到期时间。失败/取消/重启及多次缩短后延长不恢复旧数据；20 万条旧全表 UPDATE 超过 500ms，新保存约 0.04–0.17ms，清理并发新用量写入最慢约 40ms。当前 v1 新结构仅显式离线转换，无运行时双读 |
| D7 | Capture 列表聚合与五秒轮询成本 | 52 | 待测覆盖索引、查询范围及无变化刷新 |
| D8 | WAL 高水位增长且不截断 | 52 | 待核对 checkpoint/reuse/长读者，不能把文件大小当未提交事务 |
| D9 | 本轮新增：共享内容追加重复检查全部历史引用 | 52 | 10 万相同历史引用下连续写 100 条，先因 transcript base 全组排序失败；改为候选前缀索引 EXISTS 后，再定位无变化主键 UPDATE 的 FK 扇出检查；改更新相等非键字段后约 22ms 全部完成，旧实现 500ms 内只能写 3/35 条 |
| D10 | 本轮新增：raw 到期后、物理清理前仍可揭示正文 | 52 | 真实持久库+公开 Reader 已复现并修复；列表与 ReadPayload 统一按读取时钟拒绝过期内容，延迟维护、包回归及针对性 race 通过 |
| L1 | OAuth 旋转后保存失败丢失唯一新凭据 | 53 | 已复现保存失败后重放旧 refresh token 导致 reconnect_required；现保留尚未提交的轮换结果，恢复时先重试 SecretStore CAS，自动刷新关闭也先完成保存。公开账号入口覆盖状态、拒绝旧 epoch、32 并发恢复、Owner 替换及轮换途中替换、退出重试；存储故障超过 access token 有效期时，先保存后按授权续期。10 种情形各重复 100 次、三包 race、最终 Go 全量/vet 通过。只保证进程内恢复，不绕开 SecretStore，存储不可用期间进程丢失仍可能需重新登录 |
| L2 | 手动刷新期间新请求错误返回不可重试 401 | 53 | 公开账号租约入口+真实 OAuth 管理器+合成 token 端点先复现立即 ErrOperationInProgress；共享租约入口现等待该账号的有界手动轮换，支持独立取消和关闭唤醒。自动刷新开/关、页面取消、上游暂时 503、其他账号并发均通过；未声称解决 L1 的轮换保存失败 |
| L3 | Web 用户查询取消/暂时失败永久吊销会话 | 53 | 已复现并修复：取消、超时、临时 DB 错误原先返回 401 且成对吊销；现返回 503/Retry-After、恢复后原会话可继续。serveradmin/servercontrol/serverhost 全包通过；待最终 Web 验收 |
| L4 | 私有 CA 叶证书自动续签使 CLI pin 永久失效 | 53 | 已用真实 TLS 登录先复现同一 CA 续签后 identity changed；新连接固定指定 Server 的 CA，服务端携带有效颁发链，叶证书更换可继续登录。旧叶指纹不自动弱化，新增核对 CA SHA-256 后的显式无凭据握手转换。八种异常证书在登录/relay/显式信任入口均拒绝，失败保持原信任；全 Go、六包 race、vet 通过。证书只在 Server 启动时续签，错过窗口后的冷启动与长期不重启边界另需复核，不能声称全生命周期自动续签已经覆盖 |
| L5 | App 重启后启动器不重新发现新端口 | 53 | 源码确认控制与代理均为临时端口，Launcher 只读取一次；App 正常退出又明确撤销所有 Capture，所以只重读控制端口不能恢复原 Agent。已询问用户保留“退出即撤销并提示重启”还是修改为跨 App 重启延续授权/稳定代理入口，待选择；合盖/暂时断网后的监督心跳可恢复，不代表模型请求自动续传，后者另见 L9；未擅自重建 Capture |
| L6 | 非流式 30 秒响应头预算及取消误分类 | 50 | 真实 Desktop 代理→合成上游先复现计算 31s 的请求在 30s 返回 502/exchange_canceled。模型响应头预算现采用既有 5min 响应进展预算，拨号 15s/TLS 10s 不变；HTTP/1、HTTP/2、明文托管及模型 Original Destination 共用该预算。取消分类以请求上下文为依据，不再将上游 DeadlineExceeded 当客户端取消，流终态也不因此静默省略。31s 完整代理回归、真实短 HTTP 头超时/客户端主动取消/客户端截止、相关五包与 race、全量 Go/vet 通过；非模型 original-origin 旁路的预算未扩大 |
| L7 | Codex 子代理冲突终止整轮索引且静默丢错误 | 53 | 待索引公开入口复现 |
| L8 | 激活账号因其他 Route 的集合变化返回泛化 422 | 53 | R1 已阻止全局新增账号扩散到冻结集合；本轮多 Route 控制接口进一步复现旧账号丢失凭据后无法切到健康备用账号。激活现仅检查目标账号可用性，不重做其他冻结路由的凭据健康检查；显式集合、Endpoint 关联与编译权限校验保留。成功只推进所属链修订；切回缺凭据账号仍返回明确 422 且不修改已发布配置。相关四包、race、全量 Go/vet 通过 |
| L9 | 合盖换网后模型请求 502，Agent 存活但未自动恢复 | 57（低） | 用户确认同一会话手动继续成功；现场已过去多轮，按用户要求降为低优先级，不追索旧诊断、不阻塞当前修复和发布。Codex 0.158.0 隔离对照中连续六次同形 502 后两种启动均自动恢复；已清除启动器失效配置，但未宣称睡眠根因已修复。后续用故障注入核对“底层断连转成 HTTP/SSE 错误是否改变恢复路径”，不把假设当结论 |
| U1 | 账号比较遗漏设置修订/刷新/出口 | 54 | 已通过另一端改设置、后台轮询、本端显示并再保存的公开 Widget 流程先红后绿；共享账号比较补齐三个字段，不添加独立刷新路径 |
| U2 | 额度倒计时无本地时钟更新 | 54 | 先复现 1 分钟后显示不变；额度组件每分钟仅本地重绘，后台不重绘，回到前台用当前时钟立即校正。12 小时跳时、到期仍保留原用量而标待刷新、卸载取消计时器回归通过 |
| U3 | 实时流量使明细回第一页 | 54 | Widget 先红再绿；非首页/下钻保留当前阅读快照，更新入口在同层回首页。真实隔离 Server + Chrome Web 已验证第二页期间新增请求，后台轮询只提示更新、不改变页码/行，主动更新保留 Profile 筛选 |
| U4 | 每日按 UTC 而非本地时区 | 54 | 待核对明确口径与需求，不静默改统计日期 |
| U5 | 0.1.16 实测：刷新卸载表格导致页面跳动 | 54 | 保留表格与双向滚动控制器，固定进度/状态栏；Widget 与真实 Chrome Web 在 HTTP 查询延迟 500ms 时均验证刷新前、中、后行位置及尺寸一致、旧表格不消失 |
| U6 | 0.1.16 实测：下钻遇到正常更新报错 | 54 | 409 自动续取一致的小计/分页快照，最多续取两次；持续变动保留旧视图且不报红色错误，翻页/刷新失败不提前改变页码。真实 Web 用新增成功请求使下一页旧游标返回 409，自动恢复同 Profile 的新快照且无重试报错 |
| U7 | 0.1.16 实测：透视表阅读与操作混乱 | 54 | 独立分组明细区、层级面包屑、数据时间、固定列头/限高行区、右对齐等宽数字；390/1440px Widget 回归通过，真实 Chrome Web 宽屏与 390px 深浅主题截图/点击验收通过（隔离本机 Server，不冒称远端部署） |
| R1 | Profile 无法选定自己的账号子集 | 56 | 控制接口与 Widget 回归通过；手动与规则共用显式 Route Account Set，支持搜索多选，新增全局账号/普通保存不扩集合，激活范围外账号拒绝；失效当前账号必须明确替换。真实 Web 验证搜索、多选、取消不保存，编辑→检查影响→发布→重开仍为选择的单账号；另一个 Profile 的集合及修订不变 |
| A1 | 严格语义解码不应作为同方言转发闸门 | 51 | 原则需结合安全审批/变换能力逐路径裁决，不全局忽略错误。P2 已分离命名原生声明与可移植函数、服务端执行证据与客户端 ToolIntent；官方无名称 browser_toolset / computer_toolset 会展开工具家族，需补其成员绑定/审批和未知输出回归，不能伪造名称或 schema 绕过校验 |
| A2 | schema 全文 hash 应改编号增量迁移 | 55 | 与用户明确的单一 v1/离线转换选择有关；待区分格式敏感性和演进策略，不直接引入兼容链 |
| A3 | Transform 能改 model/服务等级，与权限约定冲突 | 55 | 部分采纳：真实 Desktop 代理已先复现脚本覆盖 Route model 后仍发送并返回 200；共享脚本输出边界现拒绝顶层 model 改写/删除/歧义重复键，预览与转发共用。正文修改、未知嵌套字段及 service_tier 保留；现有契约禁止选择模型，没有禁止服务等级，不能自行扩大权限限制 |
| A4 | 工具审批/观察模式屏障延迟整个流 | 51 | 待客户端渐进输出与执行许可语义验证 |
| A5 | Client Flow/Manual Proxy Login 等代码术语不同 | 55 | 待区分公开契约漂移与内部名字；不为改名大范围迁移 |
| A6 | productruntime/exchange/CLI 依赖膨胀 | 55 | 待实际调用/二进制成本证据，避免无目标重构 |
| A7 | App/Server 严格字段契约缺版本协调 | 55 | 待异版连接错误呈现；保持用户不做旧格式猜测的约束 |
| A8 | 托管协议样例不代表真实客户端覆盖 | 51 | 采纳验证方向；新增捕获真实结构的合成/去敏夹具，不读取用户会话 |
| A9 | 每请求新 TCP/TLS 无连接复用 | 50 | 待传输实现和请求隔离约束测量 |
| A10 | 每 SSE 消息新 JS runtime | 55 | 待吞吐/隔离成本测量；不牺牲脚本隔离 |
| A11 | 托管请求完整解析多次 | 55 | 待分配/CPU 证据及可复用字节表示 |

## 收口门禁

### 0.1.16 实测交互追加（54、56）

- 报表：不在后台刷新时卸载表格；固定工具栏高度，保留滚动位置。首页静默更新；用户已下钻或翻页时保留阅读快照、显示非阻断更新入口，更新只在当前筛选层级内进行。过期快照自动重新获取，不将正常流量增长当红色错误；明示主动更新会从该层第一页开始，不能静默丢掉页码。
- 透视交互：上方范围与总体指标，下方独立「分组明细」工作区；分组、层级面包屑、数据时间/更新集中在工具栏。表格保持等宽数字、右对齐、统一精度、合计分隔，未知/下限含义不变。长名称、窄屏、键盘操作与错误保留旧数据均有验收。
- Profile：复用现有 Route Account Set，明确从 Endpoint 资格集合选择子集；范围和选择方式分开，支持搜索/多选/已选数量。保存、发布、激活均不得扩成全量；两个 Profile 的集合互不影响，新增全局账号不自动加入。现有集合保留，不擅自改变真实配置。
- 设计约束：复用暗色 canvas `#10151A`、panel `#182028`、divider `#465462`、正文 `#F1F5F8`、次要文字 `#B7C1CB`、数据强调 `#75B8F0` 及对应浅色主题。标题用系统字体 semibold，正文与中文沿用系统/PingFang，金额和数量用 tabular figures。特征是稳定的层级导航与数字网格，不新增装饰、字体依赖或转场动画。
- 先验证公开 Widget 操作与控制接口，再在隔离 Web 页验收；不为这些交互缺陷引入新的统计库、任意透视表达式或运行时旧结构兼容。

- 每个已确认缺陷先有失败检查，再有最小共享路径修复与相关反例检查；未证实项记录原因。
- 协议原始字段/状态/usage 与错误头保真；未知证据仍为未知，不能伪造成功或安全审批。
- Server 私网/DNS/权限、凭据轮换和存储失败属于安全/数据完整性门禁。
- Go/Flutter、功能 race、非插桩规模与实际 Web 复验；独立候选验包之后才考虑后续正式发布。
- 不重写 v0.1.16，不自动替换或重启正在使用的 App，不重复旧数据转换。

## 回归证据

- L3：`TestTransientUserLookupFailureDoesNotEndWebSession` 先红（三类暂时错误均导致 HTTP 401、恢复后读写凭据仍失效），再绿。鉴权接口显式返回错误，Owner 管理、自助报表和会话/密码接口共用错误分类；真实用户撤权仍返回 401。`go test ./internal/servercontrol ./internal/serveradmin ./internal/serverhost -count=1` 通过。
- T1：`TestFollowClientOpenSSLHelloReachesStrictTLSUpstream` 先报 `unsupported extension 22`，再成功收取合成 HTTPS 响应；不可信 CA/错误主机名仍被拒。未知扩展只解码辨识，不实际宣称支持。现有 SOCKS 失败用例进一步约束只允许编译降级、禁止网络错误再拨号。transportprofile/wireprofile/providertransport/environment/loopbackproxy 全包通过。
- S1：`TestServerProxyDeniesImplicitPrivateDestinations` 修复前经 HTTP 访问隔离 Server 回环服务两次，CONNECT localhost 也返回 200；修复后均为 403，显式 Owner 目标仍可连接，透明策略不再忽略 deny。连接策略的显式规则和默认 monitor 有不同权限；请求上下文跨 HTTP/1 与 HTTP/2 保留该约束，egressnetwork 统一校验系统 DNS/DoH 的完整地址集合并固定实际拨号 IP，SOCKS 不例外。新增检查使用合成网络边界，不探测开发者 LAN/metadata。没有改变 Desktop 本机网络权限或用户当前运行实例。
- P12：`TestManagedRouteUsesConfiguredAPIPrefixExactlyOnce` 覆盖根路径、`/v1`、自定义前缀、前缀+`/v1`、`/v10`。两个重复版本段先返回 404，再通过真实代理；Responses 路径和既有 ChatGPT 特例回归通过。
- P1/P4：`TestManagedAnthropicRoutePreservesMultimodalHistory` 对图片、文档、工具结果图片/文档分别回放两轮；`TestManagedAnthropicRoutePreservesNestedExtensions` 覆盖消息、内容、工具选择、thinking、output、context、diagnostics 嵌套扩展。修复前分别返回 422/400，修复后上游收到的 JSON 内容相同。`TestNativeHistoryIsOpaqueAndDoesNotBecomeCrossDialectText` 与 `TestNativeRequestProjectionStillRejectsAmbiguousOrMalformedKnownFields` 保持跨方言拒绝、未知内容不冒充文本、重复键/畸形已知字段拒绝。
- P3：`TestManagedAnthropicRoutePreservesLegalTerminalsAndUsage` 覆盖四类终态的 JSON/SSE，共八种。修复前 502 或在 `message_stop` 后追加 error；修复后客户端原始响应保留，持久库公开 `ScanUsage` 仍返回完整用量。中立 stop reason 与持久化投影使用同一验证入口，不能只修线上的响应而让正文落库失败。
- P5/P13：`TestManagedAnthropicRoutePreservesUpstreamErrors` 覆盖上下文超长 HTTP 400、配额 HTTP 429、首事件报错、文本后报错和工具开始后报错；客户端错误原文/请求 ID/重试头保留。最后一个反例只释放消息元数据与错误，不泄漏未批准的工具调用。`NativeProviderError` 的 HTTP envelope 有 64KiB 限制、独立字节所有权、方言隔离及禁止诊断序列化检查。
- 最新集成门禁：`env -u VIBERMATE_LIVE_TEST_APP -u VIBERMATE_LIVE_AGENT_DEEP go test ./... -count=1` 全部通过；anthropicchat/protocolcore/exchangecontent/exchange/loopbackproxy/desktophost 六包 race 通过，最后新增的流前缀与工具屏障反例再次通过针对性 race；`git diff --check` 通过。Flutter/Web 与剩余矩阵项仍未收口，不以这些 Go 绿灯替代正式发布门禁。
- U3/U5/U6/U7：`usage_pagination_test.dart` 先复现后台表格消失、阅读页被重置、过期下钻报错、深行滚动后标题不可见；现均通过。另对 503 和连续 409 复现“页码先变、旧行仍在”，修复后数据/小计/游标原子提交，失败保留原页与原数据；连续变动不无限重查。22 条报表/概览/深浅与窄屏专项通过。
- R1：`TestEnvironmentDraftPublishesOneAccountAcrossExplicitEndpointProtocols` 覆盖新建全局账号后普通保存不扩选、不产生修订 422，范围外激活拒绝、明确加入后激活保持其余成员；environment/captureassignment/desktopcontrol 全包与 race 通过。Widget 验证搜索多选、390px 取消不落选项、规则/手动切换保持范围、当前失效账号明确替换及发布预览。
- 本次 UI 集成：`flutter test --no-pub` **679 通过 / 16 既有条件跳过**；`flutter analyze --no-pub` 无问题；Go 全量通过。全量 Go 与 race 同时运行时 shutdown deadline 测试曾观测 551ms 超过其断言，隔离重跑 10 次及后续全量均通过，未放宽断言或改产品超时。
- 隔离 Preview Web release 构建通过，仅合成数据，服务限于 `127.0.0.1:18646`；当前 CUA 返回无可用 browser，Chrome 原生入口也不能取得窗口，已请求用户连接浏览器。**没有完成此次真实 Web 验收，没有新发布、App 替换或真实数据修改。**
- U1/U2 后续门禁：`provider_accounts_view_test.dart` 的跨端设置操作和 `environment_account_quota_test.dart` 的计时/休眠显示均先红后绿；三个账号包 14 条专项通过。Flutter 全量 **681 通过 / 16 既有条件跳过**、analyze 无问题。复用现有 Widget 渲染入口检查报表 390/1440px、深浅主题的分组下钻与列头/数字对齐；不将离屏 Widget 渲染冒充真实浏览器验收。D6 正在继续复核，整轮 review 未完成。
- D6：`TestUsagePolicyNonShorteningDoesNotRewriteHistory` 在不变/停采/延长三种操作均先失败；`TestUsageShorterRetentionIsImmediateAndNeverResurrected` 先因保存依赖历史写入失败。现通过公开 `SetUsagePolicy` / `ScanUsage` / `MaintainExpired` 和真实数据库故障注入验证即时到期、延长不恢复、维护失败/重启恢复，以及多次不同保留期对各批观察的独立约束；没有额外控制接口或清理线程。[ADR 0020](../adr/0020-publish-usage-retention-before-bounded-cleanup.md) 记录取舍。20 万条隔离负对照恢复旧全表 UPDATE 后在 500ms 超时；当前设置保存 0.04–0.17ms、并发终态写入最慢 39.8ms，重开库/续清后可见结果一致。0.1.15/0.1.16 离线转换、本轮 Go 全量、`go vet ./...`、runtimepersistence/runtimeusage/converter 三包 `-race -short` 均通过。百万条精确计价与可取消续清回归通过：首次清理 1000 条约 35ms，全部清理约 10.8s、跨 44 个可续作预算。日志位于 `/private/tmp/vibermate-review-retention.aDvm5g/`。未运行真实库转换、App 切换或发布。
- L2：`TestManualOAuthRefreshWaitsBeforeFreezingNewAccountLeases` 经公开账号入口，使用真实 OAuth single-flight/CAS 与合成 HTTP 边界，先复现新请求立即失败；补充反例又发现离开页面会提前放行旧 epoch、Shutdown 不会唤醒等待，均已修复。七种情形重复 10 次通过：成功后只冻结新 epoch、暂时 503 保留仍有效旧 token、单个等待取消不影响轮换/其他账号、关闭及时退出。账号/OAuth/控制接口相关回归与 race、Go 全量和 vet 通过；证据在 `/private/tmp/vibermate-review-refresh.YNy4D8/`。[ADR 0011](../adr/0011-manage-codex-oauth-before-freezing-account-leases.md) 记录有界轮换与请求取消的区别；没有使用真实账号或操作正在运行的 App。
- A3：`TestManagedTransformCannotOverrideRouteModel` 通过真实子进程、CONNECT/TLS 和合成上游先复现模型映射被脚本覆盖且客户端收到 200；修复后没有上游请求，Activity 明确记为 `message_transform_failed`。`TestMessageTransformControlPreservesModelSelection` 通过公开控制 HTTP 接口覆盖三种方言、30 个输入/反例：替换/删除/null、重复及转义重复键、畸形/尾随数据拒绝；格式化、嵌套 model 数据和 service_tier 修改仍通过。校验仅在请求 Body 实际变化时执行，不给未修改的 Original Destination 增加语义闸门。调整三个旧测试中与词汇表冲突的改模型样例，保留原有顺序/上下文/模型映射+路由提示的断言。五包相关测试和 race、Go 全量、vet 通过；日志在 `/private/tmp/vibermate-review-transform.LoC2im/`，未改真实 Profile、App 或发布。
- L1：`TestOAuthRefreshRecoversRotatedCredentialAfterStorageUnlock` 使用公开账号操作、真实 OAuth 管理器、合成一次性轮换端点与可恢复写锁。先复现重放已消费 token，再逐步复现关闭自动刷新绕过恢复、待保存误报就绪、32 并发取到旧 epoch，以及 Owner 替换后仍残留待保存凭据（含正在请求 token 端点的轮换）。每项先红后绿，恢复后再做一次真实合成轮换证明新 refresh token 可用；关闭失败明确返回，解锁后可重试保存。账号/OAuth/Runtime 三包及 race 通过。证据在 `/private/tmp/vibermate-review-oauth-persistence.zr9rzS/`；不读取真实 Keychain，不承诺物理存储不可用时的跨进程恢复。[ADR 0011](../adr/0011-manage-codex-oauth-before-freezing-account-leases.md) 记录此边界。
- L1 最终门禁：另先复现存储锁定 2 小时后直接发放已过期 access token 的 epoch；恢复流程现在先保存唯一 refresh token，再按手动操作/自动刷新授权进入既有续期路径，关闭自动刷新时不擅自续期。10 种恢复情形各重复 100 次通过；三包 race、最终全量 Go、vet、diff-check 通过。首轮全量有既有 `TestLauncherKeepsChildThroughTemporaryControlFailure/revoke=false` 在 5 秒截止失败，独立重跑 10 次及后续两轮全量均通过，未改该测试或放宽截止；保留失败日志，不据此断言没有时序风险。浏览器仍返回空连接，真实 Web 和其余矩阵项未完成，未发布。
- L4：`TestRemoteLoginSurvivesPrivateCALeafRenewal` 用真实 `localca` / `serveridentity` 在续期窗口重开 Server 身份，并经过 TLS 和登录 HTTP 入口，先因原叶指纹变化失败。修复统一 PinStore 后通过；`TestTrustCommandExplicitlyMigratesPrivateServerLeafPin` 先因缺少私有 CA 转换入口失败，现通过真实命令解析、无凭据 TLS 核验、登录和第二次叶证书变化验证转换。`TestRemotePrivateCATrustRejectsInvalidPeersBeforeLogin` 的其他 CA、错误主机名、过期、尚未生效、非 ServerAuth、拼接无关根、缺失 CA、无效签名八个反例同时覆盖登录、代理 relay 和显式信任；均在密码 HTTP 请求前失败，恢复正确证书后原信任仍可用。没有把 CA 装入系统，也没有新增历史格式回退；[ADR 0010](../adr/0010-separate-server-identity-from-proxy-trust.md) 补充准确的信任边界。证据目录 `/private/tmp/vibermate-review-private-ca.fsOFlD/`；相关七包、全 Go、六包 race、vet、diff-check 通过。CUA 再次返回 `browsers: []`，本轮没有真实 Web、App 替换或发布。
- D3/D4：`TestTerminalObservationFailuresKeepIndependentRecords` 使用真实 Desktop Host/子进程/认证 CONNECT/TLS → 合成 Anthropic 上游，以 SQLite trigger 只注入指定记录失败。先复现客户端成功且 Token 已知，但 Activity 失败后公开 `ScanUsage` 为零；修复后用量独立保存。开始 Activity、终态 Activity、usage 三种故障各重复 10 次，均保持 Runtime 转发健康并显示准确录制告警；解除故障后继续记录，重开库不丢已保存用量。未增加队列、放大超时或绕过审计。`TestUsageRetentionAtScaleDoesNotHoldWriter` 另验证 20 万条保留期维护与 20 次完整核心审计竞争：新用量最慢 35.0ms、审计开始+结束 106.7ms，公开查询/重开/续清均保留终态；不能外推为所有写事务都不会超时。本轮五包集成、全量 Go、vet、四包 `-race -short` 均通过，证据目录 `/private/tmp/vibermate-review-terminal-usage.AHqOx3/`。全库锁等待后的补记和报表完整性提示仍是 D4 后续项，未发布。
- U3/U5/U6/U7/R1 实际 Web：CUA 无浏览器连接后，复用已安装 Playwright + Chrome，独立无头进程/临时配置运行新编译的正式 Web（非 Preview）和 loopback Server。公开登录/控制接口创建两个 Profile、三个合成账号，真实认证 CONNECT/TLS 向合成上游发送 58 个种子请求（Work 下 54 种模型）及 7 个增量请求。浏览器真实登录、刷新、下钻、翻页：延迟 500ms 的刷新全过程保持原行位置/尺寸；旧游标真实返回 409 后自动恢复同范围；第二页在轮询见到新数据后仍保留原行，主动更新才回该层第一页。检查 1440px 宽屏和 390px 深浅主题，窄屏通过实际滚轮进入明细，不将离屏语义节点当截图可见内容。账号范围通过搜索/多选/取消及发布后重开；公开读取确认 Work 为 r2/单账号、Personal 仍 r1/原单账号。脚本、截图及最终 HTTP 快照日志 `web-acceptance-final.log`、`web-probe.log` 位于 `/private/tmp/vibermate-review-terminal-usage.AHqOx3/`。探索阶段曾有一次并行浏览器的 45s 更新等待超时；最终单窗口显式保持可见后整条流程通过，保留原日志，不据此判断产品根因。没有操作用户 Chrome 页面、真实账号/数据或 App；这些仅收口上述 Web 交互项，未替代其他安全/协议/规模与发布门禁。

- P2 命名工具：`TestManagedAnthropicRouteAcceptsExplicitCustomTool` / `AcceptsNativeToolDefinition` 通过真实 Desktop Host、子进程、认证 CONNECT/TLS → 合成上游先复现本地 422；`PreservesServerToolResponse` 再复现上游合法 JSON 被换成 502、SSE 在 message_start 后被补发 error。现 custom 保持 schema 函数语义，命名原生声明只投影名称/类型，原始配置继续走 source body；搜索请求、服务端执行结果、引用、输入 JSON 增量及下一轮携带的历史均保留。每次成功检查公开 Activity、正文 Get、用量 ScanUsage 与录制告警，不只检查客户端 200。
- P2 权限反例：`TestManagedAnthropicNativeDefinitionsDoNotBypassClientToolPolicy` 对 custom / bash / web_search 各验证 JSON/SSE；即使原生声明通常用于服务端，若上游返回客户端 `tool_use`，仍经原审批边界，严格模式不放出工具块。新增原生定义不能作为可执行 ToolIntent，不能混入伪造 schema；重复名称、未声明的 named choice、custom 缺 schema 仍拒绝。跨方言解码/编码拒绝原生定义；Responses JSON/SSE 编码先复现静默丢声明，再补一致拒绝。相关七包通过，后续集成结果另记；日志在 `/private/tmp/vibermate-review-typed-tools.Rw3R8g/`。
- P2 引用增量：按官方 `citations_delta` 形状完善上述合成 SSE（空文本块→text_delta→citations_delta），先再次复现中途追加 error；共用引用验证入口后原始事件保留、下一轮历史与用量通过。引用缺失、非对象、无类型、投向 thinking 而非 text 的四个反例不放出畸形引用。搜索正例同时使用严格工具策略，证明服务端已执行结果不被误当成客户端待执行提案。
- P2 未覆盖边界：官方 SDK 当前还包含没有 name 的 browser/computer toolset；其工具家族展开、成员选择和对应输出尚未建模，继续由 A1 跟踪，不能据此声称完全支持当前所有 Anthropic 工具。没有把所有非 custom 类型当成服务端动作，也没有跳过未知客户端动作的审批。
- P2 最终门禁：包含引用增量修复的 `go test ./... -count=1` 与 `go vet ./...` 通过；anthropicchat/protocolcore/openairesponses/exchangecontent/exchange/toolpolicy 六包 `-race -short`、Desktop Host 的 `TestManagedAnthropic*` 真实代理专项 `-race` 通过。初次新增 namespaced 反例夹具缺少已有契约要求的 description，修正夹具后普通与 race 复跑通过，原失败日志保留；没有为测试放宽产品校验。最终日志为 `go-all-final.log` / `protocol-race-final.log` / `managed-race-final.log` / `vet-final.log`，同在上述临时证据目录。未修改 UI、正式 App、真实数据或凭据；未发布，未将整项协议审查标完成。

- L9 唤醒反馈：`TestInstalledCodexRecoversTransientHTTPFailureThroughLauncher` 从公开 Launcher 启动安装的原生 Codex 0.158.0，并与无 Launcher 的相同客户端对照。独立 CODEX_HOME、合成 API key、只响应 fixture 主机的本地代理；前六次 Responses HTTP 请求返回用户同形 502，第七次成功，二者均在同一提示词中自动恢复。代理拒绝 WebSocket CONNECT，客户端先执行其原生 HTTP fallback；本测试不是完整 Runtime/真实睡眠/原生 WebSocket 连续性验收。当时没有取得现场失败的传输分类、重试序列与运行中客户端版本；现按用户要求低优先级跟踪，不再要求回找历史诊断。后续分别注入响应头前和流中途断连、短时和超过重试预算的故障；不据单个错误文本放大超时、增加隐藏重发或宣称已经找到睡眠根因。
- L9 配置清理：真实客户端明确报告 `request_max_retries` / `stream_max_retries` 两个顶层配置被忽略，先以“包装启动不得引入原生配置警告”断言复现，再删除启动器强制注入和原有快照中的两项。保留客户端自己的重试决策；每次新 HTTP 请求独立冻结/记录，Runtime 自身的已提交内容禁止重发边界不变。[官方配置参考](https://developers.openai.com/codex/config-reference/) 也将重试参数列在 `model_providers.<id>` 下；不把旧顶层配置替换成另一种强制零重试。`retry-config-red.log` → `retry-config-green.log`、runlauncher/loopbackproxy/exchange 三包普通与 race、vet、diff-check 通过；证据在 `/private/tmp/vibermate-review-wake-http.bVyibk/`。最初探索曾误用 npm wrapper 和未正确固定的直接客户端 base URL，相关失败日志保留但不作为产品缺陷依据；随后换成原生二进制并固定测试来源。正式 App、用户数据、凭据及系统网络未改动，未发布。

协议形状核对来源（2026-09-29 读取；所有测试内容均为合成）：Anthropic 官方 SDK 的 [图片输入](https://github.com/anthropics/anthropic-sdk-python/blob/main/src/anthropic/types/image_block_param.py)、[文档输入](https://github.com/anthropics/anthropic-sdk-python/blob/main/src/anthropic/types/document_block_param.py)、[工具结果](https://github.com/anthropics/anthropic-sdk-python/blob/main/src/anthropic/types/tool_result_block_param.py)、[消息终态](https://github.com/anthropics/anthropic-sdk-python/blob/main/src/anthropic/types/message.py)、[重试头处理](https://github.com/anthropics/anthropic-sdk-python/blob/main/src/anthropic/_base_client.py)、[custom 定义](https://github.com/anthropics/anthropic-sdk-python/blob/main/src/anthropic/types/tool_param.py)、[原生 bash 定义](https://github.com/anthropics/anthropic-sdk-python/blob/main/src/anthropic/types/tool_bash_20250124_param.py)、[服务端工具输出](https://github.com/anthropics/anthropic-sdk-python/blob/main/src/anthropic/types/content_block.py)、[引用增量](https://github.com/anthropics/anthropic-sdk-python/blob/main/src/anthropic/types/citations_delta.py)、[浏览器工具组](https://github.com/anthropics/anthropic-sdk-python/blob/main/src/anthropic/types/browser_toolset_20260801_param.py) 与 [计算机工具组](https://github.com/anthropics/anthropic-sdk-python/blob/main/src/anthropic/types/computer_toolset_20260801_param.py)。
