# SOCKS User Delivery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development and test-driven-development. Execute one source/local-test owner at a time, with a scoped independent review after each task. Preserve existing WIP with before-copies; no worker commits or index changes.

**Goal:** Let a person configure an authenticated SOCKS5 client using the actual ViberMate listener and existing ManualCapture credential, then demonstrate TCP and UDP through the packaged REALITY + Vision engine.

**Architecture:** Hosts own listening sockets and supply a small immutable network-access description to authenticated control and the existing grant issuer. A separate optional SOCKS address extends ManualCapture without changing the HTTP endpoint or credentials. Flutter and CLI consume that description; the existing SOCKS/Runtime/VLESS implementations perform traffic and enforcement unchanged.

**Tech Stack:** Existing Go Host/control/grant modules, Flutter strict DTOs and workbench, localized Go CLI, pinned packaged Xray and isolated loopback fixtures.

**Spec:** `../specs/2026-10-09-vless-udp-functional-design.md`, parent Task4 in `2026-10-09-vless-udp-functional.md`. This completes the approved user-entry requirement after kernel packaging and Host bootstrap; it does not reopen protocol/TLS design or request a new approval gate.

## Global Constraints

- One product source/build/test owner. Preserve existing WIP, user data, accounts, trust and running App/9666. Tests use owned temporary data, synthetic identities and loopback services. Handwritten edits use apply_patch. No commits, cleanup, release or signing credentials by workers.
- Use the existing Capture credential: SOCKS username `capture`, password the one-time ManualCapture proxy credential. No second UDP password, owner token, Runtime User password, credentials in status/readback/logs, or authorization bypass.
- Keep `proxyAddress` as HTTP/HTTPS. Add `socksAddress` separately as canonical `host:port` (IPv6 bracketed); it has no scheme, credentials, path or query. Empty/omitted means not enabled on this Host. Never return `:0`, `0.0.0.0` or `::` as a connect destination.
- Desktop launcher already requests `127.0.0.1:0`; Desktop Host remains loopback-only. Standalone Server remains empty-disabled unless `--socks-listen` is explicit. Desktop's attached HTTP Server must not advertise the Desktop-local SOCKS port as a remote socket.
- Host owns network metadata; ProductRuntime never guesses listener addresses. The metadata is immutable after binding and before consumer construction. No racy callback that observes a later bind.
- `vlessAvailable` means a trusted executable was supplied by Host bootstrap, not that a node was contacted or is healthy. Display this distinction. No Runtime download, node-selected executable or fallback to Direct.
- Generic SOCKS UDP uses the global default exit (or built-in Direct), not an invented model/account selection. Existing connection rules, Capture authority, IP allowlist, current credential checks and association lifecycle remain enforced.
- SOCKS CONNECT delegates to the existing HTTP proxy path; eligible HTTPS interception still needs the existing CA. UDP itself does not need that interception CA. The existing remote HTTP/HTTPS launcher relay is not a UDP tunnel and does not encrypt a separate raw SOCKS listener.
- Cover the amended behavior before broad builds. Do not rerun unchanged native kernel producer/package or broad client matrices for this unit. Final clean-source/platform/install/release proof remains required separately.

## Interfaces and files

Task1 produces:

```go
// internal/desktopcontrol: host-owned authenticated projection, not Runtime state.
type NetworkAccess struct {
    SOCKSAddress   string `json:"socksAddress,omitempty"`
    VLESSAvailable bool   `json:"vlessAvailable"`
}
// Options.NetworkAccess *NetworkAccess
// StatusResponse.NetworkAccess *NetworkAccess `json:"networkAccess,omitempty"`
// runtimecontrol.Options.NetworkAccess *desktopcontrol.NetworkAccess

// capturegrant.Options.ManualSOCKSAddress string
// capturegrant.ManualCaptureContext.SOCKSAddress string
// capturecontrol.ManualCaptureContext.SOCKSAddress string `json:"socksAddress,omitempty"`
// capturecontrol.ManualCaptureGrant.SOCKSAddress string `json:"socksAddress,omitempty"`

// serverhost.Options.SOCKSAccessAddress string
// serverhost.AttachOptions.VLESSAvailable bool (trusted composition only)
```

The description is copied into the Handler, not retained as caller-mutable pointer data. Real Hosts supply it even when disabled; absent is reserved for host-neutral/legacy fixtures. Do not add passwords, file paths, process identifiers or node secrets to it.

