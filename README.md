# ViberMate

ViberMate is a user-controlled runtime for observing, routing, and governing
AI Agent traffic. It sits between a captured client and its upstream services,
but only interprets content for exact endpoints in the Environment selected for
that Capture.

This repository is the production implementation. The current source contains
an Environment-first Desktop vertical; it is not yet a Preview or Release
build. No current evidence claims system trust installation, arbitrary-client
compatibility, Developer ID signing/notarization, or a release candidate from
this working tree.

## Product model

```text
Capture
  -> current Environment assignment
       -> ClientEndpoint (exact scheme + DNS host + port)
            -> ProtocolPlan
                 -> UpstreamRoute
                      -> UpstreamEndpoint
                           -> Account policy (only Accounts owned by that Endpoint)
                      -> Model and plugin bindings

Request
  -> frozen Environment revision and digest
  -> frozen Endpoint / ProtocolPlan / Route
  -> effective Account revision and credential epoch, when managed
  -> Attempts and downstream commit evidence
```

A Capture identifies where traffic came from. A managed Capture is created by
`vibermate run`; a Manual Capture provides one-time proxy credentials for an
application the user starts independently. Capture credentials attribute
traffic and never select a route, account, model, or plugin.

An Environment is the reusable configuration aggregate. Multiple Captures may
use the same Environment, and the same client origin may appear in multiple
Environments because Capture assignment is resolved before endpoint matching.
One Environment may contain multiple exact endpoints and protocols, including
Claude and Codex plans that share an upstream service without conflating their
wire dialects.

Every Provider Account belongs to exactly one Upstream Endpoint. This is an
invariant, not a default. An Environment may reference several Upstream
Endpoints through its Routes, and each Route may select only Accounts owned by
that exact Endpoint. Accounts do not own or duplicate secret material. The
Desktop App can connect supported credentials to the private SecretStore; the
client-login path remains available and never copies a captured credential
into account storage.

## Default behavior

`vibermate run` resolves its initial Environment in one deterministic order:

1. an explicit `--env` selection;
2. the Core-owned, immutable `system_transparent` fallback.

Therefore a first run remains safe and useful without configuration:

```sh
vibermate run -- claude
```

The transparent fallback authenticates the Capture and keeps each request's
original destination, credentials, and model. For the built-in Claude and
Codex Agent API origins it still terminates captured TLS, parses the protocol,
and records the configured evidence; unrelated destinations remain body-free
blind tunnels. It never invents an upstream Route or Account.

To select a configured Environment at launch:

```sh
vibermate run --env work -- claude
```

## Remote Runtime Server

`vibermated` can own the Runtime on a headless Linux host, or the macOS App can
expose the same Runtime it is already using. The Server operator creates
Runtime Users; clients authenticate once and reuse a revocable login session.
There is no per-run approval switch.

Place the Flutter Web build in `vibermate-web/` beside the daemon, then start a
headless HTTP Server:

```sh
vibermated server --listen 0.0.0.0:9666
```

The adjacent Web build is discovered automatically. `--web-root` remains an
explicit override for container layouts that mount the assets elsewhere.

The ready record names the local owner-only admin access-key file; it never
prints the key. Open `http://<server-host>:9666/`, enter that access key, then
create a Runtime User in Settings. The Web UI manages the already-running
Runtime; it does not start a second daemon or database. The macOS App exposes
the same management UI and Runtime User model.

On each client machine, log in once with that Runtime User, then start a managed
Agent run:

```sh
vibermate login --server 192.168.1.20:9666
vibermate run --server 192.168.1.20:9666 -- codex
vibermate run --server runtime.example.net:9666 --env work -- claude
```

Bare `host:port` intentionally selects HTTP so private networks work without a
certificate. HTTP leaves login credentials and captured traffic unencrypted in
transit. Use `--transport self_signed_tls` for pinned self-signed TLS, or
`--transport tls_files --tls-cert <absolute-path> --tls-key <absolute-path>` for
operator-managed certificates; clients select TLS explicitly with
`--server https://<host:port>`. The Server remains the authority for Runtime
Users, Environments, routing, provider credentials, and evidence.

The launch choice is frozen for the lifetime of that Capture. The Desktop App
does not switch a running Capture to another Environment, and there is no hidden
workspace default. A later native `resume` is still a new Capture: omitting
`--env` selects the captured Original Destination, while `--env NAME` selects
that Environment explicitly.

A Client Flow may choose the Original Destination. Original is not a Route: it
has no synthetic Endpoint, Account, or Model Mapping. ViberMate records the
Exchange while preserving the client origin, credential transport, requested
model, and response protocol. `system_transparent` applies that same Original
Destination behavior to the built-in Anthropic Messages and OpenAI Responses
client entries. Those exact Agent API origins are MITM-inspected and recorded;
unrelated destinations remain connection-only blind tunnels.

Each user Environment explicitly chooses its Request evidence policy. New
Environments default to redacted full-content evidence for 30 days; users may
choose metadata-only evidence or disable content recording. Conversation and
tool evidence is kept in a separate retention-bound SQLite surface. Activity,
ConnectionEvent, and EgressAttempt remain body-free, and captured credential
fields/authentication headers have no representation in the content record.
Secrets typed into a message are content and may be retained. The system
Environment uses the same redacted full-content, 30-day default for supported
Agent API exchanges.

Upstream semantics do not become incremental: every provider attempt still
receives the complete client request. Locally, retained transcripts use
content-addressed message and prefix nodes so repeated history is stored once.
The Request inspector defaults to the new suffix only when an exact transcript
previously delivered in the same managed Capture is a prefix; an exact retry is
shown as a replay, while compaction or history rewriting starts a new complete
checkpoint. The full frozen request remains available on demand. Manual
captures share stored bytes but do not infer transcript continuity because one
proxy credential may carry unrelated sessions.

