# Keep Agent work independent from observation recording

## Requirement and scope

The user stated: “用户的连接，和数据量应该不要挂上任何关系” and clarified: “就是用户能正常在 agent 里工作，至于记录型的日志不是强要求的”. This supersedes the existing policy that some connection/egress audit commits are prerequisites for forwarding.

The primary acceptance contract is that an otherwise authorized Agent can start, attach, renew its Capture, forward its exact request/response, and finish while observation storage is unavailable, locked, slow, full, or disabled. Recording may lose copies. Its failure must not change the child exit status, stop Runtime, revoke a Capture, prevent outbound work, or hold a transport/body lease waiting for persistence.

Actual forwarded bytes are not droppable. Credentials, user configuration, grants, consent/deletion obligations, one-use native tool claims, and protocol delivery/replay facts remain execution state. Recording loss never permits bypassing authorization, repeating a tool side effect, changing a destination, or exposing unredacted credentials.

This is an architectural correction, not only a timeout adjustment. It includes the live recording paths, physical storage roles, startup/recovery, existing data, data management, and loss presentation. An external broker and a new recording process are outside the chosen design: a bounded in-process queue and independent storage are sufficient when ownership and failure propagation are corrected.

## Observed defects

The released baseline is `2a9ccb0b4a28b2a53d96c7a26f749f1817f00052`, version 0.1.29 (31).

- Raw HTTP chunk lookup applies `hex()` to the stored BLOB primary key, forcing a whole chunk-table scan. A read-only probe on the reported approximately 6 GB store took 6.53 seconds to retrieve four envelopes' chunks with the pinned Go driver; indexed lookup took about 0.94 milliseconds. No user body or credential was printed.
- Raw reads and Capture attachment/heartbeat use the same one-connection database pool. The failed launch record had no attached process ID and finished about 7.8 seconds after creation. The precise historic transport error was not retained, so this connection between contention and that incident is an inference. A controlled real-child regression independently reproduces the same supervision failure when attachment takes six seconds under the old five-second budget.
- Connection and egress audit writes gate sending; some terminal failures stop Runtime. Activity/content errors are ignored but their calls still run synchronously. Raw queue admission waits for capacity. Capture Finish waits for Raw durability. Native inspection payload and ACP snapshots also enter execution paths.
- Native operation/event/attempt rows mix real execution facts with inspection projections. They cannot all be dropped indiscriminately.
- Retained-WAL schema inspection can copy/hash the whole monolithic database, and egress recovery scans completed historical attempts before startup. Moving only future payload writes would leave these history-size dependencies.

The index correction and initial attachment budget regression are already being developed test-first on the branch. They remain useful, but do not establish recording isolation by themselves.

## Architecture

### Three separate interfaces

1. **Execution:** existing grant/configuration/credential/native execution interfaces are backed by a mandatory control store. They do not call an observation writer or await an observation reader. Only genuine execution failure can reject work.
2. **Recording:** producers offer an owned, sanitized observation through a non-waiting interface. Acceptance means ownership of a bounded copy, not a durability promise. Full capacity, disabled recording, or failed persistence produces a local loss outcome.
3. **Inspection:** workbench/history interfaces read committed observations, including preserved legacy archives when needed. They may report pending/incomplete/unavailable data. They do not flush a writer as a prerequisite for a live control operation or cause Runtime to become unavailable.

The normal dependency direction is execution → recording offer → bounded queue → observation writer. There is no acknowledgment or storage result that execution must wait for. Inspection reads the recording side. Neither interface can import observation rows as execution authority.

### Physical storage roles

Use a compact mandatory control database and a separate optional observation database, each with its own connection pools, operation gate, health, recovery and maintenance. A second connection to the same SQLite file is insufficient because writers still share a file lock. Historical reads must not use a control writer transaction.

The control role contains configuration, identities, Capture grants and assignments, pending decisions, native execution/once/replay state, and durable data-deletion/consent obligations. It must not accumulate ordinary connection/egress history, Raw bodies, semantic transcripts, usage rows, ACP snapshots, or native inspection content.

Native correctness must retain the facts currently used to validate Start/Verify/Finish. Required current-state proofs and native delivery receipts belong to control; chronological event history is an optional projection. A missing observation event/body/descriptor must not invalidate otherwise valid execution state. A missing required approval, once claim, current proof, or frozen binding must still refuse execution.

Retain each native operation's current event kind/time and retirement time bound in control, and each one-use delivery's immutable request coordinates and latest physical terminal in a dedicated control table. Pending approval discovery reads control approvals directly; it must not discover permission requests through optional event-history pagination.

