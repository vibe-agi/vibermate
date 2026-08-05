# ViberMate

This repository contains the production implementation of ViberMate.

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
revisions. Core always derives one invisible system `original_passthrough`
profile that targets the exact client origin, keeps client authentication, and
observes without semantic rewriting. The same route set may also name one or
more managed candidates; each owns a profile, provider target, account binding that
stores only `SecretRef` and `AuthDriverRef`, and fixed model mapping. Candidates
are compiled and frozen in the order the route set declares. The original
profile remains a selectable workspace route but can never be an automatic
fallback; any route set that contains it must disable fallback. A managed-only route set may carry
`pre_first_byte_idempotent_only`, which is the explicit permission for the
duplicate billing and possible upstream side effects a second attempt brings;
without it a failed attempt is reported rather than retried.
The sole immutable plan can now compile either the Anthropic Messages or exact
OpenAI Responses `POST /v1/responses` client operation against the existing
OpenAI Chat backend. A shared typed operation catalog is the only path truth
used by Access compilation and ingress classification. `ClientOrigin` and the
actual provider target are separate network identities, and no secret value
can enter the aggregate or snapshot.

Access lifecycle is reversible until an explicit retirement. Disable is one
aggregate-local CAS that withdraws only future admission; re-enable publishes
only for later requests. Permanent deletion is available only for a disabled
Access and starts with a revision-bound impact preview. Execution closes that
Access's per-request admission, drains requests admitted before the cut,
requires explicit retirement of workspace assignments, refuses while matching
CaptureRuns remain active or a ProxyClientBinding policy still references one
of its Profiles, preserves secrets shared by another Access, removes exclusive
saved credentials, and commits an immutable SQLite tombstone so the same
`AccessID` cannot be reused. Historical Activity, connection, and egress facts
remain. This does not force-cancel running Agents or erase historical evidence.

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
rejection, and one frozen product-level upstream presentation. New routes
default to `follow-client`: ViberMate safely reconstructs the observed client
shape for the same application protocol and preserves only the bounded current
request User-Agent (or its absence). Product emulation is used only when
the user explicitly chooses a named product; provider, model, account, and
dialect selection cannot choose it. A missing protocol variant fails before
secret access or dial and never falls back to another product, HTTP protocol,
or the standard library. The only cleartext exception is an explicitly configured literal
loopback IP: it is Direct-only, bypasses ambient proxies, and verifies the
connected TCP peer before any authenticated HTTP byte is written. It
retrieves a `SecretRef` only after egress admission, applies the typed static
bearer AuthDriver, and destroys the process-memory value. The host-neutral
SecretStore exposes no listing or plaintext read control API. The current
development sidecar uses one private file-backed driver; its contents are
plaintext-equivalent at rest and are not release secret protection. There is no
release SecretStore driver or release packaging profile in this stage.

MITM creates two independent TLS connections, so ViberMate does not claim that
the complete client fingerprint survives unchanged. The downstream client sees
an exact-DNS leaf issued by the local ViberMate Root, not the provider's
certificate. The default `follow-client` profile has H1 and H2 variants:
ViberMate retains supported cipher-suite and extension ordering, rewrites the
target SNI, and regenerates random, key-share, ticket, PSK, binder, and other
connection-bound state. Before the downstream TLS handshake, the current
Access projection intersects the client's offered ALPN values with the
protocols executable by every profile that the connection may select. The
protocol negotiated from that set is then preserved upstream; there is no
later H1/H2 downgrade, and protocol selection is not a user setting. Real local TLS and H2 services
observe the actual outbound ClientHello, request protocol, authority, bounded
User-Agent, incremental response delivery, and redacted transport evidence.
This is bounded wire evidence, not raw ClientHello passthrough or exact browser
parity. Generic H2 currently uses the fixed `x/net/http2` serializer, so it does
not claim to reproduce an observed client's SETTINGS, flow windows, pseudo
header order, or regular-header order. A named Codex H2 presentation therefore
remains unavailable until those shapes are executable and independently
captured. Literal-loopback cleartext currently supports H1 only. A client that
offers H1 and H2 can therefore negotiate H1 for such an Access; an H2-only
client fails before leaf issuance, secret access, egress admission, or dial.

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
durable Activity record with stable reason codes, the product-level
presentation choice, the same-protocol variant, and lower transport-selection
evidence, never prompt, credential, header, or raw tool-argument values.

