# VLESS TLS and CA Editor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development and test-driven-development after the TLS profile/control task passes review. This implements the approved parent Task4 UI, not a new visual redesign.

**Goal:** Let users import ordinary TLS VLESS nodes and manage a configuration-local CA with clear saved-state feedback, without re-entering existing node credentials.

**Architecture:** Add a strict safe-description read and private optional TLS mutation to the current ControlApi/WorkbenchController seam. The existing egress dialog consumes a revision-bound description and holds new CA bytes only for the dialog/submission lifetime. A focused TLS trust field Module owns file selection and its short-lived state; public TrafficEgressPolicy remains unchanged.

**Tech Stack:** Existing Flutter/Dart, `file_selector`, AppCopy, Viber theme, HttpControlApi, established VM and isolated Chrome test runners. No new packages, system font/theme changes, user Chrome/App interaction, or backend edits.

**Spec:** `../specs/2026-10-09-vless-udp-functional-design.md`, parent `2026-10-09-vless-udp-functional.md` Task4; backend Interface from `2026-10-10-vless-tls-profile-control.md` and that task's final reviewed report. Reconcile exact finalized wire names before dispatch, not by guessing.

## Global Constraints

- One product source/local test owner. Before-copy existing WIP; handwritten edits via apply_patch. No staging/commit, backend/Host/kernel/source-schema change or user App/9666/accounts/trust/system-proxy operations.
- UUID, node URI and CA material stay out of public Policy/exports, logs, exceptions, toString, preview fixtures and persisted workbench preferences. Do not claim Dart immutable strings can be reliably zeroized; clear/dispose controllers and drop references.
- System trust or configuration-local CA only; no skip switch, REALITY CA control or hidden Direct fallback. Saved-mode load failure is not permission to assume System.
- Preserve original ID/revision/secret ref for public-only edits. A changed TLS trust uses private import with empty URI and exact revision; backend owns copying the original credentials and publishing a new immutable reference.
- Preserve owner/current-controller/runtime generation/ABA/unmount guards. Network failure never auto-replays the private POST or marks the operation saved; safe reload is read-only.
- Keep the existing UI's compact visual language and exact theme tokens. User-facing actions are “保存更改 / Save changes,” not internal revision/publication jargon. No extra performance threshold is a functionality gate.

## Design application

This is an existing settings dialog, not a landing page. Use current `canvas #10151A`, `panel #182028`, `input #11171D`, `text #F1F5F8`, `route #75B8F0`, `warning #E5B663` in dark mode through ViberColors, and the existing paired light tokens. Typography stays the existing system UI/PingFang roles; do not introduce fonts. Layout stays the current520px dialog with scrollable content, stacked fields, focusable controls and reachable actions at390px width. Its distinctive useful detail is a short saved-state row (“已保存自有 CA”) beside the replacement action, never a dump of certificate internals.

Show trust controls directly below the masked node-link field only for a recognized new `security=tls` URI or an existing revision whose description is TLS. Known REALITY shows its normal authentication description and no certificate controls. Do not turn a UI detection helper into a second authoritative URI parser; backend rejection remains definitive.

| zh | en |
| --- | --- |
| 服务器证书 | Server certificate |
| 使用系统证书 | Use system certificates |
| 使用自有 CA | Use a custom CA |
| 导入 CA 文件 | Import CA file |
| 更换 CA 文件 | Replace CA file |
| 粘贴 CA 证书 | Paste CA certificate |
| 已保存自有 CA；不更换会继续使用。 | A custom CA is saved. It will be kept unless replaced. |
| 只用于这个网络出口，不会安装到系统。 | Used only for this network exit; not installed in the system. |
| 无法读取已保存的证书设置，请重试。 | Could not load saved certificate settings. Try again. |
| 请选择 PEM 格式的 CA 证书文件，大小不超过64 KiB。 | Choose a PEM CA certificate file no larger than64 KiB. |

Custom-CA mode supports choosing a `.pem`/`.crt`/`.cer` file containing PEM or expanding a paste field; DER/PFX/private keys are not claimed supported. CA validity/complete X.509 parsing is enforced by the backend, not a superficial client regex. Locally reject empty, invalid UTF-8 and oversize input with safe copy, never echo selected text in errors. System mode needs no file. Do not display technical security warnings merely because a normal load is pending.

