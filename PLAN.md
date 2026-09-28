# ViberMate 当前实施计划

当前 goal：统一完成账号出口与 OAuth 刷新策略、账号界面、启动上下文、完整用量汇总和运行概览，保持 v1，完成回归、规模验证与独立候选 App。

任务状态以 official-plane 的 ViberMate 项目 `779ab6f7-24dd-49ca-9b01-d8be8ef20a51` 为准：总任务 **VIBERMATE-37**，子任务 **38–47**。本文件是仓库内实施索引，不代替 Plane。

详细实施与验收：[账号、启动上下文与统计统一改进](docs/plans/2026-09-28-account-context-usage.md)。
审查依据：[启动上下文与用量性能 review](docs/reviews/2026-09-28-launch-context-and-usage-review.md)。

最新约束：只保留统一的当前 v1 契约，不为旧版本增加双读双写、别名或缺字段回退。用户已确认：备份后一次性离线转换现有数据，运行时只支持新结构，不清空账号、凭据或统计。整体实现后进行架构回顾和真实浏览器 Web 模式端到端验收；通过后才发布 Release 与更新 Homebrew。

发布准备：下一发行版为 `0.1.16+18`，高于已安装的本地故障修复 build 17；数据/API 契约仍为 v1。发布说明包含必须先停止、私有备份并离线转换的限制。代码冻结、远端 CI、正式签名／原生包验收及 Release/Brew 尚未完成，当前不替换正式 App。

| 阶段 | 内容 | 状态 |
| --- | --- | --- |
| 1 | 账号通用出口方案及 OAuth 自动刷新开关 · VIBERMATE-38/39 | 六类操作 SOCKS 与修订诊断通过；实际 Web 设置保存/读回、导入默认关闭与显式开关、代理失败不直连已验证 |
| 2 | 可用账号排序、倒计时、用量、刷新、按钮与套餐标签；调整试跑入口 · VIBERMATE-40 | 实际 Web 额度成功读取、重置正序、阈值颜色、单账号/组刷新与启用保持通过；紧凑自动刷新列头/开关通过中英深浅、桌面/393px |
| 3 | Run/ACP 统一启动上下文与稳定 Git 项目身份 · VIBERMATE-41 | 已共用一次创建上报；远端身份与本地克隆隔离、净化、重命名/worktree/跨机器归并测试通过，继续集成验收 |
| 4 | 完整汇总、按需分页、读写隔离及刷新性能 · VIBERMATE-42/43 | 百万条精确计价、30,000 长名称分组、可中断续清通过；补齐 6 个正文外键索引，10,000 条过期正文/原始证据清理约 1.06 秒 |
| 5 | 有统计意义的运行/会话概览、项目→成员→模型报表 · VIBERMATE-44 | 实际 Web 运行/会话、团队下钻、成员隔离通过；新候选冷启动无正文默认概览、59 模型 50＋9 页、刷新保持页签复验通过 |
| 6 | 跨模块回归、规模与布局验证、独立候选 App 与发布门禁 · VIBERMATE-45 | 真实完整私有目录已备份并独立转换，原库未变；203 次实际 Web 及最新样式候选通过，Flutter 671 项通过/16 条条件跳过；未切换数据、安装或发布 |
| 故障跟进 | 记录降级与代理故障分离、Runtime 首因、CLI 诊断 · VIBERMATE-46 | 主线实现与实际 Web 故障注入验收通过，已包含于独立候选；未安装，不包含在已交付 build 17 中 |
| 休眠恢复 | 合盖／临时断网后保留 Agent 并续接 Capture · VIBERMATE-47 | 共用心跳重连与原监督者续租已实现；12 小时跳时、真实子进程 503/超时/429 恢复、ACP、远程撤权及 race 通过；新独立候选构建与验证通过，未安装 |

故障跟进依据与验证：[长期运行 CONNECT 503](docs/reviews/2026-09-28-connect-503-hotfix.md)。可选记录失败不再撤销代理就绪状态；核心出口审计失败仍停止代理，并显示首个安全原因和时间。此边界调整不代表 SQLite 写入与转发已完全独立。

新增休眠故障：[Capture 监督与续租](docs/reviews/2026-09-28-capture-sleep-recovery.md)。App 的 90 秒代理租约可由原监督者在醒来后续接；过期期间代理仍拒绝，撤权、结束、删除和登录失效不可恢复，不通过放大 TCP 超时掩盖问题。

运行概览验证：[完整范围统计与实际 Web 验收](docs/reviews/2026-09-28-capture-overview-review.md)。无正文默认显示统计，有正文保留请求视图并可进入概览；请求分页不改变总量。请求记录与用量采集分别说明覆盖和更新时间，不把缺失用量显示成零。未替换或启动 App，最终候选包及发布门禁仍未完成。

此前已完成的流式响应、Codex 会话与恢复、终端清理、运行列表时间、持续 503 等修复保留并纳入相关回归，不重复重做。历史设计与冻结依据保留如下。

离线转换入口：[操作说明](tool/convert-v1/README.md)。工具复用既有目录锁和逐文件验证复制，原目录不变；完整私有备份与新目标分离，逐表核对记录值和数量。它不是普通的去凭据备份导出，不会自动选择新目录或启动 App。

