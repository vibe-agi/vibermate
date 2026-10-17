# Bounded recording implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Make ordinary proxy recording offers non-waiting, own all retained copies, and prevent recording capacity or persistence failures from changing forwarded results.

**Architecture:** One small allocation/stamp package serves two fixed recording workers: the existing Raw manager and a private typed Runtime recorder. Connection phases and egress attempts remain live values; only optional snapshots enter queues. Authority and native exactly-once storage are unchanged in this sub-project and are separated from observation persistence by the subsequent storage sub-project before release.

**Tech Stack:** Go 1.26.9, existing domain packages and SQLite adapters; no broker, process or dependency addition.

**Spec:** `docs/superpowers/specs/2026-10-17-recording-isolation-design.md`, recording ownership/budget sections and acceptance 3, 4. Supporting investigation: `/Users/null/Downloads/vibermate-timeout-fix-20261017-a6aswkq0/recording-interface-design.md`.

## Global Constraints

- Use one shared maximum of 2,048 items and 64 MiB across the Raw and general recording lanes, including producer copies, live Raw capture buffers, queued observations and worker-held batches.
- Keep at most two recording workers. Raw and general worker batch targets are 64 records/4 MiB; flush before adding an item that would cross the target. A larger allowed individual item is processed alone, always within the shared budget.
- No producer waits for queue capacity, writer startup, SQLite, retry or flushing. No goroutine per record, arbitrary callback queue, or unbounded retry list.
- Reserve before allocating copies or growing buffers. Retained application-owned capacities and temporary encoder/driver workspace must be charged; fixed allocator/SQLite overhead is not an RSS promise.
- Preserve credential redaction before background publication. No unredacted fallback. No queued borrowed `ContentObservation`, `Source`, request/body object or execution lease.
- Recording loss cannot reject valid Agent work, cancel Runtime, stop a child, change its exit status, or change forwarded bytes. Actual authority, route selection and delivery/replay checks remain required.
- Native current execution facts remain mandatory until the storage split removes their observation dependencies. Do not make the existing native receipt/once/approval transaction lossy.
- Existing history is preserved. Durable deletion/retention visibility and physical storage isolation are required by the overarching spec before release; this queue sub-project alone is insufficient.
- Work in `/Users/null/.codex/worktrees/1c22/vibermate`, one source implementer and one heavy test at a time. Preserve original main and the running App/data. Follow behavioral TDD and scoped race checks.

---

### Task 1: Shared non-waiting reservations, stamps and recording counters

**Files:**
- Create: `internal/recording/budget.go`, `internal/recording/stamp.go`, `internal/recording/status.go`, `internal/recording/budget_test.go`, `internal/recording/stamp_test.go`.
- Modify Runtime construction only when consumers are installed in Tasks 2–3; this task introduces the reusable owner without a second unused Runtime queue.

**Interfaces:**

```go
package recording

type Kind uint8 // closed constants: Connection, Egress, Activity, Usage, Content, Raw, Native, ACP
type OfferResult uint8 // Accepted, DroppedCapacity, DroppedUnavailable, DroppedTooLarge, DroppedInvalid, DroppedStale, DroppedClosed
type Limits struct { Records int64; Bytes int64 }
type Budget struct { /* private synchronized totals and per-kind counters */ }
type Reservation struct { /* pointer-owned; release exactly once */ }
func NewBudget(Limits) (*Budget, error)
func (b *Budget) TryReserve(Kind, int64) (*Reservation, bool)
func (r *Reservation) TryGrow(int64) bool
func (r *Reservation) Shrink(int64)
func (r *Reservation) Release()
func (b *Budget) Snapshot() Statistics
func (b *Budget) RecordOffer(Kind, OfferResult, uint64)
type Settlement uint8 // Persisted, Discarded, Unconfirmed
func (b *Budget) RecordSettlement(Kind, Settlement, uint64)

// Neither a Fence nor a Stamp authorizes execution.
type Fence struct { ArchiveEpoch, CaptureGeneration uint64 }
type Epoch struct { /* one Runtime-owned monotonic atomic epoch */ }
func NewEpoch(uint64) (*Epoch, error)
func (e *Epoch) Advance(uint64) bool
type Stamp struct { /* immutable reference/fence + revocable in-memory token */ }
type ScopeToken struct { /* only fixed scope identity, generation and revoked bit */ }
func NewScopeToken(*Epoch, captureidentity.Reference, Fence) (*ScopeToken, error)
func (t *ScopeToken) Stamp() Stamp
func (t *ScopeToken) Revoke()
func (s Stamp) Current() bool
func (s Stamp) Capture() captureidentity.Reference
func (s Stamp) Fence() Fence
```

