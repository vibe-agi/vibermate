# VibeMate

This repository contains the production implementation of VibeMate.

The current code is a narrow M0 runtime, executable Access-plan, protocol,
controlled-egress, Exchange, loopback-ingress, and Desktop-host foundation. It
provides a typed `ProductRuntime` lifecycle, a Host-owned readiness commit
point, a mandatory offline-egress coordination boundary, a real versioned
SQLite store with operation admission and bounded drain, and a complete Access
aggregate with transactional compare-and-swap writes. A pure compiler validates
ownership, references, and declared capabilities before producing the sole
process-local immutable `AccessPlanSnapshot` and deterministic `PlanHash`.

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

DesktopHost now owns the literal proxy and control listeners, complete routes,
generation lock, capability separation, launcher discovery, and the only
product readiness publication. It publishes discovery only after
ProductRuntime, both listeners, and every route are ready. The packaged
`vibermated` sidecar writes a one-shot bootstrap descriptor to the native shell;
the Tauri shell exchanges that nonce outside the Webview and transfers one
read/write control session to the main Webview. Development and packaged
Webview origins are selected explicitly and never accepted together.

The authenticated control slice exposes status, active-plan metadata and apply,
write-only credential replacement, Activity, ConnectionEvent, approval, and
offline-hold actions. Credential metadata inspection never reads secret bytes,
and responses never contain a secret value or `SecretRef`. The React UI uses
the synchronized `en-US` and `zh-CN` catalogs, can load the active Access
revision before editing, and does not place capabilities or secrets in Web
Storage.

The `vibermate run -- <command>` launcher consumes only short-lived, private,
generation-scoped discovery; creates one CaptureRun; supervises one child;
injects authenticated proxy variables; removes protected Agent authorities
from inherited `NO_PROXY`; and heartbeats and finishes the run. Host integration
tests exercise this path over real loopback listeners with a local child
process. They do not send provider traffic.

The opt-in `vibermate-acceptance` command exercises the packaged macOS arm64
assembly with fixed Claude Code 2.1.220. It derives the daemon and launcher from
one App bundle and cross-checks an embedded build manifest against actual
artifact digests and Go build metadata. The manifest binds the App build to one
clean Git revision, pinned toolchains, explicit Desktop/development-sidecar
profiles, configuration digests, and both packaged sidecars. The deterministic
sequence uses a unique missing SecretRef; the credentialed continuation uses
the development file SecretStore and defaults to a local Cherry Studio API at
`http://127.0.0.1:23333/v1` with model `dashscope:glm-5`. No acceptance mode takes
a secret value on its command line.

SQLite is the only durable Access authority; active-plan publication occurs
after commit. An indeterminate commit or post-commit publication failure marks
only the affected Access projection unavailable, so new reads and writes fail
closed instead of serving an unmarked stale plan. A normal close/reopen recovery
recompiles the same revision and hash from SQLite. ProductRuntime reports only
`initialized`; DesktopHost derives product readiness and withdraws discovery
before shutdown. Unit and component tests do not by themselves prove packaged
Claude, provider, `SIGINT`, or force-kill behavior; those claims require a
passing private v3 report from the clean frozen artifact.

Even a passing M0 assembly report does not prove physical network loss/sleep,
power-loss durability, arbitrary client/provider compatibility, Root
installation, signing, notarization, or release secret protection. The code
does not implement Server, Windows/Linux, unmatched-endpoint blind tunneling,
system proxy installation, multi-profile routing, or a full control API.

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
