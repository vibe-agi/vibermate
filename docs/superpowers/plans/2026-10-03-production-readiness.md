# ViberMate Production Readiness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 关闭 R1–R7，完成当前未发布功能的生产候选验证和已要求的 GitHub/Homebrew 修复版交付。

**Architecture:** 沿现有模块修复权限证明、实际客户端进程、证据链、提交边界与 UI 生命周期。三条子计划各自独立，控制器按 P1 优先顺序串行提交，最后合流到真实客户端、容量、打包和整体审查。

**Tech Stack:** Go 1.26.8；Flutter 3.41.5；Node.js 22.23.1；SQLite revision 1 基线，Task 8 含用户已授权的安全格式升级。

**Spec:** `docs/superpowers/specs/2026-10-03-production-readiness-design.md`

## Global Constraints

- Flutter revision `2c9eb20739dfec95e2c74bd3dfa4601b0a8a36aa`；不更新工具链或新增 runtime 依赖。
- R1–R7 不改 SQLite revision 1；Task 8 可按用户新授权安全升级格式，先验证备份与迁移，保留所有数据、记录、账号/秘密引用及权限；不得重置，禁止迁移运行中的目录。
- 每项修复必须有原问题 red→green 和独立 spec/quality review，未解决 P1/P2 不满足 goal 完成条件。
- 当前主工作区中的未发布功能完整带入隔离基线；执行修改和提交在隔离分支。
- 英文/简体中文同步；默认界面文案面向用户。
- 真实 OS/故障实验使用临时数据与隔离 runner；不输出凭据/私钥/真实对话。
- macOS 14+、Linux amd64/arm64 是正式范围；Windows 为编译兼容检查；Paseo/ACP 实验范围不扩大。
- GitHub/Homebrew 交付遵循本会话已有发布要求，全部适用验收先完成，实际版本冻结前重新核对远端。

## 子项目及执行顺序

| 顺序 | finding | 执行计划与 task | 交付 |
| --- | --- | --- | --- |
| 1 | R1 | authority-and-client-lifecycle Task 1 | 本地文件权限证明 |
| 2 | R3 | evidence-and-durability Task 1 | 默认阅读完整性 |
| 3 | R2 | authority-and-client-lifecycle Task 2 | Codex 进程/Capture 隔离 |
| 4 | R4 | evidence-and-durability Task 2 | writer 索引清理 |
| 5 | R7 | evidence-and-durability Task 3 | 凭据耐久提交 |
| 6 | R5 | reader-lifecycle Task 1 | 晚到结果重新查询 |
| 7 | R6 | reader-lifecycle Task 2 | 真实人类输入呈现 |

完整文件名统一为 `docs/superpowers/plans/2026-10-03-<表中计划名>.md`。子计划拥有各自 SDD workspace/ledger；控制器切换到相应子计划时按其身份恢复，不读写与本 goal 无关的 workspace。主计划仅记录调度与整体验收。

模型路由使用工具实际允许的名字：多文件实现默认 `gpt-6.1-sol`/high；明确机械的完整代码任务可使用 `gpt-6-luna`/medium；权限、并发、持久化 review 与最终整体 review 使用 `gpt-6-astra`/high。派发指定 model 和 reasoning_effort，fork_turns=none。实现者不再派发子代理，多个实现者不并行修改本工作区。

## Task 0: 隔离基线与恢复账本

**Files:** `.gitignore`；上述 spec/三条子计划；隔离工作区的 SDD scratch 与 ledger。

**Interfaces:** 当前工作区 snapshot → `fix/production-readiness-20261003` 分支及 `.worktrees/production-readiness`；SDD 账本第一行必须指向实际 plan。

- [ ] 核对 git-dir/common-dir、submodule、branch 和现有工作区变化，确认已知未发布文件清单。
- [ ] 根据 using-git-worktrees 建立隔离工作区。没有原生 worktree 工具时采用 git；先保证 `.worktrees/` 已忽略。
- [ ] 使用独立临时 Git index 构造当前已知改动的基线 commit，保留当前真实 index 和分支状态；只 stage 已确认的代码/测试/docs 和 ignore 文件。snapshot 不包含 node_modules、build、临时报告或凭据。
- [ ] 新 worktree 指向此基线 commit，核对全部未发布文件与源码 snapshot 一致。源码基线与计划由此有本地 Git 记录。
- [ ] 在隔离工作区安装已锁定依赖，运行基线 go unit、Flutter analyze/相关 reader tests；已知 bug 的 red 不作为环境故障。
- [ ] 用 skill 的 `scripts/sdd-workspace PLAN_FILE` 为主/当前子计划创建 scratch；建立带 plan 身份的 `progress.md` 和任务自洽/共享接口检查表。

已知失败见本会话评审复现；执行者不得把它们删除来建立“绿色基线”。临时复现源可从 `/tmp/vibermate-production-review.QrdKJU/` 读取，但正式测试必须完整写入对应仓库 test 文件，不依赖该临时路径。

