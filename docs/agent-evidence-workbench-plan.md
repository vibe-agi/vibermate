# Agent Evidence Workbench Plan

Status: execution plan for the Flutter desktop migration and deep Agent
protocol validation. This document does not expand any current capability
claim by itself.

## Outcome

Deliver a runnable ViberMate App that can explain one managed Agent run at two
levels without inventing evidence:

1. a compact conversation view for daily reading; and
2. a wire/evidence view for debugging, audit, and security investigation.

Claude and Codex must both be exercised through their real local clients,
including tools, MCP, reasoning/thinking evidence, subagents, nested
subagents, streaming, cancellation, and provider failures.

## Delivery checkpoints and current state

| Checkpoint | Deliverable | State |
| --- | --- | --- |
| A | Preserve real provider status and accept valid Base64URL identities | Done; focused tests pass |
| B | Establish Claude/Codex protocol truth with real clients, MCP, parallel Agents, and supported nesting | Done; observed matrix is documented |
| C | Persist encrypted Raw HTTP evidence through a bounded batch writer and flush barriers | Done; terminal scope drain, recovery, audited reveal, and concurrent queue bounds pass focused tests |
| D | Split independently distinguishable Agent streams into separate Conversation projections | Done; the index is flat, stably ordered, and never mixes independently identified streams |
| E | Freeze one Flutter component/type/spacing system and rebuild the screens against it | Done for implementation and automated wide/390 px, keyboard, semantics, theme, and overflow gates; operator screenshot review remains because this host cannot grant screen-recording permission |
| F | Build and inspect a packaged macOS App with real runtime data | Done for signed package construction, isolated real-daemon launch, packaged CLI discovery, and shutdown; operator screenshot review remains |

There are two user-testable App checkpoints instead of one long invisible
rewrite:

1. after D, build a functional evidence App so Raw capture and Conversation
   separation can be tested with real Claude/Codex runs; and
2. after F, build the visually consolidated delivery candidate.

## Non-negotiable model boundaries

- `CaptureRun` remains the proven conversation authority for a managed run.
- A manual capture remains Exchange-scoped until an explicit session authority
  exists. Time, title, workspace, and similar-looking prompts are not merge
  authorities.
- A distinguishable Agent stream becomes its own Conversation projection
  inside the enclosing Capture authority. It is not promoted into a new
  runtime authority.
- P0 does not attempt to associate a subagent Conversation with a parent. The
  product requirement is isolation, not reconstructed lineage: Conversations
  are ordered by first observed Exchange time, observed name, and projection
  ID, and each selected timeline contains only that Conversation's evidence.
- P0 is deliberately flat: main Agent and subagent Conversations are
  independently selectable and their messages are never interleaved as one
  chat. Parent/child reconstruction and a tree UI are outside this delivery.
- Missing relationship evidence is normal. Even when the wire happens to
  expose a parent reference, P0 may retain it as evidence but does not require
  it for grouping, ordering, or display.
- `cc_is_subagent=true` is sufficient to separate a Claude Exchange into a
  client-asserted subagent thread. It is not, by itself, proof of a parent,
  name, or description.
- No relationship is inferred from time, title, prompt similarity, tool-call
  proximity, or workspace. If the protocol cannot prove a stable subagent
  instance identity, each Exchange remains a separate subagent Conversation
  rather than risking mixed content.
- Visible assistant text is not labelled as hidden thinking. Reasoning summary,
  opaque/encrypted reasoning or signatures, assistant text, tool calls, and
  tool results remain distinct evidence kinds.
- Normalized content never replaces raw evidence. Both views point back to the
  same Exchange and Attempt identities.

## Priority

### P0: current delivery

- Preserve real upstream failure status while returning ViberMate's safe error
  envelope.
- Accept all valid Base64URL resource identities in the Flutter client.
- Capture and expose the raw evidence layers required to diagnose Claude and
  Codex.
- Separate main and subagent traffic into flat Conversation projections.
- Present the result through a compact thread navigator and a Raw inspector.
- Stabilize the Flutter visual system before doing another screen-by-screen
  polish pass.
- Complete real-client, load, accessibility, narrow-layout, and packaged-App
  verification.

### P1: subsequent vertical, not claimed by P0

- Controlled request editing, breakpoint interception, and replay.
- Diffing original ingress, transformed egress, provider response, and
  downstream response.
- Export/import of a redacted evidence bundle.

Editing and replay must never rewrite retained history. They create a new
Attempt or Exchange with an explicit derivation reference.

## Phase 0 — Stabilize known deterministic defects

Work:

- Preserve an upstream `529` as downstream HTTP `529` while keeping the safe
  ViberMate error body and reason header.
- Permit leading `_` and `-` in Flutter resource IDs, matching Base64URL IDs
  produced by the runtime.

