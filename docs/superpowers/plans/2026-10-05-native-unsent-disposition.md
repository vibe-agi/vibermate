# Native Committed-but-Unsent Disposition Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans. Execute the two units serially with behavioral TDD and independent review. Preserve the existing diagnostic tests and failed-run history.

**Goal:** Distinguish a positively proven never-sent native attempt from possible upstream effects without weakening authority, reusing a receipt or inventing a successful cleanup.

**Architecture:** A private send result records unproven, newly claimed/not entered, or transport-entered progression. A caller may close an attempt as failed/known-zero only when this same Serve just obtained its own successful real Start and exclusively claimed that receipt. Durable closure failure still fences the owner and retains reconciliation obligations.

**Tech Stack:** Go1.26.8, real native Store/ToolDecision/Offline/executor, existing SQLite commit-fault seams and canonical contained client acceptance.

**Spec:** `2026-10-03-production-readiness-design.md`; the truthful physical/logical accounting and exact current-send checks in `2026-10-04-native-stream-recovery.md`. This corrects the required fresh-GET observation retained by the independently reviewed profile checkpoint335556f; it does not implement upstream continuation or replace the full46 matrix.

## Proven cause and source boundary

Root fully read the final73-line diagnostic report,69-line independent phase/cleanup design, actual sendCommitted/finishFailure/Store.Finish/Revoke/Close and existing real Store commit-fault fixtures. Source BASE `2e5a87077b895c058ccbaae2de88548715d0c135`; docs-only HEAD66d0547; the sole untracked `internal/nativemcp/fresh_get_receipt_order_test.go` belongs to the dispatched diagnostic and must be preserved.

Fresh GET4706: real Start freezes session2/catalog0; real tools/list publishes3/1; final real Verify returns ErrConflict before any RoundTrip entry. The current caller nevertheless records uncertainty/NULL byte counts and revokes scope/session with a replay barrier. Desired regression a44274 fails on that behavior. Both ordering controls and race82041 pass.

Ordinary tools/call89907 proves the same family after an actual consumed AllowOnce: frozen3/1 becomes current4/2, first exclusive claim is observed at Verify, ErrConflict and tool transport entries0, yet operation becomes uncertain and owner revoked. Desired regression43325 fails; diagnostic/control race11943 passes. Include BOTH proved paths, not a GET-only workaround. Numeric audit columns0 with `byte_counts_known=0` mean UNKNOWN, not known zero.

These are actual mechanism proofs, not retrospective attribution of untraced client95764/78685. Later actual-client verification must retain that distinction. No product code or schema was changed during diagnosis, and no failing test was committed.

## Global constraints

- One mutable implementer in the existing isolated worktree; no real App/daemon/login/trust/user DB/original dirty-tree changes. Use `DEVELOPER_DIR=/Library/Developer/CommandLineTools GOFLAGS=-mod=readonly`.
- Keep current catalog/session/owner/assignment/OFF/route/once/receipt checks exactly decisive. A stale receipt must still fail. Do not delay catalog publication in the fixture, serialize broad session locks across Offline/network, add a retry framework or make an error class grant authority.
- No automatic original POST retry, receipt reissue, second ToolDecision consumption, cleared once/claim, new public API, wire enum, runtime dependency or DDL. Existing policy/default-OFF/original-login separation remains.
- Initial preclaim failure, replay and actual transport entry are not positive never-sent proof. Never rewrite an earlier sent attempt as zero because a later invocation was refused.
- Finish success is distinct from unknown commit. No false completed audit or successful shutdown; no resurrection after independent revocation and no rewriting a known terminal.

## Task 1: Qualified send phases and the two proved paths

Completed and independently approved at `e83cfac26a0eacf4f5c0b649f483d7f881c3d401` (`unsent-disposition-task-1-report.md` / `unsent-disposition-task-1-review.md`). Root fully read both, verified clean source and retained all failed-fixture/history evidence. Phase/identity/fault races, existing controls, exact historical-decision race and all five normal packages pass. This closes Task1 only; remaining kinds, actual clients and claim lifetime still require Task2/separate gates.

