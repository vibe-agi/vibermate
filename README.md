# VibeMate

This repository contains the production implementation of VibeMate.

The current code is a narrow runtime, executable Access-plan, protocol,
controlled-egress, Exchange, loopback-ingress, Desktop-host, and local
certificate-authority foundation. It provides a typed `ProductRuntime`
lifecycle, a Host-owned readiness commit point, a mandatory offline-egress
coordination boundary, a real versioned SQLite store with operation admission
and bounded drain, and a complete Access aggregate with transactional
compare-and-swap writes. A pure compiler validates ownership, references, and
declared capabilities before producing the sole process-local immutable
`AccessPlanSnapshot` and deterministic `PlanHash`.

The current plan contains one enabled Agent endpoint, one default route set,
Direct egress, an explicit empty pass-through plugin plan, and dependency
revisions. The route set may name more than one candidate; each candidate owns
a profile, a provider target, an account binding that stores only `SecretRef`
and `AuthDriverRef`, and a fixed model mapping, and each is compiled and frozen
in the order the route set declares. A route set may carry
`pre_first_byte_idempotent_only`, which is the explicit permission for the
duplicate billing and possible upstream side effects a second attempt brings;
without it a failed attempt is reported rather than retried.
The sole immutable plan can now compile either the Anthropic Messages or exact
OpenAI Responses `POST /v1/responses` client operation against the existing
OpenAI Chat backend. A shared typed operation catalog is the only path truth
used by Access compilation and ingress classification. `ClientOrigin` and the
actual provider target are separate network identities, and no secret value
can enter the aggregate or snapshot.

The protocol layer now implements the corresponding network-free bilateral
path. Constructor-validated immutable IR separates Anthropic Messages client
semantics from OpenAI Chat provider semantics; the typed codec composition
handles bounded request/response translation, incremental SSE framing, token
deltas, usage and stop reasons, complete tool blocks, and the tool-intent commit
barrier. Official Anthropic and OpenAI Go SDKs are pinned test-only wire
oracles and are structurally forbidden from the production codec hot path.
Anthropic `eager_input_streaming` is a typed tool intent; because OpenAI Chat
has no equivalent request flag, the codec emits an explicit translation notice
while retaining incremental tool-argument handling and the commit barrier.
Unsupported or ambiguous protocol shapes fail closed.

Controlled egress is now a mandatory runtime-owned boundary. The planned
offline coordinator serializes logical action admission with Enter Hold,
allows actions admitted before that cut to finish, queues later egress with a
complete frozen non-secret target identity, and probes those exact identities
before bounded FIFO release. `SafeToDisconnect` is true only after pre-Enter
actions and active egress have drained. Provider and original-origin clients
cannot emit an external byte without a lease and have bounded cancellation,
response-body ownership, and shutdown.

The provider client separates origin, HTTP authority, network host, and TLS
server name. Remote targets enforce strict certificate verification, redirect
rejection, and the frozen transport-fingerprint plan with explicit fallback
evidence. The only cleartext exception is an explicitly configured literal
loopback IP: it is Direct-only, bypasses ambient proxies, and verifies the
connected TCP peer before any authenticated HTTP byte is written. It
retrieves a `SecretRef` only after egress admission, applies the typed static
bearer AuthDriver, and destroys the process-memory value. The host-neutral
SecretStore exposes no listing or plaintext read control API. The current
development sidecar uses one private file-backed driver; its contents are
plaintext-equivalent at rest and are not release secret protection. There is no
release SecretStore driver or release packaging profile in this stage.

ProductRuntime now owns an internal Exchange executor. Each admitted Exchange
begins a planned-offline action before resolving configuration, resolves the
active plan exactly once, revalidates the frozen ingress identity, translates
the request through an explicitly assembled typed protocol-path selector,
invokes only the gated provider transport, and publishes either a complete
response or incremental SSE output. Frozen client-operation evidence is
revalidated against that one plan before decoding. A commit ledger prevents
unsafe transport replay after client-visible Anthropic or Responses semantics,
and complete tool groups wait behind a durable fail-closed approval authority
before any tool block or terminal event is released. Attempts append a redacted
durable Activity record with stable reason codes and transport-selection
evidence, never prompt, credential, header, or raw tool-argument values.

