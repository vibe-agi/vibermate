# VLESS Configuration UI Implementation Plan

> **For agentic workers:** Use superpowers:subagent-driven-development and test-driven-development. This is Task4A of the approved VLESS functional plan, not a substitute for its remaining delivery/TLS/kernel work.

**Goal:** Let an owner import, select and edit a REALITY + Vision exit through the existing workbench, with private node-link entry and readable Direct-domain controls.

**Architecture:** Extend the existing egress DTO/API/controller/editor chain. Saved policies contain only immutable public credential references; the private URI travels only through the owner import mutation. Keep unsaved private import intent distinct from ordinary public policy publishing so no dummy credential reference can be persisted.

**Tech Stack:** Existing Flutter3.41.5/Dart3.11.3, ViberTheme and Control API; no package, Go, theme or font upgrade.

**Spec:** `../specs/2026-10-09-vless-udp-functional-design.md`; Task4 of `2026-10-09-vless-udp-functional.md`. Source-confirmed wire/UX details: that plan's `task-4-ui-preparation.md` in its SDD workspace. The Runtime/Host packet slice and its shutdown correction are now implemented and independently reviewed.

## Global Constraints

- One product source/local-test owner. Preserve all earlier Go/Flutter WIP; before-copy each overlapping file. No user App/9666/accounts/trust/system proxy changes, stable-line edits, dependency upgrade or subagents.
- No unconfigured direct fallback. Existing account/flow/global selection still chooses the frozen profile.
- UUID, node URI, authentication parameters and private credentials must not enter normal lists, exports, errors or logs. Preserve owner-only mutation and Runtime-generation/ABA/disposal guards.
- Use existing theme/geometry and bilingual user-facing names. A person pastes a node link and edits Direct domains, not secret references or epoch fields.
- Current import supports raw/TCP + REALITY + none encryption + Vision. Ordinary TLS/custom CA and default packaged kernel/host settings remain explicit subsequent Task4 work; do not show working controls or claim a complete VLESS release before those paths exist.
- No implicit change of the global default on import and no blind retry of an outcome-unknown private mutation.

## Existing interface map and visual direction

`control_models.dart` has strict TrafficProxyPolicy/TrafficEgressPolicy; it currently rejects VLESS fields. `control_api.dart` owns authenticated mutations and headers. `workbench_controller.dart` generates profile IDs. `egress_profile_editor.dart` returns EgressProfileDraft; Settings `_edit` in `settings_view.dart` fences the result by owner/controller/generation. `preview_control_api.dart` is explicitly a preview, not a secret store.

Retain ViberTheme: dark canvas#10151A, panel#182028, input#11171D, text#F1F5F8, route#75B8F0, danger#F0848E; light counterparts remain existing tokens. Use system/PingFang font families with existing dialog18/body13/utility11 scale, standard520px dialog/max-height0.74, existing focus/radii. No new decorative layout or font. The deliberate simplification is one masked node-link field instead of separate UUID/key/short-ID fields. Keep keyboard focus and narrow390px layout usable.

## Task 1: One complete owner import/edit flow

**Files:**
- Modify `ui/flutter_app/lib/core/api/control_models.dart`, `control_api.dart`.
- Modify `ui/flutter_app/lib/preview/preview_control_api.dart`.
- Modify `ui/flutter_app/lib/features/workbench/workbench_controller.dart`, `egress_profile_editor.dart`, `settings_view.dart`.
- Modify `ui/flutter_app/lib/core/i18n/app_copy.dart`.
- Create focused tests `ui/flutter_app/test/vless_egress_contract_test.dart`, `vless_egress_api_test.dart`, `vless_egress_editor_test.dart`; extend existing lifecycle fixture tests only when needed to test the same existing guards.

