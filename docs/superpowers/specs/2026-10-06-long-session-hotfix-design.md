# Stable long-session hotfix design

Status: the user explicitly approved this independent first-release scope on 2026-10-06: repair the 4096 long-session blocker from the stable version; retain complete history; verify forwarding, recording, reading and resource usage; then review, sign, notarize and publish GitHub/Homebrew. MCP and global-default egress ship later. This is not approval to merge the unfinished development branch or to change real user data.

## Outcome and baseline

A Codex client must be able to continue its existing long Client Session without deleting history, opening another session, switching Accounts, or changing a script to work around an internal 4096-item limit. The observed 4102–4111-item approximately4MiB requests are concrete regression cases, not a new supported maximum. Preserve all already-configured same-dialect, Account Selector, model mapping, Transform Policy and supported cross-dialect behavior.

Start at stable main `c61f8c6b5e2196da8e99010c169d4e3c17797933`, verified source-equivalent to formal v0.1.23 `2237b3ae35e8097987b685f6bcd08224bde62f0f` except three release documentation files. Implementation branch `fix/long-session-hotfix-20261006`, isolated worktree `.worktrees/long-session-hotfix`. Original checkout and `.worktrees/production-readiness` retain all existing edits. No unfinished native/schema2/global-egress changes are prerequisites for this release.

The full production-readiness Goal remains intact. Completing this hotfix is an intermediate delivery, not completion of that Goal.

## Established cause

The actual stable codec rejects the reported complete histories at `protocolcore.Request.Validate` because `MaxMessageCount` is4096. Two exchangecontent projection/readback guards also depend on that constant. Merely bypassing the first check can still fail recording or reading.

Request decode and encode loops repeatedly merge immutable translation reports. Merge copies the accumulated notice prefix twice on every iteration, including empty reports. The existing isolated diagnostic measured approximately667.5MB cumulative allocation for a 4.15MB/4111-item request with IDs before returning the count error. This is cumulative allocation, not peak memory. The same-dialect path discards the notices only after paying the cost.

Unchanged script components can process complete 4111-item bodies and access/edit the final sentinel, but their allocations are significant: approximately85–158MB cumulative allocation at4.15MB, and a diagnostic child peak up to237.5MB near16MiB. These are direct component observations, not complete Pipeline/Runtime performance or a hard VM memory guarantee.

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

The first independently reviewable implementation plan is `../plans/2026-10-06-long-session-linear-notices.md`. It removes the measured amplification before deriving new admission costs. Subsequent implementation plans cover coupled admission/record rules, actual end-to-end/client/data/resource acceptance, then the existing release chain. These are required parts of this same approved hotfix, not optional follow-up releases. The controller settles internal implementation choices from evidence without asking the user to reapprove each code edit.

Self-review: the first release remains explicitly scoped; no claim of infinite history, universal script memory isolation or completion of the full Goal. Every execution/record/data invariant has an actual acceptance counterpart. Performance and resource evidence must precede a release capacity claim.
