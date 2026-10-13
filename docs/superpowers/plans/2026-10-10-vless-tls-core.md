# VLESS TLS and Configuration-Local CA Implementation Plan

> **For agentic workers:** Use superpowers:subagent-driven-development and test-driven-development. This is the first backend unit of parent Task4B; preserve the existing REALITY/UDP work and do not claim the later owner editor or packaged delivery is complete.

**Goal:** Make the existing managed-core Module carry ordinary VLESS/TLS TCP and datagrams with normal verification or a configuration-local CA bundle.

**Architecture:** Keep TLS trust parsing/config generation inside `vlesscore`, behind its existing Start/DialContext/DialUDP/Close Interface. Add a small typed trust value reusable by the later private profile importer. Do not change shared egress Policy, packet framing, the Runtime drain or system trust.

**Tech Stack:** Existing development Go/Flutter pins and already-owned isolated Xray fixture. The release must later use qualified patched toolchain/dependencies/kernel; this unit does not qualify the old fixture for distribution.

**Spec:** `../specs/2026-10-09-vless-udp-functional-design.md`, parent `2026-10-09-vless-udp-functional.md`, and primary source facts in its SDD `tls-ca-integration-facts.md` (root has read fully).

## Global Constraints

- One product source/local build/test owner, no subagents. Work only production-readiness and before-copy overlapping files. No commits/index, stable-line/Flutter/shared-Policy/schema change, dependency upgrade, real node/user App/9666/accounts/trust/system proxy operation.
- REALITY + Vision and ViberMate-owned UDP ASSOCIATE remain supported. Do not redesign the reviewed private authenticated Unix packet transport or expose a bare unauthenticated core UDP relay.
- No unconfigured direct fallback. TLS failure cannot change trust mode, turn verification off or route through Direct.
- Default TLS trust is the system trust of the Runtime host. Explicit custom CA means only this configuration's roots, retaining hostname verification; do not install certificates into any user/system trust store.
- Current pinned upstream rejects allowInsecure=true after2026-06-01. Do not offer/bypass that gate or use certificate pinning/fingerprint unsafe as an undocumented skip. Skip support is not required by the approved conditional design.
- Invalid CA input must fail in our Module: upstream can log a root-load error and continue with system roots. Preserve original failures and do not declare a missing system-trust/native-platform proof passed.

## Task 1: Verified TLS core transport

**Files:** modify `internal/vlesscore/config.go`; create `internal/vlesscore/tls.go`, `tls_test.go`; extend existing `integration_test.go`/owned fixture only as needed. Existing client/process/SOCKS/datagram ownership should not change.

**Consumes:** existing Config fields and fixed literal Server/original ServerName; existing real owned engine fixture and fixed packet Interface. No general Xray JSON/argv/file path enters the public configuration.

**Produces:**

```go
type Security string
const (
    SecurityREALITY Security = "reality"
    SecurityTLS Security = "tls"
)
type TrustMode string
const (
    TrustSystem TrustMode = "system"
    TrustCustomCA TrustMode = "custom_ca"
)
type TLSOptions struct {
    Mode TrustMode `json:"-"`
    CAPEM string `json:"-"`
}
func (TLSOptions) Validate() error
var ErrInvalidTLS = errors.New("vlesscore: invalid TLS trust configuration")
```

Config gains redacted `Security Security` and `TLS *TLSOptions`. Empty Security remains legacy REALITY for existing callers. Explicit TLS with nil options uses System; a supplied custom_ca with empty/invalid PEM is an error, not System. Config/TLSOptions String/GoString and JSON output must not expose credentials or PEM.

- [ ] Add behavior tests using the existing Config normalization/config generation path; first observe TLS being rejected or emitted as REALITY by the old implementation. A compile failure for the new type alone is not RED. Test the old REALITY path remains unchanged.
- [ ] Implement one strict CA parser in tls.go and reuse it from Validate/config construction. Accept nonempty PEM CERTIFICATE blocks containing parseable X.509 certificates, permit surrounding/inter-block whitespace, reject private keys, other blocks, malformed DER, empty bundles and unparsed non-whitespace. Do not rely on AppendCertsFromPEM's partial-success behavior. Bound the input to64KiB; the later private importer must additionally respect the existing SecretStore64KiB encoded-material limit without truncation or globally enlarging it. Certificate validity/hostname remains enforced during handshake, not converted into a skip.

