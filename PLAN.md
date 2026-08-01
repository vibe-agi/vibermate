# M1.0-B Desktop Trust Operation Foundation

Status: implementation complete; evidence freeze pending
Created: 2026-08-01
Implementation baseline: `f8534654cbbd3b9eec839de3d23a888111f22617`
Prior implementation candidate: `c19cca4eb2842aa00d8e8fc17160b342a111f0b6`

## Objective

Define the long-lived typed contract for observing the current public Root,
derive immutable exact-identity install and remove plans, and deterministically
validate fixture-backed macOS trust-operation orchestration through injected
executors.

No production executor is wired. No production path observes or modifies an
operating-system trust store. This slice does not add ProductRuntime,
DesktopHost, Control API, Tauri, UI, system authorization, Root rotation, proxy,
provider, protocol, Offline Hold, Language Bridge, or plugin behavior.

The only completion claim for this slice is:

> VibeMate defines an exact typed contract for observing its current public
> Root, derives immutable install/remove plans, and deterministically validates
> fixture-backed macOS trust-operation orchestration through injected
> executors. No production executor is wired, and no production path observes
> or modifies the operating-system trust store.

This slice does not prove live macOS inspection, successful authorization,
verified trust-store mutation, supported-client trust, Preview readiness, or
Release readiness.

## Read-only design authority and checkpoints

The authoritative design repository is
`/Users/null/Code/github/vibe-agi/vibermate-design`. Its current disk contents,
including uncommitted work, are read-only implementation authority. CodeGraph
is consulted before direct text search whenever its `.codegraph/` index exists.

The relevant baseline is:

- `CONTRIBUTING.md`;
- `docs/design/00-overview.md`;
- `docs/design/02-architecture.md`;
- `docs/design/06-security.md`;
- `docs/design/10-client-compatibility.md`;
- `docs/design/11-delivery-and-operations.md`;
- `docs/design/14-technology-stack.md`;
- `docs/design/18-production-composition.md`;
- `docs/design/19-hosts-and-deployment.md`;
- `docs/adr/0006-agent-endpoint-mitm-allowlist.md`;
- `docs/adr/0007-client-and-upstream-authority-separation.md`;
- `docs/adr/0011-shared-runtime-and-host-shells.md`;
- `docs/adr/0013-core-language-bridge-and-typed-transformer-adapters.md`.

The initial ordered SHA-256 manifest digest for those files is
`2d38c6df509a15fb9ffc3b60345ce5421cc97d8a3e967887f3b86d8e93c00cea`.
Re-read and re-hash them after contracts, after orchestration, and immediately
before freeze. A changed digest requires a semantic diff and goal
reconciliation.

Initial checkpoint, 2026-08-01:

- The design still requires one current Root, explicit per-operation OS
  authorization, post-command trust reinspection, manual recovery when exact
  removal is unavailable, and no permanent administrative grant.
- macOS writable trust settings are the admin domain. The immutable Apple
  system trust-settings domain is not the target.
- The certificate object target is the fixed
  `/Library/Keychains/System.keychain`.
- DER SHA-256 remains the only Root machine identity. Subject, serial,
  certificate path, command output, and formatted fingerprint are not
  authority.
- Language Bridge remains outside this platform-trust slice.

Post-contract and orchestration checkpoint, 2026-08-01:

- A fresh CodeGraph-first review followed by the ordered manifest hash found
  the same digest,
  `2d38c6df509a15fb9ffc3b60345ce5421cc97d8a3e967887f3b86d8e93c00cea`.
- The current design still separates exact certificate presence from the
  admin-domain trust decision, requires post-command reconciliation, and uses
  the four stable result statuses `applied`, `user_cancelled`, `needs_manual`,
  and `failed`.
- The design repository's ongoing Language Bridge and localization work does
  not change this slice's Root authority, platform boundary, dependencies, or
  no-UI scope. No goal correction was required.

## Authority and package boundary

1. The existing `localca.Authority` remains the sole Root authority. This
   slice may consume only an immutable current-public-Root snapshot containing
   its `RootIdentity` and defensive-copy certificate DER.