ProductRuntime also composes a handler-only loopback proxy boundary. An opaque
proxy credential must first resolve to one immutable, route-neutral
`CaptureAdmission` before exact `ClientOrigin` lookup, policy-dependent dial,
local leaf issuance, CONNECT MITM, path classification, or data-plane dispatch.
One closed authorizer dispatches disjoint persisted CaptureRun and
ManualCapture credential namespaces; the proxy imports neither aggregate.
Admission records ingress profile, optional run/manual identity, an internal credential epoch,
attribution confidence, and any already-authenticated workspace/client-adapter
evidence. It cannot carry or select Access, Profile, route, account, model,
plugin, or provider credential. Every request on an existing CONNECT
connection revalidates its frozen AgentEndpoint evidence against the current
active plan.
With no active Access plan, an authenticated capture is intentionally
transparent: the proxy skips the editable network-policy question, never
terminates TLS or enters the semantic pipeline, and blind-forwards the original
HTTP/CONNECT target while retaining only body-free connection and egress
evidence. Projection read failure still fails closed. Once an Access is active,
the normal policy-first and exact-AgentEndpoint rules apply again.
Exact semantic Anthropic Messages and OpenAI Responses HTTP operations enter
the same Exchange executor; semantic ingress carries no client authentication
or hop-by-hop headers into IR or provider construction.

`vibermate run -- ...` now gives local launches a stable installation and
workspace scope without asking for a route name. The runtime creates one
private random `MachineID` plus a private workspace HMAC key, derives an opaque
`WorkspaceID` from the exact canonical launch directory, and keeps the absolute
path out of the route identity and route row. The current local-only CaptureRun
audit still retains its cwd; no remote Host is implemented. The launcher also
copies `$USER` on Unix or `%USERNAME%` on Windows into the CaptureRun as an
optional `LocalUserLabel`. That label is trimmed, bounded, and display-only: it
is not authentication evidence and cannot affect machine/workspace identity,
Access selection, routing, approval, quota, or upstream headers.

The local CLI finds the current daemon through a private, expiring discovery
record, then authenticates as an immutable generation-scoped
`ControlPrincipal`. Discovery refresh changes only rendezvous freshness; it
does not silently rotate login authority. One Core capture-grant issuer owns
client verification, Root-delivery decisions, workspace resolution, and
durable CaptureRun creation. Its returned per-run proxy and lifecycle
credentials are separate from the control credential: mixed credential
namespaces fail closed, and the child process never receives the control
credential or discovery path. The same local principal can now request a
ManualCapture through the shared handler. The durable authority for remote
enrollment now exists below product composition, but no Server listener or
`vibermate login` path is wired.
Exchange correlation now
carries the complete admission and a separately generated connection identity;
workspace routing can read only the scope frozen into that admission, not a
second caller-supplied option. The managed-run ingress profile is mechanically
`capture-run/<run-id>` and its non-rotating per-run capability revision is 1.

The next ingress foundation now exists below product composition: one durable,
owner-scoped ManualCapture authority can create, atomically rotate, revoke,
expire, observe, list, and recover an opaque proxy capability. SQLite stores
only a domain-separated credential digest; raw values are returned once on
create or rotation, and `manual-capture/<capture-id>` is mechanically derived.
Local-installation and future ProxyClientBinding owners are isolated, while a
ManualCapture deliberately has no Access, Profile, route, client adapter,
process, machine, or workspace authority. ProductRuntime now restores this
authority before building its existing proxy and dispatches the disjoint
`run_…` and `manual_…` credential namespaces into the same route-neutral
admission and downstream pipeline. One authenticated ManualCapture HTTP adapter
is shared by the local CLI and Desktop control router. It exposes review
context, create, list, detail, rotate and revoke without exposing the raw
password after create/rotate or the internal credential epoch. Creation uses an
opaque context-confirmation token; mutation CAS uses opaque ETags. The bounded
`vibermate capture create` command defaults interactive confirmation to no,
requires explicit `--yes` for non-terminal use, and emits either human-readable
or shell output without accepting route or account coordinates. The Desktop
Activity surface now provides the same two-step review and creation contract,
delivers the proxy password exactly once, and then retains only a secret-free
observation card with rotate and revoke actions. The UI never places the
one-time grant in its query cache or Web Storage. It reports whether traffic
has arrived, but it does not prove application identity; the remote login and
traffic paths plus current packaged evidence are still absent, so the feature
remains an engineering surface rather than a Preview product.

