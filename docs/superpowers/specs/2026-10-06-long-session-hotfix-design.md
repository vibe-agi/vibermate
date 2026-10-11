# Stable long-session hotfix design

Status: the user explicitly approved this independent first-release scope on 2026-10-06: repair the 4096 long-session blocker from the stable version; retain complete history; verify forwarding, recording, reading and resource usage; then review, sign, notarize and publish GitHub/Homebrew. MCP and global-default egress ship later. This is not approval to merge the unfinished development branch or to change real user data.

## Outcome and baseline

A Codex client must be able to continue its existing long Client Session without deleting history, opening another session, switching Accounts, or changing a script to work around an internal 4096-item limit. The observed 4102–4111-item approximately4MiB requests are concrete regression cases, not a new supported maximum. Preserve all already-configured same-dialect, Account Selector, model mapping, Transform Policy and supported cross-dialect behavior.

Start at stable main `c61f8c6b5e2196da8e99010c169d4e3c17797933`, verified source-equivalent to formal v0.1.23 `2237b3ae35e8097987b685f6bcd08224bde62f0f` except three release documentation files. Implementation branch `fix/long-session-hotfix-20261006`, isolated worktree `.worktrees/long-session-hotfix`. Original checkout and `.worktrees/production-readiness` retain all existing edits. No unfinished native/schema2/global-egress changes are prerequisites for this release.

The full production-readiness Goal remains intact. Completing this hotfix is an intermediate delivery, not completion of that Goal.

## User-approved delivery split — 2026-10-07

The user explicitly separates functional correctness from performance optimization: a complete successful write taking3seconds is functionally successful, not a failed feature merely because an old2second cutoff was exceeded. Ship the reliable long-session functionality first; ship further performance optimization in a later version. This supersedes earlier interpretation of the fixed2s observer budget or full performance-calibration matrix as an immutable first-release gate.

First-release gates remain: complete history forwarding/recording/readback on the real default path, original-session continuation, correct account/route/tool permissions, safe existing-data upgrade/restore, finite resource ownership, reliable cancellation/shutdown, no missing records or actual failure/stall of other normal conversations/control operations, applicable tests/review/signing/notarization and GitHub/Homebrew delivery. Do not substitute clipping, metadata fallback, hidden errors, infinite waiting or an untested arbitrary budget.

Measure complete-write cost and choose/verify a reasonable recording lifecycle and finite budget. A diagnostic30s overlay is not a proposed production value or release PASS. Detailed throughput/latency/TotalAlloc targets, minimizing already-safe allocations and exhaustive large-shape/concurrency tuning are retained for the subsequent performance release; deferral is not reported as a passed benchmark. Previously completed safe optimizations remain. The full production-readiness Goal and its remaining functionality/security/delivery obligations are not reduced.

Functional closure ruling: the completed100000-item diagnostic measured a2.375s committed request,0.214s response completion and full readback with zero diagnostics. Select a separate30s **content-recording** completion ceiling while retaining2s for existing small activity/raw-transform observations. This is conservative failure containment, not a universal performance SLO or a delay imposed on successful operations. Preserve synchronous borrowed ownership, finite failure reporting and cancel/shutdown drain; verify actual product wiring and concurrent control success before adoption. The earlier diagnostic changed all observers and alone does not prove this separated product behavior. No queue, retry, schema or performance-tuning work is added by this ruling.

## Established cause

The actual stable codec rejects the reported complete histories at `protocolcore.Request.Validate` because `MaxMessageCount` is4096. Two exchangecontent projection/readback guards also depend on that constant. Merely bypassing the first check can still fail recording or reading.

Request decode and encode loops repeatedly merge immutable translation reports. Merge copies the accumulated notice prefix twice on every iteration, including empty reports. The existing isolated diagnostic measured approximately667.5MB cumulative allocation for a 4.15MB/4111-item request with IDs before returning the count error. This is cumulative allocation, not peak memory. The same-dialect path discards the notices only after paying the cost.

