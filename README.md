# VibeMate

This repository contains the production implementation of VibeMate.

The current code is an M0 runtime, executable Access-plan, protocol, controlled
egress, Exchange-pipeline, and loopback-ingress foundation. It provides a typed
`ProductRuntime` lifecycle, an explicit host contract, a mandatory
offline-egress coordination boundary, a real versioned SQLite store with
operation admission and bounded drain, and a complete Access aggregate with
transactional compare-and-swap writes. A pure compiler validates ownership,
references, and declared capabilities before producing the sole process-local
immutable `AccessPlanSnapshot` and deterministic `PlanHash`.

The current M0 plan contains one enabled Agent endpoint, one owned OpenAI Chat
profile and provider target, one account binding that stores only `SecretRef`
and `AuthDriverRef`, one default route set, Direct egress, a fixed model
mapping, the Anthropic Messages to OpenAI Chat codec identity, an explicit
empty pass-through plugin plan, and dependency revisions. `ClientOrigin` and
the actual provider target are separate network identities, and no secret value
can enter the aggregate or snapshot.

The protocol layer now implements the corresponding network-free bilateral
path. Constructor-validated immutable IR separates Anthropic Messages client
semantics from OpenAI Chat provider semantics; the typed codec composition
handles bounded request/response translation, incremental SSE framing, token
deltas, usage and stop reasons, complete tool blocks, and the tool-intent commit
barrier. Official Anthropic and OpenAI Go SDKs are pinned test-only wire
oracles and are structurally forbidden from the production codec hot path.
Unsupported or ambiguous protocol shapes fail closed.

Controlled egress is now a mandatory runtime-owned boundary. The planned
offline coordinator serializes logical action admission with Enter Hold,
allows actions admitted before that cut to finish, queues later egress with a
complete frozen non-secret target identity, and probes those exact identities
before bounded FIFO release. `SafeToDisconnect` is true only after pre-Enter
actions and active egress have drained. Provider and original-origin clients
cannot emit an external byte without a lease and have bounded cancellation,
response-body ownership, and shutdown.

The provider client separates origin, HTTP authority, and TLS server name,
enforces strict certificate verification and redirect rejection, and applies
the frozen transport-fingerprint plan with explicit fallback evidence. It
retrieves a `SecretRef` only after egress admission, applies the typed static
bearer AuthDriver, and destroys the process-memory value. The host-neutral
SecretStore exposes no listing or plaintext control API. Ordinary development
builds have a private file-backed driver selected by build tag; its contents
are plaintext-equivalent at rest and are not release secret protection.
Native Keychain selection remains deferred to the Desktop/release stage.

ProductRuntime now owns an internal Exchange executor. Each admitted Exchange
begins a planned-offline action before resolving configuration, resolves the
active plan exactly once, revalidates the frozen ingress identity, translates
the request, invokes only the gated provider transport, and publishes either a
complete response or incremental SSE output. A commit ledger prevents unsafe
transport replay after client-visible semantics, and complete tool groups wait
behind a durable fail-closed approval authority before any tool block or
terminal event is released. Attempts append a redacted durable Activity record
with stable reason codes and transport-selection evidence, never prompt,
credential, header, or raw tool-argument values.

ProductRuntime also composes a handler-only loopback proxy boundary. An
authenticated, persisted CaptureRun capability must be accepted before exact
`ClientOrigin` lookup, local leaf issuance, CONNECT MITM, path classification,
or data-plane dispatch. Every request on an existing CONNECT connection
revalidates its frozen AgentEndpoint evidence against the current active plan.
Semantic Anthropic Messages operations enter the Exchange executor;
auxiliary/opaque operations use the separately gated original-origin transport.
Body-free ConnectionEvents persist connection phase evidence. The local Root
is installation-persistent and exported as public evidence, but this code does
not install it into an operating-system trust store.

The `vibermate run -- <command>` launcher contract is implemented and tested
against a fixture control server. It consumes only short-lived, private,
generation-scoped loopback discovery; creates one CaptureRun; supervises one
child; injects authenticated proxy variables; removes protected Agent
authorities from inherited `NO_PROXY`; and heartbeats and finishes the run.
The narrow CaptureRun control handler, launcher discovery publisher, generation
lock, and fixed-client verifier are implemented as typed components. They are
not yet assembled into a Desktop Host, so the production CLI cannot currently
discover a running VibeMate instance.

SQLite is the only durable Access authority; active-plan publication occurs
after commit. An indeterminate commit or post-commit publication failure marks
only the affected Access projection unavailable, so new reads and writes fail
closed instead of serving an unmarked stale plan. A normal close/reopen recovery
recompiles the same revision and hash from SQLite. Forced process termination,
operating-system failure, and power-loss recovery are not yet proven. Startup
reports only `initialized`; it does not publish product readiness or discovery.
There is no Host-owned proxy/control listener or discovery publication, so this
stage still exposes no client-connectable product route. No Desktop shell,
Server host, assembled control server, or product UI exists yet. Unmatched
AgentEndpoint blind tunneling, system/application proxy installation, physical
network loss, sleep, and credentialed provider behavior are not proven.

The implementation is not Preview-ready or Release-ready.

## Development

The required toolchain is Go 1.25.12. Run the deterministic repository checks
and test layers with:

```text
make check
make test
make test-race
make vet
make vuln
```

The runtime and package ownership map is in
[`docs/module-map.md`](docs/module-map.md).

## License

VibeMate is licensed under the Apache License 2.0. See
[`LICENSE`](LICENSE).