ACP's registration marker and frozen mode remain small control facts. Its conversation snapshots and terminal display copy are optional. A snapshot failure cannot turn a successful Agent exit into failure; transient authority-store errors must not be mislabeled as revocation.

Observation data includes ordinary connection/egress snapshots, Activity and conversation identity/display, Raw evidence and its redaction/chunk graphs, semantic content graphs, usage observations, ACP snapshots, and native content/history projections. Keep each graph's publication and garbage collection atomic within that role.

### Queue, ownership, and budgets

- No producer waits for queue capacity, writer startup, SQLite, retry or flushing. Short in-memory synchronization must never cover I/O or payload encoding.
- Reserve bounded item/byte capacity before expensive cloning. Do not queue borrowed `ContentObservation`, `Source`, mutable request/response objects, or leases that keep the live pipeline open. The recording side owns its snapshot and releases it on commit, drop, cancellation or shutdown.
- Preserve credential redaction before data enters a background queue. If redaction cannot be established, drop the observation; never retain a raw fallback.
- Use one shared maximum of 2,048 items and 64 MiB across the Raw and general recording lanes, including producer copies, live Raw capture buffers, queued observations and worker-held batches. Keep at most two workers, with Raw batches bounded to 64 items/4 MiB. Do not add queues per Capture, goroutines per record, or an unbounded retry list.
- Raw response/downstream buffers reserve shared bytes before growing, then transfer their owned storage when finalized. Capacity exhaustion stops retaining the copy while forwarding continues. Removing a wait inside `Observe` is insufficient if concurrent live bodies allocate before that call.
- Remove recording-only materialization workspace from live execution admission. Required transform/transport workspace remains charged to execution; optional encoding and worker scratch remain separately bounded by recording policy.
- A failed batch may be abandoned. Do not retry indefinitely while retaining capacity or block later Agent work. Unknown commit outcomes are reported as unconfirmed recording, not falsely counted as definite loss or definite durability.
- Self-contained final connection/egress observations must remain meaningful if an earlier observation was dropped. Live phase/ID bookkeeping is independent of persisted row IDs and sequences. Do not invent a successful history row merely because its predecessor is missing.
- Limit inspection concurrency and request sizes independently. Keep the indexed chunk lookup and existing bounds/digest validation. Historical data can make its own inspection slower, but cannot hold an execution database connection.
- Recording startup/recovery/cleanup failure affects recording health only. Startup must not eagerly open or traverse old observation archives.
- Closing a Capture seals recording admission without waiting for record durability. Runtime shutdown discards optional queued copies, cancels workers and joins owned I/O under the caller's bound; it must not wait for a durability drain. Actual I/O that has not stopped remains owned—never report a closed store or release data-directory ownership while a writer can still access it.
- Shared disk/CPU still require bounded work, batch sizes, memory and concurrency. Do not claim independent files isolate the entire operating system from resource exhaustion. Recording must stop accepting work before its configured capacity/reserve is exhausted rather than consume resources without bound.

### Failure and visibility

Expose a small recording-health snapshot: starting/available/degraded/disabled state, accepted/queued records and bytes, drops before persistence, unconfirmed writes, and closed reason codes. Keep accounting in memory so reporting recording failure does not itself require a database write.

UI must distinguish incomplete/unavailable evidence from a failed Agent operation. Missing records do not prove no traffic occurred. A crash can lose uncommitted observations whose exact count is unknown; do not fabricate a precise loss count after restart.

Preserve existing readiness distinction: healthy execution with failed recording is ready for Agent work. `doctor` reports recording degradation separately from execution/authority availability. Startup supervision errors retain safe stage/cause details. Initial attachment and ordinary UI control reads allow 30 seconds while honoring caller cancellation and explicit shorter launcher control budgets; this is tolerance, not the mechanism that isolates recording.

## Existing data and compatibility

Use an explicit new control schema/layout version. Do not pretend a partial observation schema is the existing complete Runtime schema, and do not allow an old binary to resume writing a stale legacy file as current authority.

Perform one verified cutover under the existing data-directory and host ownership guards. Build and validate the new control database from the required authority closure; preserve the original monolithic database and retained WAL as a versioned legacy archive. Do not bulk replay payload history into the new observation database during startup. No implicit `ClearEvidence`, deletion, VACUUM, credential reset, or new identity is permitted as a migration shortcut.

The one-time verified upgrade may take time proportional to the source snapshot. Subsequent startup/recovery must use the new control role and must not scan/copy old observation history. Existing legacy history is read lazily through an independently budgeted reader. Preserve archive identity, stable evidence IDs, salt interpretation and read cursors; deduplicate legacy/current results by their actual identities.