The remote-client foundation now owns durable ProxyClientBinding,
ClientEnrollmentGrant, MachineRegistration, and enrolled ControlPrincipal
state. Enrollment and long-lived control credentials use disjoint typed
namespaces and are stored only as domain-separated digests. Completing one
active, unexpired enrollment atomically consumes it and creates exactly one
machine and principal; concurrent consumers have at most one winner. Every
control authentication rereads the principal, machine, and binding, and
binding revocation closes pending enrollment and existing control admission.
MachineID and display name remain attribution metadata and cannot select a
route, Access, Profile, account, model, plugin, or provider credential. This
authority is exposed only through the RuntimeStore for a later Server
composition slice: there is no remote HTTP/TLS endpoint, Root delivery,
client-side credential file, remote proxy grant, or remote traffic path yet.

After an exact AgentEndpoint resolves an Access, the runtime atomically creates
or reads the durable `(AccessID, MachineID, WorkspaceID)` route binding. Each
Exchange freezes the binding and profile revisions once, so a Desktop CAS
switch affects only later requests while already admitted requests finish on
their pinned route. The Activity UI groups current runs by stable workspace,
shows the untrusted local-user labels, lists the Access/model/account attached
to each route, and can switch among profiles already approved by that Access.
Before a workspace has any Access-scoped binding, the group appears immediately
as `Waiting for first request`; the UI does not guess an Access from the client
name or working directory. This slice implements the local Desktop path only.
Remote MachineRegistration, enrollment, and per-run Server capabilities remain
unimplemented.

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
original-origin transports emit one per outbound and classify response EOF,
read failure, timeout, and cancellation rather than treating every body close
as success. Blind tunnelling appends its attempt before dialing and keeps its
attempt and client-connection terminals consistent. Runtime-originated egress
has no writer yet. The authenticated control slice serves the two records
separately.

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
private v6 deterministic reports are bound to implementation commit
`112600f822794d226bf96ff296c3334c26f5d7b1`: fixed Claude Code 2.1.220
passes 18 of 18 checks and fixed Codex CLI 0.145.0 passes 19 of 19. The latest
credentialed reports bind `63de401374358af2f6630201c8d75dd5ab3ca9d9`,
where Claude passes 26 of 26 checks and Codex passes 29 of 29. Together they
exercise the managed-client bootstrap, certificate, explicit connection-policy,
client-side ConnectionEvent, client-specific fallback, Hold, tool execution,
shutdown, SQLite recovery, and one explicitly configured provider route. They
do not prove successful Responses WebSocket semantics or client-visible
per-token TUI behavior.

DesktopHost now owns the literal proxy and control listeners, complete routes,
generation lock, capability separation, local control discovery, and the only
product readiness publication. It publishes discovery only after
ProductRuntime, both listeners, and every route are ready. The packaged
`vibermated` sidecar writes a one-shot bootstrap descriptor to the native shell;
the Tauri shell exchanges that nonce outside the Webview and transfers one
read/write control session to the main Webview. Development and packaged
Webview origins are selected explicitly and never accepted together. Bootstrap
is a two-frame, capability-free progress/final contract with separate progress
and ready deadlines. Storage-newer-than-binary, storage unavailable,
SecretStore unavailable, an already active generation, and generic runtime
failures cross as closed reason codes and map to synchronized localized
recovery guidance; raw Go/Rust errors do not cross into the Webview. The native
shell also lends the daemon an inherited parent-lifetime pipe. Closing that
pipe cancels and drains the daemon even when the shell is killed before its
ordinary exit handler can run, so an orphan cannot retain the generation lock.

After readiness, the Rust host retains the packaged child and its generation
identity. An unexpected exit before or after one-shot session delivery closes
that generation, drops any undelivered capability, and emits only
`{schema, reason: "daemon_exited"}` to the `main` Webview. The React boundary is
listening before spawn, closes the old control client (aborting active and
future requests), presents bilingual blocking guidance, and starts a new
generation only after the user selects Restart. Intentional bounded App
shutdown enters `stopping` before SIGTERM/kill and does not emit the crash
event. Process status, stderr, paths, argv, environment, and capabilities are
never event payloads. The retained minimal fault-record store and user-driven
diagnostic export required by design 11 remain open because their
storage/retention/export contract is not yet frozen.