```go
// Validation shape; every remainder must be consumed as certificates.
rest := bytes.TrimSpace([]byte(options.CAPEM))
if len(options.CAPEM) > 64<<10 || len(rest) == 0 { return ErrInvalidTLS }
for len(rest) != 0 {
    if !bytes.HasPrefix(rest, []byte("-----BEGIN CERTIFICATE-----")) { return ErrInvalidTLS }
    block, next := pem.Decode(rest)
    if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 { return ErrInvalidTLS }
    if _, err := x509.ParseCertificate(block.Bytes); err != nil { return ErrInvalidTLS }
    rest = bytes.TrimSpace(next)
}
```

Use a closed safe error rather than embedding raw PEM, node fields or parser payloads. Keep parsed/canonical trust data local to this Module; no new storage abstraction.

- [ ] Separate only the outbound security branch. REALITY requires its current key/shortID/Vision rules and rejects TLS options. TLS rejects REALITY key/shortID fields, permits ordinary empty flow plus existing Vision variants, and retains the original ServerName. Generate:

```go
options := c.TLS
if options == nil { options = &TLSOptions{Mode: TrustSystem} }
tlsSettings := map[string]any{
    "serverName": c.ServerName,
    "allowInsecure": false,
    "disableSystemRoot": options.Mode == TrustCustomCA,
}
if options.Mode == TrustCustomCA {
    tlsSettings["certificates"] = []any{map[string]any{
        "usage": "verify",
        "certificate": strings.Split(strings.TrimSpace(options.CAPEM), "\n"),
    }}
}
streamSettings := map[string]any{"network": "raw", "security": "tls", "tlsSettings": tlsSettings}
```

Run validation before this configuration generation. System mode emits false/omits custom certificates. Custom mode emits true and verification certificates, consistently on macOS/Linux/Windows. Never generate certificateFile/keyFile, pinning, alternate-name verifier or insecure settings. `encryption:none`, private inbound authentication, packet framing and ownership are unchanged.

- [ ] Add a genuine local TLS/VLESS fixture using fresh synthetic certificates/keys and only owned loopback listeners. Exercise Start→DialContext/write/read and Start→DialUDP/write/read with exact distinct datagrams through the same Module Interface. Self-signed fixture with explicitly supplied correct CA and hostname must work. Wrong CA and wrong hostname must not deliver payload to the owned target; do not count channel creation alone as remote success. Demonstrate that System mode does not trust this uninstalled fixture CA. Re-run the existing REALITY success/auth/Close behavior only where the security switch could affect it, not a new large campaign.
- [ ] Exercise System-mode success only with an already-existing isolated process trust seam or isolated CI runner; do not modify the developer's real trust store or connect a real provider/node. If that native success requires later runner work, record the exact remaining proof rather than weaken verification or pretend it ran. System-mode mapping plus negative fixture evidence is not itself native System-mode success; that remains required before the assembled feature release.
- [ ] Run new unit tests and one explicit real TLS integration invocation with the existing pinned engine, retaining first failure, command, actual bytes, certificate/trust mode, engine hash and cleanup. No broad race/fuzz/soak, cross-platform success inferred from compilation, arbitrary timeout increase, URI/profile/UI expansion or new engine download here.
- [ ] Freeze with task-only before-based diff, source hashes and report. Report primary-source-driven deviation (no skip switch), actual native/System-trust limits, all process/handle exits and subsequent work. No commits or release claim.

## Subsequent parent Task4 work

After this transport unit, extend the private VLESS profile import/store and owner control to TLS and configuration-local CA, preserving immutable references/CAS and existing credentials when only trust changes. Add safe saved-mode description for the editor without exposing URI/UUID/key; keep shared public Policy stable unless a concrete consumer requires a reviewed extension. Then add user-facing CA controls to the existing editor, the managed patched kernel and Host/bootstrap/launcher settings/address, and prove the actual assembled UI/Runtime/TCP/UDP/domain path. These remain explicit implementation/verification requirements, not completed by this core proof.

## Self-review

| Interface | Consistency check |
| --- | --- |
| Existing Config → legacy REALITY tests | empty Security stays REALITY, no lifecycle/packet rework |
| Typed TLSOptions → future importer → engine JSON | one strict parser, no malformed-CA fallback, redacted value |
| System/custom modes → target platforms | custom uses only configured roots; Windows mixed-root difference is not hidden |
| Module tests → later delivery | real TCP/UDP proof; native System-success and patched distribution gaps explicitly remain |

Continue the already selected subagent-driven execution after source-slot handoff; no new user approval round or alternative-architecture study.
