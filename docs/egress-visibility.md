# What outbound evidence can prove

ViberMate observes traffic that actually uses its proxy. It cannot infer a local
file path from a network request or prove that an Agent made no other, direct
connections.

| Path | Evidence ViberMate can keep | Content limit |
| --- | --- | --- |
| Supported HTTP request at an exact, inspected client endpoint | Capture, destination, method, transfer bytes, recording state, and a SHA-256 digest of the HTTP Body bytes received by the proxy | `Full` may retain request/response bodies for the configured period; `Metadata only` retains no bodies; `Off` does not retain Raw HTTP records. |
| Uninspected CONNECT or generic cleartext forwarding | Connection, destination, decision/rule, and transfer byte counts | No request Body, Body digest, decoded upload, or local path. `blind` means **not inspected**, not necessarily encrypted. |
| Traffic bypassing the proxy | None | Absence of a record is not evidence of absence of traffic. |

New traffic policies default to **Full** recording. The policy editor shows its
retention period and lets the owner choose `Metadata only` or `Off`. The
outbound-audit view does not create a second copy of retained bodies. Raw
evidence access is owner-only on Server Web; recognized credential Header
values are removed before retention.

A Body digest covers the bytes received by ViberMate after HTTP transfer
framing, not a file on disk. JSON or multipart packaging, compression, base64,
archives, and client-side ciphers change the representation. Comparing a
file's SHA-256 with a request Body is meaningful only if the exact same bytes
were sent; even a match does not establish which local path supplied them.
Full-body, observed-prefix, and unavailable digests are labeled separately.

Connection rules can deny or ask before a request reaches an unknown
destination. They cannot determine whether content sent to an allowed endpoint
was intended by the user. Reliable process/file-path attribution would require
separately authorized observation on the Client Device; a remote Server alone
cannot provide it. ViberMate does not claim generic DLP or coverage of
application-encrypted payloads.

On macOS, Endpoint Security is one possible source of local process/file
events, but its [client entitlement must be requested from
Apple](https://developer.apple.com/documentation/BundleResources/Entitlements/com.apple.developer.endpoint-security.client).
ViberMate does not currently collect those events.