Task2 consumes these exact JSON names. Dart adds `HostNetworkAccess? RuntimeStatus.networkAccess` and optional `String? socksAddress` on ManualCapture context/grant. Use constructor defaults so unrelated fixtures need not invent unavailable data. CLI uses the existing Go wire fields. No new `run` grant fields or automatic `ALL_PROXY` changes.

## Task 1: Actual Host endpoints and Capture-bound delivery

**Files:** Modify `internal/desktophost/host.go`, `internal/serverhost/host.go`, `internal/serverhost/options.go`, `internal/runtimecontrol/application.go`, `internal/desktopcontrol/handler.go`, `internal/capturegrant/issuer.go`, `internal/capturecontrol/manual.go`, `cmd/vibermated/main.go`. Reuse `internal/serverhost/access_targets.go` and `internal/serverconnection/address.go`; modify them only for a demonstrated missing rule. Extend focused tests in `internal/desktophost/socks_test.go`, `internal/serverhost/socks_test.go`, `internal/capturegrant/manual_capture_test.go`, `internal/capturecontrol/handler_test.go`, `cmd/vibermated/socks_config_test.go`; add focused `internal/desktopcontrol/network_access_test.go` and `internal/serverhost/socks_delivery_test.go` if the existing files would mix unrelated fixtures.

**Consumes:** Actual Host listener, trusted `options.Runtime.VLESSExecutable`, existing `runtimeAccessTargets(listenAddress, advertisedAddress)`, grant context confirmation, unchanged Capture auth. **Produces:** the exact interfaces above and usable actual endpoints in authenticated status/context/create/rotate.

- [ ] **Step 1 — RED on existing HTTP user entry.** Extend the actual Server owner setup/context fixture from `host_integration_test.go` and Desktop SOCKS fixture. Bind `127.0.0.1:0`, authenticate through existing controls, decode the context as a map before adding fields, and demonstrate missing `socksAddress`/`networkAccess`, not merely compile failure.

```go
// After the existing authenticated GET context and JSON decode:
if got, _ := body["socksAddress"].(string); got != host.Status().SOCKSAddress {
    t.Fatalf("manual delivery SOCKS=%q want bound %q", got, host.Status().SOCKSAddress)
}
```

Run only the new named tests with `/tmp/vibermate-go-security.JcRyfL/go/bin/go test ./internal/serverhost ./internal/desktophost -run 'Test.*SOCKS.*Delivery' -count=1`; retain the actual failing assertion and command.

- [ ] **Step 2 — bind once before constructing consumers.** Desktop already binds before issuer/handler; inject the actual string into both. Server currently constructs management/issuer before binding SOCKS. Move the optional SOCKS bind into the private startup path before `runtimecontrol.New`/`capturegrant.New`. Preserve startup rollback, IP allowlist gate, owned goroutine joins and `Host.Status().SOCKSAddress` as the actual bound address. Attach has no SOCKS bind and never acquires a second socket. A failed later HTTP/transport/control startup must close the prebound SOCKS listener; failed SOCKS startup must leave no HTTP listener or Runtime lock behind.

```go
// Compute only after a successful actual bind, before consumer construction.
targets, err := runtimeAccessTargets(socksListener.Addr().String(), socksAccessAddress)
if err != nil { return nil, err }
manualSOCKSAddress := targets[0]
access := &desktopcontrol.NetworkAccess{
    SOCKSAddress: manualSOCKSAddress,
    VLESSAvailable: trustedVLESSExecutable != "",
}
```

Use existing interface ranking for wildcard binds; no browser Host-header inference or second discovery algorithm. Add optional standalone `--socks-access-address host:port` to match explicit advertised deployments. Canonicalize/reject using `serverconnection.ParseAddress`; require the flag to accompany a nonempty SOCKS bind. It does not promise NAT/UDP traversal. This flag is not accepted as a Desktop public listener. Pass VLESS availability to attached Server explicitly from Desktop Host's trusted option, while keeping its SOCKS address empty.

- [ ] **Step 3 — grant and projection invariants.** Validate `ManualSOCKSAddress` at issuer creation: empty allowed; otherwise exact canonical `serverconnection.ParseAddress` equality. For local-listener delivery require a literal loopback address. Add it to the context and the NUL-separated confirmation hash before issuing a grant. Map into create and rotate wire outputs, never ordinary ManualCapture view/list/readback. Copy/validate the optional `NetworkAccess` on Handler construction and include it only behind existing authenticated status. No new unauthenticated discovery route.

