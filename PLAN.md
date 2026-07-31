# M0.9 Codex Responses HTTP Expansion Plan

Status: active
Created: 2026-07-31
Implementation baseline: `7560937a358f23d1924a7dffa1978537171efb12`

## Objective

Add one fixed Codex CLI 0.145.0 macOS arm64 input to the existing packaged
Desktop assembly without creating another runtime, configuration authority, or
provider path. The first slice is OpenAI Responses over HTTP/SSE at the Agent
edge, translated through the immutable protocol IR to the existing OpenAI Chat
provider edge and controlled by the same Access snapshot, Exchange, approval,
Offline Hold, CaptureRun, and shutdown tree.

This plan extends the supported client/protocol matrix. It is not a Preview,
Release, installer, Server, or multi-platform plan.

## Design authority

The read-only design repository remains authoritative:

- `docs/design/18-production-composition.md` sections 7 and 8;
- `docs/design/17-validation-roadmap.md` sections 4, 5.2, and 6;
- `docs/design/07-protocol-translation.md` sections 2 through 6;
- `docs/design/10-client-compatibility.md` sections 2 through 6;
- `docs/design/14-technology-stack.md` sections 4 and 5;
- `docs/design/20-transparent-hold-and-resume.md`;
- `docs/adr/0006-agent-endpoint-mitm-allowlist.md`;
- `docs/adr/0012-planned-offline-core-egress-gate.md`.

The design repository must not be modified or copied into this implementation
repository.

## Required invariants

1. `productruntime.Start` remains the sole business composition root, and the
   Desktop Host remains the sole Desktop readiness publisher.
2. SQLite remains the only durable Access authority. Every Codex Exchange
   resolves exactly one immutable `AccessPlanSnapshot` through the existing
   `SnapshotResolver`.
3. OpenAI Responses is a new typed client codec and operation capability, not a
   raw JSON passthrough, SDK hot-path dependency, string registry, or second
   static configuration model.
4. Client wire identity, IR call identity, provider wire identity, Responses
   item ID, and function call ID stay in distinct typed namespaces. Unknown or
   colliding correlations fail closed.
5. The existing OpenAI Chat backend codec and controlled provider transport are
   reused. No client credential, header, or original target may leak to the
   provider request.
6. Every external byte remains behind the runtime-owned Offline Hold action and
   egress leases. Held work retains the exact frozen plan/target identity.
7. Tool arguments remain bounded and incomplete tool calls remain invisible.
   Complete tool intent, output, and terminal events stay behind the durable
   approval barrier.
8. Responses `failed`, `cancelled`, malformed terminal sequences, and truncated
   streams are errors/aborts, never successful stop reasons.
9. Same-dialect and cross-dialect translation both emit an explicit
   `TranslationReport`; unknown does not become zero and loss is never silent.
10. Fixed-client support is bound to canonical executable and compound release
    evidence plus a typed launch recipe. Name, `--version`, User-Agent, or an
    npm wrapper alone is insufficient.
11. WebSocket upgrade behavior is explicit and fail closed. This slice may
    prove the fixed Codex HTTP fallback from a bounded 426 response, but it may
    not claim Responses WebSocket semantic conversion or successful WS support.
12. Secret values never enter Access, SQLite, reports, command lines, logs, or
    protocol IR. The development file SecretStore remains development-only.

## Development method

Every production change follows a TDD loop:

1. introduce a deterministic failing fixture at the owning boundary;
2. implement the smallest typed production behavior;
3. run focused unit and race tests;
4. run structural and generated checks;
5. run the affected integration contract;
6. freeze one coherent commit before moving upward.

No real-client acceptance assertion may be weakened to accommodate Codex,
provider, or timing behavior. Safe diagnostics contain only typed reason codes,
event kinds, counts, hashes, and provenance; they never capture semantic
payloads or credentials.

## Bottom-up execution order

### 1. Freeze the Responses semantic contract

- [x] Capture the fixed client's initial HTTP request shape without semantic
  payloads or credential values. The pinned client sends stateless streaming
  input items, developer-scoped `additional_tools`, function/custom/namespace
  definitions, custom Lark grammar, optional output-token limits, reasoning
  context, encrypted-reasoning inclusion, text verbosity, prompt-cache identity,
  and bounded client metadata.
- [x] Add official-SDK-oracle fixtures for fixed Codex request, complete
  response, SSE text, function call, function output, usage, failure,
  cancellation, malformed ordering, and unknown extensions.
- [x] Extend immutable protocol-core values only for concepts actually required
  by those fixtures: Responses item/call identity, refusal/error/abort, tool
  correlation, and known/unknown usage details.
- [x] Keep observation-only provider extensions typed and immutable; define
  explicit translation notices or rejection for every non-forwarded concept.
- [x] Prove deep input/output alias isolation, deterministic cloning, strict
  bounds, fuzz convergence, and race safety.

### 2. Implement the pure OpenAI Responses client edge

- [x] Add a typed Responses client codec for bounded HTTP request decoding,
  complete-response encoding, and incremental SSE encoding.
- [x] Preserve source ordering and independently track response, output item,
  content part, function call, and argument-fragment state.
- [x] Require a valid terminal event; reject unknown-item deltas, duplicate
  terminal events, incomplete calls, invalid JSON arguments, and trailing
  semantic events.
- [x] Map generic function calls/results through stable IR identities without
  treating item ID and call ID as interchangeable.
- [x] Keep OpenAI SDK imports restricted to tests.

### 3. Compose Responses to the existing Chat backend

- [x] Generalize the typed protocol path so Responses client edge and OpenAI
  Chat backend edge compose explicitly without a global registry.
- [x] Reuse the existing Chat request encoder and response/SSE decoder; do not
  create a second provider client or transport.