## Task 1: Safe TLS settings transport and user-facing editor

**Files:**
- Modify `ui/flutter_app/lib/core/api/control_models.dart`: safe `VlessProfileDescription`, private `VlessTLSImportOptions` types only; no public Policy expansion.
- Modify `core/api/control_api.dart`: description GET plus optional private `tls` POST; preview counterpart in `preview/preview_control_api.dart` remains explicitly unsupported for private operations and must not fabricate a description.
- Modify `features/workbench/workbench_controller.dart`: delegate read/import parameters.
- Modify `features/workbench/egress_profile_editor.dart`: readback/loading/trust-edit intent; replace unsupported unconditional “REALITY + Vision” summary with honest generic “VLESS” where description is unavailable.
- Create `features/workbench/vless_tls_fields.dart`: trust choices/file/paste/saved-state rendering and locally bounded reads; use current widgets/theme.
- Modify `features/workbench/settings_view.dart`: pass revision-bound loader, forward private TLS intent, preserve existing current/authority guards and safe feedback.
- Modify `core/i18n/app_copy.dart`: the bilingual copy above and accurate supported TLS/REALITY import-error copy.
- Extend `test/vless_egress_api_test.dart`, `vless_egress_contract_test.dart`, `vless_egress_editor_test.dart`; add `test/vless_tls_editor_test.dart` for file/async/state behavior. Existing lifecycle tests only when affected, no unrelated changes.

**Interfaces:**

```dart
final class VlessProfileDescription {
  // Strict values: security reality/tls; trustMode system/custom_ca for TLS only.
  final String security;
  final String? trustMode;
  factory VlessProfileDescription.fromJson(Object? value, String path);
}
final class VlessTLSImportOptions {
  const VlessTLSImportOptions.system() : mode = 'system', caPem = null;
  const VlessTLSImportOptions.customCA(String pem)
      : mode = 'custom_ca', caPem = pem;
  final String mode;
  final String? caPem;
  // No public toJson; no PEM in toString. Only HttpControlApi serializes the private body.
  @override
  String toString() => 'VlessTLSImportOptions(redacted)';
}
// Add to ControlApi and WorkbenchController:
Future<VlessProfileDescription> vlessProfileDescription(String id, int revision);
// Existing create/importVlessEgressProfile and VlessImportIntent gain:
VlessTLSImportOptions? tls;
// Existing showEgressProfileEditor gains optional loader for an existing VLESS revision:
Future<VlessProfileDescription> Function()? loadVlessDescription;
```

Description GET path is `/api/v1/egress-profiles/{id}/revisions/{revision}/vless-settings`, authenticated existing read transport. Reject unknown/security-inconsistent values; never default bad or missing settings to System. Preserve resource-ID/revision validation and exact request identity. Backend responds only `{"security":"tls","trustMode":"custom_ca"}` or System/REALITY equivalents.

Private POST adds `tls: {mode: system}` or `{mode: custom_ca, caPem: ...}` only when an explicit trust input is needed; otherwise omit. Existing create/new-URI TLS defaults to System. Saved credentials + trust replacement uses `uri:""`, exact prior If-Match and normal idempotency handling. Existing unchanged private trust with only public field edits uses existing public draft/publish path, preserving secret reference. New URI selection creates a new node context: clear prior selected CA/trust intent, never silently attach the previous node's CA to a replacement link.

- [ ] **Step 1: Behavioral RED.** Add a summary test asserting unknown VLESS security is presented as generic VLESS, not claimed REALITY. Add widget behavior expecting TLS trust fields after pasting a TLS node link. These compile against existing interfaces and fail due to present behavior before implementation. Retain real failing output, not compilation-only failure.

```dart
expect(egressPolicySummary(copy, vlessPolicy), isNot(contains('REALITY')));
await tester.enterText(find.byKey(const Key('egress-profile-vless-uri')), tlsNode);
await tester.pump();
expect(find.text(copy('environment.egress.tls.label')), findsOneWidget);
```