```go
// Additional member in manualCaptureConfirmation's ordered value list:
issuer.manualSOCKSAddress,
// Both context and one-time grant carry the endpoint; credentials stay unchanged.
SOCKSAddress: context.SOCKSAddress,
```

- [ ] **Step 4 — targeted functional coverage.** Test disabled/missing kernel, trusted configured kernel without a contacted node, actual ephemeral port, canonical IPv6 and explicit advertised address, invalid wildcard/port0/credential/path strings, and advertised-with-disabled rejection. Test context confirmation changes with only the SOCKS address changed and stale context is rejected; create/rotate share the reviewed endpoint; list/readback/status contain no proxy password. Through the actual Host control grant, authenticate SOCKS using the same returned username/password; anonymous/owner-token use must not become accepted. Preserve existing occupied-listener, shutdown and IP allowlist tests; add the reverse rollback case (HTTP occupied after SOCKS bind), proving the socket and Runtime guard can be reopened.

Run once after focused GREEN:

```sh
/tmp/vibermate-go-security.JcRyfL/go/bin/go test ./internal/capturegrant ./internal/capturecontrol ./internal/desktopcontrol ./internal/runtimecontrol ./internal/desktophost ./internal/serverhost ./cmd/vibermated -count=1
/tmp/vibermate-go-security.JcRyfL/go/bin/go test -race ./internal/desktophost ./internal/serverhost -run 'Test.*SOCKS' -count=1
```

- [ ] **Step 5 — freeze for review.** Preserve before/after hashes, full-context task-only diff, RED/GREEN commands/exit/output and rollback/credential evidence. No existing production WIP is reverted or committed. Release all owned handles and source/test slot. The reviewer checks exact Host→status→grant propagation, context binding and startup lifetime, not a new protocol audit.

## Task 2: Understandable Flutter and CLI connection instructions

**Files:** Modify `ui/flutter_app/lib/core/api/control_models.dart`, `control_api.dart`, `lib/features/workbench/captures_view.dart`, `settings_view.dart`, `lib/core/i18n/app_copy.dart`; `workbench_controller.dart` only if an actual consumer needs an update. Modify `internal/manualcaptureclient/client.go`, `cmd/vibermate/capture.go`, `locales/en-US.json`, `locales/zh-CN.json`. Tests: `ui/flutter_app/test/control_models_test.dart`, `control_api_http_test.dart`, existing manual tests in `workbench_behavior_test.dart`, new `socks_delivery_test.dart` for focused render/copy/disabled coverage; `internal/manualcaptureclient/client_test.go`, `cmd/vibermate/capture_test.go`. Update `ui/flutter_app/README.md` with the concrete SOCKS manual flow and server flags. Preview fixtures only as needed, without pretending a real proxy exists.

**Consumes:** Task1's immutable host description and optional `socksAddress`, unchanged one-time credential and Root delivery. **Produces:** Settings availability/address plus ManualCapture copyable SOCKS host/port/credentials, and localized CLI text. Existing HTTP shell output remains unchanged.

- [ ] **Step 1 — strict contract RED.** Add a valid new status/context/grant fixture and prove the current strict decoder rejects it; use the existing fixture builders rather than a second schema. Add malformed address cases: wildcard, port0, userinfo, path, query, noncanonical casing, and invalid IPv6. Missing optional fields remain accepted. The local Go ManualCapture client must require literal loopback for SOCKS, matching its existing local-only HTTP contract.

```dart
final json = <String, Object?>{
  'confirmationToken': 'ctx_${List.filled(43, 'A').join()}',
  'proxyAddress': 'http://127.0.0.1:32123',
  'socksAddress': '127.0.0.1:32124',
  'environmentId': 'work',
  'environmentRevision': 7,
  'environmentDigest': List.filled(64, 'a').join(),
  'launchAuthorityDigest': List.filled(64, 'b').join(),
  'protectedAuthorities': <String>[],
  'managedCredentialAuthorities': <String>[],
  'defaultTemporarySeconds': 3600,
  'maxTemporarySeconds': 86400,
};
final context = ManualCaptureContext.fromJson(json, 'context', expectedEnvironmentId: 'work');
expect(context.socksAddress, '127.0.0.1:32124');
```

Run `flutter test --no-pub test/control_models_test.dart --plain-name 'SOCKS delivery'` for the new test group and retain RED before implementation.

- [ ] **Step 2 — consume one explicit field.** Add exact DTOs and validation, include `socksAddress` in review/grant equality in CLI `capture.go` and Flutter `HttpControlApi.createManualCapture`. The latter currently checks Capture/environment attributes but not endpoint equality; add both HTTP and SOCKS address comparisons there, with a response-mismatch rejection test. Do not infer the endpoint from the control URL, HTTP proxy port or configured `:0`. Decode unknown additional keys as errors as before. A backend with no description produces unavailable/unknown text, not a made-up operational proxy.

