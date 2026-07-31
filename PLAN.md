# M0.9.1 Acceptance Evidence Correction Plan

Status: active
Created: 2026-07-31
Implementation baseline: `ede07ab1fd9663d0307e0eb57b0ed901a6c30b79`

## Objective

Correct three fixed-Codex acceptance-evidence defects without changing the
runtime, protocol, Access, Exchange, Hold, proxy, or provider object model:

1. require both the typed Codex HTTP-fallback event and the proxy's bounded
   426-to-HTTP connection audit;
2. stop describing Codex completion as multiple client-visible stream deltas;
3. report the actual approved Codex tool (`exec`) instead of Claude's `Write`.

Freeze a new clean packaged build and private v5 deterministic/credentialed
reports. The previous v4 reports remain valid for their underlying runtime
checks but are superseded for these three evidence claims.

## Required invariants

1. Production data-plane behavior remains unchanged. This plan may strengthen
   only the opt-in acceptance runner, its tests, evidence schema, and evidence
   documentation.
2. Fixed Codex fallback passes only after a trusted typed client event and the
   existing body-free proxy audit independently agree on the same behavior.
3. Report details derive from typed evidence returned by the exercised path;
   they are not unconditional client-neutral prose.
4. Codex evidence may claim completion through the Responses streaming path,
   but not multiple CLI/TUI deltas, per-token display, or successful WebSocket
   semantics.
5. Claude may retain the multiple-delta claim only while `deltas >= 2` remains
   an enforced assertion.
6. Tool evidence names the exact approved tool and remains bound to the durable
   allow-once decision and bounded post-approval proof file.
7. The report schema changes so machines cannot confuse pre-correction and
   post-correction evidence.
8. Reports remain mode `0600` and contain no prompt, response, thread ID, tool
   arguments, headers, credential value, or semantic payload.
9. No concrete model/provider observation creates a production branch.
10. The deferred OpenAI Chat Agent edge is not implemented in this plan.

## TDD execution

### 1. Bind fallback to two independent evidence sources

- [x] Add a failing test that rejects connection-audit-only evidence.
- [x] Call the existing trusted Codex `waitForHTTPFallback` boundary.
- [x] Run the proxy connection audit only after the typed client event.
- [x] Return typed evidence with separate client-event and connection-audit
  fields, and reject either field missing.
- [x] Keep the dedicated fallback invocation WebSocket-capable; do not force
  HTTP in order to make the assertion pass.

### 2. Make report detail match exercised evidence

- [x] Add a failing table test for Claude `Write` versus Codex `exec`.
- [x] Return the proven tool name from the approval exercise and generate the
  report detail from that typed result.
- [x] Add a failing test that forbids multiple-delta/token/TUI wording for
  Codex.
- [x] Preserve Claude's multiple-client-delta wording only behind its existing
  `deltas >= 2` assertion.
- [x] Describe Codex Hold completion only as traversing the Responses streaming
  path.

### 3. Version and test the evidence contract

- [x] Add a failing report test for schema v5.
- [x] Bump the private acceptance report schema from v4 to v5.
- [x] Pass focused unit and race tests for the acceptance package.
- [x] Update acceptance documentation, module evidence, and the superseded v4
  archive boundary.

### 4. Freeze corrected packaged evidence

- [ ] Commit the narrow implementation and evidence-contract change.
- [ ] Build App, daemon, launcher, and acceptance runner from one standalone
  clean source revision with pinned toolchains.
- [ ] Run deterministic acceptance and retain one private mode-`0600` v5
  report.
- [ ] Run credentialed acceptance and retain one private mode-`0600` v5 report.
- [ ] Verify both reports bind the clean source and exact artifact digests.
- [ ] Verify the fallback detail states both evidence sources, tool detail says
  `exec`, and Codex Hold detail contains no multiple-delta/token/TUI claim.
- [ ] Run a literal safe scan over both reports and retained logs.

### 5. Final gates

- [ ] `make check`
- [ ] `go test ./...`
- [ ] `go test -race ./...`
- [ ] `go vet ./...`
- [ ] `go mod tidy -diff`
- [ ] `go mod verify`
- [ ] fixed `govulncheck ./...`
- [ ] pinned frontend and Rust checks
- [ ] honestly report the unchanged RustSec warning set
- [ ] `git diff --check`
- [ ] clean tracked source and matching v5 provenance

### 6. Restore product-first execution order

- [x] Move the OpenAI Chat Agent edge plan to `docs/plans/deferred/` without
  claiming it is obsolete.
- [ ] Audit the current implementation against the M1 delivery boundary in the
  read-only design.
- [ ] After this plan closes, create a narrow Proxy/Trust Foundation successor
  before any additional protocol-width expansion.

## Completion statement

This plan is complete only when evidence supports:

> One clean packaged fixed-Codex build emitted its trusted HTTP-fallback event
> and independently produced the bounded proxy 426-to-HTTP audit; its actual
> `exec` tool remained behind durable allow-once approval; and its held request
> completed through the Responses streaming path without claiming unobserved
> CLI/TUI delta behavior. Both private v5 reports bind the same clean source and
> contain no secret or semantic payload.

The underlying M0.9 runtime implementation remains frozen. This correction
does not make VibeMate Preview-ready or Release-ready.

## Archive protocol

After every checkbox and the completion statement are proven:

1. move this plan under `docs/plans/archive/`;
2. record the frozen source, artifact, report paths, hashes, and warning set;
3. replace the root plan with the Proxy/Trust Foundation gap-closure plan;
4. keep protocol-width expansion deferred until that product boundary is
   reviewed.
