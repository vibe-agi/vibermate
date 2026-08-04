# Route-Neutral Capture Admission

Status: complete
Created: 2026-08-04
Completed: 2026-08-04
Implementation baseline: `6e10b1ef2ca66950bde0529619cd6f3a21ca2d5f`
Design authority: `vibermate-design` ADR-0019 at `ff822a58cf8ee8c08ee840f9e70676ec08a856c5`

## Goal

Remove CaptureRun as a data-plane authentication type. The proxy must consume
one immutable, route-neutral `CaptureAdmission` that can later be produced by
either a managed run or ManualCapture without creating another listener,
handler, Exchange shape, workspace authority, or route selector.

This slice converts the existing CaptureRun path only. It does not create a
ManualCapture grant, route, API, CLI command, or UI surface.

## Invariants

1. A proxy credential authenticates into one typed admission before policy,
   DNS, dial, certificate issuance, AgentEndpoint lookup, or body read.
2. Admission contains ingress attribution only. It cannot contain or select an
   Access, Profile, route, provider account, model, plugin, or credential.
3. Managed-run admission mechanically derives `capture-run/<run-id>`, binds
   exactly one CaptureRun, and gives its non-rotating per-run capability
   revision 1.
4. Manual admission mechanically derives `manual-capture/<capture-id>`, binds
   one positive credential revision, and cannot assert client-adapter,
   process, machine, or workspace evidence.
5. Digest-verified adapter evidence is defensively copied. Configured or manual
   admission cannot acquire a version feature by naming a client.
6. Workspace scope has one authority: the admission. Exchange has no second
   workspace option that could disagree with it.
7. Connection, ingress profile, CaptureRun/ManualCapture, and Exchange
   identities remain separate typed references; none is delimiter-encoded into
   another identity.
8. The production proxy cannot import `internal/capturerun`; a repository
   fixture must reject that managed-only dependency if it returns.

## Deliverables

- immutable `internal/captureadmission` domain values, opaque redacted proxy
  credential, closed kind/confidence enums, and strict constructors;
- one managed-run authorizer adapter from durable CaptureRun capability
  evidence to the shared admission;
- proxy options and handler refactored to consume only the shared authorizer;
- Exchange correlation refactored to carry admission plus independent
  connection identity and optional managed/manual references;
- workspace routing derived only from admitted scope;
- ProductRuntime production composition and structural fixtures updated;
- direct, integration, race, full-repository, and cross-platform validation.

## Explicitly out of scope

- durable ManualCapture state and proxy credential index;
- create/rotate/revoke/list/verification control API, CLI, or UI;
- remote enrollment and Server proxy listener;
- changes to Access/Profile routing or provider behavior;
- packaged Preview or Release evidence.

## Completion statement

When complete, the repository may claim only:

> The production proxy and Exchange path consume one immutable route-neutral
> CaptureAdmission. The current producer is the durable CaptureRun authority;
> ManualCapture remains unimplemented, and the result is not Preview or
> Release evidence.

## Frozen result

- `internal/captureadmission` owns a closed, immutable admission and redacted
  proxy credential; manual evidence cannot assert managed attribution;
- ProductRuntime wraps its durable CaptureRun authority once and gives the
  proxy only the shared admission interface;
- the proxy revalidates every authorizer result before policy, dial, DNS,
  certificate issuance, endpoint lookup, or body read and reports the generic
  `capture_admission_rejected` reason on failure;
- Exchange correlation freezes the admission and independent connection ID;
  optional run/manual references and workspace scope can only be derived from
  that admission;
- ConnectionEvent uses the admission's ingress profile and confidence rather
  than treating a CaptureRun ID as the complete ingress identity;
- a public-entry known-good/injected-bad repository fixture rejects any direct
  production import of `capturerun` from `loopbackproxy`.

Validation passed with uncached full Go tests, full Go race tests, vet, module
verification, generated/tidy/format checks, structural fixtures, immutable
workflow pins, native-secret and Windows/Linux cross-builds, 360 frontend unit
tests, 21 Playwright tests, 29 Rust tests, and dependency vulnerability gates.
The local Node runtime was 25.8.1 rather than the repository-pinned 22.23.1;
the commands ran rather than skipping work, and CI remains pinned to the exact
declared version. Rust audit retains 16 allowed unmaintained warnings. No
packaged acceptance report was produced for this refactor.