**Files:** `internal/nativemcp/transport.go`, `executor.go`; create focused `send_completion.go` if it keeps the private closure helper out of the large executor file. Preserve/extend `fresh_get_receipt_order_test.go`; update real consumers in `native_send_test.go`, `sensitive_tool_decision_test.go`, `sibling_session_test.go`, `retirement_reachability_test.go`. Add fault tests in `internal/runtimepersistence/native_unsent_finish_fault_test.go`, reusing `newNativeLeaseFixture`, real executor construction and existing transactionCommitter patterns from `native_stream_finish_fault_test.go` / `native_recovery_finish_fault_test.go`. Do not expose a product fault hook.

**Private interfaces:**

```go
type nativeSendPhase uint8
const (
    nativeSendUnproven nativeSendPhase = iota
    nativeSendFirstClaimUnsent
    nativeSendTransportEntered
)
type nativeSendResult struct {
    response *http.Response
    err error
    phase nativeSendPhase
}
func (e *executor) sendCommitted(context.Context, *http.Request, environment.ClientToolPlan, nativetool.StartReceipt) nativeSendResult
func (e *executor) finishKnownUnsent(nativetool.ID, *session, http.ResponseWriter, error) Terminal
```

- [x] **Retain RED evidence:** Keep a44274 and43325. After implementing, those exact deterministic fresh-GET and consumed-tools/call regressions must become GREEN; their real Start/catalog/Verify/counter assertions cannot be removed. The passing diagnostic tests must be updated only where they intentionally described the old bad final outcome, preserving their ordering facts and recorded historical output.
- [x] **Implement phase ownership:** Default result is Unproven. Preserve existing nil/canceled context, closed owner/executor, empty ID, already-claimed/replay and claim-budget failures. Set FirstClaimUnsent only under the existing lock AFTER the operation is unclaimed,128-byte reservation succeeds and `e.claimed[id]=true`. Preserve target validation and real Verify; immediately before actual RoundTrip set TransportEntered. Any response/error/nil response/timeout after entry retains that phase, even if the transport reports zero bytes.
- [x] **Qualify at the real caller:** The local Store.Start must have returned nil error and this exact nonzero receipt, with OperationID equal c.id and AttemptID equal the just-created local attempt. Only then may FirstClaimUnsent select the known-zero helper. In this first unit select ordinary tools/call and fresh get_stream with `r.stream==nil`; unknown/foreign/copied/mismatched attempt/replay cannot qualify from phase alone. Update direct private send tests for the result type; direct helper callers do not acquire permission to perform zero closure. Successful response flow remains unchanged.
- [x] **Known successful closure:** Use a bounded independent cleanup context and real Store.Finish with `Outcome:"failed", Reason:"transport_failed", BytesIn:0, BytesOut:0`. Only nil error yields OutcomeFailed with the safe original reason, known0/0 operation and audit (`byte_counts_known=1`), and no new replay barrier or owner revocation. It is a failed request, never a successful tool result. Keep once/claim and spent Allow revision/key/consumed timestamp unchanged. A new current-plan request needs its own ordinary authority and decision.
- [x] **Cleanup failure:** On ANY Finish error explicitly invoke existing e.Revoke/retireNativeWork and return OutcomeUncertain/ReasonStorageUnavailable, not known closed. Use a separate bounded cleanup context if the Finish budget has elapsed; do not use an already canceled caller context as the sole fencing attempt. Revoke fences memory before SQL. Keep the revoked owner in e.scopes and preserve unresolved Store work for Close/Revoke/Recover. A commit-then-error's already-terminal known0 row must remain immutable; rollback may require real Revoke to close started/open audit uncertain. If persistence remains unavailable, retain the fence, report storage failure and keep the obligation; never claim successful closure. The existing Serve defer runs only for rejected/canceled outcomes and MUST NOT be assumed to run for this uncertain return. No generic timer/queue/retry or compensating receipt.
- [x] **Claim and identity negatives:** Race two calls on the same receipt; only one gets the first claim and a refused later invocation cannot alter an earlier attempt. Cover zero/public-issuer/foreign-store/stolen/mismatched receipt, wrong attempt, owner/context closure and claim-budget failure. Positive first-claim phase alone is not same-Store evidence. Assert exact original audit/counts/once remain unchanged on every rejected reuse.
- [x] **Real commit-fault matrix for BOTH proved kinds:** Rollback then successful Revoke; commit-then-error preserving failed/known0; persistent Finish+Revoke failure with owner fenced and open audit explicitly observed; faults disarmed then Close successfully settling work; separate restart Recover invalidating receipts and settling rows. Check consumed approval history, exact once/audit counts, terminal immutability, no reactivated owner and zero extra sends. Use actual Store committer seams, not a mocked successful terminal.
- [x] **Post-entry and revocation controls:** Actual RoundTrip entry followed by error/nil response/deadline/downstream cancellation keeps existing unknown-effect/barrier behavior. Start commit-then-error returns no receipt and never takes zero closure. OFF/stop/assignment/owner races cannot resurrect authority. Run sibling-session, receipt, sensitive-decision, historical-retry and existing I3 recovery ambiguity controls.
- [x] **Verify and review:** Run exact new GREENs, affected normal Store/module/handler/control/Runtime suites, targeted race and repository checks. Self-review, commit owned source and report commands/results/failures/limits with drained handles. Independent scoped review precedes Task2. No actual-client or all46 claim arises from these module tests.

