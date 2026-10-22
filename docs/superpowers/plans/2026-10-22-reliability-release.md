# Reliability Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans. Work task-by-task; preserve RED/GREEN evidence and independent review.

**Goal:** Repair the reviewed reliability/client compatibility defects and publish a coherent verified v0.1.36 release.

**Architecture:** Retain shared Runtime authority and transport boundaries. Repair client/service adapters and optional inspection lifecycle at their existing seams; test real serialized interfaces across Go and Flutter.

**Tech Stack:** Go, Flutter/Dart, native Codex, Node/Python packaging, SQLite, pinned VLESS kernel, GitHub Actions.

**Spec:** `docs/superpowers/specs/2026-10-22-reliability-release-design.md`.

## Global Constraints

- Work in `/Users/null/.codex/worktrees/fbde/vibermate`, branch `codex/reliability-release-0.1.36`; preserve the user's original checkout and unrelated `.superpowers/diagnostics/`.
- Respect every invariant in the spec. Do not weaken privacy/credential/egress checks to make tests green.
- Use production public seams and synthetic temporary state. Never read real credentials or send synthetic tokens to real providers.
- One implementation subagent at a time; independent source review, fixture research and parent-owned toolchain work can overlap. Only one Go-heavy test/build runs at a time; coordinate with the parent.
- Parent owns Git staging/commits/integration on the shared worktree. Agents edit only assigned files and report exact paths; no Git index or branch mutation by agents.
- Current native baseline is Codex 0.163.0. Keep Claude and generic transport coverage. Go release floor is 1.26.10, with matching kernel build receipts.
- Existing audit fixtures live in `/Users/null/Downloads/vibermate-codebase-review-20261022` and `/Users/null/Downloads/vibermate-web-detail-review-20261022`.

### Task 1: Web/Desktop detail contracts and controller state

**Files:** `ui/flutter_app/lib/core/api/control_models.dart`, `control_api.dart`, `features/workbench/workbench_controller.dart`, `conversation_reader.dart`, relevant API/controller/widget tests; add a small content-cursor helper only if needed for a single shared rule.

**Interfaces:** Public `HttpControlApi.exchange`, `exchangeContentPage`, `ExchangeDetail.fromJson`, `WorkbenchController` and conversation reader. No Go production mutation.

- [ ] Promote the external actual-server JSON/continuation and reader/allowlist repros into permanent focused tests. Obtain the same expected RED at these seams; use synthetic fixtures from actual production inspection readers, not a new hand-invented cursor.
- [ ] Share bounded content-cursor syntax between incoming DTO and outgoing request validators. Accept the existing single component and current two-component Base64URL syntax, never whitespace/extra separators/empty components or more than 2048 characters; keep the value unchanged.

```dart
// The server remains the authority for proof verification.
final pattern = RegExp(r'^[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)?$');
```

- [ ] Refresh incomplete/pending cached details when reopening against newer Activity evidence; propagate `contentAvailable` changes through polling and row updates. Keep bounded loads and stable scroll; do not infer terminal success from Capture lifecycle.
- [ ] Fence old-selection detail failures the same way as existing inspection reads, preserving global invalidation for actual source/privacy conflicts.
- [ ] Fence stale allowlist reads against acknowledged saves and later read generations. Preserve conflict reconciliation and disposal behavior.
- [ ] Surface a safe category/code for a failed request-detail read, distinguishing transport/control failure from contract decoding, without exposing raw responses or secrets.
- [ ] Run focused API/reader/controller tests and analyzer, inspect the diff, and report RED/GREEN commands, outputs and files. Parent commits this task after review preparation.

### Task 2: Stable native Codex startup and proxy ownership

**Files:** `internal/runlauncher/codex_provider.go`, `codex_isolation.go`, `invocation.go`, `launcher.go`, relevant tests; `internal/operationcatalog/catalog.go`, `internal/upstreamservice/account_reads.go`, native account acceptance tests and exact auxiliary-operation contracts.

**Interfaces:** Preserve effective managed provider/base/auth overrides across native subcommands, ordinary login identity, and original enterprise workspace bootstrap. Shared launcher changes must retain Claude behavior.

- [ ] Turn the external parser/config-scope/Enterprise-vs-Plus probes into permanent regressions. Confirm native 0.163 drops prefix configuration when later `-c` occurs, current parser rejects the Cyber option, and cloud bundle is blocked before original transport.
- [ ] Normalize config override placement/collection without changing intentional user precedence or reserved managed values. Validate via actual native `config/read` and public launcher behavior, not argv substring assertions alone.
- [ ] Admit official `exec --cyber-access-program` scope/value forms while preserving unknown remote/shared invocation rejection.
- [ ] Add the exact bodyless official workspace configuration read, retaining original native workspace identity and selected egress. No arbitrary paths, caller-chosen URL, or blank-policy fabrication. Verify server-side valid enterprise bundle and Plus skip controls with an isolated native client.
- [ ] Reproduce native bootstrap system-proxy fallback using isolated route fixtures; constrain capture-owned native requests to their injected proxy while honoring trusted native requirements. Cover configured/unknown feature handling on supported older versions.
- [ ] Run focused launcher/catalog/account acceptance and generic/Claude regressions; report effective configuration and zero unintended network calls. Parent owns CI pin updates and final integration.

### Task 3: Recording maintenance, diagnostics, and summary conflicts

**Files:** `internal/productruntime/recording_cleanup.go`, `recording_maintenance.go`, `recording_storage.go`, status/inspection diagnostics; `internal/runtimepersistence/recording_cleanup.go`, capture/summary inspection boundaries and focused tests when the cause warrants them.

**Interfaces:** Public Runtime recording/status/control HTTP and persistence cleanup APIs. Coordinate any DTO/localized UI extension with Task 1 owner/parent.