Gate:

- Focused Go and Flutter tests pass.
- No raw provider body or credential is leaked by failure handling.

Current state: implemented and focused tests pass.

## Phase 1 — Establish protocol truth with real clients

Do not design the final thread schema or Raw UI from remembered API formats.
First capture a bounded, secret-scrubbed fixture matrix from real local
clients.

Claude matrix:

- normal streaming response;
- visible assistant text and tool use/result;
- MCP tool use/result;
- thinking enabled, including visible summary and opaque signature/status;
- one subagent;
- concurrent subagents;
- nested delegation capability discovery. Claude Code `2.1.228` explicitly
  does not allow subagents to create other subagents, so this is recorded as an
  unavailable client capability rather than a failed ViberMate feature;
- cancellation, `429`, `529`, and malformed/provider failure paths.

Codex matrix:

- normal Responses streaming response;
- function/tool use and result;
- MCP tool use and result;
- reasoning summary and encrypted/opaque reasoning items when emitted;
- one subagent and nested/multi-agent behavior supported by the installed
  client;
- cancellation, retryable provider status, and malformed stream paths.

For every case, produce a mapping table:

`wire item -> raw evidence -> normalized block -> thread signal -> UI label`.

Gate:

- Every normalized field used by the UI is backed by a captured fixture and a
  decoder test.
- Unsupported or unavailable fields are labelled unavailable; no test fixture
  fabricates them.
- Fixtures contain no reusable account secret, cookie, authorization value, or
  private user prompt.

Current state: the baseline, MCP + one subagent, Claude parallel-subagent, and
Codex parallel + nested real-client scenarios pass. The observed field mapping
and unsupported capabilities are frozen in `agent-wire-evidence-matrix.md`.

## Phase 2 — Raw evidence and storage contract

### Evidence layers

One semantic Exchange can have four distinct wire envelopes:

1. client ingress request as received by ViberMate;
2. provider egress request after route, model, proxy, and credential handling;
3. provider response as received by ViberMate; and
4. client downstream response after any protocol transformation.

Each envelope records:

- method/status, URL authority/path/query, normalized header fields and their
  repeated-value order, and trailers;
- exact body bytes or stream frames, content encoding, byte count, and digest;
- captured/truncated/unavailable state and the reason;
- Exchange, Attempt, Capture, Environment revision, Route, Account revision,
  and timing references;
- whether secret material is present and whether it can be revealed.

P0 Raw evidence is an HTTP/protocol envelope, not a packet capture. Retained
body bytes and stream frames are byte-exact. Go's parsed HTTP boundary cannot
honestly preserve original header casing or the inter-field wire order, so the
UI and export contract must not label those normalized headers as byte-exact
wire bytes. A pre-parser byte tap is a separate later capability.

### Secret and retention policy

- Secret-bearing headers and bodies are never written as plaintext SQLite
  columns.
- The schema separates safe metadata from encrypted payload blobs. The
  encryption key belongs to the host secret boundary, not the database.
- The default UI masks authorization, cookie, API-key, and configured secret
  fields. Reveal is a single explicit local action: it asks for no reason and
  presents no confirmation dialog. Plaintext is temporary, the runtime records
  actor/envelope/outcome/time automatically, and reveal is unavailable when
  the recording policy did not retain the value.
- Raw body capture follows explicit Environment content-governance and
  retention policy. Metadata-only mode remains honest about missing bytes.
- Digests are calculated over the original bytes so normalized and Raw views
  can be cross-checked.

### Write path

Only high-frequency append-only evidence uses the batch writer. Editable
authorities and CAS operations remain synchronous transactions.

The evidence writer has:

- a bounded queue measured by records and bytes;
- batch commit thresholds for elapsed time, records, and bytes;
- one SQLite transaction per batch;
- explicit backpressure and an observable degraded/incomplete state instead of
  silent loss;
- a per-Capture watermark and flush barrier;
- forced flush on Capture finish/revoke and daemon shutdown, bounded by the
  caller's shutdown deadline;
- crash semantics that state the maximum unflushed window;
- metrics for queue depth, last durable watermark, flush latency, and failures.

Initial thresholds are benchmark inputs, not API promises. Tune them with the
real Claude/Codex load matrix.

Current implementation state:

- semantic model Exchanges record client ingress, provider egress, provider
  response, and client downstream envelopes against the same Exchange;
- provider Attempts additionally freeze the selected upstream Endpoint, Route,
  and Account, while client-side envelopes deliberately do not invent one
  Attempt or Account before retry selection exists;
- encrypted payloads enter a record-and-byte bounded queue and commit in
  batches; metadata-only recording does not require creating an encryption
  key or retaining a body prefix;