## Task 2: Remaining lifecycle cases and actual-client proof

This is required follow-up coverage before a broad native no-send/completion claim. Task1's two-path allowlist is an intermediate boundary, not permission to leave an identically demonstrated defect in another supported kind. No blanket generic expansion without actual lifecycle evidence.

**Files:** Existing `internal/nativemcp` request/lifecycle/stream tests and narrow send/cleanup consumers; `internal/runtimepersistence` real receipt/stream/fault tests; `cmd/vibermate-acceptance/native_mcp*.go` only for bounded phase/SQL evidence and exact-client assertions if needed. No new schema, release evidence format or product test API.

- [ ] **Exercise every remaining kind with a genuine own Start and positive first claim, then a controlled actual Verify refusal:** initialize, initialized, server_reply (ping/form/URL as supported), ping, tools_list, cancel, delete_session and recovery GET. Use appropriate real catalog/expiry/revocation boundaries; no forged success or direct SQL authority mutation. Record which independent lifecycle event already terminalized/revoked the operation; preserve such known outcomes instead of overwriting them with zero. Before first-claim cases remain unproven, not invented proof.
- [ ] **Adopt only required kind-specific cleanup, then RED→GREEN:** For a still-open positively proven never-sent attempt, require truthful known-zero failure without new upstream-effect uncertainty or unrelated-owner poisoning. Initialization/initialized must neither fabricate readiness nor leak provisional session resources; use existing exact-session cleanup and preserve valid siblings. Server replies must retire their exact pending witness/observation as needed, keep consumed approval spent and preserve the original RPC's independent state. Ping/list/cancel/delete must not apply response-side catalog/cancellation/session-close effects for an unsent request. If existing helpers cannot satisfy a kind without a new authority/lifecycle contract, stop for a precise root ruling rather than keeping a known failing case as a pass.
- [ ] **Recovery child is not the original RPC:** Use the qualified no-entry fact only for the recovery GET child's receipt-checked FinishAttempt with `Counts:&ObservedCounts{BytesIn:0,BytesOut:0}`. Never Finish/Complete the original as zero, clear its barrier, alter initial POST counts, renew cursor/callback authority or resend POST. Entered/unproven child counts remain unknown as appropriate. Real child commit/rollback/retirement-order faults preserve existing I3 exact-receipt cleanup; original remains live or uncertain according to its own evidence.
- [ ] **Verify full lifecycle coverage:** Repeat claim/reuse/current-authority negatives, cancellation/expiry/owner races, real SQL counts/projections and relevant resource-release tests. Phase-related broad claims require all named cases, not a smaller easiest subset. Retain unchanged constraints and independent review for the extension.
- [ ] **Actual current-source clients:** From an independent clean real-.git checkout build source-bound tagged diagnostic binaries. Run exact contained145/159.2/160 native startup/ordinary approval/denial cases, including formerly intermittent credential-sensitive Deny. Check real SQL/API phase, known/unknown counts, owner/barrier and exact native/model sends. A success repeat alone is not the mechanism proof; correlate any reproduced failure with bounded body-free facts. Preserve all first reports/failures and the full46 matrix; do not invent pass rows or silently retry until green. No real user login or public-network fallback.
- [ ] **Final scoped handoff:** Commit and independently review, identifying precisely which old client incidents have matching causal evidence versus source-proven correction/current passing acceptance. Only then return to upstream continuation and the remaining46/production-classification/CA/soak/final-source/release gates. Screenshot quota alignment and activity_start timeout remain separate.

