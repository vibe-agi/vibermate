# Cyber mode compatibility implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Let an account's preferred Daybreak mode use explicitly supported Standard on incompatible models, retaining the chosen model and displaying this behavior accurately.

**Architecture:** Resolve the effective program inside the existing `cyberaccess.ApplyPreference` interface, after account leasing and final model mapping have already established scope. Keep the stored account preference and wire values unchanged. Extend the existing account widget with a derived Standard fallback summary.

**Tech Stack:** Go 1.26.9, existing exchange pipeline and SQLite persistence, Flutter/Dart, existing EN/zh-CN copy catalog.

**Spec:** `docs/superpowers/specs/2026-10-19-cyber-model-compatibility.md`

## Global Constraints

- Base: published v0.1.30 / origin/main commit `85a257a27a70c4e539289d94d43a3ecb285f43be`.
- Keep account-level storage, API wire fields and enum values `standard`, `daybreak_blue`, `daybreak_red` unchanged.
- Preserve the final upstream model; existing explicit route mappings still apply. No new model or account fallback.
- Prefer the configured Daybreak program when the exact selected account/model advertises it. Otherwise use Standard only when that exact model advertises `standard`.
- Missing/nil/empty/wrong-model capability evidence and discovery errors must not authorize a Standard fallback.
- Do not silently substitute the other Daybreak program. Preserve Guardian Red-to-Blue derivation before compatibility resolution.
- Account preference overrides the incoming client mode. Preserve unrelated request JSON, other access programs and opaque numbers.
- No new CLI, Codex changes, schema migration, dependency upgrades, production-data mutation or app publishing.
- Keep English and Simplified Chinese behavior/copy aligned. Historical release notes stay historical.

## Task 1: Resolve compatible programs at the request boundary

**Files:**
- Modify: `internal/cyberaccess/programs.go`
- Test: `internal/cyberaccess/programs_test.go`
- Test: `internal/cyberaccess/preference_test.go` (align the old Red-on-Standard-capable expectation)
- Test: `internal/exchange/cyber_access_test.go`

**Interfaces:**
- Consumes: `ApplyPreference(body []byte, catalog Catalog, mode Preference) ([]byte, bool, error)`, `Catalog map[string][]string`, existing pipeline account lease and final model mapping.
- Produces: the same interface, now selecting Standard when the preferred program is absent and Standard is explicitly present for the final model.

- [x] **Step 1: Add independent, table-driven regressions.**

Use literal catalogs and expected values. Cover Blue and Red with Standard-only models, client modes absent/Standard/Blue/Red, preference present alongside Standard, the other Daybreak mode with/without Standard, unknown/nil/empty catalogs and evidence for a different model. Assert outgoing JSON `model`, `input`, an opaque integer `9007199254740993`, and unrelated `access_programs.other` survive. For an already-Standard body falling back to Standard, assert `changed == false` and identical bytes. Update the old test that classifies a known Standard-only model as unavailable.

A minimal new red case is:

```go
body := []byte(`{"model":"ordinary","input":"hello","access_programs":{"cyber":"daybreak_blue","other":{"keep":true}}}`)
catalog := Catalog{"ordinary": {"standard"}}
got, changed, err := ApplyPreference(body, catalog, Blue)
if err != nil || !changed { t.Fatalf("changed=%v err=%v", changed, err) }
var wire struct {
    Model string `json:"model"`
    Access struct { Cyber string `json:"cyber"` } `json:"access_programs"`
}
if json.Unmarshal(got, &wire) != nil || wire.Model != "ordinary" || wire.Access.Cyber != "standard" {
    t.Fatalf("unexpected fallback body: %s", got)
}
```

Extend the real exchange test matrix to exercise a mapped final model that is Standard-only while the incoming model has Blue support; it must send the mapped model unchanged with Standard. Add Blue/Red fallback overriding a client's conflicting mode; preserve exact selected account/epoch assertions. Add Guardian Red-derived-Blue with a Standard-capable model and verify Standard, never Red. Retain unknown and directory-error failures with zero provider sends.

- [x] **Step 2: Verify red.**

Run `go test ./internal/cyberaccess ./internal/exchange -run 'Cyber|ApplyPreference|ParseCatalog|ParsePrograms' -count=1`. The new known Standard fallback cases must fail with unavailable, while pre-existing failures remain intentional. Save the relevant output to the task report.

- [x] **Step 3: Implement the minimum resolution.**

Inside the existing non-Standard branch, parse the final model and resolve a local effective `mode` before the existing preservation/rewrite logic:

```go
if mode != Standard {
    if json.Unmarshal(root["model"], &model) != nil {
        return nil, false, ErrProgramUnavailable
    }
    programs := catalog[model]
    switch {
    case slices.Contains(programs, mode.WireValue()):
    case slices.Contains(programs, Standard.WireValue()):
        mode = Standard
    default:
        return nil, false, ErrProgramUnavailable
    }
}
```