The authenticated control slice exposes status, active-plan metadata and apply,
write-only credential replacement, Activity, ConnectionEvent, per-egress
attempt, approval, capture-run, connection-rule, and offline-hold actions.
Creating and controlling a capture run belongs to the launcher and its per-run
capability; reading the list is an ordinary app read and carries no capability
in either direction. Credential metadata inspection never reads secret bytes,
and responses never contain a secret value or `SecretRef`. The React UI uses
the synchronized `en-US` and `zh-CN` catalogs and uses neither `localStorage`
nor `sessionStorage`. Its eight top-level views are real pinned TanStack Router
routes. A Policy URL can carry one validated approval locator, legacy
`#/…`, `/policy`, `/policies`, `/approvals`, and `/system` links replace into
their canonical locations without retaining unsafe state, and the nested
main-content scroller participates in browser history restoration. External
hashes use the design's exact `#overview` spelling rather than `#/overview`.
All 14 frozen ICM hash-route skeletons are direct-entry routes:
`#overview`, `#access`, `#access/{accessId}/routing`,
`#activity/requests/{exchangeId}`, `#extensions/discover`,
`#extensions/installed`, `#extensions/detail/{extensionId}`,
`#quality/sites`, `#dashboards/system`, `#activity/requests`,
`#policies/approvals`, `#settings/recovery`, `#extensions/develop`, and
`#dashboards/extensions/{dashboardId}`. Policy approvals reuse the real
bounded queue, and `#activity/requests` is a real read-only Exchange-summary
feed with explicit cursor pagination. Its dynamic
`#activity/requests/{exchangeId}` route reads a closed, redacted projection from
durable Activity and EgressAttempt evidence: Exchange/Access/status, a stable
result code, ordered upstream-attempt IDs, an optional egress-proxy ID, and
plugin-run IDs. Missing Exchanges return a typed 404 and incomplete evidence
fails closed. Other deeper tasks without an authoritative control contract keep
their exact URL and render an explicit unavailable boundary without simulating
an object or reflecting an arbitrary locator into the document.

For the current macOS main window, the native Tauri host also owns navigation
restart persistence. A non-empty explicit launch hash always wins. Otherwise,
the host loads a validated locator before the Router module is imported and
installs it with history replacement. Route updates save only the schema and
canonical locator in a bounded app-data file: the locator is at most 2 KiB and
the complete file at most 4 KiB. The current macOS store requires an owned
`0700` directory and owned regular `0600` file, refuses symlink traversal, and
uses an atomic synchronized replacement. Native commands reject Webviews other
than `main`. Capabilities, secrets, business records, and Query snapshots are
not part of this file; after restart, business and Query state is read again
from the authenticated control plane.