最新独立验证候选为 `dist/candidates/local-OaOh98uf/ViberMate.app`，包含紧凑自动刷新列样式，已通过包校验和实际 Web；不是安装/发布产物。本轮转换／候选包证据：[离线转换 review](docs/reviews/2026-09-28-offline-conversion-review.md)。完整私有源、备份和新目标分离；6 个账号、64 条运行、6,529 条用量及所有保留证据完成逐字段与数量核对，源与备份哈希一致。仅转换成功，未修改 App 数据目录选择或 Keychain；若旧 Runtime 再写入，切换前须重新备份转换。

包内实际 Web：203 次合成请求；本次运行 183／同会话 188；共享项目 15（Alice 10、Bob 5），下钻和小计正确；59 模型分页、无正文默认概览、手动页签保持、OAuth 开关与出口读回、拒绝代理不直连且 Runtime ready 均通过。前轮已验成员隔离；新候选三个 OAuth 与一个 API-key 混合布局，中英深浅/桌面/393px、开关保存后刷新保持均通过。另以相同生产 Server 组合和包内 Web、仅进程内信任的合成 TLS 上游验证额度成功、排序、阈值颜色及刷新；未修改正式程序、系统信任或真实账号。最终原生切换和发布仍待完成；测试页和夹具已停止，没有提前发布。

规模验收：[到期清理与维护预算](docs/reviews/2026-09-28-usage-maintenance-review.md)。百万条旧清理事务约 10.34 秒，超过 5 秒维护预算并会误报存储不可用；现在每平面 1,000 条分批提交、可取消续清，真实数据库错误仍报告，手动清理保持原子 receipt。补齐外键引用索引后，正文首批约 180ms、10,000 条过期正文和 raw 全部约 1.06 秒，保留共享活跃证据。全 Go、vet、结构、converter 和定向 race 通过；不声称正文全局 GC 为恒定成本。

## Historical foundation: Environment-first Production Vertical

This is the historical foundation plan. The current deployment/settings work is
tracked in [Runtime setup and trust experience](docs/plans/2026-09-21-runtime-setup-and-trust.md).

Status: managed Anthropic first-use vertical frozen; deterministic packaged evidence passed

## Goal

Ship the first honest Desktop vertical around two product authorities:

- an Environment is an immutable revisioned configuration aggregate; and
- a Capture is a running source with one independently switchable Environment
  assignment.

The retired Access/Profile product model is not retained through aliases,
dual writes, compatibility readers, or legacy database migrations.

## Required vertical

1. `system_transparent` is always available and performs blind forwarding with
   body-free connection and egress evidence. It never receives a local Root,
   parses semantic content, invokes plugins, or rewrites credentials.
2. A custom Environment owns exact ClientEndpoints, ProtocolPlans, Routes,
   account references, policy bindings, and egress selection. Draft, impact
   preview, and CAS publication form one atomic authority path.
3. `vibermate run --env <id> -- <agent>` and Manual Capture create a typed
   Capture assignment. The launch boundary freezes the Environment revision,
   digest, protected origins, and managed-credential origins.
4. Every admitted request freezes
   Environment -> ClientEndpoint -> ProtocolPlan -> Route -> ProviderAccount
   references. A later publish or Capture switch cannot rewrite an in-flight
   request.
5. Compatible assignment changes are hot; protocol-sensitive changes drain
   affected connections; authority expansion requires a new Capture launch.
6. SQLite is the durable authority for Environment revisions, Capture
   assignments, ProviderAccount configuration, activities, approvals, and
   launch boundaries. Secret bytes remain exclusively in SecretStore.
7. ProductRuntime, Desktop Control API, CLI, and Flutter consume those same
   typed authorities. No UI projection invents missing values or reconstructs
   historical evidence from current configuration.
8. A Desktop-managed Anthropic API-key account is selected explicitly by one
   managed Route. Core removes ambient client authentication and injects only
   the frozen account lease at the final provider boundary; failover remains
   off. Request evidence exposes the frozen target, Route, account revision,
   credential epoch, usage, Attempts, and terminal outcome without exposing
   the credential.

## Freeze gates

- all Go tests, race tests, vet, formatting, module integrity, and structural
  repository checks pass;
- the compact Flutter workbench passes analyzer, unit/widget, native host, and
  desktop/narrow packaged-App flows;
- the development App starts the production composition and exits cleanly;
- a clean committed candidate produces current deterministic packaged
  acceptance evidence bound to its exact App and sidecars; and
- current documentation, API names, locales, and generated artifacts contain
  no retired Access/Profile product authority.

## Explicitly deferred

- linked client-session account connectors, automatic account failover, and a
  retained external credentialed-provider report (the explicit private
  `0600` key-file acceptance contract is implemented but remains operator
  opt-in);
- plugin execution and the Language Bridge;
- quality evaluation and long-term usage/cost analytics;
- Server/LAN composition and remote enrollment;
- system trust-store mutation and Keychain;
- application-wide capture through Network Extension/TUN;
- signed/notarized distribution and Preview/Release claims.

Deferral keeps those seams typed; it does not permit placeholder success,
fabricated evidence, or fallback to the retired model.