The zero-value Stamp is invalid; a Runtime-wide token uses a zero Capture reference and nonzero archive epoch. Capture-bound tokens require a valid reference and positive generation. Each token retains the Runtime's Epoch pointer and the fixed epoch it was issued against; `Current` checks both revocation and equality with that atomic value. `Advance` accepts only a positive greater value and never walks tokens/queued items. Independent Runtime Epoch owners cannot revoke each other's stamps. Keep no global unbounded map or process-global epoch in this package. Scope token persistence/publication belongs to control composition in the storage plan. `Statistics` uses fixed fields/arrays for reserved records/bytes, accepted and drops by closed kind/reason, confirmed writes and unconfirmed writes; no IDs/URLs/error strings as metric labels. Document that Accepted is in-memory ownership, not durable confirmation.

Reservation methods account only ownership, not publication success. Producers call `RecordOffer` exactly once when their offer completes; workers call `RecordSettlement` once per accepted observation that finishes. The final argument is the retained observation byte count. Temporary workspace is reserved but does not count as another observation/offer. Unconfirmed commits remain distinct from known discarded copies. This separates allocation, queue admission and persistence counters without recursive logging or I/O.

- [x] **Step 1: Write concurrency/ownership tests first.** Reserve from two named lanes against one small Budget; fill it with one producer-owned, one queued and one worker-owned reservation. Another TryReserve/TryGrow must return immediately while owners remain held, and existing totals must stay unchanged on refusal. Release/transfer after dequeue must not free capacity early. Concurrent reservations never exceed limits; repeated Release is harmless; invalid negative/overflow arithmetic cannot change counters. Revoking a token makes old stamps stale without affecting another scope; advancing the Runtime epoch invalidates its old stamps without walking them or affecting a separate Runtime owner.

```go
budget, err := recording.NewBudget(recording.Limits{Records: 2, Bytes: 64})
if err != nil { t.Fatal(err) }
raw, ok := budget.TryReserve(recording.Raw, 32)
if !ok { t.Fatal("first owner rejected") }
content, ok := budget.TryReserve(recording.Content, 32)
if !ok { t.Fatal("second owner rejected") }
if extra, ok := budget.TryReserve(recording.Egress, 1); ok || extra != nil {
    t.Fatal("aggregate capacity exceeded")
}
raw.Release()
if !content.TryGrow(32) { t.Fatal("released bytes were not reusable") }
content.Release()
```

- [x] **Step 2: Run `go test -mod=readonly -p=1 ./internal/recording -count=1` and record RED.** A missing API may initially fail compilation; add only the smallest compile stubs if necessary, then capture behavioral failures of ownership/limits/stamp invalidation before implementing the real synchronization. Subsequent race tests exercise actual synchronization and totals, not private field shape.
- [x] **Step 3: Implement short in-memory critical sections only.** Reserve record and bytes atomically; no condition waits. On grow, account for the complete new backing allocation while the old buffer remains live, then shrink the old capacity after transfer. Reject invalid requests conservatively. Separate reserve/drop accounting from persistence acknowledgment so ambiguous writes are not falsely reported as definite loss.
- [x] **Step 4: Run package normal and race tests; self-review and commit.** Commit `feat: bound optional recording ownership without waiting`.

### Task 2: Make Raw admission and streaming capture bounded and lossy

**Files:**
- Modify: `internal/rawevidence/manager.go`, `internal/rawevidence/types.go`, `internal/providertransport/client.go`, `internal/loopbackproxy/handler.go`, Raw construction in `internal/productruntime/runtime.go` and `builders.go`.
- Modify: `internal/runtimepersistence/raw_evidence_repository.go` and its tests where writer sequence/settlement semantics require it; keep published frozen v1/v2 DDL unchanged.
- Create: `internal/rawevidence/capture_buffer.go`, `internal/rawevidence/capture_buffer_test.go`, `internal/rawevidence/admission_test.go`.
- Extend existing Raw/provider/loopback tests covering request/response capture and saturation.

