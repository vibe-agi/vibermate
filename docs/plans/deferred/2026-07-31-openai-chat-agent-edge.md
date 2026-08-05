# OpenAI Chat Agent Edge Plan

Status: deferred
Created: 2026-07-31
Implementation baseline: `555cd567cebac55c6c0e4c2835fc92b355ad8b1f`

Deferred: 2026-07-31

This protocol-width expansion is intentionally paused. The next product work
must first correct the fixed-client evidence contract, audit the implementation
against the M1 delivery boundary, and close the narrower Proxy/Trust gap toward
an installable, trusted product. This plan remains valid backlog material but
is not the active execution order.

## Objective

Add OpenAI Chat Completions as a generic Agent-facing protocol edge and route
it through the existing immutable protocol IR to the existing OpenAI Chat
provider edge. The result must be one typed same-dialect production path inside
the sole ProductRuntime, Access snapshot, Exchange, approval, Offline Hold,
CaptureRun, proxy, and shutdown tree.

This is a protocol-capability slice. It does not add a concrete Agent-,
provider-, or model-specific production branch, and it does not certify any
particular executable release. It also does not complete the remaining
Anthropic Messages or OpenAI Responses backend directions.

## Design authority

The read-only design repository remains authoritative:

- `docs/design/07-protocol-translation.md` sections 2 through 6 and 9;
- `docs/design/10-client-compatibility.md` sections 2 through 6;
- `docs/design/14-technology-stack.md` section 4;
- `docs/design/18-production-composition.md` sections 2 through 8;
- `docs/design/20-transparent-hold-and-resume.md`;
- `docs/adr/0006-agent-endpoint-mitm-allowlist.md`;
- `docs/adr/0007-client-and-upstream-authority-separation.md`;
- `docs/adr/0012-planned-offline-core-egress-gate.md`.

The design repository is read-only. Prototype packages and static registries
are not implementation sources.

## Required invariants

1. `productruntime.Start` remains the only business composition root, and
   Desktop Host remains the only Desktop readiness publisher.
2. SQLite remains the sole durable Access authority. Every Exchange resolves
   one immutable `AccessPlanSnapshot` through the existing
   `SnapshotResolver` and never reads a repository or writer.
3. OpenAI Chat is selected by typed dialect, operation, codec revision, and
   capability values from explicit constructor catalogs. No global registry,
   `init()` registration, string service locator, or per-tool branch is added.
4. The client edge and backend edge remain distinct typed interfaces around
   canonical IR even when both dialects are OpenAI Chat. Same-dialect routing
   must not become raw request passthrough or bypass translation evidence.
5. Known message, tool, usage, stop, error, and stream semantics are preserved.
   Unknown or non-forwardable fields produce an explicit
   `TranslationReport` decision; loss is never silent.
6. Client wire IDs, IR call identities, and provider wire IDs remain separate
   typed namespaces. Tool arguments are bounded, incomplete calls are never
   published, and durable approval still fences tool output and terminal
   events.
7. Client authentication and hop-by-hop headers are stripped before the IR and
   provider boundaries. Provider authorization is applied only by the existing
   controlled transport after action/egress admission.
8. Every provider attempt and resume probe remains behind the runtime-owned
   Offline Hold coordinator and uses the exact frozen target, Access revision,
   `PlanHash`, egress, credential binding, and transport-profile identity.
9. `ClientOrigin`, provider origin, HTTP authority, SNI, and model mapping stay
   distinct. The existing strict TLS or literal-loopback exception is not
   widened.
10. Transport fingerprint selection remains independent of protocol or Agent
    identity. This slice preserves the current bounded HTTP/1.1 evidence and
    does not claim exact JA3/JA4 or HTTP/2 fingerprint parity.
11. Generic executable versions may use a configured Chat operation without
    inheriting fixed-release launch recipes. Release evidence remains an
    acceptance/launcher concern, not a protocol API.
12. Secret values and semantic payloads never enter Access, SQLite metadata,
    reports, logs, command lines, Activity, or ConnectionEvent evidence.

## Development method

Every production change follows a bottom-up TDD loop:

1. add a deterministic failing fixture at the owning boundary;
2. implement the smallest typed production behavior;
3. run focused unit, fuzz where applicable, and race tests;
4. run generated and structural checks;
5. run the affected integration contract;
6. freeze one coherent commit before moving to the next layer.

Official SDK output may be used as a test oracle. The transparent proxy hot
path continues to own its wire codec and transport.

## Bottom-up execution order

### 1. Freeze the Chat client wire contract

- [ ] Add bounded official-SDK-oracle fixtures for complete and streaming Chat
  requests/responses, text, parallel tools, tool results, usage, refusal,
  errors, cancellation, and malformed ordering.
- [ ] Record supported request path, method, query, content type, content
  encoding, and stream-option behavior in the typed operation catalog.
- [ ] Enumerate observed extensions and define preserve, notice, or reject
  behavior without retaining raw request envelopes in runtime evidence.
- [ ] Prove fixtures contain no credential or acceptance semantic payload.

### 2. Close only real canonical-IR gaps

- [ ] Reuse existing immutable message, content, tool, usage, stop, and error
  values wherever they already express Chat semantics.
- [ ] Add new protocol-core values only when a wire fixture proves a concept is
  otherwise inexpressible.
- [ ] Prove constructor validation, deep alias isolation, deterministic clone,
  strict bounds, fuzz convergence, and race safety for every added value.