- [ ] Promote the zero-expiry VACUUM and busy-cleanup failure probes into public-seam regressions. Verify RED: unchanged retained archives are rewritten and typed failure cause is lost while recording remains sealed.
- [ ] Identify no-work/per-source cleanup work before compaction. Avoid rewriting untouched sources while retaining all physical-removal, late-writer and completion proof obligations.
- [ ] Preserve a redacted typed maintenance failure with phase and recovery status. A transient syscall recovery alone must not reopen readers/writers; use the existing durable claim/recovery/certification path for any in-process recovery.
- [ ] Reproduce summary 409 across fresh and upgraded archives, capture observations, source changes, expiry and concurrent routine cleanup. Fix a demonstrated false conflict at its source; keep real visibility conflicts fail-closed. Add bounded safe phase diagnostics where a conflict cannot otherwise be attributed.
- [ ] Verify required control remains usable during optional recording failure, real deletion removes bytes, no-op cycles do not rewrite archives, and source-conflict tests still reject stale evidence. Report unresolved deployment-only facts explicitly.

### Task 4: Generic network recovery boundaries

**Files:** `internal/egressnetwork/doh.go`, `dialer.go`, `internal/vlessprofile/profile.go`, `internal/vlesscore` and isolated fixture tests only as justified; existing network recovery review.

**Interfaces:** Existing context-aware resolver/dialer/VLESS lifecycle APIs shared by all Agent clients.

- [ ] Add controlled tests for a healthy address family with the other DNS query stalled, parent cancellation, established-stream lifetime, and private-target validation across the complete answer.
- [ ] Repair reproduced cancellation/budget failures with bounded setup budgets and preserved selected exit. No direct fallback or automatic model-request replay.
- [ ] Verify individual connection closure during a stalled outer VLESS handshake and helper exit/restart behavior using a local pinned-kernel fixture. If the existing kernel already bounds the path, retain evidence rather than speculative process restarts.
- [ ] Validate changing immutable exit revisions affects new operations while active operations retain their selection; verify generic HTTP and Claude-shaped calls as well as Codex bootstrap.
- [ ] Run focused network tests/races and document actual limits for physical VPN/sleep/node availability.

### Task 5: Current model capability adapters and native matrix

**Files:** `internal/provideraccount/types.go`, `internal/upstreamendpoint`, `internal/upstreamservice/model_directory.go`, `internal/providertransport/cyber_programs.go`, `internal/exchange/cyber_access.go`, relevant tests; `.github/workflows/ci.yml` coordinated with parent.

**Interfaces:** Account-scoped model metadata read and per-account mode selection. Generic Responses endpoints must not inherit official OpenAI-specific cross-origin behavior.

- [ ] Add a failing exact-official-API-key capability fixture reflecting stable 0.163: native metadata at ChatGPT Codex models, inference at OpenAI API, exact account/model capability and existing identity scopes.
- [ ] Complete official API-key mode/catalog support at the service adapter boundary, preserving final-model verification and credential scope; keep unrelated/custom providers unchanged. Verify native Guardian derivation against supported upstream contract.
- [ ] Add bounded same-scope discovery request coalescing if concurrent reproductions show duplicate discovery; cancellation of a waiter must not cancel another caller's admitted work. Never treat failed discovery as a successful entitlement or switch accounts/exits implicitly.
- [ ] Add a separately pinned 0.163 native CI lane covering provider override scope, Enterprise/Plus bootstrap, CLI syntax, model-directory modes and HTTP fallback. Retain older supported baselines and non-Codex checks.
- [ ] Run relevant account/transport/exchange tests and native fixtures, report exact scope and limitations.

### Task 6: Patched toolchain and coherent release artifacts

**Files:** `go.mod`, `.github/workflows/ci.yml`, `internal/acceptancereport/schema.go`, `tool/vless-kernel/kernel-lock.json`, kernel build/package scripts/tests, Linux verification and release provenance fixtures; root owns this task.

- [ ] Use saved current CI advisory failure as RED evidence. Fetch and verify the official patched Go 1.26.10 compiler; update every executable compiler assertion/pin and matching tests together.
- [ ] Regenerate kernel build/package receipts and hashes using the patched compiler, keeping pinned source and required dependency patch integrity. Do not hand-edit a receipt to claim an unbuilt binary.
- [ ] Run existing kernel/package/acceptance contracts and both ordinary/tagged vulnerability scans. Verify actual binary/compiler identities.
- [ ] Include PR #66's reviewed exit revision UI via normal integration after checks can pass; do not bypass branch checks due to the known advisory.
- [ ] Prepare version 0.1.36/build 38 release notes around the final fixes and evidence. Do not claim the unresolved live summary cause or universal VPN availability is fixed without evidence.

### Task 7: Integration, review, and publication

**Files:** Final changed-source set, release notes/version metadata, established macOS/Linux workflows and distribution repository metadata.

- [ ] Produce task-scoped diff packages and independent spec/code-quality review; resolve important findings with covering tests. Record commits and remaining concerns in the plan ledger.
- [ ] Run appropriate integrated Go/Flutter/contracts/race/native checks once on the final source. Broad whole-branch review includes the repaired signed-cursor producer/consumer boundary and authority/recording separation.
- [ ] Create/update the integration PR, wait for exact-head CI, and merge using the established protected-branch flow. User explicitly authorized merge and release.
- [ ] Build signed/notarized macOS Desktop/CLI and both Linux Runtime/Web distributions from the exact release source, verify checksums/provenance and packaged acceptance, then publish the coherent release and distribution metadata.
- [ ] Verify public release assets and version metadata, record actual delivery links, final test evidence and any server deployment still requiring rollout. Preserve the evidence outside disposable test scratch.