Unchanged script components can process complete 4111-item bodies and access/edit the final sentinel, but their allocations are significant: approximately85–158MB cumulative allocation at4.15MB, and a diagnostic child peak up to237.5MB near16MiB. These are direct component observations, not complete Pipeline/Runtime performance or a hard VM memory guarantee.

### Verified record-capacity conflict and scoped implementation ruling

An actual source-matched experiment now establishes that the existing32MiB encoded-record contract cannot preserve every already-supported response: the real Responses decoder accepts one6MiB literal `&` text reply (6,291,669B wire), but ordinary record JSON escapes it into a37,748,810B single block and NewRecord rejects it. An equal-size `x` control records correctly. Neither request-only reservation nor a smaller response allowance preserves the requested behavior. No live data or provider was used.

The necessary record-seam change is therefore included in this approved hotfix: keep the same logical canonical bytes, SHA256 identities, message/block indices and history relationships, while processing oversized logical blocks as bounded physical fragments using existing block manifests/ref ownership. Retain32MiB physical rows and1MiB pages, not32MiB as a universal total-history allocation. The existing32MiB byte-returning canonical convenience remains bounded; complete persistence and streaming validation must not depend on that convenience allocating the entire record.

Schema1 reuse is the selected implementation target, not a claimed proof. The exact deterministic format, strict reassembly/tamper checks, old-record hashes, expiry/GC, atomic publication, backup/restore and old-binary behavior are mandatory gates in `../plans/2026-10-06-long-session-coupled-admission.md`. If they fail, the controller must revise the internal representation before defaults switch; no implicit SQL-bound change or ad-hoc data rewrite. The user's earlier data-upgrade approval does not authorize touching the live database during testing.

Execution capacity is a separately measured internal policy, not an invented32MiB semantic allowance. Admit a complete resource envelope before reading the request body; ordinary temporary occupancy waits with cancellation and no per-waiter body retention or synthetic busy error. Control, heartbeat and auxiliary work do not enter that gate. Release only after real owned work drains. This bounds active body materialization, not arbitrary transport connection cardinality; transport buffers and real SQL/control deadlines remain acceptance gates. No public bypass or production legacy-count path remains after the final switch.

## Implementation boundaries

### 1. Linear report accumulation

Keep the immutable TranslationReport contract, including exact notice order, codes, paths and partial reports on failures. Add a small owned accumulator used by request-length loops; append notices once and freeze at a return boundary. A built report and its Notices() output cannot be mutated through later builder use or other returned slices. No report deduplication, omission, pooling, unsafe sharing or skipped validation.

Apply the shared accumulation behavior to Responses request decoding, Anthropic request decoding and Chat request encoding, including their variable-length nested notice loops. Do not rewrite unrelated response streaming or extract a general-purpose collection framework. The initial implementation unit leaves the4096 guard intact until resource and record acceptance are changed together.

### 2. Resource-safe admission and complete validation

Replace the business message-count rejection with explicit checked accounting of materialized structure and retained/copyable data. The accepted shape must remain independent of a new arbitrary message-count constant. Keep16MiB request wire/decompressed input,32MiB retained content, existing nested/tool/JSON protections and unsupported-input semantics unless a separately justified part of this same scope requires a change.

Measure the real new decode/selector/transform/encode/record path before freezing capacity values. Account for dense empty nodes as well as text, JSON fragments, tool results, native/opaque history, notice paths and concurrently live clones. Check before material expansion where possible; a final guard after all allocations is not the resource strategy. Direct construction of protocolcore.Request must not evade the equivalent validation. Overflow must reject deterministically, without a credential/model send or a fabricated permission grant.

Maintain full-tail semantic validation, duplicate/case-fold detection and exact original same-dialect wire ownership. Never truncate, summarize, silently remove unknown/native fields or convert complete history into a projection-only substitute. Model-only rewrites may alter only the approved fields. History tool records must not become fresh executable tool intents; new response tools still use actual approval.