ProductRuntime also composes a handler-only loopback proxy boundary. An
authenticated, persisted CaptureRun capability must be accepted before exact
`ClientOrigin` lookup, local leaf issuance, CONNECT MITM, path classification,
or data-plane dispatch. Every request on an existing CONNECT connection
revalidates its frozen AgentEndpoint evidence against the current active plan.
Exact semantic Anthropic Messages and OpenAI Responses HTTP operations enter
the same Exchange executor; semantic ingress carries no client authentication
or hop-by-hop headers into IR or provider construction.

The operation catalog freezes an `OperationPayloadClass` beside the operation
kind, and the two axes are independent. `POST /v1/messages/count_tokens` is an
auxiliary operation whose body is the complete messages, system text, and tool
schema, so it is declared `client_semantic`. Admission is decided from that
frozen class before any body is read: an operation that carries client payload
and has no typed handling plan is drained within its catalogued limit and
rejected locally with HTTP 422, in the client dialect's own error envelope,
carrying `profile_operation_unsupported` in `X-Vibermate-Reason`. It reaches no
transport, credential, egress lease, or Exchange, and its body never enters a
retained buffer, log, error, record, or row. The same rule covers an
uncatalogued body-bearing request, which has no proven class either. Bodyless
uncatalogued requests such as the fixed Claude Code control-plane GETs keep the
original-origin path until connection policy replaces that narrow exception
with an explicit allow/deny/ask decision. The agent-probe and opaque dispatch
arms are separate, each re-proves its own admission, and a structural
repository rule prevents them from being merged again. The original-origin
transport refuses `client_semantic` and `client_data` outright. Profile
operations, and therefore remote token counting, are not implemented; this is a
fail-closed correction, not `count_tokens` support. Responses
WebSocket upgrade is an explicit unsupported typed operation. A bounded 426 is
available only when the CaptureRun carries the exact verified fixed-client
feature; generic Codex versions receive the ordinary unsupported-path result.
Both decisions happen before body read or data-plane dispatch. Responses management,
upload, batch, media, Realtime, and foreign semantic operations cannot enter
model translation. Body-free ConnectionEvents persist connection phase evidence, and they now
describe one client-side connection only. A persistent MITM connection carrying
several requests used to overwrite its route host and credential decision with
whichever Exchange ran last, so it reported only the final destination and an
opaque request contributed nothing. Every real outbound now records its own
immutable `EgressAttempt` instead: purpose, the policy authority that purpose
requires, payload class, a typed parent, the authoritative target, the egress
decision, pool reuse, byte counts, and a terminal that cannot be rewritten. It
holds no secret, header, body, or tunnelled byte. Both the provider and
original-origin transports emit one per outbound; blind tunnelling and
runtime-originated egress have no writer yet. The authenticated control slice
serves the two records separately.

Identities are generated independently and associated by typed reference. The
Exchange, CaptureRun, connection, upstream attempt, and egress identities no
longer encode containment in a joined string, and a structural rule rejects
both building and matching an identity that way.

A transport resend after the provider has already answered now requires an
explicit repeat-billing allowance that defaults to refusing. A 502, 503, or 504
is still a response, so something upstream handled the request; resending a
generation_cost_only operation billed the user twice with no way to decline.

The local certificate authority owns one installation-persistent ECDSA P-256
Root. Certificate DER SHA-256 is its only machine identity; the display
fingerprint is derived, the public delivery path is not identity, and the
persistent Root revision starts at 1. Existing manifest state is migrated
without replacing the key or certificate through a same-directory synced
temporary file and atomic rename. The Root private key remains structurally
inside `internal/localca`.

Leaf signing is no longer authorized by a host string. The active Access
projection validates the current Root revision and exact Access,
AgentEndpoint revision, `ClientOrigin`, canonical DNS SAN, and leaf algorithm,
then grants a one-use admission to the local authority. Projection publication
is the revocation cut: an admission obtained before the cut may complete for
its current TLS handshake, while later admission fails and invalidated work
cannot repopulate the bounded LRU cache. Same-key cold issuance is coalesced;
different keys may generate concurrently; cancellation, failure, panic, and
shutdown do not cache a result or strand waiters. The production proxy performs
this admission against the real ClientHello after exact CONNECT/SNI validation.
IP-literal leaf admission is intentionally unsupported. The canonical DNS
constructor also rejects any value parsed as an IP address, so callers cannot
reclassify a bare IPv4 or IPv6 literal as a DNS SAN.

