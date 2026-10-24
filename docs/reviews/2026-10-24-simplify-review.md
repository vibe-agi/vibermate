# Whole-project simplification review — 2026-10-24

Reviewed at `c53fcfe2` (v0.1.36) from four angles: reuse, simplification,
efficiency and altitude. This is a quality review, not a bug hunt. The
constraint was: no feature changes and no feature removal.

Every finding has one of three dispositions:

- **Applied**: changed on branch `refactor/simplify-review`.
- **Deferred**: worth doing, but it is a design decision or a refactor too
  large to apply as a cleanup.
- **Skipped**: changes behavior, or cannot be verified in this environment.

## Applied

Verification: `gofmt`, `go mod tidy -diff`, `go run ./cmd/repositorycheck`,
`make check-release-build`, `go vet` (linux, darwin, windows, and with the
synthetic acceptance tags), `staticcheck -checks U1000`, and tests for every
touched package.

**Dead code (simplification)**

- Removed about 30 unused functions, types and test fixtures under
  `internal/`. Every one was confirmed unused by `staticcheck U1000` on
  linux, darwin, windows and the synthetic tags. Among them: `RuntimeOfflineAdapter`,
  `connectionpolicy.Fixed`, `exchange.NewAttemptIDSource`,
  `originaltransport.NewTLSProber`, `providertransport.NewProviderProber`,
  `manualcaptureclient.Client.List` and `productruntime.recordingArchiveBarrier`.
- `exchangecontent`: deleted the unused `Request.validate`,
  `Response.validate` and `validatePageContent`. Dropped the `legacyCount`
  parameter, which every live caller passed as `false`.
- Removed parameters that always received the same value:
  - `allowEmpty` in `activity`, `openairesponses` and `toolapproval`;
  - `required` in `environment.validateNamedRevision`;
  - `possible` in `nativemcp`. `finishFailure` is now `finishUncertain`; its
    `cause` argument was already ignored.
- Removed `...bool` optional flags. `modelcatalog.Discover`,
  `FetchEndpointModels` and the `desktopcontrol` activity query parsers now
  take a plain `bool`, so a call that passes several flags no longer compiles
  instead of being rejected at runtime.
- Updated `PLAN.md`'s schema line to match ADR 0021: the supported startup
  upgrade exists.

**Reuse**

- New `internal/lifecyclegate` replaces three identical `lifecycle.go` copies
  in `capturerun`, `manualcapture` and `proxyclient`. Each package keeps its
  own sentinel errors and messages.
- New `internal/egressprofile/egressprofiletest.Explicit` replaces 14 copies
  of the `authoredEgress` test fixture.
- `egressaudit.Attempt.ClampCompletion` replaces three copies of
  `completionTime`.
- `protocolspec.CanonicalPath` and `ValidMethod` replace the copies in
  `pathcapability`.
- `capturecontrol.ValidManualCaptureETag` replaces the copy in
  `manualcaptureclient`.
- `exchangecontent.CloneOwned` replaces `productruntime.cloneSnapshot`.
- `servercontrol` now calls its own `writeServerJSON` instead of writing the
  same JSON response by hand in four places.
- `runlauncher` validates `--cyber-access-program` with
  `cyberaccess.ParsePreference` instead of keeping its own copy of the
  program list.

**Efficiency**

- Content-addressed chunk and block writes (`putCompressedRow`) first try an
  `UPDATE` in the same transaction. They compress and `INSERT` only when the
  row is missing. The row is still always written, never skipped. Previously,
  every already-stored chunk of a resent history was zstd-compressed at
  maximum compression and then thrown away.
  `TestContentSourcePublicationReusesSuccessfulMessagesWithinTransaction`
  counted block writes with a `BEFORE INSERT` trigger. An already-stored block
  is now rewritten by an `UPDATE`, so the test also counts `BEFORE UPDATE`. It
  still asserts the same thing: one unconditional write per distinct block
  in each transaction.
- `clientannotation.StripJSON` skips the full JSON parse when the body
  contains neither the marker text nor any `\u` escape. Under JSON escaping
  rules, this exactly identifies bodies that cannot contain a marker. A new
  test covers a marker spelled entirely with `\u` escapes.
- Activity inspection reads memoize fence visibility per scope within their
  read-only snapshot. This removes one query per row from the 1-second poll.
- The Responses stream encoder builds text deltas with a `strings.Builder`
  instead of repeated string concatenation, which was quadratic. `Feed` no
  longer clones its per-call buffer.

## Deferred — design decisions