**Interfaces:**
- Add `Budget *recording.Budget` to `rawevidence.Options`. Runtime constructs exactly one default budget and passes the same pointer to Raw and Task 3's typed recorder.
- Preserve existing repository publication/manifest/digest validation. Existing observer API can adapt for callers during transition, but a dropped observation never becomes a transport error or a claimed durability watermark.

```go
func (m *Manager) TryObserve(recording.Stamp, Observation) recording.OfferResult
func (m *Manager) TryCapture(recording.Stamp, CaptureMetadata) *CaptureBuffer
func (b *CaptureBuffer) ObserveBytes([]byte, FrameKind)
func (b *CaptureBuffer) Finish(TerminalMetadata) recording.OfferResult
func (b *CaptureBuffer) Discard()
```

```go
type CaptureMetadata struct {
    Context Context
    Layer Layer
    Method, Scheme, Authority, Path, RawQuery string
    StatusCode int
    Headers http.Header
    ProtectedHeaderNames []string
    ContentType, ContentEncoding string
}
type TerminalMetadata struct {
    Trailers http.Header
    Complete bool
    IncompleteReason string
}
```

Context holds the existing identity/scope/frozen recording metadata; bytes observed and digest/prefix completeness are tracked by the buffer itself. Headers/trailers are borrowed only during the synchronous call. CaptureBuffer retains its reservation and owned redacted metadata from creation through worker settlement. A nil/disabled handle safely ignores calls. Reasons are validated local closed values under existing Raw rules, never arbitrary transport error strings.

- [ ] **Step 1: Add tests that block the real writer seam while streaming continues.** Fill a small shared budget with multiple active buffers and one worker-held batch, then forward byte-exact local HTTP/SSE responses. Additional complete/request observations and buffer growth must promptly drop or retain an honest prefix; forwarded bytes must still complete before releasing the sink. Assert total item/byte ownership includes in-flight buffers, and no reservation leaks after failure/cancel/shutdown. Mutating a caller's input after TryObserve cannot change retained bytes. Finish must transfer owned storage without a second body-sized allocation.

```go
// The sink closes entered then waits for release. Completion before release is
// the key assertion; timeout is only the test's hang guard.
<-sink.entered
select {
case result := <-forwarded:
    if !bytes.Equal(result, expected) { t.Fatal("forwarded bytes changed") }
case <-time.After(2 * time.Second):
    t.Fatal("forwarding waited for optional Raw storage")
}
```

- [ ] **Step 2: Run focused new tests and record behavioral RED.** Existing Observe waits for semaphores and pre-Observe response buffers escape capacity; capture those failures without changing production first.
- [ ] **Step 3: Reserve before clone/redaction/buffer growth and use nonblocking channel publication.** Remove eager recording-only Header/Body/Frames clones from producer wrappers. On capacity refusal, drop the copy or freeze a correctly labeled prefix while continuing actual I/O. Stop copying/hashing once the whole observation is dropped. Include frame metadata and grow-overlap capacities. Existing full-body/prefix/digest rules remain accurate.
- [ ] **Step 4: Settle failed batches and release ownership.** A failed/ambiguous batch is counted and abandoned; no indefinite retry holding capacity. `ListExchange` reads committed rows without flushing a watermark. Replace the former contiguous-durable promise with accurately named settled/persisted counters; only explicit administrative barriers may wait. Persisting a later batch after an abandoned sequence range must succeed and retain exact later bytes: update repository sequencing validation as necessary instead of turning one dropped batch into permanent recording failure. A highest persisted sequence is not proof that all earlier records were stored. Add that recovery regression before changing the consumer contract. Shutdown closes admission, cancels worker I/O, abandons queued copies and joins the actual worker before releasing its resources; deadlines never pretend an in-flight worker has stopped.
- [ ] **Step 5: Run affected Raw/provider/loopback tests plus focused race saturation/cancellation cases; commit.** Existing tests asserting synchronous immediate visibility must explicitly wait on recording inspection/test barriers, not add waits to production forwarding. Commit `fix: keep raw recording off the forwarding critical path`.

### Task 3: Detach connection and ordinary egress history from forwarding