This code exports only immutable Root identity and defensive-copy public
certificate delivery material. It does not install, remove, replace, or rotate
an operating-system Root, and it has no trust Control API or trust UI.

The system-trust foundation now models certificate-object presence and the
target admin trust decision as separate typed axes. From the current public
Root it can derive immutable exact-digest install/remove plans and exercise a
fail-fast, per-step-reinspection coordinator against an injected executor. Its
macOS command and output shapes are versioned fixtures: no production executor
is wired, no production path observes the live trust store, and tests never run
`security` or modify System.keychain. In particular, this is orchestration
evidence, not proof of macOS authorization or system trust behavior.

Repository tests prove the version-gated Responses behavior. The latest clean
private v5 deterministic report is bound to implementation commit
`1b1a1b5fe43e1e4d89243006b10ff9c67ef0ea28` and fixed Claude Code 2.1.220; it
exercises the current certificate, explicit connection-policy, client-side
ConnectionEvent, Hold, shutdown, and SQLite-recovery paths without a provider
credential. The latest credentialed report remains the historical fixed-Codex
report bound to `c19cca4eb2842aa00d8e8fc17160b342a111f0b6`; it proves neither
current-HEAD credentialed behavior nor successful Responses WebSocket semantics
or client-visible per-token TUI behavior.

DesktopHost now owns the literal proxy and control listeners, complete routes,
generation lock, capability separation, launcher discovery, and the only
product readiness publication. It publishes discovery only after
ProductRuntime, both listeners, and every route are ready. The packaged
`vibermated` sidecar writes a one-shot bootstrap descriptor to the native shell;
the Tauri shell exchanges that nonce outside the Webview and transfers one
read/write control session to the main Webview. Development and packaged
Webview origins are selected explicitly and never accepted together.

The authenticated control slice exposes status, active-plan metadata and apply,
write-only credential replacement, Activity, ConnectionEvent, per-egress
attempt, approval, capture-run, connection-rule, and offline-hold actions.
Creating and controlling a capture run belongs to the launcher and its per-run
capability; reading the list is an ordinary app read and carries no capability
in either direction. Credential metadata inspection never reads secret bytes,
and responses never contain a secret value or `SecretRef`. The React UI uses
the synchronized `en-US` and `zh-CN` catalogs, can load the active Access
revision before editing, and does not place capabilities or secrets in Web
Storage.

The `vibermate run -- <command>` launcher consumes only short-lived, private,
generation-scoped discovery; creates one CaptureRun; supervises one child;
injects authenticated proxy variables; removes protected Agent authorities
from inherited `NO_PROXY`; and heartbeats and finishes the run. The daemon
verifies every artifact in a revisioned compound client release before granting
version-specific behavior. Fixed Codex receives the same local Root through
`SSL_CERT_FILE` plus a non-secret client placeholder after conflicting ambient
proxy, CA, base-URL, and credential variables are removed. Unknown Codex
versions remain generic unless macOS verifies a catalogued Developer ID signer
identity and the user explicitly approves the recognized-client Root handoff;
Linux has no recognized tier. Host
integration tests exercise this path over real loopback listeners with a local
child process, including bounded SIGINT convergence. They do not send provider
traffic.

The opt-in `vibermate-acceptance` command exercises the packaged macOS arm64
assembly with exactly one selected fixed client: Claude Code 2.1.220 or Codex
CLI 0.145.0. It derives the daemon and launcher from one App bundle and
cross-checks an embedded build manifest against actual artifact digests and Go
build metadata. The manifest binds the App build to one clean Git revision,
pinned toolchains, explicit Desktop/development-sidecar profiles,
configuration digests, and both packaged sidecars. The deterministic sequence
uses a unique missing SecretRef; the credentialed continuation uses the
development file SecretStore and defaults to a local Cherry Studio API at
`http://127.0.0.1:23333/v1` with model `dashscope:glm-5`. No acceptance mode
takes a secret value on its command line. The Codex runner isolates
`CODEX_HOME`, reads prompts from standard input, trusts only bounded typed
JSONL plus bounded client-status evidence, and exercises `exec resume`. One
clean packaged v5 deterministic report bound to
`1b1a1b5fe43e1e4d89243006b10ff9c67ef0ea28` passes 17 of 17 checks with fixed
Claude Code 2.1.220. The latest credentialed report is older: 25 of 25 checks
bound to `c19cca4eb2842aa00d8e8fc17160b342a111f0b6` and fixed Codex CLI 0.145.0.
It remains useful historical evidence but is not current-HEAD credentialed
evidence. The Codex tool proof names `exec`, and its Hold proof claims completion
through the Responses streaming path without claiming TUI delta rendering.

