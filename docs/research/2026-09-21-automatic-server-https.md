# Automatic Server HTTPS for native Web and Docker Web

Date: 2026-09-21. Research and proposed integration direction; no certificate issuance,
dependency changes, network configuration, or Runtime implementation performed.

## Recommendation

Embed **CertMagic v0.25.4** behind Runtime's Server HTTPS transport boundary.
This is an architectural recommendation: it supplies Caddy's certificate management while
Runtime retains its existing management HTTP and authenticated CONNECT listener.
Keep automatic certificate management separate from the existing local/self-signed and
operator-supplied certificate modes. CertMagic identifies itself as Caddy's TLS automation
library and supports HTTP-01, TLS-ALPN-01, DNS-01, persistent storage, and renewal.
[Official project](https://github.com/caddyserver/certmagic/tree/v0.25.4).

| Candidate | Verified capability | Assessment for this requirement |
| --- | --- | --- |
| CertMagic | Managed issuance/renewal, all three challenge types, storage locking, ARI. | Preferred: broader lifecycle support with a native TLS callback integration. |
| `x/crypto/acme/autocert` | Automatic issuance/renewal, HTTP-01 and TLS-ALPN-01, persistent `DirCache`, hostname policy. | Viable for a narrower public-domain feature; no built-in DNS-01 solver. |
| Caddy `reverse_proxy` | Separate HTTP proxy; adds `X-Forwarded-For`, `X-Forwarded-Proto`, and `X-Forwarded-Host` by default. | Requires a separately designed and tested CONNECT/trust boundary; not a drop-in plan here. |
| Custom ACME client | Protocol integration would become project-owned. | No identified benefit justifies owning challenge, persistence, retry, and renewal machinery. |

Sources: [CertMagic](https://github.com/caddyserver/certmagic/tree/v0.25.4),
[autocert API](https://pkg.go.dev/golang.org/x/crypto@v0.57.0/acme/autocert),
[autocert challenge selection](https://github.com/golang/crypto/blob/v0.57.0/acme/autocert/autocert.go#L770),
[Caddy forwarding defaults](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy#defaults).
The assessment column is inference, not an upstream compatibility guarantee.

## Versions, ownership, and compatibility

The Go module proxy reports CertMagic **v0.25.4**, published **2026-06-09**, as latest at
research time; its source revision is `cf6f57dc2126cf91f2b582c63a8668c6bf3b785d`.
Its tagged module requires **Go 1.25.0**; its license is **Apache-2.0**.
This is concrete release evidence, not a claim based on repository popularity.
[Version record](https://proxy.golang.org/github.com/caddyserver/certmagic/@v/v0.25.4.info),
[go.mod](https://github.com/caddyserver/certmagic/blob/v0.25.4/go.mod),
[license](https://github.com/caddyserver/certmagic/blob/v0.25.4/LICENSE.txt).

Autocert belongs to the Go project's `x/crypto` module, under its BSD three-clause license.
The observed latest module is **v0.57.0**, published **2026-09-08**, requiring **Go 1.26.0**;
the repository's existing **v0.54.0** requires Go 1.25.0. Therefore do not prescribe an
unnecessary upgrade to latest `x/crypto` for this feature. CertMagic's declared Go minimum
fits the repository's Go 1.25.13; a later implementation must still resolve and test the
complete module graph.
[Latest module record](https://proxy.golang.org/golang.org/x/crypto/@v/v0.57.0.info),
[latest go.mod](https://github.com/golang/crypto/blob/v0.57.0/go.mod),
[v0.54.0 go.mod](https://github.com/golang/crypto/blob/v0.54.0/go.mod),
[license](https://github.com/golang/crypto/blob/v0.57.0/LICENSE), [local go.mod](../../go.mod).

## Verified deployment prerequisites

| Challenge | Public validation requirement | Consequence |
| --- | --- | --- |
| HTTP-01 | CA reaches `http://<identifier>/.well-known/acme-challenge/<token>` on **TCP 80**. | Forward public 80 to the solver; another external port is insufficient. No wildcard certificates. |
| TLS-ALPN-01 | CA reaches **TCP 443**, negotiates **`acme-tls/1`**, and receives the challenge certificate. | TLS termination must deliver that handshake to the solver. No wildcard certificates. |
| DNS-01 | Public authoritative DNS exposes the expected `_acme-challenge` TXT record. | No inbound connection to Runtime is required; automated DNS API access is needed. Supports wildcards, not IP identifiers. |

HTTP/TLS challenges also support IP identifiers. DNS challenges can validate a public
domain whose application server is private; DNS visibility and propagation still matter.
Prefer narrowly scoped DNS credentials or delegated challenge zones.
[Let's Encrypt challenge requirements](https://letsencrypt.org/docs/challenge-types/).

For HTTP/TLS domain validation, point applicable public A/AAAA records at the serving
deployment and arrange firewall/NAT/container mappings. CertMagic allows alternate local
solver ports, but the public ports remain 80/443. Docker `EXPOSE` alone does not establish
those mappings; making them part of deployment configuration is an integration requirement.
[CertMagic requirements](https://github.com/caddyserver/certmagic/tree/v0.25.4#requirements),
[alternate solver ports](https://github.com/caddyserver/certmagic/blob/v0.25.4/acmeissuer.go#L105).

Publicly trusted IP certificates are **available**, not categorically unsupported:
Let's Encrypt announced IPv4/IPv6 general availability on 2026-01-15. They require the
**`shortlived` profile**, currently **160 hours**. CertMagic v0.25.4 exposes profile selection
and permits Let's Encrypt IP issuance in its precheck. This does not establish tested
Runtime support: propose public DNS names first, with IP support an explicit separately
validated option. Localhost/private IPs remain outside public certificate eligibility.
[Availability](https://letsencrypt.org/2026/01/15/6day-and-ip-general-availability),
[current profiles](https://letsencrypt.org/docs/profiles/),
[issuer/profile implementation](https://github.com/caddyserver/certmagic/blob/v0.25.4/acmeissuer.go),
[public-subject eligibility](https://github.com/caddyserver/certmagic/blob/v0.25.4/certificates.go#L579).

## Verified library lifecycle and proposed Runtime ownership

`Config.TLSConfig()` installs `GetCertificate` and the ACME ALPN value; it does **not**
start certificate management. `ManageSync` obtains/loads certificates before returning;
`ManageAsync` returns before issuance finishes and retries failures in the background.
Its context cancels its spawned work. Consequently, successful configuration acceptance
must not be reported as “HTTPS ready” until a usable certificate exists.
[CertMagic configuration source](https://github.com/caddyserver/certmagic/blob/v0.25.4/config.go).

CertMagic's cache starts maintenance, atomically replaces renewed certificates, and exposes
`Stop()` for final shutdown; a stopped cache cannot be reused. Its `GetConfigForCert`
callback lets maintenance obtain the current configuration. Proposed ownership: one
explicit Runtime manager/cache lifecycle, cancellation and cleanup on shutdown, and
dynamic certificate selection for subsequent handshakes. Renewing a certificate should
not require rebinding the listener or closing existing CONNECT tunnels; verify this in
integration tests rather than treating library behavior as proof of Runtime behavior.
[Cache implementation](https://github.com/caddyserver/certmagic/blob/v0.25.4/cache.go),
[TLS selection](https://github.com/caddyserver/certmagic/blob/v0.25.4/handshake.go).

Persist ACME account keys, certificate keys, certificates, and metadata in an explicitly
owned storage directory; Docker needs a durable mounted volume. CertMagic's storage
contract includes concurrency safety and locking for coordinated issuance. Its storage
must survive normal upgrades and container replacement. Proposed application rules:
restrict key-file access, keep secrets out of setup responses/logs, and fail visibly on
unwritable storage instead of repeatedly obtaining fresh certificates.
[Storage contract](https://github.com/caddyserver/certmagic/blob/v0.25.4/storage.go),
[storage guidance](https://github.com/caddyserver/certmagic/tree/v0.25.4#storage).

CertMagic uses ARI for renewal decisions and supplies replacement information on renewals.
Let's Encrypt grants qualifying ARI replacement orders rate-limit exemptions; ordinary
API request limits remain relevant. Autocert v0.57.0's renewal scheduler instead uses
certificate lifetime/`RenewBefore` and jitter; the reviewed scheduler does not consume ARI.
Delegate scheduling to the library and expose expiry/renewal errors, rather than hardcode
a 90-day lifetime or implement a separate fixed renewal cron.
[CertMagic ARI maintenance](https://github.com/caddyserver/certmagic/blob/v0.25.4/maintain.go),
[replacement orders](https://github.com/caddyserver/certmagic/blob/v0.25.4/acmeissuer.go#L475),
[Let's Encrypt renewal limits](https://letsencrypt.org/docs/rate-limits/#limit-exemptions-for-renewals),
[autocert scheduler](https://github.com/golang/crypto/blob/v0.57.0/acme/autocert/renewal.go).

## Integration gates and verification proposal

Current Runtime TLS uses static certificates, TLS >=1.3, and `http/1.1`; the client
`PinStore` rejects a changed leaf. Automatic renewal therefore requires a deliberate
trust migration: normal hostname/chain verification for public-PKI connections, explicit
private-CA trust where configured, and a separate pin policy for pinned/self-signed use.
Do not silently replace an existing pin or claim renewal already works.
[Listener](../../internal/serverhost/tls.go), [pin policy](../../internal/serverconnection/pins.go).

Preserve the existing HTTP/1.1 CONNECT behavior while adding `acme-tls/1` when that
challenge is enabled. Wire challenge handling independently of authenticated management
routes. Provision the allowed domain, CA agreement, storage, and challenge mode explicitly;
native-Web bootstrap/enrollment UI remains separate implementation work. A generic Caddy
HTTP proxy needs additional compatibility work because Runtime's authenticated CONNECT
and forwarded-identity rejection are part of its boundary. These are proposed integration
constraints, not a recommendation to replace that boundary.

Test issuance and accelerated renewal using a local ACME test server (Pebble), persistent
restart/replacement, renewal during a live CONNECT tunnel, ordinary client reconnection,
unknown-host denial, challenge failure, and storage failure. Use Let's Encrypt staging
for later deployment validation, never production for routine tests. Its directory is
`https://acme-staging-v02.api.letsencrypt.org/directory`; accounts are environment-specific
and staging roots are intentionally untrusted. Keep staging trust isolated to test clients.
[Official staging and Pebble guidance](https://letsencrypt.org/docs/staging-environment/).