## Commands and execution order

### Task 2 concrete routing adopted after Task1 review

Root fully read the61-line `unsent-task-2-lifecycle-routing-draft.md` and inspected current Store.Get projection, StartReceipt accessors and send_completion caller. The draft is execution context, not test evidence. Use these real interleavings without fabricated Verify errors or SQL authority mutation:

- **C, ready session:** hold a real successful target Start/first-claim Verify; drive a separate actual tools/list to completion/publication. Use for ping, cancel, DELETE and recovery GET; current catalog equality still rejects the target.
- **L, selected tools_list:** a second list is fenced by catalogBusy. Instead deliver a valid tools/list_changed notification through a separate actual ping SSE response and its correlated result; real parsing/InvalidateCatalog changes authority. The unsent list cannot publish pages/catalog and must release collection resources.
- **E, initialize/initialized:** construct an immutable atomic test Clock and legal SessionIdle before goroutines. After holding target Start, establish a distinct genuine sibling session slightly later, verify its registration extended shared scope expiry, then advance to original-session expiry while sibling/Capture/scope remain valid. Expiry must cause real Verify denial; close only the target provisional session after its own known-zero closure, never fabricate ReadySession or harm the valid sibling. Bound wall-clock execution so actual timers cannot silently substitute a different ordering.
- **P, supported replies:** use actual completed fresh GET plus observed ping/form/URL parent, with CallLifetime shorter than session/scope expiry. Bind/decide/Start the child through real gates; advance atomic test Clock past actual StreamAuthority expiry, below session/scope/Capture expiry. Catalog refresh alone does not necessarily reject server_reply. Ping uses actual Observe (no fabricated management preview); form/URL additionally use registered-parent metadata preview and real authenticated approval/consumption. Known-zero reply cleanup retires exactly its witness/URL bookkeeping; it is not a delivered answer and cannot create a URL-completion obligation.
- **Already terminal is distinct:** for every kind separately let real Revoke/CloseSession/RetireObservedCallback complete after first claim but before releasing Verify. Match the local owned operation/Capture/frozen binding to the existing typed terminal record and preserve its actual outcome/bytes/once/approval without forcing known-zero. A historical metadata read grants no send and alone is not a new proof of physical audit drain. If a lifecycle actor wins between read and Finish, ErrConflict is not overwrite permission; preserve only an exactly validated terminal, otherwise retain the storage-uncertainty fence. Missing/corrupt/unavailable evidence stays fail-closed. No new receipt lookup/constructor/API is authorized.
- **Cleanup errors stay visible:** exact provisional close currently clears/unlinks local session even if Store.CloseSession errors; failed-parent cleanup is currently deferred and ignores its error. Exercise those paths with real faults. If they leave unresolved durable or memory work, use the smallest explicit bounded error/fencing consumer, retaining obligations for Close/Recover; no successful cleanup claim based only on unlink or defer. Preserve unrelated callbacks, valid siblings and existing physical/logical separation. Return for a specific root ruling if a new authority/lifecycle interface is actually necessary.
- **No response-side effects:** unsent cancel cannot call the successful-response cancelTarget path; unsent DELETE cannot perform its response-side session close. Independent genuine stop/revoke effects remain. Recovery zero counts belong only to its receipt-checked GET child FinishAttempt; original POST bytes/result/cursor/approval/lifetime remain independent and existing I3 cleanup applies.