2. A sealed, read-only `CurrentPublicRootSource` is the only source accepted
   by planning and execution. Callers submit only a typed operation; they
   cannot submit an identity, digest, certificate, path, or command.
3. A test fake may implement the sealed source inside the trust package. No
   fake, no-op, memory driver, global locator, string registry, blank-import
   registration, or production alternate Root source is provided.
4. The trust package does not read or receive a Root private key. It does not
   persist Root state or create a second Root authority.
5. This slice does not wire the source or coordinator into ProductRuntime.
   Future production composition may connect only the existing local
   authority. Future Root rotation must share the same mutation admission
   authority; repeated reads alone do not close a rotation race.

## Typed observation

Observation has two independent axes:

```text
ExactPresence: present | absent | unknown
TrustDecision: trusted | untrusted | unknown
```

Valid interpretations are:

- `present + trusted`: the exact DER exists and has the target trust decision;
- `present + untrusted`: the exact certificate object remains without the
  target trust decision;
- `absent + untrusted`: the exact object does not exist and is not trusted;
- either axis `unknown`: the entire observation is unusable for mutation and
  fails closed.

`absent + trusted` is contradictory and rejected. Presence never proves
trust. Untrusted never proves absence.

Every observation binds:

- current `RootRevision`;
- exact certificate DER SHA-256 `RootDigest`;
- `TrustSettingsDomainAdmin`;
- `CertificateKeychainSystem`;
- `TrustUsageServerTLS`;
- the two observation axes;
- a typed evidence revision identifying the bounded macOS fixture grammar.

Existence, trust, and command success are never conflated. Unrecognized,
ambiguous, oversized, or failed evidence yields `unknown`.

## Immutable change plan

A plan is a short-lived immutable value, not a persisted Root state or bearer
authorization. It privately owns defensive copies of:

- operation: `install` or `remove`;
- current `RootRevision`;
- current DER SHA-256 `RootDigest`;
- public certificate DER;
- fixed typed target scope;
- desired observation;
- complete observation precondition;
- ordered typed steps;
- whether a mutation requires OS authorization;
- typed manual fallback.

Public accessors return immutable values or copies. Plan equality, stale checks,
and execution never use a certificate path, subject, serial number, display
fingerprint, local directory, command output, or caller-supplied string.

The long-lived steps are:

- `ensure_exact_certificate_and_admin_trust`;
- `remove_exact_admin_trust_settings`;
- `delete_exact_certificate`;
- `inspect_exact_root`.

The truth table is:

| Operation | Precondition | Plan |
|---|---|---|
| install | present + trusted | already satisfied; no mutation |
| install | absent + untrusted | ensure exact certificate/admin trust, inspect |
| install | present + untrusted | restore exact admin trust, inspect |
| remove | present + trusted | remove exact admin trust, inspect, delete exact certificate, inspect |
| remove | present + untrusted | delete exact certificate, inspect |
| remove | absent + untrusted | already satisfied; no mutation |
| either | unknown or contradictory | fail closed; no mutation |

Install completes only at `present + trusted` for the exact current digest.
Remove completes only at `absent + untrusted`. If a future feature merely
revokes trust while retaining the certificate, it must use a distinct
`revoke_trust` operation.

## macOS bounded adapter

The typed target is exactly:

- `TrustSettingsDomainAdmin`;
- `CertificateKeychainSystem`;
- fixed certificate keychain
  `/Library/Keychains/System.keychain`;
- `TrustUsageServerTLS`.

Core values never contain CLI flags. The macOS fixture adapter may map
`TrustUsageServerTLS` to the bounded `security -p ssl` shape, but that
mapping is not verified production behavior.

The adapter creates opaque fixed executable-plus-argv command specifications.
It never invokes a shell, accepts an executable/path/keychain from a caller,
uses `sudo`, stores reusable authorization, or emits arbitrary command text.
The only executable is `/usr/bin/security`.

The current bounded command shapes are limited to:

- `find-certificate`;
- `dump-trust-settings -d`;
- `add-trusted-cert`;
- `remove-trusted-cert -d`;
- `delete-certificate -Z <DER SHA-256>`;

The local help text proves only that these command shapes exist. It does not
prove authorization behavior, output stability, mutation success, or final
trust state.

