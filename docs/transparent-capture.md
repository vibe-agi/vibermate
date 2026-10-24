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
| Flow broker | `vibermated` (macOS) | The extension's local socket: an ownership query and flow hand-off to the owning run's ingress socket. | Yes, with a fake extension client |
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
- That the sandboxed extension can reach `vibermated`'s socket through the
  app group container.
- Whether `NEAppProxyFlow.remoteHostname` is populated for Node (Claude
  Code) and Rust (Codex) connections; SNI covers it if not.
- Two concurrent `vibermate run` sessions, each with a child `curl`,
  attributed to their own runs. An unrelated Terminal `curl` is untouched.
