# Transparent capture

This is the implementation design for
[ADR 0024](adr/0024-capture-agent-traffic-transparently.md). It replaces
proxy environment variables on the Agent → ViberMate leg. Several Agents run
concurrently and their child processes are captured without configuration.

```text
Agent + children ──(macOS: transparent proxy extension)──┐
                 ──(Linux: per-run netns → TUN → netstack)┤
                                                          ▼
                      vibermate run: Flow Ingress (per Capture Run)
                         names destination (platform host name → SNI/Host → IP)
                         CONNECT host:port + run proxy credential
                                                          ▼
                      existing local proxy frontier → relay → Runtime
                      (local vibermated, or remote over the server stream)
```

## Components

| Component | Where | Owns | Testable here |
|---|---|---|---|
| `internal/flowingress` | `vibermate run` | Accepting one flow plus its header, naming the destination, sending CONNECT with the run credential through the frontier, splicing bytes. | Yes |
| `internal/processlineage` | `vibermated` (macOS) | Process identity (pid + start time), parent walk, run-root registry, remembered descendant attribution. | Linux implementation, plus a darwin build/vet check |
| Flow broker | `vibermated` (macOS) | The registration socket for runs and a loopback TCP port for the extension: ownership queries, extension liveness, flow hand-off to the owning run by descriptor passing. | Yes, with a fake extension client |
| Network Extension | `ViberMate.app` (Swift) | `NETransparentProxyProvider`: claims outbound TCP, asks the broker, declines unowned flows, copies the bytes of owned ones. | Compiled in CI; acceptance on a Mac |
| `internal/netnscapture` | `vibermate run` (Linux) | Re-exec into a user+net(+mount) namespace, TUN creation, fd hand-back, `resolv.conf` bind mount, gVisor netstack, fake-IP DNS, UDP/443 refusal. | Yes (unprivileged userns works in the dev container) |

## Flow header

The extension or netstack sends one header before any client bytes:

```json
{"v":1,"proto":"tcp","dst":"203.0.113.7:443","host":"api.anthropic.com"}
```

- The header is a 2-byte big-endian length followed by JSON, at most 1 KiB.
- `host` is optional and is a platform-reported name, never a guess.
- Ownership is never part of the header. The receiving ingress belongs to
  exactly one run, and the broker chose it. A flow cannot name its own run.

## Rendezvous (macOS)

- **Registration.** `vibermated` listens on `runtime/flows.sock` (mode 0600)
  in the Desktop app cache. `vibermate run --transparent` connects and sends
  `{"v":1,"run":"<id>","token":"<token>"}` followed by a newline. The token
  is the value the run set for `VIBERMATE_TRANSPARENT_CAPTURE` in its
  Agent's environment (32–128 URL-safe characters).
  - The broker takes the root from the socket's peer credentials, never from
    the request. A process can therefore only claim its own descendants.
    Registering again from the same process replaces its previous claim.
  - Ownership: a parent walk to the registered root first; if it finds no
    owner, the token in the process's start environment
    (`/proc/<pid>/environ`, macOS `kern.procargs2`; same user only).
  - The reply is `{"ok":true,"extension":<bool>}`. `extension` says whether
    the extension is active (a liveness stream is open, or a hello arrived
    in the last 30 seconds); the run refuses to start when it is false.
  - The claim lasts as long as this connection. Owned flows arrive on it as
    passed descriptors (`SCM_RIGHTS`, one `F` byte each). The broker also
    sends a `K` byte every second; three seconds without any byte ends the
    claim on the run's side.
