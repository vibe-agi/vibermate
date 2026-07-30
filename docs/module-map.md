# Runtime Module Map

This file is the implementation index for the current M0 slice. The accepted
design documents and ADRs in the VibeMate design repository remain the
architecture authority. This map records code ownership and current evidence;
it does not redefine product scope.

| Module | Responsibility / non-responsibility | Typed input, output, and authority | Lifecycle | Evidence and current slice |
|---|---|---|---|---|
| `internal/productruntime` | Owns the only production composition sequence, incarnation, internal initialization state, rollback, and shutdown ordering. It does not publish product readiness and does not implement listeners, proxy, control resources, Exchange execution, or UI. | Consumes `Options`, host contract, runtime paths, one `RuntimeCoordinator`, one SecretStore, clock, and ID source. Produces `Runtime`, `RuntimeStatus`, schema-state reads, the Access writer/resolver, Access projection health, and typed offline enter/resume methods. It owns the provider and original-origin clients but does not expose them as a bypass. | Created by `Start`; fully constructed components are registered into bounded LIFO rollback/shutdown. Shutdown closes offline admission, cancels and closes both transports, drains egress, then stops background, Access, and SQLite ownership. Cleanup failure leaves `stop_failed`; the tree is not automatically retried. | Lifecycle, complete aggregate recovery/recompilation, exact resume-target composition, target-change isolation, projection health, host contracts, and shutdown tests. State: M0.4 controlled-egress components are wired internally, but no data plane calls them. |
| `internal/access` | Owns the typed Access aggregate, separate client/provider network identities, explicit capability catalog, pure compiler, deterministic `PlanHash`, aggregate-local CAS orchestration, immutable active-plan projection, exact `ClientOrigin` lookup, and per-Access projection trust. It does not own SQLite, secret values, protocol wire I/O, provider transport, or control handlers. | Consumes a typed repository, compiler catalog, and plan projection. Produces a writer, sole `SnapshotResolver`, typed ingress lookup/catalog boundaries, frozen provider probe identities, and projection health keyed by `AccessID`. SQLite aggregates are durable authority; the atomic plan projection is read-only process state. | Restores and recompiles every valid committed aggregate during startup; no rows is valid. Validation and compilation happen before mutation admission. Writer ownership spans CAS through active-plan publication. An indeterminate commit or failed post-commit publication poisons only the affected Access ID. Shutdown rejects new writes, cancels pre-commit work, and drains post-commit publication. | Ownership/reference/capability rejection, deterministic hashing, input/getter alias isolation, stale/concurrent CAS, old/new handles, ClientOrigin conflicts, multi-Access poison isolation, close/reopen recompilation, injected commit ambiguity/publication failure, and race tests. Current compiler accepts one profile/account/route, Direct egress, fixed model, Anthropic Messages to OpenAI Chat identity, and an empty pass-through plugin plan. |
| `internal/accessapply` | Owns the apply-only translation from one complete control-plane DTO to the authoritative Access aggregate. It does not persist, publish, expose a draft model, or define an M0-only singleton API. | Consumes a typed path `AccessID`, expected aggregate revision, and complete value DTO. Produces one `access.WriteCommand`; every owned child receives the same next revision. | Pure construction with no background work or external resources. | Identifier, ownership, origin, provider target, model, and alias-isolation tests. State: typed apply boundary only; no control route exists. |
| `internal/protocolcore` | Owns immutable protocol-neutral messages, content blocks, tool calls/results, usage, model identities, stop semantics, translation notices, and language-independent failures. It does not parse HTTP, perform I/O, choose an Access, or approve tools. | Constructor-validated values cross codec boundaries; owned byte and collection values are defensively copied, and JSON objects reject duplicate names. | Pure values with no background work or external resources. | Deep-copy, known-zero versus unknown usage, tool identity, duplicate-object, fuzz, and race tests. |
| `internal/protocolpath` | Owns the typed composition boundary between one client protocol edge and one provider protocol edge around neutral IR. It does not resolve plans, perform transport I/O, retrieve credentials, or provide a global codec registry. | Explicit `ClientCodec` and `BackendCodec` interfaces decode and encode immutable protocol-core values. A typed path validates the frozen Access codec ID, revision, and both dialects. Provider requests own cloned method/path/header/body state. | Pure composition with no background work. | Constructor rejection, dialect/revision mismatch, request alias isolation, and bilateral codec tests. The streaming bridge remains an explicit pair-specific M0 seam. |
| `internal/ssewire` | Owns bounded incremental Server-Sent Event framing. It does not interpret protocol JSON, infer success from EOF, reconnect sockets, or perform network I/O. | A caller-owned decoder accepts arbitrary byte fragments and emits only complete blank-line-terminated events under explicit byte/event limits; the encoder emits canonical frames. | Caller-owned state only; no goroutines or external resources. | LF/CRLF, multiline fields, byte fragmentation, limits, partial EOF, round-trip, fuzz, and race tests. |
| `internal/anthropicchat` | Implements the M0 bilateral protocol path: Anthropic Messages client edge, OpenAI Chat provider edge, and coordinated streaming/tool barrier. It does not select plans, retrieve credentials, send HTTP, or localize errors. | The client edge converts bounded Anthropic wire into immutable protocol-core values; the provider edge builds frozen OpenAI Chat requests and converts responses back. The stable codec ID/revision is the same catalog identity compiled into the Access plan. Official SDKs are pinned test-only oracles. | Pure request/response and incremental stream state owned by the caller. | SDK-oracle fixtures, per-byte streaming, model separation, complete/incomplete/parallel tools, no-leak decisions, unsupported-shape failures, fuzz, and race tests. No network execution is claimed. |
| `internal/runtimepersistence` | Owns SQLite connection policy, embedded forward migrations, operation admission/cancellation/drain, runtime metadata, complete Access aggregate rows, transactional CAS, and schema-state reads. It does not compile or publish active plans, retrieve secrets, send provider traffic, provide backups, or expose UI queries. | Consumes an absolute database path and bounded connection/commit-reconciliation policy. Produces a file-backed store, `SchemaStateReader`, and typed Access repository. The highest applied goose migration version is the only schema revision authority; each Access aggregate independently owns its business revision. All owned child rows are written in the same transaction as the Access root CAS. | Opened first; migrations 1–5 complete before initialization. All repository work uses one operation gate. Ambiguous commits are reread using a bounded store-owned context. Shutdown closes admission, cancels and drains owned operations, then closes SQLite. | Revision-2 upgrade, file-backed reopen, pragmas, permissions, held-transaction deadline, complete aggregate CAS/rollback, ClientOrigin uniqueness, commit reconciliation, corruption, cancellation, and schema continuity tests. State: `wired` and component-verified. |
| `internal/offlinehold` | Owns the mandatory external-egress admission seam and planned enter/drain/probe/FIFO-release state machine. It does not own sockets, secrets, OS reachability, persistence, reactive cursor resume, or UI. | A logical action and Enter share one mutex cut: actions admitted before Enter may finish, while later actions queue at egress. Every acquire carries that action plus a complete frozen non-secret network identity; provider identities bind Access revision and PlanHash. Queues are count/byte/time bounded. | One runtime incarnation binds the gate. Enter waits for pre-cut actions and active egress before `SafeToDisconnect`; Resume probes exact queued identities and releases only successful targets. Shutdown rejects queued work and drains actions and leases. | Deterministic pre/post-Enter ordering, plan-change target isolation, Safe-to-disconnect fencing, failed-probe zero release, bounded FIFO/concurrency, cancellation, revision conflicts, alias isolation, shutdown, and race tests. Physical network change remains unverified. |
| `internal/secretstore` / `internal/hostsecret` | Define canonical `SecretRef`, destroyable bounded values, redacted metadata, the typed host Factory/Store boundary, and a cross-platform development file driver. They do not put secrets in SQLite, log/list values, return values to UI, or select a backend inside ProductRuntime. | ProductRuntime receives exactly one explicit Store. Ordinary builds select a private file factory with `!vibermate_native_secrets`; its CAS document is plaintext-equivalent at rest. Native release selection is not present in this stage. | Values are caller-owned and explicitly destroyed. The development Store serializes CAS and replaces a private file atomically for the host platform; reopen reconstructs secret revisions. | Reference/value destruction, metadata/CAS conflict, permissions, symlink/path rejection, replacement failure, concurrent access, close/reopen, and race tests. Development storage is not release protection. |
| `internal/transportprofile` | Owns frozen observed/standard TLS profile material, strict connector construction, explicit fallback decisions, and redacted transport evidence. It does not select Access plans, retrieve credentials, or claim exact browser/Agent fingerprint fidelity. | Consumes the compiled Access transport fingerprint plan plus an observed ClientHello where requested. Produces a strict verified TLS connection and immutable requested/effective/fallback/ALPN/HTTP evidence. | Each connector operation is context-bounded; no global pool, registry, or background owner exists. | ClientHello capture bounds, extension sanitization, SNI/ALPN replacement, TLS-version enforcement, fallback classification, strict certificate rejection, and race tests. Current transport is HTTP/1.1 and does not prove exact JA3/JA4 or HTTP/2 fingerprint parity. |
| `internal/providertransport` | Owns the final M0 provider request, AuthDriver application, strict target/TLS dispatch, response-idle ownership, typed resume probing, and shutdown. It does not resolve Access plans, translate protocol semantics, or expose a raw client. | Consumes a frozen compiled target/action, the mandatory coordinator, one typed authenticator, SecretStore reader, and transport profile. Egress lease acquisition precedes secret read and any outbound byte. Origin, HTTP authority, Host, and SNI remain separate frozen fields. | Operations are admitted into the client lifecycle, hold action/egress leases through response EOF or close, and are canceled and drained during shutdown. | Lease-before-secret/transport ordering, target/path/Host/SNI, redirect denial, header stripping, value destruction, strict TLS, idle timeout, probe identity/reason mapping, cancellation, shutdown, and race tests. ProductRuntime owns the client, but no Exchange invokes it yet. |
| `internal/originaltransport` | Owns strict TLS forwarding and probing for approved non-model original-origin operations. It does not select an Agent endpoint, classify paths, carry provider credentials, or permit ambient proxy behavior. | Consumes a fully frozen request, action lease, exact origin/authority/SNI, and mandatory coordinator. It returns a response whose body owns the egress lease. | Client admission, cancellation, body closure, and bounded shutdown mirror the provider transport. | Lease ordering, TLS verification, redirect denial, header/body ownership, exact probe target, cancellation, shutdown, and race tests. No proxy handler invokes it yet. |
| `internal/hostcontract` | Defines valid Desktop and Server runtime descriptors and keeps management authentication distinct from proxy authentication. It does not implement either host shell or transport. | Typed constructors produce closed contracts; ProductRuntime consumes the descriptor and reports its kind. | Value object validated before resources are opened. | Constructor invariants and shared ProductRuntime contract tests. State: `verified contract only`. |
| `internal/repositorycheck` | Owns implementation-language, protocol SDK hot-path isolation, raw external-egress construction, and canonical locale catalog drift checks. It does not infer product completion or scan future shapes that do not exist. | Consumes repository files and the two canonical locale catalogs. Official SDK imports are permitted only in oracle tests; raw network-client construction is limited to the typed provider/original transports and probes. | Runs before unit, race, and contract layers. | Each rule has an injected bad repository fixture and a known-good repository fixture. State: `wired`. |