The complete named kind list in Task2 remains mandatory. These routing refinements add no source changes, weaker guards, send retries, DDL or production test hooks. Task2 may now execute serially from approved e83cfac, with first behavioral REDs before expanding closure eligibility. Its exact-client steps use the existing explicit synthetic tag, containment and independent clean-source builds; no user login/data access.

### Demonstrated expiry-sweep close failure: bounded ruling

Actual regression11804 holds the original initialize's genuine first claim at Verify, then drives another real initialize to sweep that expired provisional session. A real CloseSession rollback is ignored: the newer initialize completes with one extra send, and the original later close reports nil solely because the local session was marked closed. This is the Task2 cleanup-error boundary above, not permission for a new lifecycle API.

Retain a private failed-close latch on that old session. Under the existing executor/session lock order, a failed close must fence/cancel its owner, retain the owner for durable reconciliation, and safely unlink/drain local resources without claiming durable success. Repeated cleanup of this failed old handle must return an explicit unavailable/error outcome; `closed` or map removal cannot clear the latch. Propagate cleanup failure at BOTH admission expiry sites before allowing new initialization. Do not recursively call Revoke while holding the executor mutex. Existing bounded Revoke/Close/Recover reconciles persistence, and a fresh owner still requires real EnsureScope. Store.CloseSession is not idempotent: commit-then-error may advance SQL revision, so blindly retrying the old revision is not a success proof. No public API, schema, generic retry or send-predicate relaxation.

Require11804 RED→GREEN plus real rollback, commit-then-error, persistent-failure, repeat-close/new-initialize/late-original-request controls, normal close and valid sibling/drain checks. Preserve already-terminal audit/once/counts. Earlier passing suites predate this correction; rerun affected normal and race checks before clean-source client diagnostics and independent review.

```sh
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOFLAGS=-mod=readonly go test -count=1 ./internal/runtimepersistence ./internal/nativemcp ./internal/loopbackproxy ./internal/productruntime ./internal/desktopcontrol
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOFLAGS=-mod=readonly go run ./cmd/repositorycheck
env DEVELOPER_DIR=/Library/Developer/CommandLineTools GOFLAGS=-mod=readonly go test -count=1 ./internal/repositorycheck
git diff --check
```

Run named RED/GREEN/fault/race selections separately; report zero-selected cases and timeouts honestly. Main fully read and adopted both diagnostic addenda and the independent design before this plan. Task1 is independently complete at e83cfac; Task2 is now authorized with the concrete routing above and must return for any demonstrated new kind-specific authority/lifecycle contract. Existing upstream-continuation Task1 remains queued behind these no-send/lifecycle gates.

Self-review: both identically proved GET and tools/call failures included; first-claim evidence is not conflated with mere non-entry or same-Store authority; cleanup acknowledges commit uncertainty explicitly; other supported lifecycles remain required rather than silently excluded; no automatic resend or weakened current checks; actual-client evidence, all46 and every final gate retained.
