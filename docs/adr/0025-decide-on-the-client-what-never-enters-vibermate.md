# Decide on the client what never enters ViberMate

Some of an Agent's destinations exist only from the user's machine:
`localhost`, the LAN, intranet names, hosts behind the user's own VPN. When
`vibermated` runs on the same Mac that is invisible, because the Runtime's
direct egress *is* that machine's network. When the Runtime is a server,
reached with `--server`, the same traffic leaves from the server, where those
destinations do not exist or mean something else. Today the only exclusions
are loopback on macOS and, in proxy-variable mode, whatever `NO_PROXY` the
user happens to have.

Two decisions had been folded into one place, and they have different
owners:

- **Egress** is where `vibermated` sends what it carries: direct, an upstream
  SOCKS5 proxy, an imported profile. It is a property of the machine that
  runs the Runtime and stays configured there (ADR 0024 and the egress
  profiles are unchanged).
- **Capture scope** is which of an Agent's destinations never enter
  `vibermated` and leave directly from the user's machine. Only the client
  can reach them, so only the client can carry them out.

## Decision

### Where it is configured

Capture scope is part of the **Environment**, next to its `PolicySet`, as an
optional `captureScope`. It is edited where Environments are edited (the
Desktop App, and the same Flutter UI served as the web console by a server
Runtime) and published with the Environment revision. Each Capture Run
receives the scope of the revision it launched with, inside the launch grant
the CLI already fetches before it starts the Agent, and keeps it for the
run's lifetime: the scope is part of the revision's digest and therefore of
the run's launch authority.

A bypass removes traffic from policy, approvals and recording, so it is an
administrative decision, like the protected authorities the grant already
carries. Per-Environment scope lets one server give each team its own
intranet. No schema change is needed: an absent `captureScope` is canonical
for the default, exactly as an absent `policySet` is, so stored revisions
keep their digests.

### What a rule can say

A rule names one of:

- an exact host (`git.corp.example`);
- a domain suffix (`*.corp.example`), matching the domain and every name
  below it, with at least two labels;
- an IP prefix (`10.0.0.0/8`, `fd00::/8`) inside reserved space only:
  loopback, RFC 1918, carrier-grade NAT (`100.64.0.0/10`, which Tailscale
  uses), link-local, and IPv6 unique-local and link-local;

and optionally one port.

Connection rules deliberately match exact hosts only (design 06: a wildcard is
how an allow list quietly becomes allow-everything). Capture scope differs in
kind: it is an exclusion from capture, not a permission, and it is useless
without suffixes and prefixes, because intranets are named and numbered by
range. The bounds keep it from becoming capture-nothing: no bare wildcard, no
single-label suffix, no public address ranges, at most 256 rules. Public
ranges stay a later, explicit decision.

A rule can never cover a **protected or managed authority** (the model and
credential endpoints the Environment serves): publishing refuses such a rule,
and the client checks again whenever it decides by name. Decisions made by IP
alone cannot see names; the reserved-space restriction is what keeps those
from reaching a model endpoint.

### Built in, always

- Loopback (`localhost`, `127.0.0.0/8`, `::1`) stays local. On Linux the
  Agent's loopback is its own namespace's; reaching host services on loopback
  is a separate decision (see below).
- Link-local addresses stay local.
- On macOS, DNS (port 53, UDP and TCP) stays on the system path. The
  extension sees the system resolver's queries as the Agent's, and through a
  server Runtime they would go to a resolver address that exists only on the
  Mac. Names still reach the Runtime from TLS SNI and HTTP Host, and the
  Runtime resolves them itself. On Linux, DNS is already answered inside the
  namespace (ADR 0024).

### The user's own `NO_PROXY`

An Environment may set `allowUserAdditions`. When it does, the entries in the
user's `NO_PROXY` that obey the same bounds are added to the scope; when it
does not, they are ignored in every mode. The default is off. This changes
proxy-variable mode, which has honored `NO_PROXY` so far, in favor of one
rule everywhere and of failing closed: a user who needs an exclusion asks for
it in the Environment, where it is visible.

### How each mode carries it out

One matcher, in `internal/capturescope`, decides for every client path from
the destination's name (when known), address and port.

- **Proxy variables.** `NO_PROXY` is generated from the built-in set, the
  scope and any allowed additions. Clients interpret `NO_PROXY` differently
  (prefix support in particular), so this mode is best effort; transparent
  capture is exact.
- **Linux namespace.** The run's ingress names each flow as it does now
  (fake DNS, SNI, Host, address). A bypassed TCP flow is dialed directly by
  the launcher, outside the namespace, instead of through `vibermated`; a
  bypassed UDP destination is sent from a launcher socket. The namespace's
  resolver answers names that match a host or suffix rule with real
  addresses from the host's resolver, so intranet DNS works, and remembers
  those addresses as bypassed for the time they were valid.
- **macOS.** The run sends its compiled address rules with its registration
  to the broker. The extension adds the destination to each ownership query,
  and the broker answers `N` for a bypassed address or for port 53, so the
  operating system handles the flow as if ViberMate were absent. Name rules
  are decided by the run's ingress once it has named an owned flow; a
  bypassed flow is then dialed directly by `vibermate run`, whose own
  connections are never captured. Owned UDP to a bypassed address is sent
  from the run's socket.

The direct dials are the one place the client opens connections of its own;
they are a named, reviewed exception in the repository's egress check.

### Compatibility

| Peer | Behavior |
|---|---|
| New CLI, Runtime without `captureScope` | Built-in set only |
| Old CLI, new Runtime | Field ignored; everything goes through ViberMate |
| Old extension, new broker (no destination in queries) | Owned flows are relayed; the run's ingress still applies name rules; address-only rules and port 53 fall back to relaying |
| New extension, old broker | The broker ignores the destination it does not know; owned flows are relayed and the run's ingress applies name rules |

Every combination either honors the scope or sends more through ViberMate,
never less.

### Visibility

Each bypassed flow is reported to the Runtime as metadata only (run, name or
address, port, protocol, the rule that matched), as one more action on the
run's control API next to heartbeat and finish, authorized by the run's
capability. It appears with the run's connections as bypassed. No bytes are
counted or recorded. On macOS a flow the broker declines for its address never
reaches the run, so the broker sends a notice down the run's registration
connection and the run reports it; the broker's own `vibermated` may not be
the run's Runtime (`--server`).

## Out of scope

- Reaching services on the host's loopback from inside the Linux namespace,
  and the reverse direction (a browser's OAuth callback reaching the Agent).
  Both need port-specific forwarding across the namespace boundary and get
  their own decision.
- Public address ranges in rules.
- Choosing an egress per destination on the server; that is egress policy.

## Delivery

1. This ADR; `internal/capturescope` (rules, bounds, matcher, `NO_PROXY`
   rendering); `Environment.captureScope` with publish validation; the grant
   field; generated `NO_PROXY` in proxy-variable mode; port 53 on the macOS
   system path.
2. Linux: direct dials for bypassed TCP and UDP in the run's ingress,
   real-address resolution for bypassed names, and end-to-end checks
   including `--transparent --server`.
3. macOS: scope in run registration, destination in the ownership query,
   name decisions in the ingress; tests on hardware.
4. The Environment editor (App and web) and bypass reporting.