## Current initialization boundary and deferred readiness

`productruntime.Start` currently proves only that:

1. options and the host descriptor were valid;
2. the SQLite database opened with the required driver and connection policy;
3. all embedded migrations completed and schema state resolved from goose;
4. zero or more committed Access aggregates were validated, compiled, and
   projected from SQLite; an empty Access set is valid;
5. the owned storage-health goroutine started;
6. strict provider and original-origin transports and typed resume probes were
   constructed;
7. the supplied offline-hold coordinator accepted the runtime incarnation.

The resulting state is `initialized`, not `ready`. A future Host owns external
readiness and discovery publication only after runtime handlers, complete
routes, authenticated listeners, and host-specific entry points are all ready.
No current code publishes a Desktop discovery file, Server readiness endpoint,
or any client-connectable product entry.

## Access durable and process-local authority

SQLite is the only persistent Access authority. The in-memory projection is a
read-only optimization for the current process and is not transactionally
atomic with SQLite across media. Each write is serialized through:

```text
validate aggregate ownership and references
  -> compile candidate immutable plan and deterministic hash
  -> SQLite compare-and-swap commit
  -> publish immutable active plan
  -> return success
```

The writer lock covers the CAS-to-publication interval, and projection code
rejects a non-advancing revision. Cancellation before commit rolls back and
does not publish. A known commit is published even if the caller cancels
afterward. An ambiguous commit result is reconciled from SQLite using a bounded
store-owned context; an unrecoverable ambiguity returns
`access_commit_outcome_unknown` rather than implying that no write occurred. An
unreconciled ambiguity or a known commit followed by publication failure marks
that Access ID `access_projection_unavailable`. New resolves and writes for the
affected ID then fail closed; already returned immutable snapshot values do not
change. A new process restores a fresh projection from SQLite.

The recovery test creates durable state without publishing it, performs a
normal bounded store shutdown, reopens the SQLite file, and reconstructs the
projection. It proves the close/reopen recovery path, not recovery from
`SIGKILL`, a real process crash, operating-system failure, or power loss.

This slice proves that an old plan handle remains unchanged, a new resolve
observes the new revision, and exact `ClientOrigin` lookup returns immutable
ingress identity evidence. It does not prove that a real request resolves once
because no ingress or data-plane handler exists. It also does not execute the
declared codec, retrieve the declared `SecretRef`, or send the provider target.
Writer serialization is process-local; this slice does not prove that two
simultaneous ProductRuntime processes may safely share one database. Host
generation ownership remains a later boundary.
