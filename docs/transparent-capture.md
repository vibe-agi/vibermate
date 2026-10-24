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
  `{"v":1,"run":"<id>"}` followed by a newline.
  - The broker takes the root from the socket's peer credentials, never from
    the request. A process can therefore only claim its own descendants.
  - The reply is `{"ok":true,"extension":<bool>}`. `extension` says whether
    the extension sent a hello in the last 30 seconds; the run refuses to
    start when it is false.
  - The claim lasts as long as this connection. Owned flows arrive on it as
    passed descriptors (`SCM_RIGHTS`, one `F` byte each).
- **Extension port.** The system extension runs as root inside a sandbox and
  cannot reach a user-owned Unix socket. It therefore uses loopback TCP:
  `vibermated` listens on `127.0.0.1:<port>` with a per-start secret. Both
  reach only the native shell, as `flowPort` and `flowSecret` in the
  bootstrap descriptor. The app writes them into the transparent proxy's
  `providerConfiguration`. Every request is framed as a 2-byte big-endian
  length followed by JSON:
  - Hello, sent at start and at least every 10 s:
    `{"v":1,"secret":"…","hello":true}`. The answer is the byte `H`.
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
  - Bound the query at about 200 ms and decline on timeout. Unlike Linux
    namespaces, process lineage leaves localhost untouched.
- **Outage.** If `vibermated` quits, registrations drop and each run pauses
  its Agent, as described in ADR 0024.

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

## Verify on a Mac before release

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