- **Extension port.** The system extension runs as root inside a sandbox and
  cannot reach a user-owned Unix socket. It therefore uses loopback TCP:
  `vibermated` listens on `127.0.0.1:<port>` with a per-start secret. Both
  reach only the native shell, as `flowPort` and `flowSecret` in the
  bootstrap descriptor. The app passes them as ephemeral
  `startVPNTunnel(options:)` arguments. They are not written to the saved
  `providerConfiguration`, logs, or UI state. Every request is framed as a 2-byte big-endian
  length followed by JSON:
  - Liveness stream, opened at start once capture rules are installed:
    `{"v":1,"secret":"…","hello":true,"stream":true,"queryFailures":<n>}`.
    The answer is the byte `H`; the connection then stays open and silent
    until the extension stops. The extension reopens it if it closes.
  - Hello, at least every 10 s: `{"v":1,"secret":"…","hello":true,
    "queryFailures":<n>}`. The answer is `H`. `queryFailures` (optional) is
    the extension's running count of ownership queries it could not
    complete; Host status reports it.
  - Flow query: `{"v":1,"pid":<source pid>,"secret":"…"}`. The answer is
    `Y` (owned: the extension then sends the flow header and copies bytes
    both ways on the same connection) or `N` (unowned: `handleNewFlow`
    returns `false`).
  - A wrong secret closes the connection without an answer.
- **Extension rules.**
  - Claim outbound TCP only.
  - Exclude `127.0.0.0/8`, `::1` and the broker port, so the extension's own
    connection to the broker and Agents' localhost traffic (dev servers, IDE
    sockets, OAuth callbacks) stay direct.
  - Bound the query at about 200 ms. On failure, refuse a process already
    seen owned and decline any other, counting the failure. Unlike Linux
    namespaces, process lineage leaves localhost untouched.
- **Outage.** The broker ends every claim when the extension's stream closes
  or its hellos go stale; a run ends its claim when keepalives stop or
  `vibermated` quits. Either way the run pauses its process group and every
  process carrying its token, as described in ADR 0024.

## Destination naming

1. Use `host` when present.
2. Otherwise peek up to 16 KiB or 2 s:
   - a TLS ClientHello gives SNI;
   - an HTTP/1 request line plus `Host` gives the host.
3. Otherwise use the IP.

Peeked bytes are replayed into the CONNECT tunnel, so nothing is lost.

## Attribution (macOS)

- `vibermate run` registers `{root pid, root start time, ingress socket}`
  with the local `vibermated` before it starts the Agent, and deregisters
  when it exits. The registration uses the existing authenticated local
  control API.
- The extension sends `owner(pid, pidversion)` from the flow's audit token.
  `vibermated` walks parents until it reaches a registered root, pid 1, or a
  depth of 64.
  - Every intermediate `(pid, start time)` it visits is cached against the
    run, so later reparenting keeps the answer.
  - A pid whose start time no longer matches is a different process, and its
    cache entry is dropped.
- The broker answers within the extension's flow callback. Unowned flows
  get `false`, so the operating system handles them unchanged. ViberMate's
  own processes are never registered roots.

## Linux namespace launch

`vibermate run` re-executes itself with
`CLONE_NEWUSER|CLONE_NEWNET|CLONE_NEWNS` and maps uid→uid and gid→gid (not
to 0). The child:

1. brings up `lo`;
2. creates `tun0` with a default route and a resolver address inside the
   fake-IP range;
