# Native Decision History Correction Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans. One behavioral TDD unit with independent review; do not overlap the current profile-composition writer/checkpoint.

**Goal:** Return an exact historical callback approval response after its decision has completed, without renewing any tool authority.

**Architecture:** Keep immutable approval/operation identity checks first. Historical idempotency is a read of a retained committed result; live callback authority is required for a new pending decision and for every Start/Verify/send, not for returning that existing result.

**Tech Stack:** Go1.26.8, existing SQLite native approval/receipt repository, actual authenticated control handler and executor.

**Spec:** Existing idempotent-answer requirement in `2026-10-03-native-tool-store.md` and authenticated approval contract in `2026-10-04-native-tool-reader.md`; this corrects an interaction with Task2 callback retirement in `2026-10-04-native-stream-recovery.md`.

## Source-backed finding and boundary

Root fully read `profile-repeat-allow-diagnosis.md` and actual Store DecideApproval, handler decision validation, corrupt-binding regression and callback completion pruning. Race81754 returned409 for an exact repeated Allow expected200. Worker independently reproduced it on clean7558ddf (callback-only race count5, handle5837), so it predates the current synthetic profile seam.

Store currently checks server_reply live execution authority before its explicit historical idempotency branch. Successful callback completion removes that registration; ordinary tool decisions have no such precheck. The source establishes a lifecycle-dependent conflict, but the exact edge winning either intermittent run is not proven. A deterministic completion/retirement regression must run before any fix. Do not change the test to accept409 or preserve dead registrations merely to make retries work.

## Task 1: Historical response before new-decision authority

**Files:** `internal/runtimepersistence/native_tool_approval_repository.go`; focused tests in `internal/runtimepersistence/native_tool_repository_test.go` and callback fixtures/tests; `internal/nativemcp/control_integration_test.go` and `control_harness_test.go` only to make real lifecycle ordering observable. No handler authorization change, schema, new capability, dependency or generic retry mechanism.

**Interfaces:** Existing `DecideApproval(context.Context, nativetool.DecisionInput) (nativetool.Approval,error)` and HTTP DTOs stay unchanged. Same actual approval ID, original ExpectedRevision, key, decision, binding, operation and authenticated Capture ownership are required; no new public constructor or safe flag.

- [ ] **RED:** Through real Store and authenticated handler, obtain a callback question, Allow once, let its actual child send/Finish complete, explicitly observe consumed approval and retired registration, then repeat the identical body/key/original revision. Expect200; current code must fail409. Use a completion barrier or exact state assertions, not a sleep or repeated-race probability. Add direct Store equivalent. Separately cover child completion while parent remains live and parent completion/retirement.
- [ ] **Minimal implementation:** Preserve input validation and actual typed approval/operation loading, including binding equality, before either branch. Compute requested state; for nonpending rows use only the existing exact state/key/revision tuple and return the existing result unchanged. Apply current callback/execution authority to the pending branch, keeping every existing new-decision expiry/preview/current-authority check. Do not change Start/Verify/once logic.

```go
// Existing validated inputs and immutable approval/operation identity first.
desired := "allowed"
if in.Decision == nativetool.DecisionDeny {
    desired = "denied"
}
if a.State != "pending" {
    var key string
    if err := tx.QueryRowContext(ctx, `SELECT idempotency_key FROM native_tool_approvals WHERE approval_id=?`, a.ID).Scan(&key); err != nil {
        return err
    }
    if a.State == desired && key == in.IdempotencyKey && a.Revision == in.ExpectedRevision+1 {
        answer = a
        return nil
    }
    return nativetool.ErrConflict
}
if op.Kind == nativetool.RequestServerReply {
    if _, _, _, err := r.nativeExecutionAuthority(ctx, tx, op, in.ObservedAt); err != nil {
        return err
    }
}
// Existing revision/expiry/nativeApprovalAuthority/key uniqueness/resolution.
```

- [ ] **GREEN and no-new-effect proof:** Exact replay after consumption/retirement returns200, with unchanged revision/key/consumed timestamp/expiry, no preview and unchanged once/audit/events/outbound counts. Duplicate Start/Verify/reply still reject; no additional tool execution. Merely passing ExpiresAt on an already resolved retained decision does not renew it and follows the existing ordinary-history contract. Actual stored canceled/expired state or a cleared key remains409.
- [ ] **Negative matrix:** Allowed-but-unconsumed then cancellation/OFF/retirement is canceled/key-cleared and rejects the old tuple. Changed key/decision/original revision/digest rejects; corrupt binding still fails before historical lookup. Wrong Capture/deleted history404, missing or read-only token401. Pending expired first decision409. Consumed history after expiry/OFF returns only immutable historical metadata when management ownership still permits reading; all current send gates remain denied.
- [ ] **Verify and review:** Run each intended RED alone, then exact GREEN normal/race and the original callback-repeat test count5. Run affected Store/module/control normal tests, focused race and repository/checker rules with `DEVELOPER_DIR=/Library/Developer/CommandLineTools GOFLAGS=-mod=readonly`. Preserve earlier failures, drain handles, self-review/owned commit and independent scoped review. No actual user account, App, trust or database use.

The separate fresh-GET uncertainty before outbound remains under diagnosis and is not fixed by this ordering change. This unit does not complete profile/all46/production classification, continuation, CA/soak or release. It must be resolved before treating the approval gate as stable; it is not a parked baseline failure.

Self-review: exact existing idempotent result semantics, no broader approval scope, immutable identity before historical response, current authority still required for every new decision/send, deterministic observed completion before retry, ordinary and callback behavior aligned. Existing user production-fix authorization covers this demonstrated regression; no new external authority is inferred.
