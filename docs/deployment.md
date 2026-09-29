[English](deployment.md) · [简体中文](deployment.zh-CN.md)

# Deployment and HTTPS: pick your setup first

Start with one question: who connects, and from where? You do not need to
understand CAs, SANs or container networking first. The App, the native Web
workbench and the container Web workbench all use the same Runtime, the same
account system and the same certificate boundaries.

| Setup | Start with | Address and certificate |
| --- | --- | --- |
| Personal App | Open the App, install the terminal command, run `vibermate run -- codex` | No Web account, domain or system-wide CA install needed |
| Native Web on the same computer | `vibermated server` | `http://127.0.0.1:9666`, no certificate needed |
| Container Web on the same computer | `compose.yaml` | Always published on host loopback, over HTTP |
| Private network/VPN, no public domain | `private_ca_tls` or `compose.private.yaml` | ViberMate private CA; certificate for a hosts-file name or an IP |
| Public domain, certificates managed for you | `automatic_tls` or `compose.public.yaml` | Public certificate requested, renewed and hot-reloaded automatically |
| You already have a public or company certificate | `tls_files` or `compose.team.yaml` | You provide the full certificate chain and private key |

"Personal use" does not make remote HTTP safe. As soon as a browser or CLI
connects from another device, choose one of the HTTPS modes or use an
authenticated, trusted tunnel. Never hand users `0.0.0.0`, a container `172.x`
address, or a certificate warning page.

## Three connections, three kinds of trust

1. Browser/CLI → ViberMate: protected by the **Server HTTPS certificate**
   configured on this page.
2. Agent → inspected AI domain: the **AI traffic inspection CA (Proxy CA)**
   issues leaf certificates for the target domains.
3. Runtime → the real AI provider: the provider's own certificate is verified
   strictly, and the selected network exit is used.

Automatic public certificates and certificates you provide have nothing to do
with the Proxy CA. `private_ca_tls` is a deliberate exception: so that managed
devices without a public domain only need one root, it uses the same ViberMate
private CA to issue the Runtime's server leaf certificate. Trusting that CA in a
system trust store therefore also trusts this Runtime's traffic inspection.
Install it only on managed devices. Do not distribute it publicly.

## Native Web on this computer

Run from the release package directory:

```sh
./vibermated server
```

Open <http://127.0.0.1:9666>. By default the Server listens on loopback only.
If the port is taken, use `--listen 127.0.0.1:9667` and change the port in the
browser and CLI as well. In another terminal, read the setup/recovery key:

```sh
./vibermated server recovery-key
```

Create the owner account in the Web page. There is no default `admin/admin`. If
you started the Server with `--data-dir`, pass the same absolute directory to
every server-local command. The CLI connects to a standalone Server explicitly:

```sh
vibermate login --server http://127.0.0.1:9666
vibermate run --server http://127.0.0.1:9666 -- codex
```

A local `vibermate run` without `--server` connects to the App. The default
traffic policy, System Transparent, records conversations (kept for 30 days by
default) and keeps the original destination and credentials. When you want
different routing, recording or retention, publish your own traffic policy and
select it with `--env <policy ID>`.

### Create a manual capture login for other clients in the Web workbench

The owner signs in to the Web workbench and opens **Traffic → Captures**, then
**Create manual capture**. Choose the traffic policy, client type and lifetime,
check the address and the inspected AI domains, and create it. The proxy
username and password are shown only once, after creation or rotation. Rotating
invalidates the old password immediately. Revoking stops new traffic but does
not delete existing records. Regular team members cannot create these logins.

Native Web on the same computer shows a local loopback proxy address. Remote
Web uses the client-reachable `--access-address` you configured and its HTTPS
certificate identity. It never hands clients `0.0.0.0`, an internal container
address, or a certificate file path on the server.

Download the **Proxy CA** from the delivery page, check the SHA-256 fingerprint
shown there, and install it as the client requires on the device that actually
sends the AI requests. It only validates the inspected AI domains; the outer
proxy connection still has to validate the **Server HTTPS certificate**
separately. With `private_ca_tls`, the same private root also issues the
server leaf certificate. With a public or company certificate, downloading the
Proxy CA does not fix a server certificate error. Remote clients should prefer
HTTPS; HTTP is only for trusted local or private networks.

## Private HTTPS without a public domain

### Option A: a custom name plus the hosts file

Pick a stable, internal-only name such as `vibermate.home.arpa`:

```sh
./vibermated server \
  --listen 0.0.0.0:9666 \
  --access-address vibermate.home.arpa:9666 \
  --transport private_ca_tls
```

On every client, add the server's real address to the hosts file, for example:

```text
192.168.1.20  vibermate.home.arpa
```

