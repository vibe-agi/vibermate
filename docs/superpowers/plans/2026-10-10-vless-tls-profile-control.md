# VLESS TLS Private Configuration and Owner Control Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development with test-driven-development. Continue the approved parent Task4B; this is a backend configuration deliverable, not a new design approval round.

**Goal:** Let an owner save ordinary TLS VLESS profiles, change their configuration-local trust without re-entering node credentials, and read the saved trust mode without exposing private material.

**Architecture:** Keep node credentials and CA bytes in the existing immutable SecretStore payload owned by `vlessprofile`. Its Interface performs import/update and returns a small safe settings description; the existing owner-control transport serializes that description. Public egress Policy and revision digests stay unchanged. Runtime loading passes the stored security/trust to the already-tested `vlesscore` Module.

**Tech Stack:** Existing Go1.26.8 development toolchain, physical isolated runtime persistence and secret-store fixtures, current typed vlesscore TLS options. No new dependencies, engine download or system trust operation.

**Spec:** `../specs/2026-10-09-vless-udp-functional-design.md`; parent `2026-10-09-vless-udp-functional.md`, Task4. The approved TLS design and exact core types are in `2026-10-10-vless-tls-core.md`.

## Global Constraints

- One product source/local test owner; preserve all existing Go/Flutter WIP and before-copy modified files. No staging/commits, stable-line changes, running App/9666/accounts/system trust/system proxy operations.
- No unconfigured direct fallback. Existing account/flow/global selection still chooses the frozen profile.
- Default TLS trust is the system trust of the Runtime host. Explicit custom CA means only this configuration's roots, retaining hostname verification; do not install certificates into any user/system trust store.
- REALITY uses its independent identity parameters and rejects TLS trust input. Do not expose a skip-verification option rejected by the selected engine.
- UUID, original URI, authentication keys, CA PEM and private payload must not enter public Policy, exported revisions, audit/error text or formatting. Keep public Policy comparable and old revision bytes/digests unchanged.
- Existing sessions retain their original immutable credential reference. A trust edit produces a new private reference/public profile revision; never replace or delete the old secret. Preserve indeterminate-commit orphan retention.
- Use the core's complete CA validator, not a second permissive PEM parser. Both the raw CA limit and SecretStore's existing 64KiB encoded-payload limit apply; reject oversize before writing, without truncation or global cap changes.
- This task ends at verified private save/load and owner control. UI, actual assembled Runtime TLS handshake, native System-positive runner and qualified kernel packaging remain named parent tasks, not claims made by a configuration test.

## Task 1: Immutable TLS import, credential-preserving trust edit and safe settings readback

**Files:**
- Modify `internal/vlessprofile/profile.go`: typed command/interface, private payload fields, supported URI security branch, Import preparation and start mapping only. Preserve reviewed client/Shutdown ownership logic.
- Create `internal/vlessprofile/tls.go`: private trust conversion/load/description helpers; no process ownership or alternate storage abstraction.
- Create `internal/vlessprofile/tls_test.go`: configuration behavior through Manager, persistence fixtures and rejection cases.
- Modify `internal/desktopcontrol/egress_profiles.go`: accept the private TLS input, map closed errors, add safe read handler.
- Modify `internal/desktopcontrol/handler.go`: register the saved-settings GET route.
- Extend `internal/desktopcontrol/vless_profiles_test.go`: existing owner import tests plus TLS save/update/readback/authority/idempotency behavior. Update the old REALITY-only error assertion to the new accurate safe wording.
- Create `internal/productruntime/vless_tls_profile_test.go` for one actual SQL/host-secret save and Runtime reopen test, using the existing `testOptionsWithPaths` helper and temporary paths; no engine/listener required. The desktopcontrol `credentialStoreFixture` is a map and cannot establish disk persistence by itself.
- Create `internal/serverhost/vless_tls_control_test.go` for the actual server owner/member/anonymous settings-route check using existing package-local host/login fixtures; no server routing product change.
- Narrow integration correction in `internal/vlesscore/config.go` and `tls_test.go`: preserve an explicitly supported TLS fingerprint in generated TLS settings. The frozen core exposes Config.Fingerprint but currently serializes it only for REALITY; the importer must not accept and silently discard a TLS node's existing fingerprint. Before-copy these two core files; no parser/lifecycle/framing changes.
- Existing Runtime/host consumer type adjustments only if required by the Interface below; no listener/schema/shared-Policy changes. If a new test helper is needed, keep it local to these tests.

**Interfaces consumed:**

```go
// Already implemented core; do not duplicate its parser.
type TLSOptions struct { Mode TrustMode; CAPEM string }
func (TLSOptions) Validate() error
// vlesscore.Config has Security Security and TLS *TLSOptions.
// Empty Security in stored historical private payloads means REALITY.
// Existing controller provides GetRevision(ctx, id, revision), List(ctx), Publish(ctx, command).
```