Presence parsing computes DER SHA-256 from bounded PEM output and ignores
subject/common-name/displayed-hash identity. Trust parsing is strict,
versioned, fixture-backed, and fail-closed. Raw stdout/stderr never leaves the
adapter or appears in results, logs, reports, or audit values. The executor
contract must report capture overflow as an error or indeterminate outcome; it
may not silently truncate evidence and report success.

If a future real executor needs a certificate file, only the adapter may
materialize verified current DER in a private operation-owned directory with
minimal permissions and bounded cleanup. The fixture adapter exercises that
public-certificate materialization and cleanup around the injected executor;
this slice provides no `os/exec` runner and never invokes `security`.

## Coordinator semantics

Mutation orchestration uses fail-fast admission:

- admission ownership is the in-process linearization point;
- at most one trust mutation orchestration is active;
- concurrent attempts return stable reason `operation_in_progress`;
- there is no FIFO, fairness, queued cancellation, or background operation
  queue;
- ownership remains held through final reconciliation.

Execution order is:

```text
take fail-fast ownership
→ read current public Root
→ compare plan revision and digest
→ inspect current OS evidence
→ verify the complete plan precondition
→ re-read current public Root
→ compare revision and digest again
→ execute one typed step
→ inspect through a coordinator-owned reconciliation context
→ continue only if the typed intermediate state permits the next step
→ perform final inspection
→ release ownership
```

The plan is never authority by itself. A stale Root revision, stale digest,
changed observation, changed target, malformed certificate, or changed
evidence revision fails before mutation.

Each mutation step is followed by inspection. Typed success plus the required
intermediate state permits the next destructive step. Error, cancellation,
timeout, permission denial, panic, or indeterminate outcome stops later
destructive steps and triggers bounded reconciliation.

The coordinator serializes only its own in-process operations. External
trust-store changes are not cross-process atomic with the plan. Mandatory
preinspection and postinspection detect and reconcile them without claiming an
OS-wide lock.

## Result, cancellation, and shutdown

Results contain only typed operation, status, reason, completion, plan-bound
Root identity, and defensive-copy observation. They never contain certificate
material, paths, argv, private data, raw stdout/stderr, or user-facing text.

- Already-satisfied preinspection returns `status=applied`,
  `completed=true`, reason `already_satisfied`, and runs no mutation.
- Only typed executor success followed by the required postinspection returns
  `status=applied`, `completed=true`.
- Platform-reported cancellation returns `status=user_cancelled`; permission
  denial returns `status=needs_manual`; caller cancellation, timeout, failure,
  or indeterminate outcome returns `status=failed`. Every such outcome keeps
  `completed=false`, even when reconciliation observes the desired state. The
  real observed state is still returned. A later operation may complete
  idempotently from its new precondition.
- Failed or unavailable reconciliation returns `unknown`; it never infers the
  operating-system final state.

Before mutation, caller cancellation prevents execution. Once mutation starts,
the executor is asked to stop, but caller cancellation cannot cancel final
state reconciliation. Reconciliation uses a coordinator-owned independent hard
deadline.

Shutdown is bounded and idempotent:

```text
close admission
→ cancel active command
→ perform bounded reconciliation
→ drain
```

Shutdown rejects new planning/execution admission. A deadline cannot be
reported as a completed stop while owned work remains. This slice proves
bounded behavior only for executors that obey the executor contract. A future
real process runner needs separate kill, wait, and drain evidence.

## Stable language-independent reasons

The closed reason set includes:

- `applied`;
- `already_satisfied`;
- `operation_in_progress`;
- `plan_stale`;
- `observation_unknown`;
- `caller_cancelled`;
- `user_cancelled`;
- `permission_denied`;
- `command_timeout`;
- `command_failed`;
- `command_indeterminate`;
- `postcondition_mismatch`;
- `shutting_down`;
- `reconciliation_unknown`.

These are developer/API reason values, not user-facing copy. This slice adds no
UI text or locale keys.

## Failure conditions

The operation fails closed without mutation when:

- current public Root cannot be read or validated;
- certificate DER does not match the current Root digest;
- plan Root revision or digest is stale;
- observation is unknown, contradictory, oversized, ambiguous, or changed;
- target scope, usage, evidence revision, or ordered steps are invalid;
- command specification cannot be derived exclusively from typed current Root
  material and fixed platform constants;