On Unix/macOS the file is `/etc/hosts`; on Windows it is
`C:\Windows\System32\drivers\etc\hosts`. Browsers and the CLI both use
`https://vibermate.home.arpa:9666`. Do not switch back to the IP, or the name
check fails.

### Option B: use the IP directly

If the IP is stable, you do not need a hosts file:

```sh
./vibermated server \
  --listen 0.0.0.0:9666 \
  --access-address 192.168.1.20:9666 \
  --transport private_ca_tls
```

The certificate includes an IP SAN, and clients use `https://192.168.1.20:9666`.
Do not treat a DHCP address as a stable identity. If the address changes,
update `--access-address` and restart. ViberMate reissues the leaf certificate
from the same CA, so clients that already trust the CA do not need to reinstall
the root.

### Get and trust the CA safely

Start the Server once, then run on the server itself:

```sh
./vibermated server ca-certificate > vibermate-private-ca.crt
openssl x509 -in vibermate-private-ca.crt -noout -fingerprint -sha256
```

The command reads the public CA certificate directly from the server data
directory. It never opens or exports the CA private key. Because it runs on the
server, the fingerprint it prints is your trusted reference. Send the
certificate file and that fingerprint to each managed client over an
independent trusted channel (in person, or a verified chat). On each client,
run the same `openssl` command on the received file and confirm the
fingerprint matches before importing it into the system/browser trust store.
Do not click through a browser warning and then download the CA from that same
untrusted page; that cannot establish safe first trust.

The CLI prefers system-root verification. The first time it connects to a
Server whose certificate comes from a private CA, it scopes that CA to the
exact Server host and port, so normal leaf reissues do not require trusting
again. This does not install the CA in the system, and it does not replace the
Agent's Proxy CA setup. The first connection is still TOFU (trust on first
use). Before logging in or sending a password, it is better to establish trust
with a CA SHA-256 fingerprint you checked over an independent trusted channel
(64 hex characters, no colons):

```sh
vibermate trust --server https://vibermate.home.arpa:9666 --ca-fingerprint <verified-CA-fingerprint>
```

This command only performs a TLS handshake. It sends no login data, and it
checks the host name, validity period and certificate chain. It can also
explicitly switch a leaf-certificate fingerprint already saved for this Server
to CA trust; if it fails, the existing trust is left unchanged. A saved leaf
fingerprint is never upgraded to CA trust automatically during a connection.
If you have installed the CA in this machine's system trust store, you can
choose system-root verification instead:

```sh
vibermate trust --server https://vibermate.home.arpa:9666 --system-roots
```

## Automatic public HTTPS

This mode supports one publicly issuable DNS name, validated with HTTP-01 or
TLS-ALPN-01. It does not support private names, IPs, wildcards or DNS-01.
ViberMate uses CertMagic to manage certificate state. Certificates are stored in
`server-https` inside the data directory and are hot-reloaded after a
successful renewal. The Proxy CA does not change.

The simplest native setup forwards public TCP 443 to the process's listen port
(8443 in this example):

```sh
./vibermated server \
  --listen 0.0.0.0:8443 \
  --access-address runtime.example.com:443 \
  --transport automatic_tls \
  --acme-agree-terms \
  --acme-email admin@example.com \
  --acme-challenge tls_alpn_01
```

Public DNS must already point to this server, and public port 443 must reach
the listen port unchanged. If you listen on 443 directly, have your service
manager grant only the minimal capability to bind low ports. Do not run the
whole Runtime as root.

HTTP-01 works with public port 80 forwarded to an unprivileged internal port:
add `--acme-challenge http_01 --acme-http-port 8080` and forward public port 80
to local port 8080. Without `--acme-challenge`, HTTP-01 is the default, and
without `--acme-http-port` it uses port 80. `--access-address` is always the
HTTPS address users actually open. `--acme-agree-terms` records that you
explicitly accept the issuer's terms; on start, the domain and the optional
contact email are sent to the chosen CA. For a custom ACME directory, use the
expert flag `--acme-ca`.

While a certificate is being requested or renewed, or if issuance fails,
**Settings → Safety & data → Server connection** shows the real state and
error. The first TLS-ALPN request is asynchronous: a running process does not
mean the certificate is ready.

## Use an existing certificate

The certificate must cover the exact domain or IP in `--access-address`. If it
does not, the Server refuses to start:

```sh
./vibermated server \
  --listen 0.0.0.0:9666 \
  --access-address runtime.example.com:9666 \
  --transport tls_files \
  --tls-cert /absolute/path/fullchain.pem \
  --tls-key /absolute/path/privkey.pem
```

The certificate and key must be regular files (not symlinks), and the private
key must be readable only by the user running the Server (`0600`). ViberMate
does not copy or rewrite these files. After replacing them, restart the
Server. If you put an external entry point such as Caddy or Nginx in front, a
plain HTTP reverse proxy may not support authenticated CONNECT on the same
port. Verify layer-4 passthrough; checking that the Web page opens is not
enough.

