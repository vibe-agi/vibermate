# ViberMate 生产修复设计

日期：2026-10-03。基线：main `c61f8c6b5e2196da8e99010c169d4e3c17797933` 加当前未发布的阅读器、图片预览、工具补查和客户端 CA 改动。

用户在完成 codebase review 后要求使用 Superpowers 规划并创建 goal 推动。本设计沿用评审 R1–R7 的修复方向，将其细化为现有模块的修改；正常实现选择由执行控制器判断并记入账本。

## 目标与范围

关闭评审的 3 个 P1 和 4 个 P2，将当前未发布功能形成可验证的下一修复版本。先完成隔离修复、回归、独立审查和生产候选验证，随后依照本会话已有 GitHub/Homebrew 发布要求推进版本交付。公开发布只在所有适用验收门槛通过后进行。

已确认缺陷：

| ID | 问题 | 交付线 |
| --- | --- | --- |
| R1 | 远端工作区被服务端本地文件系统自动证明 | 权限与客户端 |
| R2 | Codex 共享后台复用旧 Capture 代理和策略 | 权限与客户端 |
| R3 | reading 视图跳过冻结 transcript 链校验 | 证据与持久化 |
| R4 | 已提交的未查看 Exchange 仍占用 writer 索引 | 证据与持久化 |
| R5 | 未找到/未完成的工具查询被长期缓存 | 阅读器 |
| R6 | Claude 纯工具返回变成空白用户发言 | 阅读器 |
| R7 | Server 凭据 rename 后缺少耐久性与提交不确定性处理 | 证据与持久化 |

已有复现：R1、R3、R4 的 Go 隔离测试与 R5、R6 的 Flutter 测试稳定失败。R2 有本会话 devcontainer 实机证据；R7 的缺少目录同步由代码确认，执行阶段必须补故障注入。

## 方案选择

采用现有模块内的定向修复。另一方案是增加统一的后台代理/会话协调服务，但会扩大生命周期和权限接口，当前七个缺陷没有要求这种新系统。只发布 P1 修复、把 P2 留到以后也不足以闭合本次生产目标，因为记录内存和晚到工具结果直接影响持续运行与用户判断。

拆成三个可独立验证的子项目，执行控制器按 P1 优先顺序串行修改，共享文件的任务必须经过前项审查后再执行：

1. 权限与客户端：R1、R2。
2. 证据与持久化：R3、R4、R7。
3. 阅读器：R5、R6。

## 权限和客户端设计

### R1：声明路径与本地权限证明

保留 `Admission.WorkspaceRoot() (string, bool)` 作为已观察到的路径声明，用于展示和脱敏。增加 `Admission.LocalWorkspaceRoot() (string, bool)`，仅在 Workspace scope 有效且 `EvidenceLocalLauncher` 时提供本机文件检查的根。

`Pipeline.decideTools` 只把 `LocalWorkspaceRoot` 的结果放进 `ToolDecisionContext`；结构化工具自动判断还要求相应的精确客户端 capability。远端 `registered_companion` 的 Review 走已有人工决策，Strict 返回拒绝，Observe 保持原有观察行为。

### R2：独立的实际请求进程

实际发出请求的 Codex 进程必须使用本次 Capture 的代理环境。交互式启动和 resume 采用客户端已支持的独立进程入口；exec/app-server 等已有独立进程入口保持其原生调用方式。显式连接另一 app-server 的 `--remote` 调用不能伪装成当前 Capture 已控制的会话，返回明确的不可捕获说明。

选择保留现有 release 验证和 Root 授权规则，将启动参数兼容性作为不增加权限的独立事实。对 catalog 已知 Codex family 做限时、限输出的原生 `--help` 参数能力读取，在支持 `--no-daemon` 的交互入口前加入该参数。仅收集参数存在性，不能因此提高 recognition、授予 Root 或结构化工具权限。

探测上限：1500 ms、64 KiB 输出；失败、输出异常或无法证明调用入口安全时返回明确的隔离不支持错误。既有官方 0.145.0 无共享后台启动契约作为明确 legacy 分支；不得以任意未来版本号推定兼容。Global option 的值和 `--` 后提示文本不能误认成子命令。

精确验收对象：Codex 0.145.0、0.159.2、容器中已观察到的 0.160.0；Claude 2.1.220。前两者检查兼容回归，0.160.0 检查共享后台已存在的触发条件。采集 MCP 422 的 method/authority/path/status，按官方契约增加必要的精确操作支持或修复实际错误边界；保持未知操作拒绝、原始凭据同源和工具决策约束。

## 证据和持久化设计

### R3：有界读取保持原有证据校验

提取共用的 transcript 节点校验，检查当前冻结 root、递减 depth、parent 形状、message digest 格式及 `transcriptNodeDigest`。reading 与完整分页共用该规则。