- Capture terminal paths flush the latest Raw watermark admitted for that
  Capture, and daemon shutdown flushes every admitted envelope;
- successful per-Capture flushes release their in-memory watermark unless a
  newer admission raced the snapshot;
- finish/revoke first closes request admission for the Capture scope, drains
  already admitted requests, then flushes and commits the terminal transition;
- metadata reads never return ciphertext, nonce, or plaintext; reveal requires
  write authority and automatically records actor, envelope, outcome, and time
  without asking the local researcher to justify the action;
- recovery reports unclean writer sessions and the configured maximum
  unflushed interval;
- the Flutter inspector loads safe metadata on demand and retains revealed
  plaintext only in the local widget state until hide, collapse, navigation,
  or disposal.

Phase 2 does not claim packet capture, pre-parser header casing/order, editing,
or replay. Those remain explicitly outside P0.

Gate:

- A database inspection proves no configured secret exists in plaintext.
- A load test with concurrent Agents proves bounded memory and substantially
  fewer commits than evidence records.
- Capture completion is not reported durable before its evidence watermark has
  flushed, or it is explicitly marked incomplete.
- Graceful daemon shutdown flushes all admitted evidence; a forced crash loses
  no more than the documented batch window and recovery reports the gap.

## Phase 3 — Native Agent Conversation separation and hierarchy

### Projection

Build a rebuildable Conversation projection. Every distinguishable Agent
stream is a first-class selectable Conversation. Exact native parent evidence
may form a presentation tree; the durable Conversation remains independently
selectable. The projection carries:

- stable projection ID;
- display name and optional description only when observed;
- kind: main, subagent, or isolated subagent Exchange;
- status;
- protocol source and evidence strength;
- assigned Exchange IDs and source evidence references;
- activity and usage summary.

Claude separation order:

1. an explicit stable Agent/session identifier groups Exchanges into one
   Conversation;
2. `cc_is_subagent=true` proves that an Exchange is from a subagent, but not
   which subagent it belongs to;
3. explicit `Agent` tool-call/result metadata may supply a display name or
   description only when the identity can be joined without inference;
4. without a stable instance identifier, keep that Exchange as an isolated
   subagent Conversation; never merge by timing or prompt similarity.

Codex uses the same rule: explicit actor/session identity first, isolated
Exchange fallback otherwise. Native Claude parent-agent IDs and Codex parent
thread/fork IDs are retained as opaque client evidence. The UI renders them as
a hierarchy only when both actors resolve inside the same client session.

Conversation identity must not depend on timestamps or generated titles.
Ordering is stable: first observed Exchange time, then observed display name,
then projection ID as the final tie-breaker.

### UI

Desktop layout becomes adaptive rather than permanently three-column:

- left: Capture/Conversation list, resizable and collapsible;
- middle: resizable Agent Conversation tree, shown only when more than one
  Conversation exists; exact descendants are indented beneath their parent,
  while unresolved or unattributed Conversations remain roots;
- right: selected Conversation timeline and evidence.

Selecting an item filters to that Conversation. An optional `All activity`
audit stream may exist only as a clearly labelled transport chronology; it is
not presented as a chat. The default reading view never mixes Agent messages.
On a narrow viewport, the two navigators become drawers/sheets rather than
squeezing the timeline.

Gate:

- main, sibling, concurrent, and otherwise unassigned subagents are
  independently selectable in the real Claude fixture. Exact Claude and Codex
  parent edges produce a multi-level tree without changing Conversation
  ownership.
- The equivalent supported Codex cases are separable without Claude-specific
  assumptions.
- A single-thread conversation shows no empty middle rail.
- If the protocol exposes only `is_subagent` and no stable instance identity,
  the UI shows isolated Exchange Conversations and documents why they were not
  merged.
- Hundreds of Turns and dozens of Conversations remain scrollable and keyboard
  navigable.

Current state: implemented. Explicit conversation identity groups Exchanges;
`is_subagent` without an instance identity produces an isolated Exchange
Conversation. The Flutter directory is a stable creation-ordered tree when
exact native parent evidence exists. It exposes no inferred relationship;
missing parents stay at the root.

## Phase 4 — Freeze the Flutter desktop design system

No further isolated size, radius, or spacing tweaks before these tokens and
components are fixed.

Baseline tokens to validate at actual macOS 1x/2x rendering:

- 4 px spacing grid;
- 14 px primary content, 13 px dense rows/controls, 12 px metadata, 11 px only
  for truly tertiary captions;
- 22 px page title, 18–20 px modal title, 14 px section title;
- 32 px standard field/button height, 28 px compact toolbar/icon button;
- one control radius, one panel radius, one modal radius; pill radius only for
  statuses/tags;