| ID | Finding | Why deferred |
|---|---|---|
| ALT-1 | The ChatGPT/Codex service is identified by origin string at about 30 Go sites, and the same rules are mirrored in Dart (`provider_origin.dart`). Proposed fix: typed service capabilities computed once when the endpoint is created and sent to the UI. | Cross-cutting change touching Go and Dart. It needs its own plan, and it removes the special cases behind ALT-3 and ALT-12. |
| ALT-3 | Codex turn-state and native-header handling is spliced into the generic Exchange pipeline. Proposed fix: a per-service native adapter with header hooks. | Depends on ALT-1. |
| ALT-4/5 | `runlauncher` branches on the `codex-cli` ID three ways and on an exact `0.145.0` version. Client identity has no single owner. Proposed fix: release feature bits and a per-recipe launch hook. | Changes to the launcher's contract need native Codex acceptance. |
| ALT-6/7 | Codex arguments are rewritten in three passes, and the base URL is derived twice. Proposed fix: parse once into a `codexInvocation`, and reuse `clienttarget.Profile.Resolve`. | Needs parity table tests before switching. |
| ALT-8 | Archive change detection tolerates WAL sidecars through a hand-written WAL header parser. Proposed fix: checkpoint and set `journal_mode=DELETE` under the maintenance lease. | Touches the on-disk archive lifecycle, and archives already on disk would need conversion. |
| EFF-2/5/6 | On the egress path, each same-dialect request is re-decoded, re-validated and deep-copied 4–6 times, about 100–200 ms per 2.4 MB body. | These passes may be deliberate defence in depth on the egress boundary. The owner should decide before any is removed. |
| EFF-4 | `openairesponses` re-runs the duplicate-key check on input items cut from a root that was already checked. | The fix threads a "names already validated" mode through the decoder. If that is done wrong, the documented duplicate-key rule silently weakens. Needs its own focused change and tests. |
| EFF-10 | Content recording hashes and copies the full history twice per exchange. | Medium-risk restructuring of recording. |
| SIMP-4 | The schema upgrade is copied per revision (1→2, 2→3). | Could become a table-driven runner. Merging `LegacyDigest` and `SourceDigest` would change the receipt format used during an upgrade. |
| REUSE-3 | Strict JSON decoding is implemented in several places (`protocolcore`, `releasemanifest`, `acceptancereport`, `localca`, `systemtrust`). | Some copies reject only exact duplicate keys, so unifying them changes what is accepted. Error text differs too. |
| REUSE-5 | Display-name and ASCII-ID validators are repeated in about 15 packages. | Each site needs its own diff, because trimming and limits differ. |

**Acceptance harness**

- SIMP-1: deleted 42 functions and types from `cmd/vibermate-acceptance`
  (about 2,100 lines), unused in every build including the synthetic tags.
  They were scenarios such as `runToolApproval`, `runHeldIngressPreflight`,
  `runCodexResume`, `runHeldStreaming` and `runAgentInterrupt`, plus their
  helpers, with no callers since `e91ba03e` (2026-08-08). No document,
  workflow or contract test referenced them; git history keeps them.

**Bug fix found during the review**

- ALT-11: five header sanitizers read only the first `Connection` line with
  `Header.Get`. RFC 9110 allows the header on several lines, so a field named
  on a later line was forwarded. The affected paths are provider and original
  egress, loopback forwarding, response copying and the Exchange response
  envelope. They now share `hopbyhop.RemoveConnectionNamed`, which reads every
  line. Tests for each site failed before the fix and pass after it.

**Checked, not a bug**

- `nativemcp.finishUncertain` (formerly `finishFailure`) records every failure
  as `execution_uncertain`. Every call site runs after `Store.Start`, so the
  request may already have been sent, and uncertain is the correct outcome.
  The `nativetool.ErrDenied` argument at one call site was a misleading label
  for a receipt mismatch, and its `cause` was never used.

## Skipped

| ID | Finding | Why skipped |
|---|---|---|
| SIMP-14 | Compatibility shims: the old `self_signed_tls` transport spelling and the old login-directory migration. | Kept on purpose. The login migration was requested and added on 2026-10-16 (`31057526`), the same day the supported schema upgrade landed. Public releases preserve existing installs, and a deployed server may still start with `--transport self_signed_tls`. |
| ALT-10 | `responsesDataKeepaliveFor` picks keepalive by origin for one operation and by feature bit for another. | Older Codex releases would first need the feature bit set, so changing this alters behavior. |
| SIMP-3 (partial) | The agent said the recorder's `sealed` state was never set, so the seal code was dead. | Wrong: recording maintenance and storage moves do set it. Only the unused `recordingArchiveBarrier` was removed. |
| ALT-9 | Connection failures are classified by matching error text. Proposed: an error type checked with `errors.As`. | These errors pass through `crypto/tls` handshakes. Substring matching survives wrapping with `%v`; `errors.As` might not. |
| ALT-12 (history) | History permission is checked both before acquiring a credential and inside the request. | The early check is deliberate: it denies the read before any credential is acquired. |
| Unused `ctx` parameters | Reported by `unparam` on audit helpers. | Keeping `ctx` is idiomatic Go and cheap; removing it is churn. |
| REUSE-1, REUSE-4, SIMP-12 | Acceptance manifest contract exported for the verifier; one problem+json writer; a generic code-library publish helper. | Medium-sized refactors with small payoff. Left for a focused change. |
| Dart | Duplicated `_localizedCopy`, `_bytes`, time and number formatters. Tool arguments are pretty-printed again on every rebuild. | No Flutter or Dart toolchain is available here, so `dart format`, `analyze` and `test` cannot verify the edits. |