**Files:**
- Create: `internal/productruntime/recording.go`, `internal/productruntime/recording_test.go`, `internal/runtimepersistence/egress_snapshot_test.go`.
- Modify: `internal/connectionevent/manager.go`, `types.go`, `manager_test.go`; `internal/egressaudit/repository.go`; `internal/runtimepersistence/egress_attempt_repository.go`; `internal/productruntime/egress.go`, `runtime.go`, `builders.go`, `status.go`.
- Update ordinary recording dependencies in `internal/providertransport`, `internal/originaltransport`, `internal/loopbackproxy`, `internal/egressnetwork`, account/OAuth/model/update adapters; locate call sites from existing `egressaudit.Writer`/`ReportTerminalFailure` types.
- Native control transactions and their current attempt persistence remain required until the storage plan; do not redirect them through this queue.

**Interfaces:**

```go
// connectionevent: Reader remains the existing history interface.
type Observer interface {
    ObserveConnection(recording.Stamp, Event) recording.OfferResult
}
// Options now holds Observer and Reader instead of a live Repository writer.
func (connection *Connection) BindObservationScope(recording.Stamp) error

// egressaudit: live transports no longer receive durable Record/Sequence.
type Observer interface {
    ObserveEgress(recording.Stamp, Attempt) recording.OfferResult
}
type AttemptStore interface { PutAttempt(context.Context, Attempt) error }
```

The private productruntime recorder owns a finite tagged union for connection and egress snapshots, one channel and one worker, and the same Budget as Raw. Add metadata/content arms only in Task 4. A queued value owns its copied slices/strings and reservation; it never stores callbacks, context, repository or transport lease. Private Runtime composition passes a trusted Stamp alongside the frozen admitted work. Inspectors retain their existing Reader interfaces.

- [ ] **Step 1: Add blocked-sink integration tests.** With connection and egress storage blocked/failing, a real valid cleartext/CONNECT request reaches a local upstream and returns exact bytes, a subsequent request succeeds, and Runtime owner/readiness remains live. Include blind CONNECT and UDP local peers through their existing deterministic harnesses. Genuine denied/revoked admission still refuses bytes. Connection state reaches terminal even if every offered snapshot drops; no persisted sequence is fabricated.
- [ ] **Step 2: Add terminal-only persistence tests and observe RED.** A full egress terminal with no stored start inserts all frozen and terminal facts. Repeating it is idempotent; a late same start cannot erase the terminal; a conflicting immutable tuple or terminal fails locally without modifying the existing row. Compare normalized complete tuples, not just IDs.

```go
if err := store.PutAttempt(ctx, terminal); err != nil { t.Fatal(err) }
if err := store.PutAttempt(ctx, started); err != nil { t.Fatal(err) }
page, err := store.List(ctx, egressaudit.PageRequest{ExchangeID: exchangeID})
if err != nil { t.Fatal(err) }
// Assert exactly one row retaining every terminal and frozen start field.
```

- [ ] **Step 3: Advance live connection state before offering snapshots.** Start returns its in-memory ID/time without repository access or history recovery. Decide/Connected/Finish preserve required state validation and update in memory; persistence errors cannot change their result. Bind the initially buffered attempted event once trusted Capture scope is known, preserving its original time. An unbound rejected connection may publish only a Runtime-scoped terminal. Avoid recording optional display-field failures as authorization failures.
- [ ] **Step 4: Replace ordinary egress writes with observation offers and implement PutAttempt upsert.** Transport retains the frozen Attempt independently of queue acceptance and offers the full terminal. Keep required route/credential/action validation before actual I/O. Remove the pending-terminal global circuit, unbounded retry slice and recording-triggered owner cancellation. Report malformed optional terminal construction through closed recording counters. Native sealed receipts still use reliable native repository transactions.
- [ ] **Step 5: Run connection/egress and affected real transport tests plus focused race lifecycle cases; commit.** Update obsolete mandatory ordinary-audit tests to the clarified user contract. Do not weaken actual route, authorization or native replay tests. Commit `refactor: separate live connections from retained audit history`.

### Task 4: Own metadata/content snapshots and remove recording workspace from execution admission

**Files:**
- Create: `internal/exchangecontent/owned.go`, `internal/exchangecontent/owned_test.go`, `internal/productruntime/recording_content_test.go`.
- Modify: `internal/exchangecontent/source.go`, `manager.go`; `internal/productruntime/recording.go`, `builders.go`; `internal/exchange/resource_envelope.go`, affected resource/admission tests.
- Extend productruntime content-budget/long-session tests at their existing adapter seams. Keep test fixture changes focused on eventual recording visibility.

**Interfaces:**