Its session-scoped TanStack Query client owns seven independently polled
loopback sources plus Access-plan and credential-metadata reads. Activity polls
only its first page and loads older pages explicitly; each item is the closed
four-field summary `id`, `occurredAt`, `accessId`, and `status`, and unknown
status values render neutrally after bounded control-character validation.
Partial failure keeps last-success evidence with per-source freshness,
including when another source has not produced its first snapshot. Stale empty
data is never presented as an authoritative empty result. Control writes do not
retry, complete independently of follow-up reads, and invalidate only related
snapshots after success; browser offline state does not pause local-daemon
reads. Credential values and complete Access-apply payloads remain only in
short-lived command memory and never enter Query or Mutation cache. The native
one-shot session is consumed once under React StrictMode and is reused if the
first loopback inspection needs a UI retry. The wider entity tab families and
unfrozen filter/time-range/inspector grammar remain open. Browser preview does
not invoke the native navigation store and therefore does not itself prove a
macOS restart. The current v6 packaged acceptance contract instead seeds a
noncanonical safe locator in an isolated HOME, requires the Router to rewrite
it atomically, verifies the exit flush, and repeats the proof in a second cold
launch. No clean v6 report has yet been produced for this dirty worktree.
Multi-window restore and synchronization are not implemented. Windows
owner/DACL and reparse-point protections are also not implemented, so the
non-Unix store fails closed. The UI event/WebSocket
invalidation and recovery contract is not frozen; the current snapshots
continue polling. SQLite, the Access Manager, the canonical control contract,
and the UI now share one complete Access shape. The control API lists Accesses,
hydrates a complete aggregate and active plan by ID, applies a typed CAS update,
and rotates only the selected credential through its separate write-only
boundary. The UI can create a complete enabled Access in the recommended
current-login mode without a provider account or API key, or explicitly create
managed provider routes. It can reopen an existing Access, switch its current
route between the system original path and configured managed candidates, add
a managed account/route candidate, and submit the next revision without
exposing internal identifiers or inventing defaults for fields it did not read. A
successful apply response is a closed commit receipt: `active` includes the
exact candidate plan hash frozen at publication, while a known durable commit
whose projection failed returns `unavailable` without a hash. The UI reports
that state as unavailable and asks for a restart instead of claiming traffic is
active; the route never performs a racy post-commit plan lookup. An existing
Access can also be disabled and re-enabled through a status-only CAS mutation.
Disable withdraws new admission while preserving the complete durable
configuration and historical Activity; re-enable recompiles and publishes the
next revision for later requests only. Safe deletion is now an explicit
preview-then-delete workflow: it closes and drains per-Access request admission,
blocks on active captures or remote-client Profile references, requires
confirmation before retiring workspace assignments, cleans up only exclusive
saved credentials, and preserves history plus an immutable identity tombstone.

Desktop Settings also inspects the exact packaged `vibermate` sidecar and can
install, refresh, or remove one receipt-owned link at `~/.local/bin/vibermate`.
The operation never edits a shell profile, never accepts a caller-selected
source or destination, never overwrites an unrelated filesystem object, and
shows an absolute command that can be copied even when the managed link is not
installed.

The `vibermate run -- <command>` launcher consumes only short-lived, private,
generation-scoped discovery; creates one CaptureRun; supervises one child;
injects authenticated proxy variables; removes protected Agent authorities
from inherited `NO_PROXY`; and heartbeats and finishes the run. The daemon
verifies every artifact in a revisioned compound client release before granting
version-specific behavior. Fixed Codex receives the same local Root through
`SSL_CERT_FILE`; fixed Claude receives it through `NODE_EXTRA_CA_CERTS`. A
launch grant keeps the MITM authority set separate from its managed-credential
subset. Only an exact effective client origin in that subset causes the
launcher to remove conflicting client auth/base-selection inputs and inject a
non-secret local placeholder; Core removes client authentication again before
the selected provider AuthDriver finalizes the real upstream request. An
origin outside that subset preserves the client's own API key/OAuth and base
selection. The placeholder is neither a provider secret nor route authority.
Unknown Codex
versions remain generic unless macOS verifies a catalogued Developer ID signer
identity and the user explicitly approves the recognized-client Root handoff;
Linux has no recognized tier. Host
integration tests exercise this path over real loopback listeners with a local
child process, including bounded SIGINT convergence. They do not send provider
traffic. The production Access compiler now requires the Core-derived system
`original_passthrough` profile and keeps it account-free. Selecting it sends
the supported operation to the exact configured client origin with the
client's current authentication, preserves the downstream H1/H2 protocol, and
uses the default `follow-client` presentation. It does not run model mapping,
plugins, language transformation, provider authentication, or semantic retry.
Workspace route selection exposes both original and managed profiles. Managed
profile changes that use the same ViberMate-owned credential bootstrap affect
new requests without restarting a running tool. Changing between the tool's
own login and ViberMate-managed credentials is rejected while that workspace
has an active CaptureRun; the operator stops the tool, selects the route, and
starts it again. The guard uses an exact SQLite workspace query rather than a
page of recent activity, and the route repository evaluates it in the same
write transaction as the route CAS. CaptureRun creation precedes route-aware
launch authority resolution, so a concurrently starting run cannot pass
between the guard and the update.
Payload-bearing auxiliary operations such as Anthropic `count_tokens` still
receive the local typed 422 response on both route kinds in this changeset.
The fixed client is known to continue with its local estimate. Original-route
forwarding for that operation requires the separate typed ClientOperationRun
and `profile_operation` audit path; it is not reintroduced through the generic
original-origin forwarding arm.
A clean development-Host opt-in run at
`6803c07925927d84b8ddbbe4b7906715aa69b49c` proves that Codex CLI 0.146.0 can
preserve its existing ChatGPT login, cross the exact `chatgpt.com`
AgentEndpoint, fall back from the locally rejected WebSocket upgrade to HTTPS,
produce model output, and leave a completed exact-origin EgressAttempt without
a ViberMate provider account or API key. The UI therefore distinguishes ChatGPT
sign-in from an OpenAI API-key origin instead of presenting one ambiguous Codex
setup. The bounded run is recorded in
[`docs/evidence/2026-08-05-current-login-original-route.md`](docs/evidence/2026-08-05-current-login-original-route.md).
It is not packaged acceptance. No packaged credentialed report yet proves this
original route.