## Task 1–7: 执行七项修复

- [ ] 按上表顺序提取准确的子任务 brief，向新实现者提供 brief/spec/interface/report 路径。
- [ ] 每次记录 BASE、red 命令/失败原因、green 命令/结果和 commit；同一源码的测试结果不由 reviewer 重复执行。
- [ ] 由独立任务 reviewer 检查 spec compliance 和 code quality；跟进同一 implementer 修复，按 skill 控制 fix loop。
- [ ] 任何 ruling 记录原因、影响和错误代价；主要缺陷不能以“parked”代替本 goal 的关闭标准。
- [ ] 子计划账本完整后继续调度下一项，不在每个正常任务结束时重复等待用户确认。

## Task 8: 精确客户端、MCP 与 CA 验收

**Files:** `internal/runlauncher/codex_retry_integration_test.go`；`internal/loopbackproxy/native_compatibility_test.go`；`internal/loopbackproxy/handler_internal_test.go`；`internal/desktopcontrol/content_page_integration_test.go`；`internal/serverhost/runtime_root_ca_integration_test.go`；`cmd/vibermate/ca_test.go`；`docs/capability-support.md`；`docs/deployment.md`/`.zh-CN.md`。

**Interfaces:** 已修复的 Launcher、reading projection、工具补查、CA 命令 → 精确版本/平台验收 evidence。MCP 操作支持只产生基于正式契约的精确 operation，不放开通用路径代理。

用户后续授权的绑定要求：每个 Environment 的 MCP 权限默认关闭；工具保留原客户端登录账号、模型仍用所选账号；ToolDecision 不绕过；完整 tools/call 与异常恢复不能缩为只读 discovery。用户允许内部数据格式安全升级，旧 revision-1 的数据先备份、迁移/恢复验证后才能启用新格式。controller 在执行前将迁移/原生持久化、原生传输与 CA 验收拆为自洽子计划并自检；这些准备稿不是已经通过的验收。不得伪造模型 Route/Exchange/批准或把生命周期藏在无关字段。

第一个已自检、可执行的子计划是 `2026-10-03-schema-upgrade-snapshot.md`：冻结 v1 并验证完整私有回滚快照，不提升 revision、不启用 MCP。原生 DDL、事务迁移/启动与传输/CA 后续子计划须各自采用后执行。

快照 Task1 已于 d8a8a77 通过正常/并发/仓库/编译验证及独立复核。下一自检子计划 `2026-10-03-native-tool-store.md` 先执行 Task1（原生权限/绑定纯模型与非启用态 v2 DDL）；事务升级另行采用，之后才执行存储任务2/3。`2026-10-03-client-ca-acceptance.md` 已自检，按三项串行行为/一次性原生 runner/CI 任务排队。以上不是原生客户端、迁移或发布验收完成。

原生准备 Task1 已于 8209f1b 通过测试及独立复核，无剩余 findings。`2026-10-03-schema-migration-switch.md` 已自检并采用：使用该精确 DDL、已复核的完整快照，以及持锁事务/提交结果核对/异常恢复接入启动；桌面启动只在实际数据准备或升级时延长一次有界等待。完成该项复核后再执行原生存储任务2/3；尚未迁移真实数据或完成 MCP 验收。

- [ ] 使用隔离、固定版本的官方客户端：Codex 0.145.0、0.159.2、0.160.0，Claude 2.1.220。校验实际 version，已有后台 fixture 与请求接收器不使用用户生产账号/配置。
- [ ] 验证已有后台、interactive/exec/resume、两个 Environment 并行、工具输入/返回、图片、取消、断网重连与下一次启动。按持久 Activity/Capture/Environment ID 断言实际请求归属，不只检查终端文本 OK。
- [ ] 为已观察 MCP 422 只采集 method/authority/path/status 元数据，核对相应官方源码/契约，写精确失败用例。修复应保留未知操作拒绝、原始凭据同源、保留策略和工具决策；测试 initialize/list 等只读协议入口，不触发外部应用写操作。
- [ ] CA download/status/install 使用合成 CA、临时服务端和隔离 OS runner：验证平台识别、可信 fingerprint、拒绝错误 fingerprint、普通客户端只读下载、撤销、旧服务端说明、已有信任的幂等与实际安装后的验证。禁止用跳过 TLS 检查来使测试通过。
- [ ] 将失败对应的产品边界修正后重跑精确用例；未执行或 skipped 的原生门槛单独记录，不能计为通过。
- [ ] 做 reader 与真实 codec/raw evidence 的集成：确实从 HTTP 图片保留字节取得预览、正文与图片同时出现、large tool result 可加载，权限拒绝时清楚说明。

本 task 的代码步骤保持 TDD；新增 endpoint 的 method/path/query/body 边界及 secret-free 回应必须各有行为断言。

