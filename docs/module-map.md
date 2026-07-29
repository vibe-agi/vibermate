# Runtime Module Map

This file is the implementation index for the current M0 slice. The accepted
design documents and ADRs in the VibeMate design repository remain the
architecture authority. This map records code ownership and current evidence;
it does not redefine product scope.

| Module | Responsibility / non-responsibility | Typed input, output, and authority | Lifecycle | Evidence and current slice |
|---|---|---|---|---|
| `internal/productruntime` | Owns the only foundation composition sequence, incarnation, internal initialization state, rollback, and shutdown ordering. It does not publish product readiness and does not implement host transport, proxy, control resources, provider traffic, or UI. | Consumes `Options`, host contract, offline-hold coordinator, clock, ID source, and runtime paths. Produces `Runtime`, `RuntimeStatus`, schema-state reads, the Access writer, snapshot resolver, and Access projection health. Runtime status is authoritative only for this process incarnation and becomes degraded when the Access projection is unavailable. | Created by `Start`; successful components are registered after construction; rollback and shutdown run in LIFO order with internal deadlines. Access admission drains before SQLite. Cleanup failure leaves `stop_failed`, never a false `stopped`, and the cleanup tree is not automatically retried. | Lifecycle, Access recovery, projection-health observation, and host-contract tests. State: `wired` for the M0 runtime and Access snapshot foundation, not a data plane. |
| `internal/access` | Owns typed `AccessID`, aggregate-local revision/CAS semantics, serialized commit-to-publish coordination, process-local immutable snapshots, resolver semantics, and per-aggregate projection trust. It does not own SQLite, a compiled `AccessPlanSnapshot`, ingress request freezing, provider routing, secrets, endpoints, profiles, or control handlers. | Consumes a typed repository and snapshot projection. Produces a writer, `SnapshotResolver`, and projection health keyed by `AccessID`. SQLite records are the durable authority; the atomic projection is read-only process state. | Restores every valid committed aggregate during startup; no rows is valid. After validation, mutation admission and writer ownership span the wait through pointer publication. An indeterminate commit or failed post-commit publication poisons the affected projection; new resolves and writes for that ID fail closed until startup recovery. Shutdown rejects new writes, cancels pre-commit work, and drains post-commit publication. | Real SQLite CAS, conflict, alias isolation, controlled interleaving, cancellation, normal close/reopen recovery, injected commit ambiguity and publication failure, and race tests. State: `wired` for stable Access root fields only. |
| `internal/runtimepersistence` | Owns SQLite connection policy, embedded forward migrations, operation admission/cancellation/drain, runtime metadata, Access rows, transactional CAS, and schema-state reads. It does not own Access snapshot publication, compiled plans, secrets, provider data, retention, backups, or UI queries. | Consumes an absolute database path and bounded connection/commit-reconciliation policy. Produces a file-backed store, `SchemaStateReader`, and typed Access repository. The highest applied goose migration version is the only schema revision authority; each Access row independently owns its business revision. | Opened first; migrated before initialization completes; all repository work uses one operation gate. Ambiguous commit results are reread using a bounded store-owned context. Shutdown closes admission, cancels and drains owned operations, then closes SQLite. | File-backed reopen, pragma, permission, held-transaction deadline, Access CAS, commit reconciliation, corruption, cancellation, and schema continuity tests. State: `wired` and component-verified. |
| `internal/offlinehold` | Defines the mandatory deny-by-construction egress and lifecycle contract. It does not implement enter, queue, probe, release, transport retry, or any external dialer in this slice. | ProductRuntime supplies a process incarnation binding. Future egress owners must acquire typed leases by egress kind. A host-provided coordinator remains the authority for admission and drain. | Bound during startup; admission is closed and active leases are drained before background and storage shutdown. | Runtime ordering tests use a contract double. State: `planned boundary`; capability is not implemented. |
| `internal/hostcontract` | Defines valid Desktop and Server runtime descriptors and keeps management authentication distinct from proxy authentication. It does not implement either host shell or transport. | Typed constructors produce closed contracts; ProductRuntime consumes the descriptor and reports its kind. | Value object validated before resources are opened. | Constructor invariants and shared ProductRuntime contract tests. State: `verified contract only`. |
| `internal/repositorycheck` | Owns implementation-language and canonical locale catalog drift checks. It does not infer product completion or scan future shapes that do not exist. | Consumes repository files and the two canonical locale catalogs. Emits language-independent developer diagnostics. | Runs before unit, race, and contract layers. | Each rule has injected bad and known-good fixtures. State: `wired`. |

## Current initialization boundary and deferred readiness

`productruntime.Start` currently proves only that:

1. options and the host descriptor were valid;
2. the SQLite database opened with the required driver and connection policy;
3. all embedded migrations completed and schema state resolved from goose;
4. zero or more committed Access aggregates were validated and projected from
   SQLite; an empty Access set is valid;
5. the owned storage-health goroutine started;
6. the supplied offline-hold coordinator accepted the runtime incarnation.

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
validate
  -> build candidate record and immutable snapshot
  -> SQLite compare-and-swap commit
  -> publish immutable snapshot
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

This slice proves only that an old snapshot handle remains unchanged and a new
resolve observes the new revision. It does not prove that a real ingress request
resolves exactly once because no ingress or data-plane handler exists. The
writer serialization is process-local; this slice also does not prove that two
simultaneous ProductRuntime processes may safely share one database. Host
generation ownership remains a later boundary.