The opt-in `vibermate-acceptance` command exercises the packaged macOS arm64
assembly with exactly one selected fixed client: Claude Code 2.1.220 or Codex
CLI 0.145.0. It derives the daemon and launcher from one App bundle and
cross-checks an embedded build manifest against actual artifact digests and Go
build metadata. The manifest binds the App build to one clean Git revision,
pinned toolchains, an explicit Desktop/sidecar profile,
configuration digests, and both packaged sidecars. The deterministic sequence
anchors a fixed read-only SQLite connection before the client run, requires
exactly one new terminal Exchange failure, and cross-checks its ID, Access, and
status through the canonical paged `/activities` API before and after a true
cold reopen; the private seam contributes only ordering and terminal reason.
It uses a unique missing SecretRef; the credentialed continuation uses the
development file SecretStore. Provider origin and model have no hidden
default: every acceptance run must provide both explicitly. No acceptance mode
takes a secret value on its command line. The Codex runner isolates
`CODEX_HOME`, reads prompts from standard input, trusts only bounded typed
JSONL plus bounded client-status evidence, and exercises `exec resume`. The
latest deterministic reports bind implementation candidate
`112600f822794d226bf96ff296c3334c26f5d7b1` and pass 18 of 18 checks with
fixed Claude Code 2.1.220 and 19 of 19 with fixed Codex CLI 0.145.0. The latest
credentialed reports bind `63de401374358af2f6630201c8d75dd5ab3ca9d9`
and pass 26 of 26 and 29 of 29 respectively. The deterministic reports
independently verify the selected source, App, sidecars, acceptance executable,
client entrypoint, and frozen configuration. The current deterministic digests
and evidence boundary are recorded in
[`docs/evidence/2026-08-05-current-dual-client-packaged-acceptance.md`](docs/evidence/2026-08-05-current-dual-client-packaged-acceptance.md),
while the current credentialed run and independent mode verification are
recorded in
[`docs/evidence/2026-08-05-current-credentialed-packaged-acceptance.md`](docs/evidence/2026-08-05-current-credentialed-packaged-acceptance.md).
The Codex tool proof names `exec`, and its Hold proof claims completion through
the Responses streaming path without claiming TUI delta rendering.
If no Access is active at launch, the child receives an authenticated generic
proxy environment but no local Root and no protected authority. Interrupting
the CLI cancels supervision and escalates child termination within the existing
bounded shutdown interval, returning the conventional exit code 130.

SQLite is the only durable Access authority; active-plan publication occurs
after commit. An indeterminate commit or post-commit publication failure marks
only the affected Access projection unavailable, so new reads and writes fail
closed instead of serving an unmarked stale plan. A normal close/reopen recovery
recompiles the same revision and hash from SQLite. ProductRuntime reports only
`initialized`; DesktopHost derives product readiness and withdraws discovery
before shutdown. Because no database format has shipped, the embedded database
is one complete development baseline at schema revision 1 rather than a chain
of compatibility migrations. Its identity and exact embedded-source digest
reject an older same-revision development database, while newer Goose history
is rejected before any migration is applied. The Desktop development host
archives an unsupported old database, WAL, and SHM into a private backup
directory and retries once with the clean baseline; it never deletes the old
files. Runtime
startup reconstructs every durable EgressAttempt through the domain
constructors before changing a row; corrupt or partial terminal evidence aborts
the transaction and startup, while valid nonterminals left by an earlier daemon
become `failed(daemon_restarted)` with a completion time no earlier than start.
Terminal construction and persistence errors latch storage unavailable, cancel
the runtime owner, and make bounded shutdown fail instead of leaving an
outbound nonterminal silently. The baseline includes the typed
`plugin_catalog_sync` and `plugin_artifact_fetch` runtime egress purposes and
all current query indexes without carrying pre-release migration history.
Unit and component tests do not by themselves prove packaged Claude, provider,
`SIGINT`, or force-kill behavior; those claims require a passing private report
from the clean frozen artifact. The current producer emits v6; the verifier
retains the historical v5 contract solely so older evidence remains checkable
against its original, smaller check set.