## Limit who can connect (IP allowlist)

On a public address anyone can reach the sign-in page. To accept connections
only from networks you trust, sign in as the Owner, open **Settings → Access &
launch → IP allowlist**, and list them one per line: a single address
(`203.0.113.7`) or a CIDR network (`203.0.113.0/24`, `2001:db8::/32`). Saving
takes effect at once for the Web workbench, sign-in and Agent traffic, and
disconnects connections that are no longer allowed.

- An empty list allows every address.
- The Server machine itself (`127.0.0.1`, `::1`) is always allowed.
- The panel shows the address it sees for your browser and refuses a list that
  would disconnect you; **Add my address** fills it in.
- Refused clients are disconnected before TLS or HTTP. The panel counts them
  and shows the latest address.
- Automatic HTTPS keeps renewing: certificate validation connections are let
  through without reaching the workbench.

If no allowed network can reach the Server any more, run this on the Server
machine (add `--data-dir` if you started the Server with one). The running
Server applies it within a few seconds:

```sh
./vibermated server ip-allowlist clear
./vibermated server ip-allowlist        # show the current list
```

### Which address the Server sees

The allowlist judges the address of the TCP connection. HTTP headers such as
`X-Forwarded-For` or `X-Real-IP` are never trusted, because any client can send
them.

- **Directly reachable Server** (public IP, port forwarding, Docker on Linux
  with published ports) and **layer-4 (TCP) load balancers that keep the client
  address**: the Server sees each client's real address. Nothing to configure.
- **Layer-4 load balancers that replace the client address with their own**:
  turn on **PROXY protocol v2** on the load balancer, then start the Server with
  the load balancer's own addresses:

  ```sh
  ./vibermated server ... --trusted-proxies 10.0.0.0/24
  ```

  Only connections from those addresses may name a client, in the PROXY header
  the load balancer writes. A connection from them without a header is judged
  by the load balancer's own address, so a misconfiguration never widens
  access; the panel reports such connections. List only the load balancer's
  subnet, never client networks (`0.0.0.0/0` is refused). TCP health checks
  work unchanged, and PROXY protocol `LOCAL` health checks are allowed. With
  Docker, set `VIBERMATE_TRUSTED_PROXIES` in `.env.public` or `.env.team`.
- **Layer-7 (HTTP) reverse proxies**, such as Nginx, Caddy's HTTP mode or cloud
  application load balancers, are not supported in front of the Server: they
  cannot carry the Agents' CONNECT traffic, and the Server refuses management
  requests that carry forwarded headers.
- **Docker Desktop and rootless Docker** usually hide client addresses: every
  client appears as the Docker gateway. Use the native Server, or Docker on
  Linux, when you need the allowlist.
- **Tunnels on the Server machine**, such as frp, cloudflared, `ssh -R` or
  socat forwarding to `127.0.0.1`, make every client arrive from this machine,
  which is always allowed, so the allowlist cannot tell them apart. The panel
  warns when your own browser arrives this way. Use a tunnel or load balancer
  that sends PROXY protocol, with `--trusted-proxies`, or let clients connect
  directly.

If the panel shows a private address (such as `10.x`, `172.16–31.x` or
`192.168.x`) for your browser while you connect over the internet, the Server
is behind one of the address-hiding setups above.

## Accounts and pages

You can see where data is stored in **Settings → Safety & data**. The App can
pick a new local directory and move the data there safely. Native Web and
container Web use the server's `--data-dir` or persistent volume, never a
directory on the computer running the browser. For steps, backup scope and
read performance, see
[Storage location and read performance](storage-and-read-performance.md)
(in Chinese) and
[Backup and restore](backup-and-restore.md).

- **Access & launch** only explains how to sign in and launch. It never mixes in
  the user list or certificate private keys.
- **User management** creates, resets and disables Runtime users. Upstream
  service accounts are configured elsewhere.
- **Safety & data** shows the server connection certificate, the Proxy CA and
  data retention separately. Blocking errors are never collapsed; expert
  details such as issuer, fingerprint and verification method are in the
  details view.

Everyone signs in to the Web page and the CLI with their own account. Users can
change their own password, and the owner can reset members' passwords. The
recovery key can only be read on the server itself. Rotate it after use, and
never share it with members.

## Diagnostics and checks

Show the three common setups, the server-local commands and the offline data
commands:

```sh
./vibermated server --help
```

Local smoke tests use temporary data and never read existing user data:

```sh
node tool/server/smoke-native.mjs
node tool/docker/smoke-local.mjs
```

Real public ACME issuance still has to be accepted in a staging environment
with DNS and ports you control; unit tests never request certificates from a
public CA. For container commands, data volumes and rollback, see
[Docker deployment](docker.md).
