# Owned Account Egress Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development for this bounded Task6 increment. Start only after the current terminal task is independently reviewed and committed. One source/test owner; root owns Git.

**Goal:** Make existing owner quota/history/reset operations use their selected exit through actual sends and Offline Hold recovery, without replaying a reset or changing an in-flight selection.

**Architecture:** Preserve the existing Runtime → accountoperation.Reader → provideraccount.Manager lease → providertransport pipeline. Correct only the closed owned-operation reachability target that currently conflicts with the Runtime's strict Auxiliary TLS prober. Keep sender/audit/account identity separate from origin-only TLS reachability. Use existing private Runtime transport/prober seams for contained real-network proof.

**Tech Stack:** Current pinned Go1.26.9 at `/tmp/vibermate-go-security.JcRyfL/go/bin/go`, current locked modules and temporary SQLite Store; private strict TLS/SOCKS fixtures. No dependency, schema, UI, permission or public API change.

**Spec:** Approved `docs/superpowers/specs/2026-10-05-global-default-egress-design.md`; existing Task6 in `2026-10-05-global-default-egress.md`; current phase sequence in `2026-10-09-production-continuation.md`. The source-backed gap and retained evidence inventory are `.superpowers/sdd/2026-10-05-global-default-egress/phase-b-remaining-audit.md`. This is execution of the approved design, not a new proxy architecture or a new release prerequisite outside that scope.

## Global Constraints

- Independent owned operations have account proxy / account Direct / global proxy / unset builtin Direct layers (AP/AD/G/B), not an invented flow. Explicit choice wins; failure never changes exit. Freeze once in the real Manager lease before Hold/DNS/credentials/audit.
- Use actual Runtime/account Reader/Manager/Store and authenticators. Hand-typed AccountRef, fake success prober, observed-policy-only transport or next-lease metadata cannot prove the real operation's network path. Private production transport adapters remain honestly labeled, not installed App/fingerprint qualification.
- Test network permits only synthetic `chatgpt.com:443` mapped to isolated listeners and exact synthetic proxy addresses. Use private roots with matching hostnames. Reject unexpected DNS/dials before any RED; never contact real ChatGPT, use real credentials or redeem a real credit.
- Do not loosen originaltransport.TLSProber, runtimeResumeProber, purpose/kind mappings, target/private-peer admission, account authorization, history permission or final authenticator. Do not change sender wire-plan/audit identity or reconstruct authority from stored audit rows.
- Owned read and reset producers only: `PurposeUpstreamAccountRead` and `PurposeUpstreamAccountAction`. Captured `PurposeRouteOperation` remains separate and must not be silently generalized into this correction. The broader captured family remains required by the parent Goal, not claimed complete here.
- Keep the three existing recognized ChatGPT origin forms and the exact service paths declared by upstreamservice. A provider base path is not part of TLS network origin; the reachability identity is canonical `https://chatgpt.com`, authority/SNI `chatgpt.com`, Auxiliary/strictTLS, PlanRevision0, empty PlanDigest, TargetAccess0, with the already-frozen policy.
- Preserve original purpose, account/credential epoch/settings, target reference and immutable initial/terminal audit of the actual HTTP operation. A probe sends no HTTP, authorization header, body or reset command.
- Owner quota may enrich available-reset details with an additional GET; assert the actual operation's literal counts, not universal one-request behavior. Reset details and consume share one lease/selection. Cancellation, failed probe or closed proxy must drain, cause no direct fallback or extra token rotation, and never replay consume.
- Preserve original worktree, running App/Codex/9666, user data/trust/proxy and generated cache. No version/release/kernel rebuild or unrelated full test campaign. Do not reimplement already reviewed managed/token, metadata, update or global UI units.

## Task 1: Owned operation selection and concrete held recovery

**Files:**
- Modify `internal/providertransport/runtime_fetch.go` only for closed owned probe-target selection; a small private `owned_account_probe.go` is permitted if it keeps that validation readable.
- Add `internal/providertransport/owned_account_probe_test.go` for real producer → concrete TLS-prober and closed-shape negatives, following the existing token/metadata contract fixture pattern.
- Add `internal/productruntime/owned_account_egress_test.go` for real Runtime/Reader operations. Reuse `egress_network_test.go`'s `runtimeContainedProvider`, `runtimeSOCKSFixture`, `runtimeBuildOverrides`, exact-network builder and SQL audit patterns; do not grow its unrelated managed matrix.
- Existing `account_global_integration_test.go`, `account_egress_integration_test.go`, `accountoperation/reader.go`, `upstreamservice/account_reads.go` and `account_reset.go` are behavior/fixture references, not invitations to rewrite them. Any required production edit outside the first item must be justified by the observed failure before changing it.

**Interfaces:** No public signature changes. Exercise `Runtime.AccountReads().ReadOwned(ctx, accountID, operation)` and `RedeemOwned(ctx, accountID, expectedRevision, creditID)`, existing account creation/settings and default managers, `EnterOfflineHold`/`ResumeOfflineHold`, and actual audit reads. Production `runtimeProbeTarget(runtimeFetchSpec) (offlinehold.ProbeTarget, error)` consumes the already validated managed credential/account and frozen resolved exit; no independent resolver is added.

