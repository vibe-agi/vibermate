# Native Acceptance Profile Composition Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans. Execute this bounded acceptance prerequisite with TDD and independent review before returning to the exact-client matrix.

**Goal:** Let the isolated native acceptance runtime exercise classified synthetic Review/Full content without adding a production classifier or weakening approval.

**Architecture:** Pass the existing typed `nativemcp.ContentProfiles` dependency through trusted Runtime construction. Only an explicitly tagged acceptance build can construct one fixed synthetic profile; normal product construction remains nil. Bind acceptance evidence to the actual source, tag, profile and executed artifact.

**Tech Stack:** Go 1.26.8, real ProductRuntime/SQLite/ToolDecision/control API, existing pinned native client harness.

**Spec:** `docs/superpowers/specs/2026-10-03-production-readiness-design.md`; this is an implementation detail of the already adopted synthetic Task 4 in `2026-10-04-native-mcp-transport.md` and the approved per-profile/default-OFF/retain-all-data boundaries, not a new production-tool permission.

## Context and adopted decision

Root fully read the 59-line read-only composition draft and inspected the actual catalog/profile, pending-preview, Runtime, fixture, management and provenance consumers at source `7558ddfae2b93052f65b599ed7dd1543c3168ae1`. Ordinary calls intentionally have no management preview when classification is unknown; authenticated AllowOnce refuses that case. Runtime currently supplies no ContentProfiles. Observed actual-client 160 failure at that boundary must stay in the evidence history.

Adopt the typed composition seam plus fixed tagged adapter. Reject a permissive exported profile constructor, name/annotation-only matching, arbitrary path registration and any CLI/environment/wire switch. A form-only callback scenario remains useful partial evidence but cannot replace ordinary Review/Full cases. No change to the production classifier, pending-preview gate, Store receipts, schema, protected release approval or user data is authorized by this plan.

## Global constraints

- One mutable source implementer. Use the existing isolated production-readiness worktree; preserve the original dirty workspace. No actual user App, daemon, login, trust, DNS or database changes.
- Use `DEVELOPER_DIR=/Library/Developer/CommandLineTools GOFLAGS=-mod=readonly`. Tests run with real private Runtime/Store authority; no fake preview or preapproved send.
- Exact synthetic canonical route remains `https://chatgpt.com/backend-api/ps/mcp`, dialed only through the existing literal-loopback transport with private certificate verification, exact targets, no redirects and rejecting blind dialer. Run containment preflight before any pinned native client.
- Per-profile default OFF, original-client tool authentication, managed model authentication separation and the complete 46-case matrix remain unchanged. Synthetic profiles prove no real production schema/classification.
- The separate Stream Task 3 checkpoint is not completion: 145 POST recovery, approved stream callbacks, real-model cancellation and broader matrix gates remain open. Do not modify those protocols here.

## Task 1: Fixed synthetic classification and honest acceptance provenance

**Files:**

- Modify `internal/productruntime/options.go`, `runtime.go`: trusted composition only.
- Create `internal/nativemcp/acceptance_synthetic_profiles.go` and `_test.go`, both tagged `vibermate_acceptance_synthetic`.
- Create paired `cmd/vibermate-acceptance/native_mcp_profiles_synthetic.go` / `native_mcp_profiles_disabled.go` and tagged/untagged tests.
- Modify `cmd/vibermate-acceptance/native_mcp{,_fixture,_execution,_management,_provenance}.go` only for classification composition, precise prerequisites and evidence. Add focused tests beside these consumers.
- Add Runtime composition coverage in `internal/productruntime/native_mcp_test.go` and a tagged counterpart if necessary; do not restructure unrelated Runtime code.
- Root-approved Step 7 owner refinement after actual RED24439: `cmd/vibermate-acceptance/provenance.go` / `provenance_test.go` may reject the synthetic tag early in `sidecarProfile` for every packaged artifact role. `internal/acceptancereport/verifier.go:638–650` already rejects nonempty acceptance tags and anything other than the exact release sidecar tag; this is not a demonstrated publication bypass. A focused test-only addition to `internal/acceptancereport/verifier_test.go` may preserve that strict negative. Keep V7 and the Go version pin unchanged in this unit.

