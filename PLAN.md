# Production Composition Structural Guard

Status: complete
Created: 2026-08-02
Implementation baseline: `6a88a00`
Predecessor: `docs/plans/archive/2026-08-02-signed-client-identity-hardening.md`

## 现状

审计结论（只读，已复审修正）：

- Desktop production chain 当前**事实上**唯一：
  `cmd/vibermated` → `desktopdaemon.ProductionOptions`/`Run` → `desktophost.Start`
  → `productruntime.Start` → `productionBuilders()`。
- 但这是事实，不是被机器守住的性质。没有任何门禁阻止第二条路径、测试 builder、placeholder
  或 development 依赖混入发行路径。
- 历史 packaged evidence 存在且强（`c19cca4` 的 deterministic 17/17 与 credentialed 25/25，
  0600，digest 与归档一致），但**只绑定 c19cca4**，当前 HEAD 已前进 85 个提交，且没有任何
  consumer 要求「当前 commit 必须有 passing report」。

## 这一片要做什么

**只做源码形状的守卫。** 用 Go AST 建立 `CheckProductionCompositionBoundary`。

不构建 App、不运行 acceptance、不实现报告 verifier、不接 recognized client、不改设计仓。

## 不变量

1. `cmd/vibermated` 只能经 `desktopdaemon.ProductionOptions` 与 `desktopdaemon.Run` 启动产品；
2. Desktop main 不得直接 import 或调用 `desktophost` / `productruntime`；
3. `desktopdaemon.Run` 必须调用 `desktophost.Start`；
4. `desktophost.Start` 必须调用 `productruntime.Start`；
5. `productruntime.Start` 必须选择 `productionBuilders()`；
6. 新的非测试 `ProductRuntime` caller 只能进入明确审查过的 Host composition allowlist；
   当前只有 DesktopHost，将来 ServerHost 需显式扩展。

每条规则都必须有 public `Check` 路径下的 known-good / injected-bad fixture——不是手工变异，
因为手工变异只证明写它那天有效。

## 门禁

`gofmt -l .`、`go vet ./...`、`go test -count=1 ./...`、`go test -race`（触及包）、
`go run ./cmd/repositorycheck`、`make check-release-build`、`pnpm --dir ui/desktop run check`、
`git diff --check`、工作树干净。不 push。

## 完成陈述

> 当前 Desktop production composition 的源码形状由 CI 守住；**这仍不证明当前 commit 的打包
> 产物运行通过。**