Missing or corrupt observation storage disables recording/inspection for that role while execution continues. Missing, unknown or corrupt control state must never silently initialize a fresh authority. Existing bad observation files are retained for recovery rather than automatically overwritten.

## Deletion, retention, backup and restore

Data loss under load does not authorize silently deleting existing history. Preserve user-authored retention and explicit deletion behavior.

Removing data must publish a reliable control tombstone/generation before acknowledgment of visibility removal. Late queued observations and restarted workers cannot recreate deleted data. Physical cleanup is retryable recording administration; any claim that erasure is complete must be based on actual completion. Do not put this asynchronous cleanup back on ordinary Capture attachment, heartbeat, forwarding or Finish.

A write already executing in observation storage may finish after a tombstone is published. Apply visibility filtering to both current and legacy readers in addition to queue admission/worker checks. Bind connection observations to their trusted scope before offering them; pre-authorization display events cannot escape a later scope deletion merely because their scope was unknown at creation.

Retention shortening must take effect immediately for existing observations; later lengthening must not resurrect or extend them. Legacy archive-local sequence caps cannot be treated as a single global sequence. Define archive/generation-aware caps and carry the original consent, observation time and publication generation in queued records.

Version backup manifests to describe control, observation and legacy roles, their schemas/identities and the WAL files needed to recover them. Keep old backup formats readable. Preserve current private snapshot/rollback guarantees and provider-secret/keychain exclusions for public backups. Optional data problems must be disclosed accurately, not masquerade as a fully verified history backup. A restore cannot replace missing control with empty state or open legacy data as authority.

## Required TDD acceptance

Tests use temporary synthetic stores, local peers and controlled blocking/failing sinks. No tests mutate the running user's App, real database, account state or OS trust/proxy settings.

1. Real default CLI child survives a six-second attachment response, including when Create has an explicitly shorter budget; caller cancellation and explicit shorter control budgets still work.
2. Raw evidence retrieval in the presence of unrelated history remains prompt and byte-exact, preserving corruption/digest/size checks.
3. While recording persistence is demonstrably blocked, an authorized real proxy request reaches a local upstream, returns exact bytes, renews its Capture and finishes before the recording block is released. Cover semantic, Original, blind and UDP transport owners.
4. Saturate count and byte capacity. Offers return promptly, memory/worker counts stay bounded, and owned snapshots do not borrow later-mutated/released input.
5. Hold an actual observation database writer transaction and an inspection read independently. Capture Create/Attach/Heartbeat/AuthorizeProxy/Finish still complete through the control store. A same-file second pool must fail the red test.
6. Missing/corrupt/slow observation initialization and maintenance do not block execution startup or mark it unavailable. Real authority failure remains correctly classified.
7. Native tool execution/replay/approval/once/receipt guards remain correct with recording disabled; missing optional content/events cannot prevent valid execution or alter delivered JSON/SSE bytes. Required control proof corruption still rejects.
8. ACP child startup, forwarding and exit status survive optional snapshot failure. True revocation stops it; temporary storage failure is not 403.
9. Late queued records cannot resurrect erased scopes, including across a recording outage/restart. Retention shortening and later lengthening preserve consent.
10. Interrupted role cutover, retained WAL, old backups, private rollback and multi-role restoration preserve control/identity and existing history. Legacy readers are not opened during normal control startup.
11. UI timeout cancels its own request, uses the 30-second budget, preserves concurrent requests/session behavior, and shows actionable recording-gap/timeout information without affecting Agent work.

Each implementation task requires an observed behavioral red case, minimal production change, affected normal/race checks, and independent scoped review. Replace obsolete mandatory-log expectations while preserving tests of actual authorization, execution ownership, delivery and replay safety. Do not run broad campaigns repeatedly; run the final repository checks on the qualified candidate once, then address only actual failures.

## Delivery constraints

Keep the current user's App/runtime/data intact while developing. Use the existing isolated worktree. The published 0.1.29 tag/assets are immutable. Qualify any follow-up release through normal source review, exact-source CI, macOS signing/notarization/installation, Linux distribution verification, public asset checks and Homebrew publication; do not claim a remote server deployment.

## Implementation sequence

Execute the supporting [control/inspection reliability plan](../plans/2026-10-17-control-inspection-reliability.md), the [bounded recording plan](../plans/2026-10-17-bounded-recording.md), and the [storage isolation plan](../plans/2026-10-17-storage-recording-isolation.md) in that order. Their task reviews are local gates; one final whole-branch review and qualified release follow the complete architecture. An intermediate commit that has only queues or timeouts does not satisfy this spec.
