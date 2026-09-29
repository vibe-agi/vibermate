[English](docker.md) · [简体中文](docker.zh-CN.md)

# Docker deployment

Containers and native processes share the same account and certificate logic.
Pick your setup first. Each template uses its own named data volume, so a test
run cannot accidentally overwrite another Runtime.

| Setup | Configuration | Browser address |
| --- | --- | --- |
| Personal use on this computer | `compose.yaml` + `.env.example` | `http://127.0.0.1:9666` |
| Private network/VPN, no public domain | `compose.private.yaml` + `.env.private` | HTTPS on a custom hosts-file name or an IP |
| Public domain, automatic certificate | `compose.public.yaml` + `.env.public` | `https://your-domain` (443) |
| Existing public or company certificate | `compose.team.yaml` + `.env.team` | HTTPS on the domain/IP the certificate covers |

## Build the current source

```sh
bash tool/docker/build-local.sh
```

The script builds the Server, CLI and Web, and produces the image
`vibermate-runtime:local`. If Flutter is not on `PATH`, set
`VIBERMATE_FLUTTER_BIN` to its absolute path. The templates use
`pull_policy: never`, so an old remote image can never pass itself off as the
current code.

You can run `node tool/docker/smoke-local.mjs` to test the local template in a
separate container and volume. The script picks a free loopback port, checks
Web setup, sign-in, the Proxy CA and recovery after a restart, and then removes
its test resources.

## This computer: the default template

```sh
docker compose --env-file .env.example up -d --wait --wait-timeout 90
```

Open <http://127.0.0.1:9666>. If the port is taken, change `VIBERMATE_PORT` in
`.env.example`. This template always publishes on host loopback and ignores
variables for remote network interfaces. It is not a remote plain-HTTP
deployment. It also passes `127.0.0.1:<VIBERMATE_PORT>` to the Runtime as the
client access address, so a manual capture login created in the Web workbench
never hands users the container's internal `172.x` address.

Read the setup/recovery key:

```sh
docker compose --env-file .env.example exec vibermate \
  /opt/vibermate/vibermated server recovery-key --data-dir /data
```

## Private HTTPS: hosts-file name or IP

```sh
cp .env.private.example .env.private
# Edit VIBERMATE_ACCESS_ADDRESS and VIBERMATE_PRIVATE_BIND_ADDRESS
docker compose --env-file .env.private -f compose.private.yaml \
  up -d --wait --wait-timeout 90
```

`VIBERMATE_ACCESS_ADDRESS` is the canonical `host:port` that clients actually
use:

- DNS example: `vibermate.home.arpa:9666`. Add
  `192.168.1.20 vibermate.home.arpa` to the hosts file on every client.
- IP example: `192.168.1.20:9666`. No hosts file needed.

`VIBERMATE_PRIVATE_BIND_ADDRESS` is the host network interface the port is
published on, not a container IP.

ViberMate uses the same private CA to issue a server leaf certificate that
covers that DNS name or IP. Export the public CA certificate safely:

```sh
docker compose --env-file .env.private -f compose.private.yaml exec -T vibermate \
  /opt/vibermate/vibermated server ca-certificate --data-dir /data \
  > vibermate-private-ca.crt
openssl x509 -in vibermate-private-ca.crt -noout -fingerprint -sha256
```

The command reads the CA certificate straight from the data volume on the
server and never exports the private key, so the fingerprint it prints is your
trusted reference. Send the certificate and fingerprint to managed clients over
a trusted channel, and have each client check that the fingerprint matches
before installing it. Never download the CA back from an untrusted Web page.
Changing the access name or IP and restarting reissues the leaf certificate
under the same CA; clients' trust in the CA is not affected. This private CA
also authorizes this Runtime's AI traffic inspection. Do not install it on
unmanaged devices.

## Public domain: automatic HTTPS

```sh
cp .env.public.example .env.public
# Edit the public domain, contact email and any bind address you need
docker compose --env-file .env.public -f compose.public.yaml \
  up -d --wait --wait-timeout 120
```

First make the domain resolve to the server, and make public TCP 443 reach port
443 on the host. The template forwards host port 443 to the container's
unprivileged port 9666 and uses TLS-ALPN-01. The container needs no root,
`NET_BIND_SERVICE` or host network. The template's `--acme-agree-terms` records
that you explicitly accept the issuer's terms. Certificates and ACME state are
stored in the `vibermate-public-data` volume and hot-reloaded after renewal.

The first TLS-ALPN request is asynchronous. A running container does not mean
the certificate has been issued; check the logs and **Settings → Safety &
data → Server connection** in the Web page. Private DNS names, IPs, wildcards
and DNS-01 are not supported by the automatic mode.

## Existing certificate

```sh
cp .env.team.example .env.team
# Edit the access address, interface, and absolute certificate and key paths
docker compose --env-file .env.team -f compose.team.yaml \
  up -d --wait --wait-timeout 90
```

The certificate must cover the domain or IP in `VIBERMATE_TEAM_ACCESS_ADDRESS`.
Both PEM files are mounted read-only and must be regular files (not symlinks).
The private key must be `0600` and readable by container UID 10001. The template
never creates missing paths or writes the private key. After replacing the
certificate, run the same command with `--force-recreate` to recreate the
container; the data volume is kept.

## Sign-in and managed runs

Complete owner setup at the address the browser actually uses, then create
members. Clients use the same address, with the port written out:

```sh
vibermate login --server https://runtime.example.com:443
vibermate doctor --server https://runtime.example.com:443
vibermate run --server https://runtime.example.com:443 -- codex
```

In private-CA mode, install the CA first (see above). Public and company
certificates are verified directly by the system roots. If the CLI already
saved a leaf-certificate fingerprint for this Server, you can switch it to
system-root verification explicitly, once system verification works:

```sh
vibermate trust --server https://runtime.example.com:443 --system-roots
```

## Data, security and rollback

- `/data` holds the database, users, upstream keys, the Proxy CA, the server
  identity and automatic certificate state. Back up the whole volume and store
  the backup encrypted. Run only one Runtime per volume. See
  [Backup and restore](backup-and-restore.md) for how.
- The root filesystem is read-only, the container runs as UID/GID
  `10001:10001`, the Docker socket is not mounted, and privileged mode is not
  needed. The health check only proves the local entry point is alive. It does
  not prove browser trust or that upstream models are reachable.
- `docker compose down` keeps volumes. Do not use `down -v` unless you really
  mean to delete the identity, accounts and evidence for good.
- For stopping, logs, the recovery key and rollback, always pass the same
  `--env-file` and `-f` you started with.
- To limit which networks can connect, use the IP allowlist described in the
  [deployment guide](deployment.md#limit-who-can-connect-ip-allowlist). Behind a
  layer-4 load balancer that hides client addresses, set
  `VIBERMATE_TRUSTED_PROXIES` to the load balancer's own addresses.
- An external HTTP reverse proxy may break CONNECT on the same port. If you need
  a gateway, verify layer-4 passthrough; testing that the Web page opens is not
  enough.

Render and check the four configurations only:

```sh
node --test tool/docker/compose.test.mjs
```

For the full native commands, certificate boundaries and first trust, see
[Deployment and HTTPS](deployment.md).
