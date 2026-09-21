# Runtime setup and trust experience

Status: integration implementation; public automatic HTTPS, private-CA DNS/IP
identity, explicit access addresses and TLS status are implemented on the
integration branch. DNS-01 and a real public-CA staging gate remain open.

Stable base: `m1/root-leaf-foundation` at `1adc765` (v0.1.10).
Integration branch: `feat/runtime-setup-experience`.
Reviewed contribution: [PR #15](https://github.com/vibe-agi/vibermate/pull/15), head `1508c437f1a7fdb047876948283c993e89996317`.
ACP remains in its separate feature worktrees.

## Product decision

Organize setup around the task a person wants to complete, not around the transport modules we implemented. A newcomer should get a working command without learning CA/SAN/CONNECT; a regular user should find a stable address and a useful diagnosis; an expert should be able to inspect exact configuration and trust boundaries without changing to a different product mode.

Do not add a global "beginner/expert" switch, an all-in-one certificate wizard, another layer of nested settings tabs, or a shared default `admin/admin` account. Progressive disclosure uses the same underlying configuration for everyone.

## Deployment and access are separate dimensions

Cover App, native standalone Web, and container Web. Independently distinguish this-computer access from access across devices. Personal versus team determines account workflows, not whether transport encryption is needed. Container detection must not change authentication, certificate purpose, or which settings tabs exist.

The native process and container run the same Server/Web contract. They differ in process supervision, file paths/permissions, and network binding/port publication. In particular, the native CLI already defaults to loopback HTTP; do not add a certificate requirement for this local path.

| Deployment | First successful action | Address / encryption | Accounts | Certificate task |
| --- | --- | --- | --- | --- |
| Personal App | Open App, install/repair terminal command if needed, copy a managed run command | Existing local App control; no domain prompt | No Runtime User needed for local App control | Managed launcher supplies scoped Proxy CA trust when inspection is enabled; no silent global trust installation |
| Personal native Web on this computer | Unpack Server and adjacent Web assets, run `vibermated server`, create owner in browser | Default `http://127.0.0.1:9666`; no domain/certificate required | Machine-local recovery proof, then username/password | Proxy CA persists in the selected data directory; no browser CA installation |
| Personal Docker on this computer | Start the explicit local profile, open the displayed loopback URL, create owner, copy login/run | `http://127.0.0.1:9666`; host-published loopback only | Owner setup via server-machine recovery proof, then username/password | No browser CA installation; Proxy CA persists in the data volume |
| Team / remote native Web | Service operator supplies a reachable HTTPS address, chooses automatic HTTPS, private CA, or existing certificate files, creates owner/members | Native TLS or verified L4 passthrough; fixed service account and data directory | Each person has their own Runtime User | Public/external Server identity is independent; private-CA mode explicitly shares the Runtime CA and discloses that wider trust scope |
| Team Docker | Admin chooses the private, public-auto, or supplied-certificate Compose profile and shares its canonical address | Native TLS or verified L4 passthrough; DNS/IP must match certificate SAN | Each person has their own Runtime User; owner-only management | Automatic, private and external sources have separate truthful lifecycle states |

"Personal" does not mean all HTTP is safe. A native process or Docker host on another computer is remote, even for one person. Offer an authenticated encrypted tunnel to a loopback endpoint or an explicitly trusted HTTPS deployment. Never recommend bypassing browser warnings as normal onboarding. The step-by-step operator entry point is [Deployment guide](../deployment.md), with Docker-specific mechanics kept in [Docker deployment](../docker.md).

Docker's host port restriction is not an application authentication rule: NAT may give local callers a bridge address, and other containers on a shared network may reach the service. Keep owner setup authenticated, isolate the Compose network, document supported Docker versions, and do not grant owner authority based on `RemoteAddr`.

## Information architecture

Keep the existing five settings destinations and stable cross-links:

| Main tab | Answers | Contents | Does not contain |
| --- | --- | --- | --- |
| Preferences / 通用 | How should this workbench look? | Language, appearance, current Runtime identity | Deployment certificate forms |
| Access & launch / 接入与启动 | How do I start or connect? | Contextual steps, correct address, copyable login/run, terminal status, troubleshooting link | User table, editable upstream accounts, CA rotation |
| Users / 用户管理 | Who can use this Runtime? | Owner/member lifecycle, reset/disable, link to connection guide | CLI installation and proxy certificates |
| Safety & data / 安全与数据 | What is trusted and retained? | Separate HTTPS and Proxy CA summaries, recording/storage information, offline controls | Outbound proxy routing |
| Network exits / 网络出口 | How does this Runtime reach upstream services? | Direct/HTTP/SOCKS exits and DNS policies | The address clients use to reach ViberMate |

Use capabilities and authenticated role, not guessed "Docker detection" or user-selected experience level. App-local capabilities determine whether terminal management exists. Web members see their own connection guide and account actions, not disabled owner controls. Missing status is "not checked/unavailable", never fabricated success.

### Access page: default and optional content

App default:

```text
Access & launch
Use on this Mac                         [Ready / Repair needed]
1  Terminal command                     [Install / Repair] only if needed
2  Start an Agent                       [copy command]
   Local App runs do not need a Web account.

> Browser or another device             optional, collapsed by default
  Web address                           [copy]
  Owner/member setup status             [Manage users]
  Sign in once                          [copy login]
  Start an Agent                        [copy run]

> Connection details and troubleshooting
```

Web default (owner or member, scoped by permission):

```text
Access & launch
This Runtime                            https://runtime.example.com
1  Install the CLI on your computer      concise platform-specific help
2  Sign in                              [copy login]
3  Start an Agent                       [copy run]
   Only need the browser? You are already connected.

Share this Runtime                      [copy address] [Manage users: owner only]
> Connection details and troubleshooting
```

The guide must say that `vibermate run --server ...` reaches the standalone Runtime, including native Web on this computer; a bare local `vibermate run` targets the App. An Environment is still selected explicitly by `--env`; deployment never silently changes traffic policy, account or model. Explain that the transparent default does not record conversation bodies; link to Traffic policies when recording is desired.

One task section has one primary action. Keep technical details out of the first screen: no PEM, SAN lists, fingerprints, container IP enumeration or every possible CLI flag. Commands remain selectable, keyboard-copyable and horizontally scrollable without clipping.

### Safety page: two distinct certificate summaries

```text
Offline controls                        keep the emergency pause action first

Server connection                       HTTP local / HTTPS / status unavailable
Protects the browser and CLI connection to this Runtime.
Address: https://runtime.example.com
> Certificate / deployment details      read-only source, SAN, issuer, expiry

AI traffic inspection certificate       Proxy CA ready / unavailable
Used only for AI connections captured through this Runtime.
Managed runs receive scoped trust automatically; browser-only users need no CA.
> Manual client setup                   [Download proxy CA] fingerprint, expiry
> Advanced certificate operations       owner only, explicit impact confirmation

Data and recording                      existing behavior, grouped separately
```

Show one Proxy CA section, not a Web export card plus a second App certificate card describing the same root. Keep native App trust/rotation controls when available, but distinguish optional global OS trust from managed process trust. Download name: `vibermate-proxy-ca.crt`; do not change existing API schema or saved certificate bytes just to rename a UI concept.

HTTPS status means encrypted transport, not proof that every client trusts the issuer. Do not display "trusted" from the URL scheme alone. For local HTTP use a calm, explicit explanation; for remote HTTP show a prominent warning with the action to configure HTTPS. Warnings must never encourage installing the Proxy CA to fix an unrelated external server certificate.

### User management

- App-local use works before an owner exists. Explain that creating an owner enables browser/team login, not the local CLI.
- Owner setup belongs to Users or the first-run Web setup flow. Access provides a status/action link; it does not duplicate the user table.
- Show owner/member role, active/disabled state and clear reset/disable consequences. Do not label upstream Account credentials as Runtime User passwords.
- Reset password revokes relevant sessions as defined by the auth contract. Copyable connection instructions contain no password or Recovery Key.
- Member pages do not expose CA replacement, other users, server paths, credentials or administrative APIs.

## Visual / interaction plan

Retain the established workbench design instead of introducing a new landing-page aesthetic. The six main dark roles are canvas `#10151A`, surface `#182028`, raised surface `#222C35`, primary text `#F1F5F8`, supporting text `#B7C1CB`, and action/focus accent `#75B8F0`; reuse the matching existing light-theme tokens. Existing success/warning/danger colors retain their semantic meaning.

Use the existing system UI font and Chinese fallbacks for labels and instructions; monospace only for commands, fingerprints and paths. Keep headings consistent with `ViberType`; use the existing 4-point spacing and 1080px left-aligned content maximum. No extra fonts, gradients, decorative badges or repeated title cards.

Considered layouts:

1. More main tabs for CLI, Web, certificates and accounts: rejected; too many places to search and poor narrow-window scanning.
2. Nested sub-tabs under Access: rejected for this iteration; hides dependencies and creates two navigation levels for a simple launch task.
3. Existing task-based main tabs plus contextual steps and optional disclosure: selected. Its distinctive element is the small, actionable connection summary followed by the next required step, not a wall of equal-weight cards.

Critique before implementation: a stepper can become another wizard or duplicate user setup. Use ordinary sections, do not lock navigation, and do not infer completion from a copied command. A local HTTP summary can falsely imply security; name the scope and lack of encryption explicitly. Details disclosures must not hide blocking errors.

Acceptance includes 390px and wide windows, both themes and languages, 200% text where practical, keyboard/focus traversal, long DNS names, loading/failure/no-owner states, and no clipped input labels. Status changes do not reorder the page or steal focus. Copy success is local to its action and dismissible; no permanent success banner occupying the first screen.

## Configuration ownership and trust boundaries

### One source of truth per setting

| Setting | Owner / where edited | What UI shows |
| --- | --- | --- |
| Bind interface and port | Native process/service flags or Compose | Read-only listening configuration; never a suggested client URL |
| Server Access Address | Explicit deployment URL, falling back to verified connected origin when appropriate | Copyable address with provenance; no container IP substitution |
| Server TLS certificate and key | Deployment policy selects operator-owned files or opt-in automatic HTTPS; server identity module owns the lifecycle | Source, covered names, validity, renewal state and actionable errors; never private key bytes |
| Proxy CA | Runtime data directory (container volume when applicable) and controlled rotation API | Purpose, fingerprint, public export; no CA private key |
| Runtime User | Runtime auth authority | Users page and personal account actions |
| Environment / Account / model | Traffic policy and upstream settings | Links only from the connection guide |

Prefer one native Runtime URL for both control and CONNECT. Separate Web/control/proxy public URLs are a future deployment capability, not independent text fields that currently do nothing. When implemented, they need endpoint validation, authenticated distribution to launchers, and cross-origin/security tests. Do not ship an arbitrary URL override that sends credentials to an unverified host.

For the current native listener, support literal IP and DNS Server Access Addresses without credentials, paths, queries or fragments. Validate scheme against the listener mode; configured addresses are presentation/discovery, not authorization. The connected Web origin is useful for display but must never be accepted as a forwarded authentication identity.

### Three connections, three checks

1. Browser/CLI to Runtime: Server HTTPS Identity; verify server chain, name and validity. Private issuer trust must be provisioned explicitly through a trusted bootstrap.
2. Agent to intercepted AI destination inside CONNECT: destination-named leaf signed by the Proxy CA; launcher supplies scoped trust and capture authorization.
3. Runtime to actual upstream: normal upstream identity verification and the selected network exit; never disable verification to match the previous two.

An external certificate for `proxy.example.com` on connection 1 is correct. It would be wrong on connection 2 for `api.openai.com`. Tests must exercise both handshakes rather than looking at one certificate download.

### Certificate lifecycle and migration

- Preserve the original volume, Proxy CA, HTTPS identity and client pins. Keep existing experimental shared-issuer installations working; do not enforce a CA migration on upgrade.
- The new explicit local Docker profile may use HTTP; selecting it is not an automatic downgrade of the existing HTTPS Compose deployment.
- Native upgrades retain the service account, absolute data directory and Web assets; starting under another OS user must not be presented as a migrated Runtime. A working-directory change is not a change of identity. Do not run two Server processes against one data directory.
- Public/enterprise HTTPS certificates are not reissued by the Proxy CA. External certificate files are operator-owned, readable by the Runtime UID, and must satisfy the existing private-key permissions rules.
- Leaf renewal should not force Proxy CA replacement. Proxy CA rotation must not masquerade as server TLS renewal. Existing shared-root installations need explicit impact disclosure until fully separated.
- The current CLI exact-leaf first-use pinning is not public PKI validation. Implement a deliberate trust-mode and migration contract before claiming seamless public-certificate renewal. Never silently erase a pin or retry insecurely.
- Current file-based TLS reload requires a restart. Say so until a tested atomic reload exists; no "Apply" button without a real backend action and rollback semantics.
- First private-HTTPS trust cannot be obtained securely by blindly downloading a CA over that same untrusted connection. Provide machine-local export and out-of-band fingerprint verification, then authenticate normally.
- No automatic OS-wide trust installation, warning bypass, ACME account creation or public exposure as part of updating the App.

### Automatic server HTTPS — initial implementation

Native Web and container Web must offer the same Caddy-like certificate
acquisition and renewal capability. This automates **Server HTTPS Identity**,
not Proxy CA issuance or trust in intercepted AI hosts. Existing deployments
remain in their current mode until the operator explicitly chooses to change.

**Product presentation.** Keep this inside Safety & data → Server connection,
with a single link from Access & launch. Do not add an ACME tab or make users
choose an ACME library. The intended configuration choices are:

| User choice | Primary fields / next action | Detail hidden until needed |
| --- | --- | --- |
| Automatic HTTPS | Server name, a plain-language reachability check, explicit issuer-terms/privacy confirmation, then enable | Challenge method, contact address when needed, DNS provider, private-issuer configuration and diagnostic details |
| Use existing certificate | Existing certificate/key source and reload status | File permissions, chain and SAN inspection; no private-key download |
| This computer only | Loopback address and port; no certificate task | HTTP scope explanation and remote-access link |

Normal users should see the access address, whether HTTPS is ready, certificate
expiry and whether renewal is healthy. Experts can expand issuer, validation
method, next retry/renewal, source and redacted error details. Explain failures
as tasks (fix DNS, free/map the required challenge port, check DNS permissions),
not an unexplained ACME error code. A local preflight is not proof that the CA
can reach the server; keep validation pending until issuance actually succeeds.

**Deployment requirements.** Initial default automatic setup targets explicit
public DNS names the operator controls. Offer HTTP-01 where public port 80 can
reach a challenge-only listener, or TLS-ALPN-01 where public port 443 reaches
the Runtime TLS listener. The application port may stay 9666, but this does
not change the CA's required public validation ports. Container port mappings
and native-service privileges must be described separately. Do not require a
root Runtime or grant blanket container privileges merely to serve a challenge.
DNS-01 is the advanced route for a controlled public domain without inbound
challenge ports, or wildcard names; it needs supported DNS automation and
least-privilege credentials. A manually entered TXT value is not unattended
renewal. Private hostnames/loopback/private IPs must not be advertised as public
ACME names. Public-IP issuance is a separate, issuer/client-dependent capability,
not a blanket impossibility or a promise in the initial domain-based workflow.
[Challenge requirements](https://letsencrypt.org/docs/challenge-types/).

**Implemented direction.** The Server embeds Caddy's CertMagic behind the
server-identity seam; it does not implement a new ACME client or make
ordinary Caddy HTTP reverse-proxying the only deployment route. The existing
native listener must still carry both management HTTP and authenticated CONNECT.
Compare candidates and verified constraints in the
[automatic HTTPS research note](../research/2026-09-21-automatic-server-https.md).

The server identity module is the seam: it owns certificate acquisition,
persistent state, renewal, TLS certificate selection, public status, and shutdown.
App/Web callers see one lifecycle rather than separate library knobs. Native
and container hosts adapt file storage and listener binding; they do not each
implement enrollment or renewal. Its test surface covers all three implemented
modes plus the new managed mode through the same identity lifecycle.

Current `internal/serverhost/tls.go` installs one static certificate with
TLS 1.3 minimum and `http/1.1` ALPN. Managed certificates need atomic TLS
selection for new handshakes without closing established CONNECT streams.
TLS-ALPN-01 additionally needs its challenge protocol and certificate selection;
do not blindly replace the existing TLS policy with a library's defaults or
accidentally enable unsupported HTTP/2 CONNECT. Status and fingerprint must
track the active identity, not remain the startup certificate forever.

**Lifecycle and safeguards.**

- Persist ACME account material, keys, certificates and renewal state in an
  owner-private directory distinct from Proxy CA data. Reuse it across process
  restarts/container recreation; storage loss must not become routine issuance.
  Use library storage locking and atomic replacement, not duplicate cron jobs.
- Renew according to issuer/library renewal information and certificate
  lifetime, not an assumed 90-day lifetime. Failed renewal retains the last
  still-valid certificate, backs off and alerts; expiry remains a failure.
  Never fall back silently to plaintext or an untrusted self-signed identity.
- Only explicitly configured names are eligible for enrollment. Do not issue
  for arbitrary SNI/Host values or for captured AI domains. Disable unrestricted
  on-demand issuance. Bound retries; use a local test CA / staging for tests.
- Enabling automation must disclose the issuer, external validation and public
  certificate-name publication, and record required terms acceptance. Do not
  silently add a different public issuer as fallback. DNS credentials and ACME
  account keys never appear in commands copied for members, logs or CA export.
- First-time configuration must work from deployment input or authenticated
  machine-local bootstrap before public HTTPS is available. Do not expose an
  unauthenticated enrollment form on the public listener or send initial owner
  credentials over public HTTP. The challenge listener serves challenge content
  only, not the workbench, recovery or management routes.
- Deployment-owned configuration remains authoritative. The Web page shows a
  truthful read-only status/source until an authenticated persisted configuration
  and rollback contract exists; no nonfunctional Enable or Apply controls.
- Runtime Offline protection pauses Agent/provider traffic; it is not a host
  firewall and does not pause Server-identity maintenance. Automatic HTTPS may
  therefore contact only its explicitly configured ACME directory for the one
  configured Server name while Offline protection is active. Issuance and
  renewal state remains visible in Server status and process/container logs. A
  future global network kill switch must be a separate control that explains
  the certificate-expiry consequence instead of silently inheriting this one.
- Migration of CLI HTTPS trust is a prerequisite for exposing managed renewal:
  public PKI verifies chain, name and validity; private issuers require explicit
  trust. Existing leaf pins stay meaningful until an authenticated, deliberate
  migration. Never fix renewal by deleting saved pins or accepting any new leaf.

Caddy's documented automation covers certificate acquisition, renewal,
background retries and persistent storage; its on-demand mode requires abuse
restrictions. These are behaviors to adopt at the identity seam, not reasons to
relax CONNECT authorization or reuse the Proxy CA for public server identity.
[Caddy automatic HTTPS](https://caddyserver.com/docs/automatic-https).

### Reverse proxy support

Native TLS and verified L4 passthrough are the initial supported team deployment paths. The listener carries both management HTTP and CONNECT; the current router rejects forwarded identity headers. An ordinary HTTP reverse proxy is not automatically a CONNECT gateway. Do not suggest generic Nginx/Caddy HTTP proxy snippets or accept arbitrary `X-Forwarded-*` to make them work. A future L7 adapter needs explicit trusted-proxy and public-URL contracts.

## Contribution adoption and delivery order

### A. Integrate, preserve, make claims accurate

1. Commit this plan and trust decision on the integration branch before adopting PR #15.
2. Merge the exact reviewed head into this branch, preserving contributor history. Do not merge the contribution directly into the stable branch or mix in ACP.
3. Correct known formatting/English-source gates without weakening the checks or Unicode test coverage.
4. Keep the ALPN fix and strict upstream verification; retain bounded transform behavior and tests. Shared signing/export code does not justify "one certificate fixes everything" wording.
5. Provide explicit local-HTTP and team-external-TLS configuration examples without altering existing HTTPS deployments. Remove the stale `.env.example` claim that a nonexistent settings form edits certificate names.

### B. Optimize the actual setup experience

1. Centralize the read-only setup projection: role, local capabilities, connection address/provenance and transport state. Derive page actions from it; avoid repeated boolean/index logic scattered across widgets.
2. Apply the contextual Access layout, correct local/remote warning copy, separate user management and one scoped Proxy CA section.
3. Implement explicit advertised-address validation and tests before allowing administrators to promise a shared public address.
4. Add honest Server HTTPS metadata, expiry warnings and deployment-source details. Advanced controls require implemented backend actions, not placeholder configuration fields.
5. Complete public/private/pinned server trust and certificate-renewal compatibility before promoting the team path as frictionless.
6. Add opt-in automatic Server HTTPS for native and container Web using the
   shared identity lifecycle. Include challenge preflight, private persistence,
   live renewal, truthful status and safe failure behavior; do not mark complete
   merely because a one-off certificate was obtained. Preserve local HTTP and
   external-certificate deployments.

### C. Verify and promote

Only after the relevant gates below pass, merge the verified integration candidate into `m1/root-leaf-foundation` (the actual primary branch, not the old local `master`). Publish/release is a separate action. If a gate is untested or blocked, report it and keep the stable branch unchanged.

## Acceptance gates

### Behavior

- Fresh App reaches a local managed run without creating a Runtime User, configuring a domain or installing a global certificate.
- Fresh native Web uses loopback HTTP by default, discovers adjacent Web assets, requires machine-local owner setup proof, and preserves users and Proxy CA across process restart. Native and Docker Web use the same authenticated setup/login/export assertions.
- Fresh local Docker publishes only host loopback, serves HTTP, persists its Proxy CA, requires owner setup proof, and produces a working explicit `--server` command. Restart/rebuild preserves users, trust and evidence.
- The unreleased Compose surface has one default local profile and three explicit remote profiles: private CA, automatic public HTTPS and supplied certificate files.
- Team test uses external Server certificate A and independent Proxy CA B. Outer TLS presents A; inner CONNECT presents an AI-host leaf signed by B; upstream TLS remains strictly verified.
- Wrong server name, expired server cert, unknown private issuer, wrong Proxy CA, invalid upstream cert, unauthorized export and unauthorized setup all fail at the right boundary with useful errors.
- Renew A under an already trusted issuer without rotating B or unexpectedly breaking public-PKI clients; preserve explicit legacy-pin behavior until the user authorizes migration.
- Managed HTTPS is tested with the same local test-CA contract for native and
  container hosts: first issuance, restart without duplicate enrollment, live
  renewal during an active CONNECT stream, CA outage/retry, DNS or port failure,
  storage failure and expiration. Unauthorized names never trigger enrollment.
- A renewed public certificate is accepted by browser and CLI through normal
  identity verification, while a wrong hostname/untrusted replacement still
  fails. ACME state survives container recreation and Proxy CA fingerprint stays
  unchanged. Staging-issued certificates are never labeled browser-trusted.
- Challenge-only HTTP/ALPN endpoints never expose login, owner setup, account
  material or generic management routes. Agent Offline protection continues to
  block Agent/provider traffic without interrupting bounded certificate
  maintenance for the configured Server identity.
- Browser-only users do not install B. Members cannot enumerate other users or mutate deployment settings. No secrets appear in copied instructions or error reports.
- Actual observed-ClientHello-without-ALPN fixture reaches the dialer and succeeds against a valid HTTP/1.1 fixture; invalid upstream certificates still fail.

### UI and packaging

- App owner, Web owner, Web member, first setup, disconnected and restricted-capability views have truthful actions.
- Access/Users/Safety navigation has one authoritative destination each; no duplicated user list or Proxy CA cards.
- 390px, normal desktop and wide layouts pass in English/Chinese and dark/light themes, including long addresses and error messages.
- Go unit/integration/race tests, vet, formatting and repository checks pass.
- Flutter analyzer, unit/widget tests, browser tests and build pass; native host build is checked when native export changes are included.
- Compose rendering, real local-container startup and real native-process startup are checked independently. Public-team/real-upstream checks are reported separately from fixtures; no simulated "all tested" claim.

## Evidence and known baseline gaps

At PR head `1508c437`, the earlier review passed all Go tests, targeted transport/identity/transform race tests, the no-ALPN diagnostic, Flutter analysis, 422 Flutter tests (5 live-runtime skips), and 40 browser tests. These are contribution-baseline results, not acceptance of later integration edits.

Known baseline failures: Go formatting in `internal/desktopcontrol/message_transforms.go`; English-source gate on six Unicode test fixtures; stale certificate-settings instructions in `.env.example`. Known design gaps: exact-leaf CLI pins for all HTTPS, IP-only advertised targets, no TLS certificate auto-renew/reload, and shared-root wording that is incorrect for externally issued HTTPS certificates.

### Integration checkpoint — 2026-09-21

PR head `1508c437` was merged locally as `515074e` on the integration branch;
the primary branch is unchanged. This is not a GitHub PR merge or a release.

Implemented in this checkpoint:

- Task-based settings destinations share a typed definition. App-local launch
  guidance stays first; optional browser/remote instructions fold away. User
  management stays in its own destination.
- One connection projection supplies displayed addresses, commands and HTTP/HTTPS
  state. A connected Web origin takes precedence over internal container IPs;
  missing discovery does not generate a fictitious command.
- Server HTTPS and Proxy CA explanations are separate. Native App trust controls
  and Web CA export no longer produce duplicate CA cards. Manual public-CA
  details are folded; export errors remain visible. No TLS trust is inferred
  merely from an HTTPS URL.
- Explicit local and team Compose examples preserve the old HTTPS deployment.
  The common deployment guide and both README entry points cover native Web as
  well as Docker, including native loopback defaults, adjacent Web assets and
  consistent data directories for service accounts.
- Formatting and Unicode fixture source checks pass without reducing the test
  cases or changing their Unicode values.

Verification of this checkpoint (distinct from the PR baseline above):

| Check | Result |
| --- | --- |
| `go test ./...`, `go vet ./...`, repository and formatting checks | Passed |
| Race tests: transportprofile, providertransport, serveridentity, localca, serverhost, messagetransform | Passed |
| Flutter analyzer and full unit/widget suite | Passed; 444 tests, 5 live-runtime skips |
| Chrome: connection guide, settings navigation, CA export | 61 passed |
| Flutter release Web build and debug macOS App build | Passed |
| Compose configuration assertions | 3 passed |
| Current-source Linux ARM64 container image | Built as isolated `vibermate-runtime:setup-experience-test` |
| Real native process and real container setup smoke | Both passed; common assertions cover Web assets, wrong-key/unauthenticated rejection, owner setup, public CA export and restart persistence |

The smoke harnesses use disposable private data, do not read existing Runtime
credentials, and remove only their own temporary process/container/volume/files.
The container test re-reads its random published port after restart; that port
is not an identity or an assumption that the user keeps the same mapping.

Still pending before stable promotion: explicit advertised-address configuration,
actual server-certificate metadata and renewal UX, deliberate public/private/pinned
CLI trust migration, opt-in automatic HTTPS, and external-server-certificate/inner-Proxy-CA lifecycle
acceptance. The automatic HTTPS requirement was added after this checkpoint's
tests; no ACME implementation or issuance is covered by those test results.
The real-public-upstream and credentialed client flows were not run
by these setup smoke tests. Do not mark all behavior gates above complete or
merge to the primary branch on the strength of local setup tests alone.

## Primary references

- [curl: separate HTTPS proxy and destination certificate verification](https://curl.se/docs/sslcerts.html).
- [Docker: port publishing, loopback binding and network reachability](https://docs.docker.com/engine/network/port-publishing/).
- [Caddy: local HTTPS trust inside Docker is not host/browser trust](https://caddyserver.com/docs/running#local-https-with-docker).
- [Existing Runtime User and Web Session decision](../adr/0009-unify-human-login-with-scoped-web-sessions.md).
- [Server identity versus Proxy CA decision](../adr/0010-separate-server-identity-from-proxy-trust.md).