## Environment publication

Environment configuration has one write path:

```text
save private draft
  -> compute bounded impact against current Capture assignments
  -> review which running Captures keep their frozen revision
  -> compare-and-swap durable publish
  -> atomically publish immutable process snapshot
```

Drafts are never request authority. Publishing changes only future Captures;
running Captures keep the revision selected at launch. A Request resolves once
and retains the same immutable Environment, endpoint, protocol, route, and account evidence for
its lifetime. Historical Activity refers to that frozen revision rather than
re-reading current configuration.

## Runtime and data plane

`cmd/vibermated` delegates to the sole production composition:

```text
DesktopHost
  -> ProductRuntime
       -> SQLite and Environment projection
       -> CaptureRun / ManualCapture / assignment authorities
       -> Offline Hold coordinator
       -> local CA and revision-aware leaf authority
       -> loopback proxy
       -> protocol path and Exchange executor
       -> provider/original transports
       -> Activity, ConnectionEvent, EgressAttempt, and approval journals
  -> authenticated local Control API
  -> private discovery and Desktop readiness
```

The proxy authenticates a Capture before policy, DNS, certificate issuance, or
body read. For a semantic Environment it requires an exact canonical
ClientEndpoint and current revision-scoped leaf admission. CONNECT authority,
ClientHello SNI, leaf SAN, and the selected Environment must agree. Every
persistent-connection request revalidates the endpoint.

Anthropic Messages and OpenAI Responses HTTP requests have typed production
paths. Unsupported payload-bearing operations fail locally before external
egress. Opaque same-origin control operations and blind tunnels remain separate
from semantic requests and produce body-free egress evidence.

MITM creates independent downstream and upstream TLS connections; it cannot
preserve the provider certificate or complete client fingerprint. The default
wire presentation follows the observed application protocol and bounded safe
shape. Product emulation is explicit user configuration, never inferred from
provider, model, or account. The current evidence does not claim exact JA3,
JA4, HTTP/2 SETTINGS, header-order, or browser fingerprint parity.

## Safety boundaries

- SQLite is the only durable Environment and Capture-assignment authority.
- The local Root private key stays inside `internal/localca`; system trust is
  not modified by the current product path.
- `system_transparent` receives Root material scoped to its built-in Agent API
  authorities; it never grants credential mutation authority.
- A semantic Capture receives only the exact launch authority frozen at its
  creation; later expansion requires restart.
- Provider secrets do not enter Environment snapshots, Activity, logs, or UI.
- The ordinary development SecretStore is private but plaintext-equivalent at
  rest. It is not Release protection.
- Offline Hold closes action and egress admission, drains pre-cut work, and
  reports safe-to-disconnect only from authoritative counts.
- Tool policy is Environment-owned and defaults to Observe: tools continue
  without waiting while Request evidence is still recorded. Review creates
  typed, expiring, revision-bound approvals only for unproven actions; Strict
  stops unproven actions without creating a question.
- Verified structured file tools may continue automatically in Review/Strict
  only when their resolved paths remain inside the frozen launch workspace.
  Shell text, MCP tools, plugins and custom tools never acquire that authority
  from a name or regular expression.
- Network approvals remain typed, expiring, and revision-bound.
- Network policy always has an explicit mode: Open, Ask, or Block.

## Desktop App

The native Flutter workspace exposes Captures, Conversations, Environments,
Endpoint-owned Accounts, Network governance, Offline Hold, Settings and
receipt-backed CLI installation. Preview mode uses deterministic fixtures and
is always visibly marked; live mode launches the bundled daemon and uses the
same authenticated Control API as the CLI.

Dart analyzer and unit/widget/API tests cover desktop and 390 px layouts,
keyboard traversal and focus, both locales, Capture switching and pagination,
real Manual Capture lifecycle, Environment publication, Endpoint/Account
selection, frozen Request and Turn drill-down, approval decisions and Offline
Hold. Native Swift and packaged-App tests cover preferences, bootstrap/session
lifecycle, the exact bundled daemon/CLI, and repeated App launches.

The current source composition includes one complete managed-account vertical:
an Anthropic API key is stored behind the private SecretStore, selected by one
Environment Route, stripped/injected only at the final provider boundary, and
reported back as frozen account revision, credential epoch, usage, and
terminal EgressAttempt evidence. Real composition tests repeat it after a full
SQLite/SecretStore reopen and prove that 401/403, missing credentials,
cancellation, and shutdown do not fall back to client OAuth or another
account.

## Validation

```sh
make check
make check-flutter-macos
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
go mod tidy -diff
go mod verify
```

The repository also has structural negative fixtures for composition ownership,
data-plane layering, egress construction, locale use, and deprecated
Access/Profile vocabulary. Packaged acceptance is opt-in and must bind a clean
source revision, App bundle, sidecars, client artifact, report digest, and
explicit deterministic or credentialed expectation.

## Not implemented or not proven

- Server Host, remote enrollment, and multi-user authorization;
- linked client-session/OAuth account connectors, live provider acceptance,
  and automatic account failover;
- plugin execution and marketplace UX;
- QualityRun production APIs and dashboards;
- Language Bridge translation and provenance tracking;
- system Root installation/removal in production composition;
- native release SecretStore, signing, notarization, and installer evidence;
- Network Extension/TUN capture for apps without proxy configuration;
- a fresh packaged acceptance report for the current source.

Those boundaries are deliberate. Passing package and native UI tests does not by
itself make this tree Preview-ready or Release-ready.
