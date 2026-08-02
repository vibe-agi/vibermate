# M1.0-C0a Step 1: 让那个拒绝真的被触发一次

Status: active
Created: 2026-08-02
Implementation baseline: `b7ea03d`
Design authority: `vibermate-design@336909b` — `docs/design/12-implementation-readiness.md:95-112`
Predecessor: `docs/plans/archive/2026-08-02-m1.0-c0l-release-path.md`

## 现状

设计把 M1.0-C0a 定为三步。第 2 步（P0 安全切点）已完成：`AllowsOriginalOrigin()`
只对 `none`/`control` 返回真，`count_tokens` 已分类 `client_semantic`，正文不可能
被以客户端自己的凭据发往原站。

第 1 步没有完成。`docs/evidence/2026-08-02-m1.0-c0a-fixed-client-probe.md` 诚实记为
`not_observed`：两个非交互场景里 `count_tokens` **从未被请求**，所以那个 422 拒绝
从未被动态执行过。当时的结论来自对 bundle 的静态阅读（catch → log → return null，
`maxRetries: 1`）。

而且那次探针用「本地 HTTP server 冒充 AgentEndpoint」，没有 CaptureRun、没有 MITM、
没有本地 Root。现在这三样都有了，真实 Claude Code 端到端也已跑通。

## 这一片要做什么

把那条证据从「静态推断 + 未触发」升到「真运行时里真的发生过一次」。

设计原文（`12-implementation-readiness.md:97-98`）：**「记录客户端是继续本地估算、
降级还是中断；不预设结果」**。所以本片的成功标准不是「客户端没崩」，而是
**观察到并如实记录**。

## 不变量

1. 观察不预设结果。如果客户端中断，就记 `blocked` 并给出明确原因，不为了让结论
   好看去改判据。
2. 拒绝必须发生在真运行时里：真 CaptureRun、真 MITM、真本地 Root、真连接策略。
3. 无论触发与否，正文都不得流向原站——这是第 2 步已建立的性质，本片顺带回归它。
4. 触发不了就诚实记「仍未触发」，并写清尝试过哪些路径。不把「没触发」写成「通过」。

## 顺序

- [ ] 在真运行时里断言 `count_tokens` 收到 422 + `X-Vibermate-Reason:
      profile_operation_unsupported` + Anthropic 错误信封
- [ ] 记录真实客户端在该运行时里请求了哪些 operation（含是否请求 count_tokens）
- [ ] 尝试触发 count_tokens：大上下文、显式 compaction、以及静态读指出的调用点
- [ ] 回归：拒绝路径下正文不进入任何 buffer / log / record / 原站
- [ ] 按实际观察更新证据文档；触发不到就写清尝试与边界
- [ ] 第 3 步：把 C0a 状态冻结（`count_tokens` 只允许 local | estimated |
      unsupported，不实现 profile_endpoint，不夹带 Language Bridge）

## 门禁

`gofmt -l .`、`go vet ./...`、`go test -count=1 ./...`、`go test -race -count=1 ./...`、
`go run ./cmd/repositorycheck`、`make check-release-build`、`go mod tidy -diff`、
`pnpm --dir ui/desktop run check`、`git diff --check`、工作树干净。

## 完成陈述

> 那个 422 在真运行时里被真实客户端触发过（或有据可查地触发不到），客户端的反应被
> 如实记录，而不是从 bundle 里读出来的。