```go
// exchangecontent: sealed owned value, no exchange package dependency.
type OwnedRecord struct { /* private sanitized Record and ownership accounting */ }
type OwnInput struct {
    ExchangeID string
    Frozen FrozenRef
    Policy environment.ContentRecordingPolicy
    RecordedAt time.Time
    Parent ParentRef
    Request protocolcore.Request
    Response *protocolcore.Response
}
func OwnWithin(context.Context, OwnInput, SourceLimits, *recording.Reservation) (*OwnedRecord, error)
func (o *OwnedRecord) Source() *Source // synchronous worker borrow only
```

The usage arm consumes this body-free value, declared in `internal/runtimeusage/ledger.go` and deep-owned by the Runtime adapter:

```go
type CollectedObservation struct {
    Value Observation // UserID/Source/Attribution/display names frozen from trusted admission
    PolicyRevision int64
    ExpiresAt time.Time
    Stamp recording.Stamp
}
type CollectedWriter interface {
    PutCollectedUsage(context.Context, CollectedObservation) error
}
```

The producer uses a reliably published `runtimeusage.CollectionPolicy` snapshot and admitted Capture/Environment/account facts already in memory. `PutCollectedUsage` initially supplies the same typed seam to the worker; the storage plan implements the observation-only transaction and consent overlays. Any legacy bridge is worker-side only and removed before release; never fabricate a trusted owner from a request label.

Use the existing Source projection/sanitization and shape measurement, extending them with allocation accounting rather than implementing another sanitizer. Deep-own slices, byte buffers and string backing storage. Worker Source can only refer to OwnedRecord storage; no original request graph, extension reader or body lease remains. The caller transfers one reservation on accepted enqueue and releases it on refusal; worker releases after actual completion.

Add explicit `ObservationStamp recording.Stamp` to the three existing Exchange observation structs in `internal/exchange/contracts.go` and propagate it from the frozen admitted request. A zero stamp means recording drops. Do not use context values, a per-event SQL lookup or an unbounded Exchange-ID lookup map to recover the scope. Runtime auxiliary actions use a valid current global archive stamp. The storage plan connects these tokens to persisted epoch/generation state.

- [ ] **Step 1: Add borrowed-owner and saturation behavioral tests.** Offer request/response content, then mutate and release the producer objects while a sink is blocked; after release, the sink sees the original sanitized snapshot. Saturated recording capacity must allow real JSON and SSE results to finish before the sink is released and must let pipeline body owners close. Metadata-only mode never copies full content merely to discard it. Oversized optional content drops without reducing the live request's valid resource limit.
- [ ] **Step 2: Observe RED, then implement bounded ownership.** Reserve item/preparation space before Source construction or sanitization scratch. Use nonallocating shape traversal to reject overlarge inputs. Charge every owned capacity and temporary overlap before allocation; insufficient capacity drops immediately. Worker encoder/driver scratch must be reserved separately from queued payloads, never wait for capacity held by its own queue.
- [ ] **Step 3: Queue body-free Exchange start/terminal bundles.** Generate Activity ID/time before enqueue. Deep-copy mutable protocol/transport/attribution metadata; identities use the same original timestamp. Worker persists independent projections without suppressing all of them after one error. Usage is offered with frozen trusted attribution and collection-policy revision; no new per-event control query is added. The storage plan supplies the durable policy overlay and independent writer; until then use an explicit typed internal usage snapshot seam, never copy untrusted client ownership labels.
- [ ] **Step 4: Remove only recording materialization terms from RequiredExecutionEnvelope.** Retain codec, transform, tool exposure, actual downstream delivery and resend ledger requirements. Add a behavioral admission test showing that changing optional recording limits/availability does not reduce valid execution concurrency. Record actual owned encoding workspace under recording budget; a comment claiming it is separate is insufficient.
- [ ] **Step 5: Run exchangecontent, resource-admission and affected Runtime tests plus focused ownership race tests; commit.** Commit `refactor: own optional content outside agent execution budgets`.

## Completion boundary and storage handoff

This sub-project establishes bounded non-waiting producers and fixed workers. The next storage work must still separate the physical control writer, remove Capture first-observed/terminal-flush dependencies, split native current facts and ACP registration from optional history, publish durable deletion/retention visibility, preserve legacy reads, and update backup/restore. The budget/stamp and typed producer contracts above are the fixed interfaces for that work. Do not report architectural completion or release while those dependencies remain.