**Consumes:** owner POST `/api/v1/egress-profiles/import-vless`, body `{id,displayName,uri,resolver,directDomains}`, existing If-Match/idempotency headers, response EgressProfileRevision. New profile expectedRevision0; replacement uses existing ID/current revision. Public VLESS proxy has kind/credentialRef/credentialRevision and no endpoint. `directDomains` is a canonical newline-separated JSON string, not an array.

**Produces:** TrafficProxyPolicy adds nullable String credentialRef/int credentialRevision and `isVless`; TrafficEgressPolicy adds String directDomains defaulting to empty. Both preserve equality/hash/roundtrip and old empty encodings. ControlApi/HttpControlApi expose:

```dart
Future<EgressProfileRevision> importVlessEgressProfile({
  required String id,
  required int expectedRevision,
  required String displayName,
  required String uri,
  required TrafficResolverPolicy resolver,
  required String directDomains,
});
```

Controller supplies matching create/import delegates, with generated IDs only for a new operation. Editor keeps two explicit draft intents: ordinary public policy update and private VLESS import/replacement. Preserve ordinary draft callers; do not fabricate public references for an unsaved import. Settings dispatches the appropriate method under its current scope fence.

- [ ] Write a genuine contract RED against the existing parser, using the already-confirmed backend wire shape:

```dart
test('saved VLESS policy roundtrips without exposing a node link', () {
  final wire = <String, Object?>{
    'proxy': {'kind': 'vless', 'credentialRef': 'secret://egress-vless/fixture',
      'credentialRevision': 1},
    'resolver': {'kind': 'system', 'transport': 'direct'},
    'directDomains': '*.github.com\ngithub.com',
  };
  final policy = TrafficEgressPolicy.fromJson(wire, 'policy');
  expect(policy.toJson(), wire);
});
```

Run `flutter test test/vless_egress_contract_test.dart` in Flutter root. The old parser must reject the legitimate new shape; retain that behavioral failure, not a compile error. Add compact bad-kind/credential pairing, missing or invalid revision, VLESS endpoint, non-VLESS credential fields, wrong domain JSON type and old empty-encoding cases. Match existing backend bounds/helpers, not a second URI or DNS parser.

- [ ] Implement DTO decoding/encoding/equality first so a stored VLESS profile cannot break workbench/default/account/flow selection. Preserve direct/socks5 serialization. Resolver transport `proxy` is valid with VLESS; Direct cannot claim proxy transport. Canonicalization/deduplication of domain lines stays authoritative on the server; use acknowledged normalized readback.

- [ ] Add the private import API using the same authenticated `_mutation` boundary:

```dart
final response = await _mutation(
  'POST', '/api/v1/egress-profiles/import-vless',
  expectedRevision: expectedRevision,
  body: {'id': id, 'displayName': displayName, 'uri': uri,
    'resolver': resolver.toJson(), 'directDomains': directDomains},
);
final profile = EgressProfileRevision.fromJson(response, 'egressProfile');
```

Validate resource ID/revision before sending, and reject an acknowledgement for a different ID or unexpected revision; never invent successful data from a missing/malformed response. Tests use the existing HttpControlApi/MockClient fixture pattern to assert exact POST/path/headers/body and safe response. Cover create revision0, replacement of existing ID/revision, conflict/invalid/unavailable response, network failure without automatic replay, and preservation of selected default. These are wire-contract tests, not a claim of a second real Runtime proof.

- [ ] Add VLESS to the existing proxy-kind picker. New VLESS requires a private link; existing VLESS with an empty replacement preserves its current ref/revision. Changing only display name/resolver/domains takes ordinary public Publish and carries the unchanged reference. Replacing the link takes private import with the original profile ID/revision. Keep private text in the dialog/import intent only, clear/dispose it, and never include it in public policy/model `toJson` or debug output. Preview import must clearly report unsupported operation rather than pretend to persist credentials.

- [ ] Add Direct domains for applicable exits and safe summaries. Use these bilingual user-facing labels/help, adapting existing AppCopy keys without changing other vocabulary:

| 中文 | English |
| --- | --- |
| 节点链接 | Node link |
| 节点已保存；如需更换，请粘贴新链接。 | Node saved; paste a new link to replace it. |
| 直连域名 | Direct domains |
| 每行一个。example.com 包含自身和子域名；*.example.com 仅匹配子域名。匹配后由运行 ViberMate 的设备使用系统 DNS 并直连。 | One per line. example.com includes the domain and subdomains; *.example.com matches subdomains only. Matches use system DNS and connect directly from the device running ViberMate. |
| 当前支持 REALITY + Vision 节点。 | REALITY + Vision nodes are supported. |

Normal rows show the chosen name/VLESS variant, not node link/ref/revision/UUID/key. Keep existing Save changes action. Unknown mutation outcome says to refresh/confirm; do not display saved or silently retry. Invalid link directs the user to check supported node type, not internal Go error details. Existing owner/generation/cancel/unmount/late acknowledgement guards must also apply to the private branch.

- [ ] Verify actual widget interactions in English and Chinese: create/paste/save sends one private import; edit without replacement sends one public update with unchanged ref; replacement sends one private import to the same ID; cancel/unmount sends none; old dialog cannot write a new/ABA Runtime; late acknowledgement cannot update a replacement controller. Add one390px case verifying fields/actions remain reachable without overflow. Use the existing editor keys and new stable keys `egress-profile-vless-uri`/`egress-profile-direct-domains`. Test secrets are synthetic; assert normal summaries/errors do not contain them.

- [ ] Run focused tests, analyze/format, and one Web build after functional GREEN. Exact commands from Flutter root:

```sh
flutter test test/vless_egress_contract_test.dart test/vless_egress_api_test.dart test/vless_egress_editor_test.dart test/egress_profile_settings_lifecycle_test.dart test/egress_late_dialog_test.dart test/egress_choice_contract_test.dart test/egress_legacy_fixture_parity_test.dart
flutter analyze
dart format --output=none --set-exit-if-changed lib test
tool/build_web.sh
```

Run focused new browser-compatible tests once under Chrome when applicable; do not count VM-only skips as browser passes. Do not rerun the historical947/882/full-Go campaigns for this slice. Capture an isolated widget/golden rendering or equivalent screenshot for root visual review if available; never launch or manipulate the user's existing App/Chrome. Test runners using isolated fixtures are allowed; CUA is the only route for interactive UI control. Preserve first failures and rendered limitations.

- [ ] Freeze with a before-based task-only diff/source hashes and a report of real behaviors/commands/limits. No commits/index changes by implementer because this development worktree contains earlier reviewed WIP. Root owns integration. No Task4B/C feature expansion in this UI task.

## Remaining Task4 work (not claimed by Task4A)

The parent functional plan still requires ordinary TLS/custom-CA behavior, a qualified managed kernel, and user-facing Host/bootstrap/launcher listening settings/address with an actual assembled Runtime/UI/TCP/UDP/domain proof. These are backend/distribution/delivery units, not fake UI switches. This slice exposes only the implemented REALITY import contract; it cannot be called a complete or released VLESS product on its own.

## Self-review

| Shared contract | Producer → consumer | Check |
| --- | --- | --- |
| Public policy | Go wire → Dart strict DTO → selectors/default/editor | VLESS ref/revision and newline domains match actual backend, old omission preserved |
| Private mutation | dialog intent → controller → HttpControlApi → owner importer | No dummy public ref, original ID/CAS/idempotency retained, no private export |
| Lifecycle | existing Settings guard → private and public branches | owner/Runtime generation/ABA/unmount/late result preserved |
| One task | parserRED → import/edit interaction → focused verification | same independently testable user flow; host/kernel/TLS not silently marked complete |

Approved design is already settled; continue with the previously selected subagent-driven execution, no new user approval round or visual rebrand.