- [ ] **Step 2: Implement strict DTO/transport and tests.** Validate the two-enum response and new private input locally without adding public serialization; retain private toString redaction. Send exact optional TLS wire fields only through the existing authenticated private mutation. API tests inspect actual HttpControlApi request method/path/headers/body and wrong acknowledgment/HTTP failure/no-replay behavior. Preview reads/imports throw unsupported rather than fake success.

```dart
body: {
  'id': id, 'displayName': displayName, 'uri': uri,
  'resolver': resolver.toJson(), 'directDomains': directDomains,
  if (tls != null) 'tls': {
    'mode': tls.mode,
    if (tls.mode == 'custom_ca') 'caPem': tls.caPem,
  },
}
```

- [ ] **Step 3: Implement saved-state loading and editor choices.** Existing VLESS dialogs load only the selected ID/revision description. While pending, show a normal loading indicator; failed load shows retry and does not offer a misleading trust default. Block save until saved state is known. Reject late completions after dispose/replacement/retry. Keep generic list summaries secret-free without one description network request per list row.

For existing custom CA with no new PEM, the unchanged choice keeps saved trust and permits saving public edits without re-upload. Switching to System creates an explicit private trust edit with empty URI; selecting a new CA likewise creates a private edit. Switching back to the unchanged saved choice without new PEM is a no-trust-change operation. New custom CA requires actual input. REALITY never sends TLS input. Changing proxy kind or replacement URI clears short-lived CA selection and invalidates any pending file selection.

- [ ] **Step 4: Implement bounded file/paste selection.** Reuse existing `file_selector` import conventions and openRead/BytesBuilder pattern from provider_account_editor, but with raw64KiB cap and no backend authority duplicated. Check selected file length, bound each chunk, validate UTF-8, clear temporary byte buffers and do not store the path/content outside this dialog. `file_selector` can be controlled in tests through its existing platform interface; no new production mock seam is needed. Track selection generation so a delayed file completion cannot overwrite a later choice, proxy switch or disposed dialog. File cancel leaves the prior choice intact. Paste is optional expanded input; clearing the input never manufactures a stored CA.

- [ ] **Step 5: User-facing and lifecycle tests.** Cover new System and customCA imports; saved customCA loads without PEM exposure and public-only save requires no upload; CA replacement and System switch keep original ID/revision and send empty URI; REALITY has no TLS fields; bad/failed description gets retry/no System assumption; canceled/oversize/invalid UTF-8 file; delayed file completion after kind/link change/dispose; controller replacement, owner→member and ABA while dialog/read/save pending cannot mutate the new Runtime; network failure yields unconfirmed state and no replay. Test exact exception families rather than broad Object assertions for new tests. EN/zh at390px and desktop width keep actions reachable without overflow; inspect one actual rendered narrow zh frame using existing isolated test infrastructure, without claiming test fonts prove production typography.

- [ ] **Step 6: Verify and freeze.** Run touched VM tests, affected owner/lifecycle regressions, scoped isolated Chrome tests, analyze/format and one Web build. Do not use ignored Chrome `--concurrency` flags. Preserve initial failures, final source hashes and task-only before-based diff; report actual UI/API boundary vs real Runtime integration coverage. No full947/882-test unchanged campaigns, no new source-independent font work, no release claim. Release the source slot for independent spec/security/quality review.

## Self-review

| Interface | Check |
| --- | --- |
| exact Describe → saved editor | real security/mode, no fabricated System or leaked private payload |
| private intent → HttpControlApi | optional TLS omitted vs explicit System/custom kept distinct |
| empty URI edit → backend exact revision | original node credentials are never requested/re-exported |
| selected file → pending dialog | bounded bytes, cancellation/generation ownership, no filesystem path persistence |
| local validation → backend validator | backend is authoritative for complete PEM and encoded64KiB limit |
| display summaries → available evidence | no unconditional REALITY label for all VLESS nodes |

The existing VLESS UI review's broad-exception and Chrome-concurrency Minors are applied to new/touched tests here; untouched tests are not a separate improvement project. After this editor review, parent Task4 still needs managed patched kernel, Host/bootstrap/launcher and actual assembled Runtime TCP/UDP/domain/CA verification. Native System-positive platform evidence remains explicitly required for release, not provided by widget tests.