- [x] Make TranslationReport loss policy explicit for developer messages,
  refusal, reasoning, usage details, tool choice, and unsupported Responses
  controls.
- [x] Prove ordinary text remains incremental while the first unresolved tool
  fragment fences the required suffix until durable approval.
- [ ] Prove retry/commit-ledger semantics remain unchanged after any
  client-visible Responses event.

### 4. Compile the Codex operation into the sole Access plan

- [x] Add the typed codec identity, revision, client dialect, and exact
  Responses operation capability to the explicit compiler catalogs.
- [x] Support the exact fixed-client `/v1/responses` HTTP operation; classify
  management, upload, background, Realtime, and unknown semantic paths without
  routing them into model translation.
- [x] Keep `ClientOrigin` separate from `ProviderTarget`; preserve Access
  revision, `PlanHash`, endpoint, transport, authority, and model mapping.
- [x] Apply any schema change as one versioned SQLite migration and recover the
  identical plan/hash on reopen.
- [x] Prove invalid capabilities, stale CAS, failed transaction/publication,
  projection poison isolation, and old-handle immutability.

No SQLite schema change was required: operation definitions are immutable
compiler dependencies rather than a second persisted configuration model.
Normal close/reopen recompiles the same committed aggregate into the same
Responses operation, revision, content, and `PlanHash`.

### 5. Extend Exchange and loopback ingress

- [ ] Dispatch the exact Responses operation through the same one-resolve
  Exchange and revalidate frozen Codex AgentEndpoint evidence on every request.
- [ ] Strip client auth and hop-by-hop headers before IR/provider boundaries;
  retain only redacted connection and translation evidence.
- [ ] Route every provider attempt and resume probe through the existing
  controlled-egress tree.
- [ ] Return an explicit bounded 426 for unsupported Responses WebSocket
  upgrades and prove the fixed client falls back to HTTP without bypassing
  CaptureRun, endpoint authorization, or Exchange.
- [ ] Cover persistent CONNECT plan changes, hold-entry races, cancellation,
  shutdown, malformed SSE, tool approval, and multi-Access isolation under
  `-race`.

### 6. Add the fixed Codex launcher contract

- [ ] Pin Codex CLI 0.145.0 macOS arm64 compound release evidence and typed
  launch recipe in the immutable client catalog.
- [ ] Verify canonical wrapper/native-child paths and digests before issuing a
  CaptureRun grant; freeze the catalog revision in that grant.
- [ ] Inject only the owned proxy/Root/fallback inputs and a non-secret client
  placeholder; remove conflicting ambient proxy, CA, base-URL, and credential
  variables.
- [ ] Prove child supervision, heartbeat, exit status, SIGINT, and cleanup
  without introducing a Codex-specific daemon or control API.

### 7. Run one packaged Codex HTTP vertical

- [ ] Build App, daemon, launcher, and acceptance runner from one standalone
  clean revision with pinned toolchains and matching digests.
- [ ] Run fixed Codex `exec` through CaptureRun, CONNECT/MITM, exact Responses
  dispatch, one Access snapshot, Chat provider translation, controlled egress,
  and incremental Responses SSE.
- [ ] Prove normal text, function call/output approval, planned Hold/Resume,
  SIGINT, `exec resume`, daemon termination, and SQLite reopen at the supported
  HTTP boundary.
- [ ] Retain private mode-`0600` deterministic and credentialed reports with
  source/artifact/toolchain provenance and no semantic payload or secret.
- [ ] State explicitly that HTTP fallback evidence does not prove successful
  Responses WebSocket semantics or TUI interaction.

### 8. UI only after runtime evidence

- [ ] Reuse existing authenticated control routes wherever possible.
- [ ] If a new user-visible capability/state is unavoidable, add its stable
  language-independent code and synchronized `en-US`/`zh-CN` keys only after
  storage, compiler, protocol, Exchange, ingress, launcher, and acceptance
  contracts pass.
- [ ] Do not add client-specific configuration shortcuts, secret display, raw
  wire diagnostics, or a second Access editor.

### 9. Final gates

- [ ] `make check`
- [ ] `go test ./...`
- [ ] `go test -race ./...`
- [ ] `go vet ./...`
- [ ] `go mod tidy -diff`
- [ ] `go mod verify`
- [ ] `govulncheck ./...`
- [ ] pinned frontend TypeScript/unit/build checks
- [ ] pinned Rust format/tests and an honestly reported RustSec warning set
- [ ] `git diff --check`
- [ ] clean source and matching packaged-report provenance

## Completion statement

This plan is complete only when evidence supports:

> One fixed Codex CLI 0.145.0 macOS arm64 build can enter the same packaged
> VibeMate ProductRuntime through the authenticated Desktop launcher and exact
> AgentEndpoint, translate bounded OpenAI Responses HTTP/SSE semantics through
> immutable IR to the existing OpenAI Chat provider path, cross the durable
> tool-approval and Offline Hold boundaries, converge on cancellation, and
> recover committed SQLite state. The evidence is bound to one clean build and
> contains no secret or semantic payload.

Even then, VibeMate is not Preview-ready or Release-ready. Successful Responses
WebSocket semantic conversion, TUI interaction, ChatGPT-login control traffic,
additional Codex versions, Server, Windows/Linux, native secret protection,
physical sleep/network removal, signing/notarization, installers, and full
product UI remain outside this plan.

## Archive protocol

After every checkbox and the completion statement are proven:

1. move this file to a date-named path under `docs/plans/archive/`;
2. record frozen source, artifact, report paths and hashes;
3. create the next root plan from the then-current implementation and read-only
   design;
4. keep the successor bottom-up, TDD-driven, and UI-last.