**Interfaces produced:**

```go
// Add to existing ImportCommand, preserving its redacted formatters.
TLS *vlesscore.TLSOptions

type Description struct {
    Security  vlesscore.Security  `json:"security"`
    TrustMode vlesscore.TrustMode `json:"trustMode,omitempty"`
}
type Importer interface {
    Import(context.Context, ImportCommand) (egressprofile.ProfileRevision, error)
    Describe(context.Context, egressprofile.ID, egressprofile.Revision) (Description, error)
}
```

The readback intentionally contains only `security` and, for TLS, `trustMode`. REALITY has no trustMode. It contains no host/SNI, URI, UUID, key, CA content, executable or file path. This supplies the editor's real need (which controls and saved mode to show) without changing public Policy or revealing credentials.

Wire format extends existing private POST `/api/v1/egress-profiles/import-vless`:

```json
{
  "id":"profile.node",
  "displayName":"My node",
  "uri":"vless://11111111-2222-4333-8444-555555555555@node.fixture:443?security=tls&encryption=none&type=tcp&sni=tls.fixture.invalid",
  "tls":{"mode":"custom_ca","caPem":"PEM certificate bundle"},
  "resolver":{"kind":"system","transport":"direct"},
  "directDomains":"*.github.com"
}
```

Use a separate strict wire DTO because core TLSOptions deliberately redacts JSON. Optional `tls` omitted with a new TLS URI means System. `{"mode":"system"}` explicitly selects System and disallows nonempty CA; custom_ca requires a valid nonempty PEM bundle. An explicitly supplied unknown/empty mode is invalid. No arbitrary URI query trust overrides, engine JSON, file paths, insecure/pinning settings or unknown fields are allowed.

For credential-preserving edit, `uri` is the empty string, `If-Match` must identify a positive existing profile revision, and the profile must be VLESS. Load the private material via that exact profile's credential ref/revision, never accept a caller-provided reference. Omitted `tls` retains the saved trust; supplied `tls` replaces only TLS trust and must reject a REALITY profile. Keep host, port, UUID, SNI, flow, fingerprint and identity keys exactly unchanged. Preserve caller-supplied displayName/resolver/directDomains as the complete public draft, like the existing import. The final existing Publish CAS remains authoritative if another edit races; do not overwrite the winner or retry against a new revision.

Safe readback is `GET /api/v1/egress-profiles/{egressId}/revisions/{egressRevision}/vless-settings`. Validate ID/revision and no query, use existing authorized owner application routing, return only Description. A missing profile maps to existing not-found, non-VLESS to invalid request, unavailable/corrupt secret to existing egress-unavailable. It reads by exact frozen revision, not current settings. Preserve no-store/error policy. Do not add a new route that bypasses the existing Desktop read-token and server owner-role rules.

- [ ] **Step 1: Preserve baseline and observe behavior RED.** Add a test using the existing ImportCommand and a `security=tls` URI with empty flow; old parser compiles and rejects it. Retain that failure before adding new types. Use `Manager.Import` with isolated stores rather than only testing a parser. Do not label missing methods/compile errors as behavioral RED.

```go
profile, err := manager.Import(ctx, ImportCommand{
    ID: "profile.tls", DisplayName: "TLS fixture", URI: tlsURI,
    Resolver: egressnetwork.ResolverPolicy{Kind: egressnetwork.ResolverSystem, Transport: egressnetwork.ResolverTransportDirect},
})
if err != nil { t.Fatalf("TLS profile rejected: %v", err) }
if profile.Policy.Proxy.Kind != egressnetwork.ProxyVLESS { t.Fatal("not VLESS") }
```

Run the new single test with the pinned development Go and `GOTOOLCHAIN=local GOFLAGS=-mod=readonly GOPROXY=off`; preserve complete output with `set -o pipefail` and the actual handle/exit.

- [ ] **Step 2: Implement the private payload/trust mapping and URI branch.** Add explicit security and private trust fields to the payload, retaining legacy absent-security REALITY reads. For ordinary TLS accept raw/tcp, encryption none, empty flow or the two already-supported Vision flows. Require the existing UUID/host/port safety; when TLS SNI is absent use the canonical node host, while REALITY keeps its existing SNI requirement. TLS rejects REALITY key/short-ID parameters rather than ignoring them; REALITY rejects TLS input. Preserve existing duplicate/unknown query rejection and supported fingerprints. Call `TLSOptions.Validate` on import and secret read, return closed errors without payloads.

