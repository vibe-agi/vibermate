# Cyber mode compatibility

## Approved behavior

The user asked to implement a narrow improvement to ViberMate 0.1.30. Retain account-level settings and the existing account/model relationships. Interpret a selected Daybreak mode as a preference, taking precedence over the client's mode, and adapt only the mode to the actual selected account's final upstream model. Do not replace the model. Existing explicit route model mappings still apply.

For a non-Standard preference:

| Exact model's advertised programs | Effective program |
| --- | --- |
| Contains the preferred program | Preferred program |
| Does not contain the preferred program, contains `standard` | `standard` |
| Missing model, missing/nil/empty programs, or neither usable program | Existing unavailable failure |

A model advertising only the other Daybreak mode does not authorize switching Blue to Red or Red to Blue. A model advertising both Standard and the other Daybreak mode falls back to Standard. Capability lookup failure is not evidence of Standard support. Preserve the existing unknown/unavailable failure path and its bounded discovery. Standard preferences keep their existing behavior and do not acquire a new discovery dependency.

The frozen account remains authoritative over an explicit client Standard/Blue/Red value. Preserve unrelated request fields and other access programs, including exact numeric JSON values. If the effective program already equals the client's program, retain the original body and report unchanged. Never mutate the saved preference to the effective per-request fallback.

Existing Guardian identification and Red-to-Blue derivation occur before compatibility resolution and remain intact. A Guardian's derived Blue may fall back to explicitly advertised Standard; it must not fall through to Red. Unknown internal aliases, ambiguous role markers, unrelated origins and other existing admission boundaries retain their behavior.

## User interface

Keep the existing account selector, saved enum values, API and schema. Label it as a preference and explain that the model is preserved. Continue displaying exact model capability evidence, scoped to the account and selected endpoint. Add a visible Standard fallback summary listing only verified models that advertise Standard but lack the selected Daybreak program. Do not include unknown, empty, unverified, or preferred-mode-capable models in that summary. Hide this summary for a Standard preference. Preserve loading/error/scope-change protections and persistence behavior.

English and Simplified Chinese must communicate the same rule. A refresh failure or missing capability metadata must not be presented as verified Standard fallback. Update the current capability-support documentation; leave historical v0.1.30 release notes unchanged.

## Non-goals

No new CLI command, no Codex patch or cache manipulation, no automatic model/account switching, no environment-level policy migration, no schema or API wire changes, no dependency upgrades, no production-data changes and no publishing/replacing the installed app in this task. The user accepted that environments sharing one account share its account preference.

The Codex TUI can reject turns before a request reaches ViberMate when its own Daybreak switch is enabled with an incompatible model. This change targets requests that arrive at the proxy, including Standard requests from clients with Daybreak disabled; it does not promise to control pre-submit client behavior.

## Verification

Use test-first unit coverage for mode resolution and real exchange-pipeline tests that inspect outgoing model and mode under the frozen selected account. Cover Blue and Red fallback, supported preference precedence, explicit client overrides, mapped final models, missing/empty/wrong-model capability evidence and Guardian derivation. Add widget coverage for the visible fallback summary in both languages and unknown/error states. Run relevant package tests, targeted Flutter tests and analyzer checks. No live upstream inference is needed to establish this deterministic rule.
