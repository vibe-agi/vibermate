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
- **Linux.** `vibermate run` starts the Agent in an unprivileged user,
  network and mount namespace whose default route is a TUN device. Every
  descendant inherits the namespace, so attribution is structural and no
  broker is involved. A gVisor netstack in the run process turns packets
  into flows. Its DNS responder, named by a generated `resolv.conf`
  bind-mounted over the system one, answers A queries with fake IPs from
  198.18.0.0/15 that map back to host names, so the Runtime resolves real
  names through its own DoH. AAAA queries get an empty answer, so clients
  use IPv4. Resolution through the systemd-resolved socket still returns
  real addresses; SNI/Host sniffing names those flows.

  The namespace is created with the caller's uid mapped to itself (files
  keep their owners) and only `CAP_NET_ADMIN` and `CAP_SYS_ADMIN` inside
  it, which are cleared before the Agent runs. Failing closed is
  structural: if the launcher exits, the TUN device closes and the
  namespace has no route out, so no pausing or token is needed. Where
  unprivileged user namespaces are disabled, the run refuses to start and
  says so. Loopback is separate inside the namespace: OAuth callbacks from
  a browser and IDE connections into the Agent do not work in the first
  version, so `--managed-auth` is the supported login with Linux
  transparent capture.

The Flow Ingress names the destination from, in order:

1. the host name the platform reports (macOS `remoteHostname`, Linux fake
   IP);
2. otherwise the TLS SNI or HTTP `Host` sniffed from the first client bytes;
3. otherwise the IP address.

It then sends `CONNECT host:port` with the run's existing proxy credential
through the run's existing relay. Remote and local Runtimes therefore apply
the same Environment, interception, DoH and egress rules as today, and the
server needs no change.

UDP goes through vibermated too, never around it. The run carries each
captured UDP flow to the Runtime as an HTTP `connect-udp` request (RFC 9298)
on the same relay and credential as TCP, so it works for local and remote
Runtimes inside the existing tunnel. The Runtime hands it to its existing
UDP path, which applies the Environment's policy and audit and sends it
through the configured egress, including an upstream SOCKS5 proxy. QUIC is
therefore carried, not refused. DNS is the exception: it is answered by the
capture layer so that names reach the Runtime as names.

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

Each transparent run also generates a token and sets it, as
`VIBERMATE_TRANSPARENT_CAPTURE`, in its Agent's environment only; a token
inherited from an outer run is dropped. Descendants inherit it. When a
parent walk finds no owner, because the process was orphaned or daemonized
before its first connection, the broker first checks the process group: the
Agent leads its own group, a group id is its leader's pid and cannot be
reused while that leader lives, so membership in a live Agent's group names
the run. Failing that, it reads the process's start environment (same user
only) and attributes it to the run whose token it carries. macOS hides the
start environment of its own platform binaries (`curl`, `sh`, `git`), which
is why the group comes first. Lineage still wins, so a nested run's
processes belong to the nested run.

On macOS the extension cannot tell an unowned flow from one it could not
ask about. Declining both keeps the Mac usable when ViberMate is slow or
gone; refusing both would cut off every application. Failed queries
therefore pass through, and the guarantee comes from pausing:

- The extension holds a liveness stream open to the broker and sends a hello
  at least every 10 seconds. When the stream closes (stop, crash, lost
  connection) or no hello arrives for 30 seconds, the broker ends every
  run's claim, even while vibermated keeps running.
- The broker sends each claim a keepalive every second; a run that hears
  nothing for three seconds treats the claim as lost, which covers a hung
  broker.
- When its claim ends for any reason, `vibermate run` at once pauses the
  Agent's process group and every other process carrying its token,
  skipping processes the user had already stopped. It resumes exactly that
  set when the claim is restored with an active extension, and stops the run
  after 30 seconds without one.

Flows already handed to the run continue, because they no longer depend on
the broker. The extension refuses a failed query only for a process it has
already seen owned.

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
- On macOS, a process orphaned before its first connection is attributed
  only if it stayed in the Agent's process group while the Agent lives, or
  kept the run's token in an environment macOS lets the broker read. A
  platform binary that also left the group (`setsid`, a new job) is unowned,
  as is any process started with a cleared environment (`env -i`) outside
  the group.
- One ownership query that fails while the broker and extension are both
  healthy, for a process the extension has never seen, goes direct. The
  extension counts these and Host status reports the count. Removing the
  case would need the extension to hold such flows and relay unowned ones
  itself.
- Pausing cannot reach a process of the run that already exited, and between
  a capture loss and its detection (at most about three seconds for a hung
  broker, 30 seconds for an extension that hangs with its stream open) new
  connections are not captured.