- [ ] Keep observation extensions typed and separate from translatable
  capability decisions.

### 3. Implement the pure OpenAI Chat client edge

- [ ] Decode bounded Chat requests into immutable IR without selecting Access,
  credentials, provider transport, or a concrete Agent executable.
- [ ] Encode complete Chat responses and incremental SSE from immutable IR.
- [ ] Maintain a caller-owned stream state machine with explicit role/content,
  tool-call index/ID, argument-fragment, usage, stop, error, and terminal state.
- [ ] Reject duplicate terminals, invalid correlation, incomplete tools,
  invalid JSON arguments, trailing semantic events, and truncated streams.
- [ ] Keep official OpenAI SDK imports restricted to oracle tests.

### 4. Compose one explicit Chat-to-Chat path

- [ ] Build one typed `protocolpath.Path` from the new Chat client edge and the
  existing Chat backend edge without a registry or raw passthrough.
- [ ] Preserve safe same-dialect semantics while still rebuilding provider
  method, path, authority, headers, model, and authorization from the frozen
  plan.
- [ ] Return an explicit `TranslationReport` for every normalized, dropped,
  rejected, or provider-observed field.
- [ ] Prove ordinary text remains incremental and complete tool intent remains
  behind the existing durable approval barrier.
- [ ] Prove provider retry and commit-ledger behavior remains unchanged after
  the first client-visible event.

### 5. Compile the Chat operation into the sole Access plan

- [ ] Add one versioned exact `POST /v1/chat/completions` HTTP operation to the
  explicit operation catalog.
- [ ] Compile the typed client dialect, provider dialect, codec identity,
  revision, operation capability, model mapping, and dependency revisions into
  the existing immutable plan.
- [ ] Keep management, upload, batch, audio, image, Realtime, and unknown paths
  outside this semantic operation.
- [ ] Preserve deterministic `PlanHash`, normal reopen recompilation, old-handle
  immutability, per-Access projection poison, and aggregate CAS behavior.
- [ ] Avoid a schema migration unless the durable aggregate itself genuinely
  changes; catalog-only capability additions are not persisted configuration.

### 6. Extend Exchange and loopback ingress

- [ ] Select the Chat path through the existing explicit selector after one
  snapshot resolve and exact AgentEndpoint/operation revalidation.
- [ ] Strip client auth and hop-by-hop headers before the protocol boundary.
- [ ] Preserve CaptureRun attribution, ConnectionEvent redaction, Activity
  translation evidence, Offline Hold admission, frozen resume target, provider
  egress, and shutdown ownership.
- [ ] Cover persistent CONNECT plan changes, stale operation evidence,
  cancellation, malformed SSE, tool approval, planned Hold/Resume, and
  multi-Access isolation under `-race`.
- [ ] Add a public structural bad/good fixture only if a new forbidden
  production shape actually exists.

### 7. Prove one deterministic production-path vertical

- [ ] Drive a generic Chat HTTP client through real CaptureRun authorization,
  CONNECT/MITM, exact path classification, one Access snapshot, Exchange, and a
  controlled provider wire fixture.
- [ ] Prove complete and incremental text, tool intent/result approval,
  provider error mapping, cancellation, Hold/Resume, and bounded shutdown.
- [ ] Prove stale CAS, transaction failure, projection poison, and unrelated
  Access IDs cannot change or observe the active plan.
- [ ] Retain only closed reason codes, counts, hashes, and provenance; do not
  retain request/response text, headers, tool arguments, or secrets.
- [ ] State that deterministic wire evidence does not certify arbitrary Agent
  releases, providers, models, HTTP/2 behavior, or external network quality.

### 8. UI only after runtime evidence

- [ ] Reuse existing authenticated Access and Activity routes if no new
  user-visible state is required.
- [ ] If a visible state is unavoidable, add a stable language-independent code
  and synchronized nonempty `en-US`/`zh-CN` catalog entries only after the
  storage, compiler, codec, Exchange, ingress, and lifecycle contracts pass.
- [ ] Do not add a tool-specific toggle, model catalog, secret display, raw wire
  console, or second Access editor.

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
- [ ] clean tracked source and evidence bound to its exact revision

## Completion statement

This plan is complete only when evidence supports:

> A generic Agent can submit the exact supported OpenAI Chat Completions HTTP
> operation through the authenticated ViberMate proxy; the sole ProductRuntime
> resolves one immutable Access plan, converts the bounded request and response
> through canonical IR and explicit Chat client/backend codecs, preserves
> streaming and durable tool approval, and sends provider traffic only through
> controlled egress. No concrete Agent, provider, or model is encoded in the
> production path.

Even then, ViberMate is not Preview-ready or Release-ready. Anthropic Messages
and OpenAI Responses backend edges, the remaining cross-dialect matrix,
programmable WebSocket/Realtime, HTTP/2 fingerprint profiles, proxy-chain
profiles, native secret protection, Root installation, signing/notarization,
Server, Windows/Linux, and full product UI remain outside this plan.

## Archive protocol

After every checkbox and the completion statement are proven:

1. move this file to a date-named path under `docs/plans/archive/`;
2. record the frozen source, artifact, report paths, hashes, and honest warning
   set;
3. create the next root plan from the then-current implementation and read-only
   design;
4. keep the successor bottom-up, TDD-driven, and UI-last.