**Interfaces:**

```go
// Existing private profile representation and validity checks stay unchanged.
// Add to productruntime.Options; nil remains unknown/unavailable.
ContentProfiles nativemcp.ContentProfiles

// Defined only in the explicitly tagged nativemcp file. No parameters.
func AcceptanceSyntheticContentProfiles() ContentProfiles

// Both private command variants provide this exact signature.
func nativeAcceptanceContentProfiles() (nativemcp.ContentProfiles, bool)
```

Wire `ContentProfiles: options.ContentProfiles` into the existing `nativemcp.Dependencies` construction. No ordinary daemon/desktop/CLI initializer populates it. Document that the seam supplies trusted retention/preview classification, never execution authorization.

- [ ] **Step 1 — Record an actual behavioral RED.** Extend an existing real Runtime/management fixture so an ordinary fixed `synthetic_read` operation requests Review and expects a nonempty safe preview; the current Runtime must fail because classification is absent. Keep a matching nil/default case expecting unavailable preview and rejected Allow. If adding a composition field first is needed to compile, its unwired zero effect is test scaffolding, not the behavioral RED. Record the exact failing assertion before connecting production dependencies.
- [ ] **Step 2 — Implement the minimal fixed adapter and Runtime connection.** The tagged factory returns an unexported immutable adapter. Resolve only `environment.ClientToolContractCodexLegacy` and the literal digest of the complete public definition below. Independently derive/verify the digest through real CatalogObserver/Lookup; never copy the digest from an incoming definition. Clone sensitive-path slices per result.

```json
{"name":"synthetic_read","description":"Synthetic public tool","inputSchema":{"properties":{"query":{"type":"string"}},"type":"object"}}
```

Candidate digest from the read-only design is `9ec74da54cc7f865d22dc1d020abf5a6d9a7df1cd7a1db07d509c6394329065f`; the real Go observer test must confirm it before adoption as a constant. Fixed argument-sensitive paths: `/proof`, `/credentialId`. Fixed result-sensitive paths: `/credentialId`, `/structuredContent/credentialId`. These paths are negative synthetic privacy probes, not a claim that arbitrary undeclared fields or real tools are safe.

- [ ] **Step 3 — Exercise exact-match and privacy boundaries.** Real catalog observations must reject zero definition, other contract/name/description, schema type/property/required/public-annotation mutations. Equivalent canonical ordering may match. Mutating a returned profile slice cannot change another result. Existing `profile.valid`, frozen binding and sensitive suppression remain authoritative; no callbacks gain a new preview family (ordinary ping remains unavailable).
- [ ] **Step 4 — Connect only named synthetic cases and real approval.** Tagged Runtime injection is per fixture scenario, never global; unknown-profile rejection and unavailable-preview negatives retain nil even when tagged. Keep ordinary Observe/OFF/Strict evidence unchanged. Add a separate ordinary `review_allow_once` diagnostic using the real authenticated management endpoint, exactly one tools/call, consumed approval/once, frozen owner/environment and safe preview. Allow helper checks `PreviewState=available`, nonempty expected intents, exact operation/binding/revision before using the unchanged decision DTO. Deny works for both known and unknown preview. Stale binding/revision, foreign Capture/Owner, expiry/revoke and repeated Allow cannot send again.
- [ ] **Step 5 — Verify Full through real private Runtime and reader APIs.** Exact synthetic text/image/large-result shapes use existing bounded descriptors/pages; test content OFF, unknown/mutated profile and exact sensitive fields suppress content/content hashes as required. Transport metadata/auth/session/callback values stay absent. Real Store/management results, not hand-made UI DTOs, establish this seam. Existing actual-client Full matrix rows stay incomplete until their complete scenarios run; this task does not wire fake passes for them.
- [ ] **Step 6 — Bind evidence to the actual tagged executable.** Extend the native report with scope `synthetic_private_runtime`, fixed profile identifier `synthetic_read/v1`, profile-definition digest, normalized actual Go build tags, Go version and SHA256 of the executed acceptance binary. Parse `debug.ReadBuildInfo`; compare compiled adapter availability with the actual tag. Hash the new factory and both command adapter source files alongside current executable fixture/audit digests. Preserve existing clean-source `vcs.revision` / `vcs.modified=false` requirements. Missing or inconsistent tag/profile/artifact/build evidence prevents passed profile-required rows.