3. bind-mounts a generated `resolv.conf` naming that resolver (systemd's
   `127.0.0.53` would otherwise point at the namespace's empty loopback);
4. sends the TUN fd to the parent over an inherited socketpair;
5. drops capabilities and executes the Agent.

The parent runs the netstack next to the existing relay, so ViberMate's own
connections never enter the namespace.

## Build order

1. `flowingress` with SNI/Host naming and CONNECT forwarding.
2. `processlineage`, with Linux and darwin process readers.
3. The flow broker plus `vibermate run` registration.
4. The Swift extension, compiled in CI, with a manual Mac acceptance
   checklist.
5. Linux `netnscapture`.

## Linux end-to-end harness

`tool/transparent-linux-e2e.sh` exercises the shared Go path end to end on
Linux: the real `vibermated` (Desktop daemon) and `vibermate run
--transparent`, a capture Runtime, and real outbound HTTPS. A root emulator
(`cmd/vibermate-transparent-emulator`) stands in for the macOS extension.
nftables redirects only a dedicated test user's outbound IPv4 TCP to it, and
it speaks the same broker protocol: liveness stream, hellos, ownership
queries, flow header and relay. An unowned flow it dials directly, as a
declined flow would go.

The binaries are built with the `vibermate_transparent_emulation` tag, which
enables the broker and `--transparent` on Linux. Release builds never carry
it and still refuse transparent capture off macOS.

The harness checks:

- refusal without an active extension, and a clean start once it is active;
- pausing when the extension goes away, resuming when it returns, and the run
  failing after the 30-second window;
- an unrelated process going direct;
- the Runtime recording an Agent's connection under its own Capture Run,
  including for two concurrent runs;
- a child orphaned before its first connection being captured by token.

It needs passwordless `sudo`, nftables and outbound HTTPS, and is never part
of `make check`. It does not exercise the Swift provider, the macOS process
readers, or Linux transparent capture itself, which will use a per-run
network namespace.

## Verify on a Mac before release

The native target is a **System Extension**, embedded in
`Contents/Library/SystemExtensions/io.vibermate.desktop.transparent-proxy.systemextension`.
Its `NetworkExtension.NEProviderClasses` entry selects
`TransparentProxy.TransparentProxyProvider`; its executable calls
`NEProvider.startSystemExtensionMode()` before entering the dispatch loop.

In the Desktop App, open **Settings → Access & launch → Transparent capture**.
Enabling it activates the extension and requests network-configuration approval.
Approve ViberMate in **System Settings → General → Login Items & Extensions**
when requested. The App must be signed and installed in `/Applications` for
normal activation. Ad-hoc source builds compile the target but cannot activate it.

The first implementation captures outbound TCP on macOS. Localhost stays direct,
and the Agent still uses system DNS before opening a connection; SNI/HTTP Host
recovery does not replace a failed local DNS lookup. Linux namespace capture is
separate future work. Orphan attribution by token, capture-loss pausing and
the remaining failed-query residual are described in ADR 0024; real-hardware
acceptance of them is still required before release.

For a local signed candidate, supply `APPLE_SIGNING_IDENTITY`,
`VIBERMATE_APPLE_TEAM_ID`, `VIBERMATE_MACOS_APP_PROVISION_PROFILE`, and
`VIBERMATE_MACOS_EXTENSION_PROVISION_PROFILE` to
`ui/flutter_app/tool/build_macos_app.sh candidate`. The profile inputs are paths
to Developer ID profiles for the app and extension; the signer checks their
certificate, team, bundle and capabilities before embedding them. This does not
install, activate, notarize, or publish the candidate.

GitHub's `macos-signing` environment provides the profiles through
`APPLE_DEVELOPER_ID_APP_PROVISION_PROFILE_BASE64` and
`APPLE_DEVELOPER_ID_TRANSPARENT_PROXY_PROVISION_PROFILE_BASE64`. The existing
certificate/password and `macos-notarization` secrets are unchanged. The unsigned
build job receives no Apple signing credentials. Profile files are materialized
only in the protected signing job and removed with its temporary credentials.

- The `app-proxy-provider-systemextension` entitlement in the Developer ID
  provisioning profile, and both user approvals: system extension and
  network configuration.
- `com.apple.security.network.client` in the sandboxed extension, for the
  loopback TCP connection to the broker.
- `NEAppProxyFlow.remoteHostname` is expected to be empty for Node (Claude
  Code) and Rust (Codex), which connect by IP after `getaddrinfo`. Do not
  send `host` unless it is present; SNI names those flows.
- Two concurrent `vibermate run` sessions, each with a child `curl`,
  attributed to their own runs. An unrelated Terminal `curl` is untouched.