```dart
final class HostNetworkAccess {
  const HostNetworkAccess({required this.socksAddress, required this.vlessAvailable});
  final String? socksAddress;
  final bool vlessAvailable;
}
// RuntimeStatus constructor: this.networkAccess; parser optional {'networkAccess'}.
// ManualCaptureContext/Grant constructors: this.socksAddress; parser optional field.
```

- [ ] **Step 3 — user-facing fields, not protocol jargon.** In Settings → Access & launch, show “SOCKS5 代理 / SOCKS5 proxy”, the actual address or “未启用 / Not enabled”, and “VLESS 内核已安装 / VLESS engine installed” or “VLESS 内核未安装 / VLESS engine not installed”. Do not label installed as node connected. Manual delivery retains HTTP/HTTPS row and shared username/password, adds separately copyable SOCKS host and port and a short explanation: use the same username/password; choose a client supporting SOCKS5 UDP; UDP uses the global network exit. Advanced help explains the TCP control connection lifetime. For non-loopback endpoints explain that SOCKS itself is not encrypted; use a trusted private network/tunnel and allow the UDP relay address/port. Preserve HTTPS interception Root instructions separately; raw UDP does not require installing that CA.

Use existing `_ManualValueRow`, localization and clipboard behavior. Do not add a new setup wizard, password store or per-client launch recipe. Review screen shows the SOCKS address before consent. Readback after the one-time dialog must not redisplay passwords. At 390px, long IPv6/domain addresses wrap without hiding copy buttons; verify rendered output, not just widget presence.

- [ ] **Step 4 — CLI text without hidden environment changes.** Add localized SOCKS address and shared-credential instructions to the normal review/created text when available. Never print it in unrelated errors or status. Keep `--format shell` byte-compatible for existing HTTP/HTTPS/CA exports; do not set `ALL_PROXY` or claim HTTP variables capture UDP. Add a small helper if needed for writing the optional localized paragraph, not a new generic rendering framework.

```go
if format != "shell" && grant.SOCKSAddress != "" {
    message, err := catalogs.Render(locale, "cli.capture.socks", map[string]string{
        "address": grant.SOCKSAddress,
        "username": grant.ProxyUsername,
    })
    if err != nil { return err }
    _, err = fmt.Fprintln(output, message)
    return err
}
```

The existing authenticated HTTP URL already carries the one-time password. The added paragraph says to reuse that displayed credential; do not introduce an unrelated permanent secret. CLI review/create mismatch must reject a changed SOCKS address just as it rejects a changed HTTP endpoint.

- [ ] **Step 5 — focused GREEN and visual check.** Run the changed Go client/CLI/locales packages; Flutter parser and manual/Settings tests in VM, then the focused new browser widget tests using the existing Chrome asset setup. Cover English/Chinese, enabled/disabled/unknown status, one-time create/rotate, actual clipboard values, remote warning, IPv6, 390px layout and unchanged HTTP-only shell output. `flutter analyze --no-pub` follows source formatting. Record exact commands/results, actual render and the existing Ahem/font qualification if present; do not rerun unrelated 900-test UI campaigns.

- [ ] **Step 6 — freeze.** Preserve task-only diff/hash/RED/GREEN/visual evidence and release source/test slot. Independent review covers schema agreement, honest copy and one-time secret handling. Document that remote `run` remains HTTP relay and remote SOCKS requires an explicitly reachable listener and UDP relay; this unit does not implement UDP-over-HTTPS.

## Task 3: Real user-entry REALITY/Vision acceptance

**Files:** Add `internal/serverhost/socks_vless_delivery_test.go` and focused `_test.go` fixture code in the same package only if needed. Reuse existing Server owner setup, control routes and Host options; existing `internal/productruntime/vless_routing_test.go`/`socks_udp_test.go` and `internal/vlesscore/integration_test.go` are reference fixtures, not alternative pass evidence. Add acceptance results to this plan's ignored SDD report and the documented flow in `ui/flutter_app/README.md`; no production protocol changes unless a concrete failure is first reported.

**Consumes:** Task1 real authenticated status/grants, Task2 wire contract, actual packaged helper; existing owner VLESS import and global-default endpoints. **Produces:** a reproducible isolated user-entry proof that fails if endpoint publication, real grant auth, VLESS route or packet boundaries are removed.