## Task 9: 全包验证与受控持续运行

**Files:** 新容量/故障用例在 `internal/rawevidence/manager_test.go`、`internal/runtimepersistence/*_test.go`、`internal/desktophost/*_test.go`；有需要的 runner 放 `tool/performance/`；结果存该计划 scratch，只有脱敏摘要进入 docs。

**Interfaces:** 本次修复分支 → 单一源码 SHA 的 verification/evidence；不新增产品测试专用公开接口。

- [ ] 对最终源码运行：

```sh
DEVELOPER_DIR=/Library/Developer/CommandLineTools go test ./...
DEVELOPER_DIR=/Library/Developer/CommandLineTools go test -race -short ./...
DEVELOPER_DIR=/Library/Developer/CommandLineTools go vet -tags vibermate_native_secrets ./...
DEVELOPER_DIR=/Library/Developer/CommandLineTools go build -tags vibermate_native_secrets ./...
DEVELOPER_DIR=/Library/Developer/CommandLineTools GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -tags vibermate_native_secrets ./...
DEVELOPER_DIR=/Library/Developer/CommandLineTools GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -tags vibermate_native_secrets ./...
DEVELOPER_DIR=/Library/Developer/CommandLineTools go run ./cmd/repositorycheck
DEVELOPER_DIR=/Library/Developer/CommandLineTools go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 -tags vibermate_native_secrets ./...
```

- [ ] Flutter 执行 pinned SDK 验证、format/analyze、全量 tests 与 Chrome reader tests；Paseo 执行 `npm ci --ignore-scripts --no-audit --no-fund`、typecheck、9 项既有测试及受影响新测试；发布工具/Actions pins 使用原 Makefile 中的固定 checks。
- [ ] 在临时 SQLite 和合成流量上运行至少 60 分钟受控 soak。每 30 秒采集不含正文的 heap、队列、map、WAL、记录/失败计数，关闭/打开 reader、切换捕获并发，验证留存清理与工作内存趋势。监控等待以不超过 60 秒的片段推进并保持用户更新。
- [ ] 用可控 clock 验证 10 天 Session 边界、隐藏/唤醒后的续期、401 重新授权重试；用已拥有的故障 seam 验证磁盘不足、write/rename/sync 失败、进程异常退出、备份恢复。源码与适用平台不同的结果不能相互替代。
- [ ] 保存测试时长、数据量、p50/p95、预算与未覆盖项。实际 60 分钟 soak 与时钟推进 10 天是不同证据，报告按实际内容标注。
- [ ] 若出现新破坏，回到对应任务 root-cause/TDD/review 环，不跳过门槛。

## Task 10: 整体审查与修复版交付

**Files:** `ui/flutter_app/pubspec.yaml`；`docs/capability-support.md`；`docs/module-map.md`；`docs/releases/v0.1.24.md`（冻结前核对版本）；现有 signing/notary/evidence tooling；Homebrew tap 的 `Casks/vibermate.rb`。

**Interfaces:** 一个冻结源码版本 → CI、同源二进制、签名/公证/安装证据、GitHub release 和 Homebrew checksum。

- [ ] 对 merge-base..HEAD 生成完整 review package，交给最高能力的独立 whole-branch reviewer，包含所有 minor/ruling/未完成项。必须收敛未解决 P1/P2 与 R1–R7。
- [ ] 核对远端版本；预期下一 patch 为 0.1.24+26。生成 release notes，准确写图片/CA/客户端范围及 Server+CLI 配套升级要求。
- [ ] 冻结并提交源码，运行该 SHA 的现有完整 CI 与 Developer ID candidate/sign/notary/installed acceptance 流程。使用既有配置的发布凭据与保护规则，产物 hash 与 provenance 必须一致。
- [ ] 按本会话既有 GitHub/Homebrew 发布要求推进 PR、合并、tag/release、上传及 tap 更新；发布后独立下载核对版本/hash，验证实际 brew metadata。任何外部机制要求新的权限时报告具体资源和已准备结果。
- [ ] 最终状态包含：七项关闭证据、候选验证、发布 URL、tap commit/checksum、精确支持版本、真实未覆盖范围及全部 ruling 的影响。未完成实际门槛时，goal 保持未完成状态。

## 本 goal 的进度记录

| 项 | 当前状态 | 完成证据 |
| --- | --- | --- |
| 设计与三条详细计划 | 已自检并执行 | 本文件及三个子计划/恢复账本 |
| 隔离基线 | 已建立 | fix/production-readiness-20261003；seed b7a9ed0 |
| R1–R7 | 已修复、独立复核通过 | 各子计划 red→green + review；core source 2cd4c53 |
| 真实客户端与 CA | 未完成本次验收 | 精确版本/平台 reports |
| 全包与持续运行 | 未完成本次验收 | 最终 SHA 的结果 |
| 新版本交付 | 未发布 | GitHub release + Homebrew |
