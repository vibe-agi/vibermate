# ViberMate

ViberMate is a user-controlled runtime for observing, routing, and governing
AI Agent traffic. It sits between a captured client and its upstream services,
but only interprets content for exact endpoints in the Environment selected for
that Capture.

This repository is the production implementation. The current source contains
an Environment-first Desktop vertical; it is not yet a Preview or Release
build. No current evidence claims system trust installation, native secret
protection, arbitrary-client compatibility, or a packaged candidate from this
working tree.

## Product model

```text
Capture
  -> current Environment assignment
       -> ClientEndpoint (exact scheme + DNS host + port)
            -> ProtocolPlan
                 -> UpstreamRoute
                      -> ProviderTarget
                      -> Account policy
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

Provider accounts belong to an upstream authentication realm. Routes reference
compatible accounts; they do not own or duplicate secret material. The Desktop
App can connect Anthropic and OpenAI API keys to the private SecretStore and an
Environment can select one compatible account. The client-login path remains
the default and never copies a captured credential into account storage.

## Default behavior

`vibermate run` resolves its initial Environment in one deterministic order:

1. an explicit `--env` selection;
2. the saved default for the exact machine and workspace;
3. the Core-owned, immutable `system_transparent` fallback.

Therefore a first run remains safe and useful without configuration:

```sh
vibermate run -- claude
```

The transparent fallback authenticates the Capture, observes body-free
connection and egress facts, and blind-forwards traffic. It has no semantic
endpoint, never terminates TLS, never rewrites credentials, never runs content
plugins, and never delivers the local Root.

To select a configured Environment at launch:

```sh
vibermate run --env work -- claude
```

The launch choice is only the initial assignment. The Desktop App may switch a
Capture later. Compatible changes apply to new requests, connection-shape
changes drain and reconnect affected connections, and any change that widens
the launch-time Root or credential-rewrite authority returns
`restart_required` before mutation.

From a managed Capture detail, the user may save its current Environment as the
default for future runs in the same machine and workspace. That preference does
not change the current Capture and is not route, account, model, plugin, or
decryption authority. Clearing it restores the transparent fallback for future
runs only.

A user Environment may use an `original_passthrough` Route. This is still an
exact semantic endpoint: ViberMate records a frozen Request and Attempt while
preserving the client envelope, upstream target, response shape, model choice,
and ambient client credential. It is the lowest-friction inspection path;
`system_transparent` remains the separate no-MITM connection-only path.

Each user Environment explicitly chooses its Request evidence policy. New
Environments default to redacted full-content evidence for 30 days; users may
choose metadata-only evidence or disable content recording. Conversation and
tool evidence is kept in a separate retention-bound SQLite surface. Activity,
ConnectionEvent, and EgressAttempt remain body-free, and captured credential
fields/authentication headers have no representation in the content record.
Secrets typed into a message are content and may be retained. The transparent
system Environment always keeps content recording off.

## Environment publication

Environment configuration has one write path:

```text
save private draft
  -> compute bounded impact against current Capture assignments
  -> review hot-switch / reconnect / restart counts
  -> compare-and-swap durable publish
  -> atomically publish immutable process snapshot
```

Drafts are never request authority. A Request resolves once and retains the
same immutable Environment, endpoint, protocol, route, and account evidence for
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
- `system_transparent` cannot receive Root material.
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

The Desktop workspace exposes seven focused destinations:

- Captures and frozen Requests;
- Environments;
- managed Accounts, plus Plugins as an honest deferred resource surface;
- Policy & approvals;
- Quality as an honest deferred analysis surface;
- Settings and receipt-backed CLI installation.

The preview implementation uses the same Environment/Capture DTOs as the real
Control client. Playwright covers desktop and 390 px layouts, keyboard focus,
both locales, Capture switching, transparent Manual Capture creation,
Environment impact publication, frozen Request drill-down, policy decisions,
managed-account connection/selection, conversation/tool/usage inspection, and
Offline Hold.

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

Those boundaries are deliberate. Passing package and browser tests does not by
itself make this tree Preview-ready or Release-ready.