Even a passing initial macOS arm64 packaged-app acceptance report (M0) does not
prove physical network loss/sleep,
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

Separate no-credential local-fixture runs at clean source
`59b687ba1205e295e44e91e22756e61410492079` drive installed Claude Code 2.1.221
and Codex CLI 0.146.0 through the same launcher, explicit recognized-client
Root decision, proxy, MITM, protocol conversion, streaming provider path, and
audit boundaries. Both print `ready`; the exact proof and non-proof boundary is
recorded in `docs/evidence/2026-08-05-current-client-local-fixture.md`.

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

For browser-only UI work, start Vite and open the explicit preview URL:

```text
pnpm --dir ui/desktop install --frozen-lockfile
pnpm --dir ui/desktop dev
open 'http://127.0.0.1:1420/?preview=1#overview'
pnpm --dir ui/desktop check:browser
```

Preview mode exists only in a development build and only when `preview=1` is
present. It uses one stateful in-memory `ControlClient`; it does not start the
daemon, proxy traffic, change host state, or read a real credential. The
preview client is removed from the production bundle, whose CSP remains strict.
The browser suite exercises the same route tree through direct entry, reload,
back/forward history, precise approval links, invalid and missing locators,
nested-scroll restoration, keyboard skip navigation, and narrow viewports. Its
permanent boundary cases also cover maximum-size identities in both locales,
all five Policy actions at phone width, cyclic cursor refusal, honest initial
metric loading, a source that fails before its first success, and missing
Exchange retry/back recovery without horizontal document overflow. Its reload
checks cover browser URL/history behavior; preview disables native navigation
load/save, so this suite is not macOS cold-start restoration acceptance. That
proof belongs to the packaged v6 acceptance command and still requires a clean
runner result.

The manual `packaged deterministic acceptance` workflow is the fail-closed
freshness runner for the initial macOS arm64 packaged-app slice (M0). Its
protected environment
must provide a self-hosted runner labelled `vibermate-acceptance` and an
absolute `VIBERMATE_CLAUDE_2_1_220_PATH`. It also requires explicit non-secret
`VIBERMATE_ACCEPTANCE_PROVIDER_ORIGIN` and
`VIBERMATE_ACCEPTANCE_PROVIDER_MODEL` environment variables; the workflow has
no repository-owned route default. It never installs or downloads a client:
the pre-provisioned executable must match the complete built-in Claude Code
2.1.220 release evidence. It builds the selected clean SHA, runs
deterministic-only acceptance without a provider credential, verifies the
private v6 report against that exact SHA and client even after an acceptance
failure, and retains the uncommitted report artifact for seven days. The
workflow explicitly requires the v6 schema, so a report cannot remove the new
checks by relabelling itself as v5. The verifier accepts a historical v5 report
only when its caller explicitly expects v5 and it has the exact frozen v5 check
set. For current v6 evidence, the workflow also supplies trusted source, App,
acceptance-executable, and client coordinates independently of the report. The
verifier rehashes those bytes and the frozen configuration, requires the source
coordinate to be the clean Git top-level checkout at the selected full commit,
and re-parses the selected App's v2 build manifest. The caller must explicitly
expect `deterministic`; the same verifier accepts current v6 credentialed
evidence only under an explicit `credentialed` expectation. A report cannot
select or downgrade its own mode, and a syntactically valid digest inside a
report is not accepted as proof of current bytes.
A passing run contributes packaged production-wiring evidence for the
architecture gate (G0); it is not Preview or Release approval.

