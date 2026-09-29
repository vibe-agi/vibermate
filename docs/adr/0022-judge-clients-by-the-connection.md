# Judge clients by the connection

The Server IP Allowlist decides on each TCP connection to the Runtime Server,
before TLS or HTTP. One port carries the Web workbench, the API, CLI login and
member Agent traffic (HTTP CONNECT tunnels), so a per-request check could not
cover the tunnels, and refusing early spends nothing on a refused client.

The client address is the TCP peer. The one exception is a layer-4 load
balancer the operator lists with `--trusted-proxies`: a connection from it is
judged by the PROXY protocol header that the load balancer writes before any
client byte. A connection from anywhere else cannot name an address, and a
trusted load balancer's connection without a header is judged by the load
balancer's own address, without the loopback exception, so a misconfiguration
fails closed even for a load balancer on the Server machine. HTTP headers such
as `X-Forwarded-For` are never trusted: any client can write them, and the
Server already refuses management requests that carry them. Layer-7 reverse
proxies stay unsupported because they cannot carry the Agents' CONNECT
traffic.

The Owner edits the allowlist in the Web workbench and it takes effect at
once, closing connections it no longer allows. A list that would disconnect
the Owner saving it is refused, loopback is always allowed, and
`vibermated server ip-allowlist clear` on the Server machine is the way back
in. Trusted proxies are startup configuration instead: they describe how the
Server is deployed, must match the load balancer exactly, and switching them
from the workbench could cut off every connection at once, including the
session changing them.

With automatic HTTPS in TLS-ALPN mode, certificate validation arrives on the
same port from addresses no Owner can list. That mode decides during the TLS
handshake and lets a pure `acme-tls/1` handshake through; net/http closes such
a connection without serving a request.

Loopback stays allowed because the container health check and local
administration use it, and it keeps a remote Owner from being locked out of
the machine they run. The cost is that a tunnel on the Server machine that
forwards to loopback (frp, cloudflared, `ssh -R`) makes every client look
local; the workbench says so when the Owner's own browser arrives that way.