- admission is closed or another mutation owns it.

After mutation starts, every terminal path attempts bounded reinspection.
Later destructive steps never run after a non-successful earlier mutation.

## Test matrix

Contract and orchestration tests must cover:

1. absent/untrusted to install plan;
2. present/trusted install idempotency without mutation;
3. present/untrusted install recovery plan;
4. present/trusted remove plan;
5. present/untrusted remove deletes the exact object;
6. absent/untrusted remove idempotency without mutation;
7. contradictory and unknown observation rejection;
8. same-subject foreign DER is neither matched nor deleted;
9. stale Root revision;
10. stale Root digest;
11. changed observation precondition;
12. fixed executable/argv with no shell;
13. caller input cannot inject executable, argv, certificate path, or keychain;
14. bounded fixture grammar and unknown output/version/oversized evidence;
15. user cancellation;
16. permission denial;
17. command timeout;
18. caller and owner cancellation before mutation, plus caller cancellation
    after mutation admission;
19. success followed by postcondition mismatch;
20. failure/indeterminate result followed by mandatory reconciliation;
21. per-step inspection and stopping later destructive steps;
22. retry and repeated execution idempotency;
23. deterministic fail-fast concurrent mutation;
24. shutdown rejects new admission and drains within deadline;
25. retryable idempotent shutdown;
26. input, output, DER, argv, steps, and observation alias isolation;
27. result/log/fixture redaction;
28. ProductRuntime, DesktopHost, Desktop control, OpenAPI, Tauri, and UI have
    no trust-mutation composition;
29. no production `os/exec` runner or concrete command executor;
30. the tests never change the current machine trust store.

Concurrency tests use barriers/channels, never timing sleeps as ordering
evidence. Critical adapter and coordinator tests run repeatedly under ordinary
and race modes.

## Structural boundaries

Structural checks are added only for concrete shapes introduced here, with
public `Check` entry good and injected-bad fixtures:

- no import of the trust-operation package by ProductRuntime, DesktopHost, or
  Desktop control;
- no production `os/exec` runner in the trust-operation package;
- no concrete `CommandExecutor` implementation in non-test source;
- fixed dangerous command strings only in the macOS planning adapter;
- no exact system-trust namespace or dangerous mutation command in OpenAPI,
  Tauri/Rust, or Desktop UI production surfaces.

Checks must not scan generic words such as `trust`, `Root`, or `security`.

## Explicit non-goals

- Live trust-store observation;
- real Root installation, trust removal, certificate deletion, or OS
  authorization;
- Root rotation;
- helper processes, `sudo`, persistent authorization, or a real
  `os/exec` runner;
- ProductRuntime, DesktopHost, Control API, Tauri, UI, or i18n wiring;
- Server Host trust installation;
- Windows or Linux adapters;
- system proxy changes;
- ClientAdapter expansion;
- provider, protocol, Offline Hold, Language Bridge, or plugin work;
- TLA+ changes.

An implementation conflict with current CA rotation or trust design stops this
goal for review; it is not resolved by editing the design repository.

## Validation and freeze

Before freeze run:

- `gofmt -l .`;
- `make check`;
- `go test -count=1 ./...`;
- `go test -race -count=1 ./...`;
- `go vet ./...`;
- `go mod tidy -diff`;
- `go mod verify`;
- fixed `govulncheck ./...`;
- repository structural checks;
- focused repeated ordinary and race adapter/coordinator tests;
- `git diff --check`.

Freeze two commits:

1. an implementation candidate containing this contract, production code,
   tests, structural fixtures, and module documentation;
2. an evidence commit binding the candidate hash, design digest, and exact gate
   results without changing runtime behavior.

Do not rerun credentialed Agent/provider acceptance because production
composition and packaged behavior do not change. Old acceptance reports remain
prior-slice evidence and are not M1.0-B evidence. If implementation changes
ProductRuntime, Desktop packaging, or packaged behavior, stop and reassess the
evidence scope.

The final implementation worktree and any evidence checkout must be clean.
M1.0-B remains neither Preview-ready nor Release-ready.