Update the function comment to describe the preference and exact-model fallback. Preserve existing input validation, unknown failure classification, Guardian handling and raw-message preservation; do not add a new public interface or a second discovery call.

- [x] **Step 4: Verify green and the affected pipeline.**

First rerun the focused red command. Then run `go test ./internal/cyberaccess ./internal/exchange ./internal/providertransport ./internal/loopbackproxy -count=1` once. Format changed Go files with gofmt. Record exact commands/results, any baseline/environment failure, and self-review the diff.

- [x] **Step 5: Commit backend implementation and tests.**

Stage only this task's files; commit as `fix(cyber): fall back to advertised standard per model`.

## Task 2: Show account preference and Standard fallback coverage

**Files:**
- Modify: `ui/flutter_app/lib/features/workbench/provider_account_cyber_access.dart`
- Modify: `ui/flutter_app/lib/core/i18n/app_copy.dart`
- Test: `ui/flutter_app/test/provider_account_cyber_access_test.dart`
- Modify: `docs/capability-support.md`

**Interfaces:**
- Consumes: unchanged `ProviderAccount.cyberAccessProgram`, account/endpoint-scoped `UpstreamModelCatalog`, each model's `verifiedAvailable` and nullable `availableCyberPrograms`, and Task 1's exact-model fallback rule.
- Produces: visible fallback model information while persisting the exact same preferred mode through existing settings calls.

- [x] **Step 1: Add a behavioral widget regression before UI production edits.**

Use the existing `_CyberApi` fixture with a mixed catalog and select Blue:

```dart
api.modelPrograms = {
  'ordinary-model': ['standard'],
  'blue-model': ['standard', 'daybreak_blue'],
  'red-model': ['standard', 'daybreak_red'],
  'red-only-model': ['daybreak_red'],
};
```

Assert the visible fallback summary contains ordinary-model and red-model, but not blue-model or red-only-model; the stored selection remains `daybreak_blue`. Selecting Red changes the fallback set to ordinary-model and blue-model; selecting Standard removes the summary. Exercise null/empty evidence and refresh failure through existing fixtures so they never produce a verified fallback claim. Cover English and Simplified Chinese rendering and a narrow viewport using existing test infrastructure. Existing literal copy assertions may be updated for new wording; do not add tests merely asserting source strings.

- [x] **Step 2: Verify red.**

Run from `ui/flutter_app`: `flutter test test/provider_account_cyber_access_test.dart`. The new fallback summary assertions should fail against the existing widget, not due to setup or selector errors. Save relevant output.

- [x] **Step 3: Add the derived summary and aligned copy.**

Compute fallback models only for a non-Standard selection, using the same verified models as the current supported list:

```dart
final standardFallback = selected == 'standard'
    ? const <String>[]
    : models.where((model) =>
        model.verifiedAvailable &&
        (model.availableCyberPrograms?.contains('standard') ?? false) &&
        !(model.availableCyberPrograms?.contains(selected) ?? false))
      .map((model) => model.id).toList(growable: false);
```

Render it alongside the existing model support information with a distinct widget key and wrapping Text. Add a localized `provider_accounts.cyber_access.standard_fallback` message such as `Uses Standard: {models}` / `以下模型使用 Standard：{models}`, formatting model names with the established ` · ` separator. Keep unknown and failed scopes out of this list.

Change the preference label to `Cyber access preference` / `网络安全模式偏好`. Preserve the existing wire/option values. Rewrite the help text to explain account priority, unchanged model, preferred-mode support, Standard fallback only when advertised, and no fallback on unknown/failed discovery. Do not claim Blue and Red are increasing levels. Preserve mutation, refresh, generation and capability-scope protections.

Update the current `codex-cyber-access` row in `docs/capability-support.md` to describe exact-model Standard fallback and the retained unknown/no-compatible-mode failure. Do not rewrite historical v0.1.30 release notes or touch unrelated UI.

- [x] **Step 4: Verify green.**

Run the focused widget suite, the related account/settings UI suite once if a shared copy assertion is affected, `flutter analyze lib/features/workbench/provider_account_cyber_access.dart lib/core/i18n/app_copy.dart test/provider_account_cyber_access_test.dart`, and formatting checks for changed Dart files. Record results. No new dependencies.

- [x] **Step 5: Commit the UI and current capability documentation.**

Stage only this task's files; commit as `feat(ui): explain adaptive cyber preferences per model`.

## Final validation and integration

- [ ] Review each task for specification compliance and code quality, then review the combined branch.
- [x] Confirm no schema, wire value, model selection, CLI or Codex configuration changes.
- [x] Run `git diff --check` and the repository's relevant existing consistency checks; reuse valid task test evidence rather than rerunning unchanged suites.
- [ ] Prepare a concrete reviewable branch/diff. Do not replace the running app or publish a release as part of this task.
