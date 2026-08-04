# Unified Capture Authority Foundation

Status: complete
Created: 2026-08-04
Completed: 2026-08-04
Implementation baseline: `8b6c304960bbc52babccd42e66640cf36f01b518`
Implementation result: `6e10b1ef2ca66950bde0529619cd6f3a21ca2d5f`
Design authority: `vibermate-design` ADR-0019 at `ff822a58cf8ee8c08ee840f9e70676ec08a856c5`

## Goal

Make the existing local `vibermate run -- <command>` path the first consumer of
the unified login and capture-grant architecture. Authentication must produce a
typed `ControlPrincipal`; one Core issuer must own CaptureRun grant creation;
and the proxy must continue to consume only the separate per-run capability.

This is an authority-boundary refactor, not a new product surface. The current
CaptureRun behavior and wire contract remain available. ManualCapture, remote
enrollment, Server listeners, connection selection, and new UI stay out of this
slice.

## Current fact

The local discovery credential currently reaches `capturecontrol.Handler`,
which reduces authentication to a boolean and performs client verification,
Root approval, workspace resolution, and CaptureRun creation inside the HTTP
handler. Its periodic rotation also couples credential lifetime to discovery
record freshness. That shape cannot safely grow a second Host or a second grant
kind without duplicating authorization and issuance logic.

## Invariants

1. A transport authenticator returns one immutable, validated
   `ControlPrincipal`; request bodies cannot declare or widen that principal.
2. The local CLI principal is scoped to the current daemon generation, has an
   explicit credential revision, and may request only its declared grant kinds.
3. Discovery expiry is a freshness lease for finding the live daemon. Refreshing
   it does not silently rotate the control credential.
4. Only the Core `CaptureGrantIssuer` performs client verification, Root-delivery
   decisions, workspace resolution, and durable CaptureRun creation.
5. A control credential can request a grant but cannot enter the proxy. The
   returned proxy capability cannot call the control API and is never encoded
   with Access, Profile, route, account, model, MachineID, or WorkspaceID.
6. The child environment receives only the existing per-run proxy/control
   capabilities and public Root delivery selected by verified evidence. It never
   receives the local control credential or discovery record path.
7. No compatibility alias is retained: unpublished launcher-token terminology
   is replaced at the touched wire and code boundaries.

## Deliverables

- immutable `ControlPrincipal`, principal kind, grant-kind allowlist, and
  credential revision types;
- a digest-only, generation-scoped credential authority with atomic explicit
  rotation, independent waiter authentication, and revocation;
- one `CaptureGrantIssuer` that owns the existing CaptureRun issuance workflow;
- a transport-only CaptureRun handler that authenticates, decodes, invokes the
  issuer, maps typed failures, and retains per-run lifecycle endpoints;
- local discovery and `runlauncher` terminology updated to a local control
  credential, with a stable credential across discovery refresh;
- Desktop composition wired through the principal authority and issuer;
- direct contract, negative, lifecycle, integration, and race tests plus the
  repository structural and cross-platform gates.

## Explicitly out of scope

- ManualCapture persistence, proxy username/password, create/rotate/revoke UI,
  or verification projection;
- remote `vibermate login`, enrollment, MachineRegistration, Server listener,
  quotas, or admin Web sessions;
- Access/Profile/route behavior, provider codecs, plugins, Language Bridge,
  system proxy, system Root trust, Keychain, signing, or release claims;
- completion of the deferred Packaged Deterministic Freshness Gate.

## Completion statement

When complete, the repository may claim only:

> The local CLI authenticates as a typed generation-scoped ControlPrincipal and
> obtains CaptureRun grants from the Core issuer; control and proxy credential
> namespaces remain separated, and discovery refresh does not rotate login
> authority. Manual and remote capture are still not implemented.

## Frozen result

- local discovery now publishes one generation control credential under the
  `vibermate-local-control-discovery-v1` schema; freshness refresh republishes
  that credential rather than rotating authentication by time;
- authentication yields an immutable typed principal, and the Core issuer is
  the only production path that constructs a durable CaptureRun create
  command;
- the HTTP handler owns only strict transport decoding, typed failure mapping,
  and per-run lifecycle routes; mixed control/run credential namespaces fail
  closed;
- the launcher strips control, enrollment, admin, discovery, connection, and
  credential-file variables inherited from the parent shell before starting
  the captured child;
- repository fixtures enforce the unique Desktop composition chain and reject
  production CaptureRun construction outside the issuer.

Validation passed with uncached full Go tests, full Go race tests, vet, module
verification, generated/tidy/format checks, structural fixtures, immutable
workflow pins, native-secret and Windows/Linux cross-builds, 360 frontend unit
tests, 21 Playwright tests, 29 Rust tests, and dependency vulnerability gates.
The local Node runtime was 25.8.1 rather than the repository-pinned 22.23.1;
the commands ran rather than skipping work, and CI remains pinned to the exact
declared version. No current packaged acceptance report was produced, so this
result is neither Preview nor Release evidence.