Use measured isolated2/4 concurrent requests to verify the selected resource strategy. If the permitted load blocks control/heartbeats or creates unbounded retained work, contain that necessary execution/recording seam in this release; do not ship a newly reproducible defect as a warning. No increased UI/heartbeat timeout, hidden recording loss, unbounded queue or claimed Goja heap limit based on its execution timer.

### 3. Complete records and existing data

Change both exchangecontent message-count guards consistently with execution admission. The actual Store and page reader must round-trip all retained history and the tail, including incremental suffix/checkpoint/replay cases. Maintain existing Full, metadata-only and Off meanings, redaction, authorization and expiry; Off/metadata-only must not start spooling private bodies to disk.

Prefer unchanged schema1 and record format; existing SQL capacity100001 is not the new business limit. Prove accepted resource-bounded requests plus response fit the storage contract rather than discovering incompatibility after forwarding. Do not import the development branch's schema2 or reset/rebuild a user database.

Use populated synthetic schema1 data for backup→upgrade→same-session continuation→restart/read→restore tests. A schema-compatible new long record may still be unreadable by old0.1.23 code; test and document that. Do not promise binary-only downgrade or overwrite post-upgrade data during rollback. No experiments on the user's live database, trust store, Accounts or active Captures.

## Acceptance and delivery

1. Reproduce the original failure with synthetic4095/4096/4097/4102/4111 bodies, approximately4MiB and significantly longer representative history. Include native reasoning/compaction, tools/results, images, many tiny nodes, last-item errors and byte/resource boundaries. Keep first failures.
2. Real Pipeline/Runtime forwarding must succeed with original and managed credentials, complete-tail Account Selector, no-op/body-edit transforms, model mapping and supported cross-dialect conversion. An exact private upstream receives one complete model request; permissions, frozen account/route and cancellation/drain stay intact.
3. Real Store and paged reader reconstruct complete permitted history with no new recording-degraded warnings in the supported load. Verify actual data/backup/restart behavior, not only a codec or fake recorder.
4. Fixed official Codex client drives the candidate Runtime through the long-session exchange and continues the same native session. Relevant stable compatibility/CI gates remain; the unfinished later MCP46 matrix is not a prerequisite for this scoped release.
5. Record per-stage elapsed time, cumulative allocation and process peak memory distinctly, including dense inputs, cancellation and2/4 concurrent actual requests. Thresholds are backed by these experiments, not success at4111 alone. Recheck source identity after changes.
6. Same-source affected Go normal/race and existing applicable repository CI, Flutter/Chrome, contracts, secret-store/packaged safety and independent task/whole-hotfix review. No skipped gate is relabeled a pass and no unresolved release-blocking finding is waived.
7. Recheck version availability and remote main before freeze. Merge only the reviewed hotfix through the existing protected flow; use a clean genuine Git checkout for final provenance because local linked-worktree build stamps are known misleading. Existing protected macOS signing/notarization/install evidence gates remain; do not change reviewers/protection. Publish verified macOS and Linux assets, verify download hashes/signature/ticket, then update and verify the Homebrew cask.

No automatic user App restart. Tell the user when to stop active Captures, back up and upgrade; a process restart for installation does not require abandoning the original client session. Release notes promise only what this candidate proves.

## Sequencing

The independently reviewed notice plan is `../plans/2026-10-06-long-session-linear-notices.md` (d978bcf); the shared compressor resource plan is `../plans/2026-10-06-bounded-evidence-compression.md` (2c3b873). Both are complete prerequisites, not4096 acceptance. The adopted coupled plan is `../plans/2026-10-06-long-session-coupled-admission.md`; it covers accounting, complete record representation/readback, pre-body backpressure, measured capacities and the final default switch. Actual end-to-end/client/data/resource acceptance and the existing release chain are required parts of this same approved hotfix, not optional follow-up releases. The controller settles internal implementation choices from evidence without asking the user to reapprove each code edit.

Self-review: the first release remains explicitly scoped; no claim of infinite history, universal script memory isolation or completion of the full Goal. Every execution/record/data invariant has an actual acceptance counterpart. Performance and resource evidence must precede a release capacity claim.