- [ ] **Step 1 — fixture with actual packaged bytes.** Require explicit `VIBERMATE_TEST_XRAY_BINARY` pointing to the preserved candidate's `Contents/MacOS/vibermate-xray`; record actual SHA/version and package material identity before the run. Empty environment skips with an explicit no-proof message, but acceptance invocation must provide it and report zero skipped target tests. Start only private TLS1.3 handshake, REALITY+Vision server, TCP echo and UDP echo fixtures. Use generated ephemeral keys in mode0600 owned files; no real nodes or external model accounts. Every goroutine/process has joined cleanup; preserve failure logs without UUID/private keys.

```sh
VIBERMATE_TEST_XRAY_BINARY="$PWD/dist/candidates/local-o7uoeCnw/ViberMate.app/Contents/MacOS/vibermate-xray" \
  /tmp/vibermate-go-security.JcRyfL/go/bin/go test ./internal/serverhost \
  -run '^TestServerManualSOCKSREALITYDelivery$' -count=1 -v
```

The preserved helper source is unchanged by Host/control edits; this is an actual packaged engine with current-source Host, not a claim that the old whole App already contains this task. A final new same-source distribution is still required before release.

- [ ] **Step 2 — configure through real controls.** Start isolated Server with explicit loopback SOCKS and helper. Set up synthetic owner through existing Web setup. POST `/api/v1/egress-profiles/import-vless`, PUT `/api/v1/egress-default`, fetch `/api/v1/status` and `/api/v1/manual-captures/context`, then POST `/api/v1/manual-captures`. Use the returned endpoint and credentials for RFC1929 negotiation and CONNECT/UDP ASSOCIATE; never directly mint a ManualCapture credential or call Runtime profile import as a substitute. Existing test network seams may map owned `.fixture` domains to loopback and count direct/proxy DNS; do not weaken production routing/target guards.

- [ ] **Step 3 — prove the requested path.** TCP must return exact binary payload. UDP must return three distinct datagrams unchanged, including zero/high bytes, through ViberMate's own association and REALITY+Vision; observe VLESS upstream traffic/egress evidence so a direct echo cannot pass. A matching direct-domain rule must use Runtime SystemResolver/Direct without DoH/VLESS for that destination; an unmatched hostname must use the selected proxy/DoH path. Rules allow only owned targets. Use bounded fixture deadlines as test cleanup, not product success/performance standards.

```go
payloads := [][]byte{
    {0, 255, 128, 1, 13, 10},
    []byte("second datagram\x00separate boundary"),
    bytes.Repeat([]byte{0, 1, 128, 255}, 64),
}
// Each returned SOCKS datagram must preserve target and bytes, separately.
```

- [ ] **Step 4 — close and failure behavior.** Closing the client control connection ends association; revoking that real ManualCapture rejects further use. A bad node/helper start must fail without a target direct dial or fabricated success; restored configuration can create a new operation. Host shutdown closes TCP/UDP and joins the managed engine. Do not expand into unrelated full fuzz/performance/client matrices; these checks establish functional correctness of the assembled entry point.

- [ ] **Step 5 — freeze and review.** Report the actual packaged helper hash, current Host source hashes, owned fixture topology, exact commands/results, no live handles, and untested platforms. Keep a failing first observation if found; implement only the demonstrated cause via the normal review loop. Independent review checks that the test crosses the actual user entry and cannot pass via direct minting/routing. After acceptance, resume stable-line integration and next phased release preparation; do not mark the full production Goal complete.

## Self-review and scope ledger

| Shared Interface / task | Producer → consumer check |
| --- | --- |
| Host → status + issuer | Actual post-bind immutable address; empty disabled; no Runtime address guessing |
| Issuer → control + CLI/Flutter | One optional canonical socksAddress, included in confirmation; same Capture credential, unchanged HTTP address |
| Server advertisement → client | Separate explicit SOCKS address, existing resolver, no HTTP/NAT/TLS inference |
| Task1 self | Authenticated delivery, ephemeral bind and reverse rollback have real HTTP/socket tests |
| Task2 self | Strict schema, review equality, secret lifetime and narrow layout/clipboard tests match actual fields |
| Task3 self | Real owner control→grant→own SOCKS→packaged REALITY/Vision, exact TCP/UDP replies and no Direct fallback |

This plan does not replace original MCP, CA/devcontainer, client/Paseo, data migration/soak or final same-source production qualification. It completes one approved functional delivery sequence before those remaining tasks.