- one icon size per context and official Agent brand marks where appropriate;
- selected, hover, pressed, disabled, and `focus-visible` states are distinct;
- Auto, Light, and Dark themes include the native title bar and system changes.

Standardize before screen work:

- text field/search field/select;
- primary, secondary, quiet, destructive, and icon buttons;
- tab bar and segmented control;
- status badge and metadata row;
- table/list row and empty state;
- panel/card/turn shell;
- modal/sheet with Escape, focus trap, initial focus, and restoration;
- tooltip and technical-ID reveal.

Current state: implemented. Shared type, spacing, control-height, radius,
theme, focus, modal, table, and responsive primitives now drive the Flutter
workbench. Widget tests cover wide and 390 px layouts, keyboard and semantic
authority, English and Simplified Chinese, resizable/collapsible directories,
and Light/Dark/Auto behavior. Native macOS tests cover title-bar theme mapping
and preferences persistence. The remaining visual gate is human inspection of
real rendered screenshots; macOS screen-recording permission is unavailable
to this execution host, so those images must come from an operator run.

Gate:

- component gallery/golden coverage in English and Simplified Chinese;
- no control-height or radius variants outside named tokens;
- keyboard and screen-reader semantics verified;
- Light, Dark, and Auto title bars match the content theme.

## Phase 5 — Rebuild screens from information hierarchy

Apply the frozen system in this order:

1. Conversation timeline, flat Agent navigator, thinking/reasoning presentation, and Raw
   inspector;
2. Capture list/detail and Environment switching;
3. Endpoints/accounts and Environment editing;
4. Network approvals, connections, egress, and rules;
5. Settings, install/repair flows, dialogs, empty states, and narrow layout.

Rules:

- Remove prototype explanatory prose from the normal path. Put necessary
  explanation behind concise help/tooltips or a first-use flow.
- Show human names first; reveal technical IDs on hover/focus or in Raw/Evidence.
- Do not repeat the same Environment, Endpoint, Account, or status in adjacent
  regions.
- Use tables for repeated transport facts and key/value groups for a single
  record. Do not scatter evidence into an unaligned label cloud.
- System context is collapsed by default. The latest active Turn is expanded;
  user scroll position is respected.
- `Scroll to latest` is an action at the right edge, not an apparent sort mode.
- The Turn map follows the visible Turn, supports hundreds of Turns, and does
  not steal horizontal space on narrow layouts.

Gate:

- desktop and 390 px evidence screenshots are inspected individually, not just
  generated;
- 7–8 concurrent Agents, long history, empty state, failure state, and active
  streaming state remain usable;
- no overflow, clipped modal, stale Turn-map selection, forced autoscroll, or
  mixed-agent timeline remains.

Current state: implemented and covered by deterministic widget behavior tests,
including 390 px Chinese layouts, 7–8 concurrent Captures, long flat Agent
Conversation indexes, Turn-map synchronization, direct Raw reveal, and
manual-capture revoke. Human screenshot review remains an explicit follow-up,
not an inferred pass.

## Phase 6 — End-to-end verification and delivery

Run, at minimum:

- Go formatting, focused tests, full `go test ./...`, race-sensitive packages,
  `go vet`, and repository checks;
- Flutter generation, formatting, analyze, unit/widget/golden tests, and native
  host tests;
- real managed-run Claude and Codex matrices from Phase 1;
- MCP and recursive subagent live tests;
- evidence queue load/shutdown/recovery tests;
- packaged macOS App launch, runtime Ready, real data rendering, keyboard,
  screen-reader semantics, Light/Dark/Auto, and narrow layout;
- `git diff --check` and a final dirty-worktree ownership audit.

Build the App only after all earlier gates pass. Report:

- exact App path and rebuild command;
- tests run and their real counts/results;
- verified Claude/Codex fields and unsupported fields;
- whether Raw bytes were captured, truncated, encrypted, or unavailable;
- known limitations and every capability deliberately not claimed.

Current state: `dist/ViberMate.app` is built from the final Flutter source and
contains the signed `vibermated` and `vibermate` sidecars. Isolated packaged
tests prove daemon readiness, Control API authority, CLI discovery, observable
unexpected daemon exit, and desktop-shell launch/termination without using the
operator's existing runtime data. Real-client Claude/Codex results remain the
bounded observations recorded in `agent-wire-evidence-matrix.md`; ordinary
test runs do not spend current-login allowance.

## Execution discipline

- Work on one phase at a time; do not polish downstream UI against an unstable
  evidence contract.
- At the first real failure, report it, fix it, and rerun the smallest proving
  test before broad suites.
- No background development server is left running.
- No reset, checkout, or overwrite of the shared dirty worktree.
- No commit or push without explicit authorization.
- A screenshot is evidence only after the App is Ready and fixture/live data is
  visible, and after the image itself has been inspected.