```go
// Explicit private JSON fields; core TLSOptions JSON is intentionally redacted.
type storedTLS struct {
    Mode vlesscore.TrustMode `json:"mode"`
    CAPEM string `json:"caPem,omitempty"`
}
// payload additions:
Security vlesscore.Security `json:"security,omitempty"`
TLS *storedTLS `json:"tls,omitempty"`
```

Use one conversion helper for validated stored payload → core Config so startup and tests exercise the same mapping. Do not start a core process merely to validate/save configuration. `start` keeps the existing pinned resolved bootstrap IP and original SNI, passing Security/TLS in addition to the existing fields. Add a behavior test showing nonempty `Fingerprint:"firefox"` is missing from TLS JSON before the narrow correction, then include it without changing absent/empty fingerprint defaults:

```go
if c.Fingerprint != "" {
    tlsSettings["fingerprint"] = c.Fingerprint
}
```

The importer keeps its existing supported fingerprint allowlist; no unsafe fingerprint option is introduced. Clear copied secret bytes/destroy Values using existing patterns; redacted formatters remain required for new secret-bearing values.

- [ ] **Step 3: Implement immutable saved-credential edits and Describe.** Resolve the prior profile with `GetRevision` and load via `ReadAtRevision` before applying trust changes. Validate private data and encoded JSON size before `Replace`; allocate a fresh secret reference and keep the existing Publish CAS/indeterminate retention path. Describe reuses validated loading and returns only the two safe enums. No changes to client/Shutdown state or old referenced secrets.

```go
prior, err := m.profiles.GetRevision(ctx, command.ID, command.ExpectedRevision)
// Reject missing/non-VLESS; load only prior.Policy.Proxy's exact secret revision.
// Apply explicit TLS replacement to copied private payload, then store under a new reference.
return m.profiles.Publish(ctx, egressprofile.PublishCommand{
    ID: command.ID, ExpectedRevision: command.ExpectedRevision,
    DisplayName: command.DisplayName, Policy: newPolicy,
})
```

- [ ] **Step 4: Wire owner-control and behavior tests.** Extend the private POST DTO/strict decoder, clear URI/CA from request structures after use, use existing mutationHeaders/idempotency fingerprint/CAS. Register the GET description route behind the existing application authorization. Update safe error text to explain supported TLS or REALITY without echoing a URI. Keep response EgressProfileResponse unchanged.

Tests must verify: System TLS import; custom CA import/Describe; malformed/partial/mixed/empty CA rejection before any secret/public write; encoded size over64KiB rejection even when raw CA is within its bound; REALITY+TLS input rejection; TLS+REALITY query rejection; unknown/duplicate query rejection; normal TLS empty flow preserved; legacy REALITY remains readable; nil trust with empty URI preserves credential and trust; explicit System clears only new revision CA; changing CA retains node identity; old private bytes and profile digest unchanged; stale CAS conflict cannot overwrite winner; same idempotency key replays same result; changed body with same key conflicts; read-only token cannot POST; no-auth/member cannot reach owner settings; public responses/errors and fmt/JSON do not leak URI/UUID/key/CA. Use actual temporary SQL/host-secret storage and reopen it for persistence evidence; the existing memory credential fixture is sufficient for HTTP authority/idempotency tests but not disk persistence proof.

- [ ] **Step 5: Targeted GREEN and freeze.** Run the new TLS profile tests, existing `vlessprofile` lifecycle tests and owner-control VLESS tests once on the final amended code. Run the new fingerprint mapping test and relevant existing TLS JSON/REALITY unit tests for that narrow correction; no new engine handshake claim is made for alternate fingerprints. Other core bytes stay unchanged; their already-passing TCP/UDP fixture need not rerun. Retain command/output, source hashes and a before-based task-only diff including newly created files. No broad full-repo/race/soak/native matrix in this unit. Write report with actual coverage and limitations; release the source/test slot for independent spec/security/quality review. No staging/commit until root's owned integration checkpoint.

## Self-review and next consumer

| Producer → consumer | Check |
| --- | --- |
| core TLSOptions → importer | one strict validator; core JSON redaction does not lose persisted CA because private DTO is explicit |
| saved profile revision → trust edit | exact secret revision, new reference, final CAS; no supplied arbitrary reference |
| Describe → later editor | only security/trustMode; errors do not silently show System |
| stored payload → Runtime core startup | same conversion path, legacy REALITY and empty TLS flow preserved |
| owner transport → backend | strict wire DTO, existing headers/authority, same safe public response |

Next parent unit adds System/custom-CA controls to the reviewed Flutter editor using these exact wire fields and Describe route, including changing CA without re-entering the link. Host/bootstrap/launcher and the patched managed kernel then supply the assembled user-path proof. This backend plan does not defer any of its own requirements to those units.