读取最近 wire-user 组时可以只遍历必要元数据，但必须验证实际经过的链，遍历次数不得超过冻结 requestCount。角色参与选择时需检查对应消息 manifest 的一致性；不能把未校验的 SQL role 当成权威。当前存储若无法便宜证明某字段，应明确退回有界校验读取，而不是跳过验证。

选择后的消息仍用已有 128 KiB 页预算/24 条上限及 deferred 内容入口。没有新 schema、历史复制或迁移。损坏、缺失、循环均返回 `exchangecontent.ErrInvalidEvidence`。

### R4：索引只描述尚未耐久的工作

成功 `AppendBatch` 后，在 admission 锁下删除 batch 中仍指向不晚于本批已提交 watermark 的 `exchangeLatest` 和 `scopeLatest` 项。若同一键出现更新 admission，保留其较新 watermark。失败提交不清除索引，所有队列统计与 slot 释放维持原有成功路径。

sealed scope tombstone 的晚到写入保护保持有效；容量验证单独记录其增长和生命周期。若实际验收显示其成为负载相关问题，控制器先验证持久终态/过期权威的安全回收方案再改动，不能直接删除屏障来通过内存断言。

### R7：耐久提交与不确定提交分离

Unix Server 文件 Store 的成功状态要求临时文件写入、文件 Sync、原子 rename、父目录 Sync 均成功。rename 前失败保留旧缓存；rename 后同步失败或取消产生内部提交不确定错误，并阻止新操作继续从旧缓存发放值。保持 existing `secretstore.ErrUnavailable` 外部分类，诊断不包含 secret bytes。

采用每个 Store 实例的私有文件操作 seam 进行故障注入，避免包级 mutable hooks。写入与删除均使用相同提交规则。Windows 保持可编译并单独核对原生替换/flush 契约；本次正式耐久性承诺针对已发布的 Linux Server，不能凭跨编译宣布 Windows 已验收。

## 阅读器设计

### R5：晚到结果可重新查询

查询工作与结果缓存分离。成功且完整的非空结果可保留；空、失败、取消或分页未完成的结果不能作为长期负缓存。关闭/重开面板、相关 Activity 变化、Controller/对话变化和记录删除都能使旧查询失效。

使用 generation 标识防止旧响应恢复旧选中项；成功缓存上限 32 项、总保留正文预算 4 MiB。空结果提供现有风格的刷新动作，相关新记录到来时自动重新查询。保持阅读位置、当前 tab 和 inspector 选择；缺少结果不能被表述为工具仍运行。

### R6：协议 role 不等于人的发言

只在筛选后确实存在可读的人类文字/附件时建立用户气泡。純 tool_result 仍出现在处理过程。混合消息保留真实文字和图片。只包含 deferred 且类型尚未证明的内容使用中性“查看内容”入口，展开后依据已验证 block kind 呈现，避免空白或伪造用户发言。

不使用提示文本相似度、邻近时间或工具名猜测配对；重复提问和重放继续使用已观察位置/稳定 anchor。

## 全局约束

- Go toolchain 1.26.8；保持 go.mod 的 go 1.26.0 声明。
- Flutter 3.41.5，revision `2c9eb20739dfec95e2c74bd3dfa4601b0a8a36aa`。
- CI Node.js 22.23.1；macOS 正式目标保持 14+；Linux Server amd64/arm64；Windows 为编译兼容检查。
- 继续使用 SQLite schema revision 1，不增加迁移或重置数据库。
- 不增加新的运行时依赖；复用原有 Go/Flutter/CLI 与认证接口。
- 英文与简体中文文案一起更新；用户默认界面不展示内部 ID、哈希和协议名。
- 每个修复先有失败的实际行为用例，再做最小实现与复核。
- 安装/故障/容量用例使用临时数据、合成账号和隔离进程/runner；不能覆盖现有运行目录或生产登录。
- 发布依赖本次源码的 CI、打包、签名、公证和安装证据；Paseo/ACP 未完成的手机实机范围继续明确标为 Experimental。

## 完成条件

R1–R7 各自有原问题 red→green 证据，独立任务审查通过；整体 Go/Flutter/Chrome/Paseo、native-tag 构建和依赖检查通过；实际客户端、CA、晚到工具结果、两 profile 并行与异常恢复均有证据；受控持续运行至少 60 分钟，时间推进覆盖 10 天过期/续期边界，并说明与真实 10 天 soak 的区别。

最终整体复核没有未解决的 P1/P2 后，冻结修复版源码，生成来源一致的产物与发布证据，完成本会话已要求的 GitHub/Homebrew 新版本交付。发布版本规划为 0.1.24+26；冻结前若远端已有更新，则基于最新已发布版本选下一 patch，并在账本记录具体选择。