SQLite is the only durable Access authority; active-plan publication occurs
after commit. An indeterminate commit or post-commit publication failure marks
only the affected Access projection unavailable, so new reads and writes fail
closed instead of serving an unmarked stale plan. A normal close/reopen recovery
recompiles the same revision and hash from SQLite. ProductRuntime reports only
`initialized`; DesktopHost derives product readiness and withdraws discovery
before shutdown. Unit and component tests do not by themselves prove packaged
Claude, provider, `SIGINT`, or force-kill behavior; those claims require a
passing private v5 report from the clean frozen artifact.

Even a passing M0 assembly report does not prove physical network loss/sleep,
power-loss durability, arbitrary client/provider compatibility, Root
installation, signing, notarization, or release secret protection. The code
does not implement Server, Windows or Linux SecretStore backends, system proxy
installation, plugins, the quality subsystem, dashboards, or the Responses
WebSocket path. A Windows or Linux release build compiles and refuses at
startup rather than degrading to a file, which is what design 06 asks for and
is not the same as supporting those platforms.

## What running it proves

Opt-in live runs drive a real model at a configured origin. A client request in
Anthropic Messages reaches an OpenAI-chat backend and the answer returns in
Anthropic Messages, with usage; the same holds for an OpenAI Responses client.
An ordinary `http.Client` with a proxy address and the local Root in its trust
store reaches the same backend through CONNECT authorization, the connection
policy, and a leaf issued for that exact host. A streamed request returns
`text/event-stream` carrying that dialect's own events. A stream abandoned by
its client is recorded as cancelled, its outbound reaches a terminal, and the
runtime still drains.

An installed Claude Code binary, launched by the product's own launcher and
given nothing but a proxy address, a CaptureRun credential and the local Root,
reaches a real model and prints its answer.

These runs skip loudly without their environment variables rather than passing
quietly, and `docs/evidence/2026-08-02-live-provider-run.md` records what each
one does and does not prove.

The outbound firewall decides every proxied connection before any dial, DNS
resolution, or certificate issuance. The released default asks about a host
nobody has decided on and allows nothing in advance; an answer can be
remembered as a rule that commits with the decision itself, and rules are
stored, editable through the control API, and take effect on the next
connection without revisiting one already decided.

The macOS Keychain is the release SecretStore. `make check` builds and vets
under the release build tag, and cross-compiles the Windows and Linux release
builds, so the backend selection cannot silently go missing again.

The implementation is not Preview-ready or Release-ready.

## Development

The pinned toolchains are Go 1.25.12, Node 22.23.1 with pnpm 10.33.2, and Rust
1.88. Run the deterministic repository checks and test layers with:

```text
make check
make test
make test-race
make vet
make vuln
```

`make check` generates development-only Desktop icons and sidecars in ignored
build directories before running the UI and native-shell tests. It also
generates the build manifest later embedded by Tauri. The generated sidecar
uses the plaintext-equivalent development SecretStore; it must not be
distributed as a release build.

`cargo audit` currently exits successfully with 17 allowed transitive warnings,
including `RUSTSEC-2024-0429` in `glib`. That dependency is absent from the
current `aarch64-apple-darwin` tree, but the lockfile warning is not described as
a warning-free audit and still requires release-time disposition.

The runtime and package ownership map is in
[`docs/module-map.md`](docs/module-map.md).
The opt-in packaged acceptance contract is in
[`docs/m0-acceptance.md`](docs/m0-acceptance.md).

## License

VibeMate is licensed under the Apache License 2.0. See
[`LICENSE`](LICENSE).