```text
untagged + profile-required row -> incomplete / synthetic_content_profiles_required
tagged + bad source/tag/profile/artifact evidence -> no passed row
tagged + valid evidence + actual scenario assertions -> eligible for that row only
any incomplete required row -> overall nonzero, with all 46 rows retained
```

The profile-required set includes `default_call_text`, `default_call_image`, `large_result_page`, `review_allow_once` and approved `sse_reconnect`; a missing tag is not a skip or metadata-only pass. Other prerequisite failures retain their truthful reasons. A copied report is not self-authenticating: the controller independently builds, hashes and executes the selected clean artifact and compares its evidence. Do not invent signatures or claim the JSON alone proves execution.

- [ ] **Step 7 — Verify ordinary builds cannot include the factory.** Build ordinary `./cmd/vibermate`, `./cmd/vibermated`, `./cmd/vibermate-acceptance`; inspect `go list -json ./internal/nativemcp` and actual build info. The factory file/symbol must be absent and no production call site may enable it. Add a repository/build isolation regression preventing regular product construction from referencing the tagged factory. The tagged executable is never a distributable App/server/CLI artifact. Final release verification must reject the synthetic tag in any product binary; the packaged V7 acceptance contract remains unchanged.
- [ ] **Step 8 — Run affected GREEN and negative evidence.** Run the affected normal and tagged suites below, focused races for the new profile/Runtime/management paths, and repository rules/checker tests. Existing authority, retention, capacity and unavailable-preview tests must remain intact. Execute the bounded actual 160 ordinary Review diagnostic only after exact path/version/hash and containment verification; record native/model counters and SQL/API approval evidence without promoting incomplete matrix rows.

```sh
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOFLAGS=-mod=readonly go test -count=1 ./internal/nativemcp ./internal/productruntime ./internal/desktopcontrol ./cmd/vibermate-acceptance
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOFLAGS=-mod=readonly go test -tags vibermate_acceptance_synthetic -count=1 ./internal/nativemcp ./internal/productruntime ./internal/desktopcontrol ./cmd/vibermate-acceptance
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOFLAGS=-mod=readonly go run ./cmd/repositorycheck
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOFLAGS=-mod=readonly go test -count=1 ./internal/repositorycheck
git diff --check
```

- [ ] **Step 9 — Freeze this checkpoint and independently review.** Self-review the full owned diff, commit only owned files, drain handles and report exact RED/GREEN, source and remaining gaps. Build source-bound acceptance evidence from an independent private clean clone with a real `.git` directory; nested linked-worktree builds are known to stamp the outer source incorrectly with this Go toolchain. No forged SHA flags. Root performs independent checkpoint review before subsequent protocol work or any release claim.

## Handoff and remaining gates

Checkpoint implemented at `335556f2080dffb841b0f19a39eccbea47785bcf` and independently approved for its isolated composition changes in `acceptance-profile-composition-review.md`; root read the complete implementation/review reports and independently checked clean-source/tag/hash/row evidence. Source-bound actual160 Review and nine Full probes pass, while the fixed matrix remains9 policy passes/37 incomplete. This is not whole-native completion: historical callback retry409 and intermittent fresh GET uncertainty remain required work, with no assertion or authority weakening. Execute `2026-10-05-native-decision-history.md` next, then the deterministic fresh-GET diagnosis before any conditional repair or upstream continuation.

This single cohesive prerequisite makes fixed synthetic preview/content testable through actual Runtime composition. It does not implement proxy continuation, a real production content profile, the remaining matrix, CA/native OS acceptance, soak or release. Production classification still requires authorized exact schema evidence. User screenshot quota alignment and `activity_start` timeout remain separately unresolved and must not be described as repaired by this work.

Self-review: the alternative staged form-only test cannot prove ordinary approvals; exact profile validity and no-preview denial are preserved; factory and source/build provenance are compile-time constrained; no DDL or real-account access is needed; all affected callers and 46 required cases remain named. This plan refines the already approved isolated acceptance workflow and does not authorize any expanded external access.
