# M1.0-C0a Payload-Bearing Client Operation Fail-Closed

Status: active
Created: 2026-08-01
Implementation baseline: `e04455b`
Branch: `m1/root-leaf-foundation`
Defers: `docs/plans/deferred/2026-08-01-m1.0-c-macos-trust-observation.md`
(M1.0-C macOS Trust Observation is preserved unchanged and resumes after the
C0 egress-identity chain.)

## Objective

No client operation may carry client payload or client credentials to the
original origin while its typed handling plan does not exist. Today
`POST /v1/messages/count_tokens` and every uncatalogued body-bearing request
inside a MITM-terminated AgentEndpoint connection are forwarded verbatim —
full prompt plus the client's own `Authorization` — to the inbound origin.

This Goal closes that class locally and fails closed. It does not implement the
replacement (Profile operations), and it does not touch opaque control-plane
forwarding for proven no-payload operations.

## Read-only design authority

- `docs/adr/0015-egress-purpose-authority-and-audit-cardinality.md` §2, §4;
- `docs/design/02-architecture.md` §4.1 (PayloadClass) and the
  "未被 catalog 声明的规范 method/path" paragraph;
- `docs/design/07-protocol-translation.md` §6.3;
- `docs/design/12-implementation-readiness.md` §3.1–3.2.

The design repository is read-only during implementation.

## Why the scope is payload class, not `Kind == auxiliary`

`ClientOperationKind` and payload class are orthogonal axes (`02` §4.1). The
catalog holds exactly one `auxiliary` entry today, so a blanket "reject all
auxiliary" rule would be accidentally correct and would have to be unwound as
soon as a proven `none/control` probe is catalogued. Keying the rule on a
frozen `OperationPayloadClass` makes it permanently correct and covers the
identical leak on the uncatalogued path, which `02` now also forbids.

`GET /api/claude_code/settings` and `GET /api/claude_code/policy_limits` are
no-body GETs, so the fixed Claude Code control plane is unaffected.

## Required invariants

1. `OperationPayloadClass` is a frozen catalog field:
   `none | control | client_data | client_semantic | unknown`. It is never
   inferred from an observed `Content-Length` or body shape.
2. An operation reaches original-origin transport only when its payload class
   is `none` or `control`.
3. `client_semantic` and `client_data` operations that are not dispatched into
   the model pipeline are rejected locally before any body read, transport
   construction, credential access, egress lease, or Offline Hold action.
4. An `unknown` payload class with a body-bearing method is rejected the same
   way. `unknown` with a non-body method keeps the current original-origin
   path; that remains an explicit gap for the connection-policy Goal.
5. Local rejections inside a MITM-terminated connection use the client
   dialect's own error envelope. The stable vibermate reason code travels in a
   response header, never by replacing the dialect body shape.
6. Rejection consumes at most `MaxBodyBytes` as a discard-only drain so the
   connection stays reusable. The body never enters a retained buffer, log,
   error value, Activity record, or database row.
7. `internal/originaltransport` refuses a request whose frozen evidence does
   not prove a `none/control` payload class.
8. A structural rule, exercised through the public `repositorycheck` entry
   point with good and injected-bad fixtures, prevents re-merging the
   auxiliary and opaque dispatch arms.

## Non-goals

- `profile_endpoint`, remote token counting, `ProfileOperationTarget`;
- Language Bridge and the prepared-handoff plane;
- `EgressAttempt` persistence, control API, or UI;
- blind tunnelling and connection `allow/deny/ask`;
- Root, leaf issuance, or trust-store changes;
- provider or backend codec changes;
- the `CaptureRunID`/`ExchangeID` concatenation (`handler.go:627`), which needs
  the typed identity model and belongs to M1.0-C0b.

## Bottom-up implementation

### 1. Freeze the payload class in the catalog

- [ ] Add `OperationPayloadClass` with constructor validation to
  `internal/access`; reject empty and unknown enum values.
- [ ] Require every `ClientOperationDefinition` to declare it.
- [ ] Set `count_tokens` to `client_semantic`, model operations to
  `client_semantic`, and unsupported entries to their true class.
- [ ] Carry it through `pathcapability.Capability`; the uncatalogued fallback
  reports `unknown`.

### 2. Split the dispatch arms

- [ ] Replace `case KindAuxiliary, KindOpaque:` with separate arms.
- [ ] Decide rejection from payload class before `readBounded`.
- [ ] Bounded discard-drain, then respond.

### 3. Dialect error envelope

- [ ] Emit the Anthropic error shape for Anthropic endpoints and the OpenAI
  error shape for OpenAI endpoints; carry `profile_operation_unsupported` in a
  response header.
- [ ] Prove the body parses against the dialect's error schema.

### 4. Tighten the transport contract

- [ ] `originaltransport` requires proven `none/control` evidence.

### 5. Structural guard

- [ ] Add the rule plus good and injected-bad repository fixtures.

### 6. Fixed-client evidence

- [ ] Run bounded fixed Claude Code 2.1.220 fixture; record `passed`,
  `blocked`, or `not_observed` without presetting the result.
- [ ] If the fixed client terminates on the rejection, keep the fail-closed
  fix, mark the capability `blocked`, and open M1.0-C0a' for the local
  tokenizer strategy (`07` §6.3 option 1), which needs no egress and no
  handoff. Restoring the original-origin bypass is not an option.

## Gates

`make check`, `gofmt -l .`, `go test -count=1 ./...`,
`go test -race -count=1 ./...`, repeated race stress for `loopbackproxy`,
`pathcapability`, `operationcatalog`, `access`, `go vet ./...`,
`go mod tidy -diff`, `go mod verify`, pinned `govulncheck`,
`git diff --check`, clean tree.

## Completion statement

> Known and unknown payload-bearing client operations are rejected locally in
> the client's own dialect and cannot reach original-origin or provider
> transport while their typed handling plan is unavailable.

It does not prove Profile operations, per-egress audit, blind tunnelling, or
release readiness.