- [ ] **Step1 — contained behavioral RED.** Build the real Runtime with its existing private concrete provider and original-TLS-prober builders pointing at the same contained network. Create a synthetic Codex OAuth account using the real Store/Manager. Set global proxy, enter Hold, start `ReadOwned` for `upstreamservice.CodexRateLimits`, and wait for the actual queued target rather than sleeping to choose a winner. Change default only after that observation. Call actual Runtime Resume; current producer supplies nonzero runtime plan/account TargetRef and concrete TLS prober rejects it before sockets. Record this exact failure, zero HTTP sends and successful cancellation/join/drain. No compile/setup error counts as RED.
- [ ] **Step2 — narrow producer correction.** Recognize only the two owned purposes with no captured plan, a genuine valid managed credential/account matching the owned target reference, and the existing closed read/reset request shape. Read allows only GET/no body and the service's quota, history or reset-details paths; reset remains the existing validated Codex OAuth POST to `resetConsumePath` with its existing command body and content type. Use the existing `upstreamservice.ResolveRead`, origin predicate and reset validators where needed; do not create a second account authority or generic arbitrary-URL exception. Produce the canonical origin-only reachability target described above from that validated operation and frozen policy. Leave actual sender body/credential/audit/runtime wire plan unchanged. Altered origin/port/base path, unknown/query path, wrong method/body form, missing/invalid/nonmanaged credential, wrong owned reference or captured scope must not gain this branch. Retain existing token/metadata/update negatives.
- [ ] **Step3 — real selection and lifecycle evidence.** Use a table with literal AP/AD/G/B expectations and conflicting lower layers. For each layer drive actual owner quota (including reset-detail enrichment), history and explicit reset through Reader, not direct transport calls. Assert exact destination method/path/credential, proxy versus direct socket counters and initial/terminal audit selection. Record each operation's expected GET/POST counts independently. Exercise held quota/history/reset, edit default while queued or between reset details and consume, and prove the old operation stays on its original exit while a new independent operation uses the new one. Cover cancellation while held, concrete failed TLS/proxy probe, selected proxy lost before send, and loss after reset details/before consume with zero successful consume and no direct retry. A synthetic server that accepts consume then drops the response must observe exactly one POST and return the existing unconfirmed result, not automatic redemption retry. Join/cancel all operation goroutines and prove existing gate/lease resources drain.

The invariant assertions use independent expectations, for example:

```go
if proxyTargetDials != wantProxyDials || directTargetDials != wantDirectDials {
    t.Fatal("owned operation did not use its frozen selected exit")
}
if terminal.Decision() != started.Decision() {
    t.Fatal("owned operation terminal audit changed its initial selection")
}
if consumePOSTs != 1 {
    t.Fatal("a single explicit redemption was missing or replayed")
}
```

These illustrate the assertions inside the new fixture, not additional production variables/APIs; derive each `want` from the named table case, never from the observed audit or selected policy. Probe connections are counted separately from HTTP sends.

- [ ] **Step4 — scoped verification and freeze.** Name new tests `TestOwnedAccountProbe...` and `TestActualRuntimeOwnedAccount...` so the exact commands below cover the new unit. Run RED alone, then its GREEN, then final selected normal/race and unchanged consumer checks. Enumerate selected tests in the report. Keep existing probe/operation negative controls; any test/source change after a run requires only its affected covering rerun. No repeat of the full historical global matrix.

```sh
/tmp/vibermate-go-security.JcRyfL/go/bin/go test ./internal/providertransport ./internal/productruntime -run '^(TestOwnedAccountProbe|TestActualRuntimeOwnedAccount)' -count=1 -v
/tmp/vibermate-go-security.JcRyfL/go/bin/go test ./internal/providertransport ./internal/productruntime -run '^(TestOwnedAccountProbe|TestActualRuntimeOwnedAccount)' -race -count=5
/tmp/vibermate-go-security.JcRyfL/go/bin/go test ./internal/accountoperation -count=1
/tmp/vibermate-go-security.JcRyfL/go/bin/go test ./internal/providertransport -run '^(TestTokenProbeClosedProducerAndConcreteTLSReachability|TestMetadataProbe|TestOwnedResetDetailsAndConsumeKeepOneGlobalSelection|TestUnselectedOwnedAccountUsesGlobalSOCKS)' -count=1 -v
git diff --check
```

Record exact BASE, before/after hashes, private transport qualification, first failure, final commands/exits/output and operation-specific matrix in this plan's SDD report. Root performs scoped independent spec/security/quality review and commits; the parent global Task6 remains open for its other already-listed families.

## Self-review

| Boundary | Check |
| --- | --- |
| Manager lease → Reader details/consume | One resolved selection; no reacquire after default edits |
| Owned probe → concrete Runtime dispatcher | Existing Auxiliary strictTLS identity, no semantic-plan erasure for captured operations |
| Probe → HTTP sender/audit | Only reachability loses runtime wire-plan metadata; send authority/account/audit remain original |
| Fixtures → actual behavior | Real account selection, socket paths, HTTP bytes, audits and drain; no mock-policy substitute |
| Current terminal task → this task | Separate files and sequential source ownership; no test overlap or early dispatch |

This unit fills the owned-operation cells of the existing accepted matrix. It does not complete manual OAuth, endpoint models, captured/original/native/blind families or the whole production Goal, and does not reopen their already-proved cells.