Desktop bundling has two deliberately named entry points. `pnpm --dir
ui/desktop bundle:development` builds a local, unsigned development bundle and
refuses inherited Apple distribution credentials. `pnpm --dir ui/desktop
bundle:packaged-acceptance` requires the clean macOS arm64 candidate profile,
native Keychain sidecars, the pinned Node/Go/Rust/pnpm/Tauri toolchains, and a
pre-bundle manifest/sidecar digest recheck. A raw `tauri build` has no implicit
profile and fails closed. There is still no publish or `bundle:release`
command. The manual `protected macOS Developer ID candidate` workflow instead
builds one unsigned Universal candidate without Apple distribution
credentials, transfers it through a closed archive, and generates
source-traceability evidence (R0) on a fresh runner that never executes
candidate code. R0 is an internal release-evidence stage code, not an industry
acronym. Here it binds the selected commit, dependency lock files, exact
toolchain, SPDX SBOM, build manifest, and staged artifact digests. That
runner invokes the admitted Syft 1.44.0 binary directly, matches its complete
file inventory and SHA-256 values to the staged payload ledger, and then runs
the independent Go artifact verifier. Protected signing is gated on that
result; separate reviewed environments perform inside-out Developer ID signing
and DMG-only notarization/stapling, with credential cleanup before artifact
upload.
After successful notarization, a fresh standard `macos-15` arm64 job with no
GitHub Environment and without Apple distribution credentials downloads that
exact stapled-DMG artifact. It checks out the candidate inertly only to prove
ancestry, executes trusted tooling plus the notarized App, mounts the DMG
read-only, and copies `ViberMate.app` into a private stable
`$RUNNER_TEMP/.../Applications` root rather than the real `/Applications`.
Its only Apple identity input is the non-secret repository variable
`VIBERMATE_APPLE_TEAM_ID`; an absent or malformed value fails closed.
The job rechecks the Team ID, certificate, hardened signatures, exact Mach-O
inventory, Universal slices, minimum macOS version, build manifest, tree
ledger, and Gatekeeper decisions. It then reuses packaged acceptance for two
bounded launches, launcher-discovery/router readiness, navigation persistence,
and graceful exit from the installed path. Only after inode-bound cleanup of
the mount, App, isolated home, and state does it create the closed
`signed-package-installation-report.json` and checksum; a separate verifier
rebinds them to the raw signing/notary evidence and stapled DMG. The report
explicitly does not assert real-system `/Applications` installation, CLI-path
installation, updater behavior, system trust/proxy behavior, or uninstall.

This workflow creates bounded review evidence, not a release. Before any real
Apple-credentialed run, repository administrators must confirm that Apple
secrets exist only in the named environments and that required reviewers,
prevention of self-review, protected default-branch deployment policy, and
administrator bypass restrictions are active. The 2026-08-03 external audit
found zero GitHub Environments in the private repository (default branch
`main`); branch
protection and rulesets APIs both returned 403 with the current plan's
“Upgrade to GitHub Pro or make public” restriction. Consequently
`github.ref_protected` is currently false and every job correctly fails closed;
that assertion must not be weakened. The PAT also received 403 when enumerating
repository secret/variable names, so their configuration remains unverified.
Release still requires provisioned protected environments, a successful real
Developer ID/notary/installed-evidence run, current packaged conformance,
independent reproducibility (R2), signed-package association (R3), release
approval, and update and uninstall evidence. The packaged-acceptance artifact,
unsigned
source-traceability payload (R0), and workflow definition must not be
represented as distributable or as completed installation evidence.

`make check` generates development-only Desktop icons and sidecars in ignored
build directories before running the UI and native-shell tests. It also
generates the build manifest later embedded by Tauri and runs Rust formatting,
warning-free clippy, and native tests under the exact pinned toolchain. The
generated sidecar uses the plaintext-equivalent development SecretStore; it
must not be distributed as a release build.

`cargo audit` findings are checked against the repository's explicit policy.
An allowed or target-inactive transitive warning is not described as a
warning-free audit and still requires release-time disposition.

The runtime and package ownership map is in
[`docs/module-map.md`](docs/module-map.md).
The opt-in packaged acceptance contract is in
[`docs/m0-acceptance.md`](docs/m0-acceptance.md).

## License

ViberMate is licensed under the Apache License 2.0. See
[`LICENSE`](LICENSE).
