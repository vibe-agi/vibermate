# Capture Agent traffic transparently, per Capture Run

A managed Agent and every process it starts must reach ViberMate without
proxy environment variables. Several Claude Code and Codex instances run at
once, so each connection must still belong to exactly one Capture Run. Proxy
variables meet neither need: child tools ignore or drop them, and some
clients never honor them. The change covers only the leg from the Agent to
ViberMate. Policy, interception, accounts, DNS/DoH and egress stay where they
are.

We capture by process lineage on macOS and by network namespace on Linux.
Both deliver ordinary TCP flows to one per-run **Flow Ingress** inside
`vibermate run`, and the existing CONNECT path carries them onward.

- **macOS.** ViberMate.app ships a Network Extension transparent proxy. For
  each new TCP flow it asks the local `vibermated` which Capture Run owns the
  flow's source process. `vibermated` compares the process lineage (pid plus
  start time, walking parents) with the roots that `vibermate run` processes
  registered. An owned flow is handed to that run's ingress. An unowned flow
  is declined and the operating system handles it as if ViberMate were not
  installed. A descendant's attribution is remembered once seen, so a
  process that is later reparented to launchd stays attributed.
- **Linux.** `vibermate run` starts the Agent in an unprivileged user and
  network namespace whose default route is a TUN device. Every descendant
  inherits the namespace, so attribution is structural. A gVisor netstack in
  the run process turns packets into flows, and an in-namespace DNS responder
  hands out fake IPs that map back to host names.

The Flow Ingress names the destination from, in order:

1. the host name the platform reports (macOS `remoteHostname`, Linux fake
   IP);
2. otherwise the TLS SNI or HTTP `Host` sniffed from the first client bytes;
3. otherwise the IP address.

It then sends `CONNECT host:port` with the run's existing proxy credential
through the run's existing relay. Remote and local Runtimes therefore apply
the same Environment, interception, DoH and egress rules as today, and the
server needs no change. QUIC (UDP 443) is refused so clients fall back to
TCP. DNS is answered locally, and other UDP is not captured in the first
version.

CA trust is unchanged. `vibermate run` still sets `NODE_EXTRA_CA_CERTS`,
`CODEX_CA_CERTIFICATE` and `SSL_CERT_FILE`, which children inherit; only the
proxy variables go away. A process that ignores them is tunneled blind under
the existing transparent policy, exactly as an unknown destination is today.

Capture fails closed and visibly. If the extension is not approved, or the
namespace or TUN cannot be created, `vibermate run` refuses to start the
Agent and names the fix. It never silently falls back to proxy variables.
Proxy-variable launch stays the default; transparent capture is chosen with
`--transparent`. The Desktop daemon treats the rendezvous as optional: if it
cannot start one, the reason appears in Host status and the daemon still
starts. The refusal belongs to the run that asked for transparent capture,
not to every capture.

On macOS the extension cannot tell an unowned flow from one it could not
ask about, so it declines both. A capture outage would therefore let new
Agent connections leave uncaptured. Instead, `vibermate run` watches its
registration:

- When the registration drops (the app quit or restarted), it pauses the
  Agent's process group at once.
- It resumes the Agent when the claim is restored with an active extension.
- It stops the run after 30 seconds without one.

Flows already handed to the run continue, because they no longer depend on
the broker.

Rejected alternatives:

- **A system-wide TUN on macOS with per-packet process lookup.** It needs a
  root helper, sees every application, and attributes by racing socket
  tables.
- **GPL tun2socks or sing-tun.** They would bar the commercial license; gVisor
  netstack (Apache-2.0) and `wireguard-go/tun` (MIT) do the same job
  in-process.
- **Fake-IP DNS on macOS.** Agents resolve through mDNSResponder, so their
  processes emit no DNS flows to rewrite.

Known limits:

- Linux namespaces separate loopback, so localhost callbacks from a browser
  or IDE into the Agent need managed authentication or a later forwarder.
- On macOS, a process that escapes its lineage before its first connection
  is unowned.
