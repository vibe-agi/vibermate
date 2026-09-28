# Offline conversion and independent candidate — 2026-09-28

Scope: VIBERMATE-45. The authorized final cutover is complete: both App locations
use signed/notarized 0.1.16 build 18, and the selected data directory is a fresh,
verified current-v1 conversion. The original data and old Apps are retained;
the user's App was not launched. Historical rehearsal entries below retain their
original timing and do not supersede the final receipt at the end.

## Architecture and preservation

The Runtime now initializes one current v1 baseline. ACP and user policies join
the already consolidated account settings, launch Git and usage tables; the two
remaining additive initializers and their schema metadata have been removed.
Schema revision remains 1; the exact source digest rejects an older shape.

`tool/convert-v1` is an operator-only executable, not linked into either Runtime
binary. It accepts the frozen 0.1.15 digest
`94865976df1130b198b9dbe28da1a249a0082adf9e797f43cb96005904ec7914`
and its four known extension digests, not arbitrary historical formats.

It reuses the existing exclusive directory locks and verified copy operation.
Original, full private backup and converted target are distinct. File-backed
credentials, configuration and CA files are copied without interpreting them;
Keychain and external TLS files are not accessed. This full copy is intentionally
different from the credential-excluding portable backup export.

The new database is built in one transaction against a read-only attachment of
the backup. Every retained table gets row-count and bidirectional value checks;
unknown tables/columns or executable schema objects reject conversion. Integrity,
foreign keys, account decoding and Git/usage validation run before promotion.
Historical sequence high watermarks survive. A receipt is written only after
successful promotion into the new target, never over the original or backup.

| Changed shape | Explicit conversion |
| --- | --- |
| ProviderAccount settings | Revision 1, inherit egress; preserve existing OAuth auto-refresh behavior; no token refresh |
| Launch Git | Preserve clone key/name/branch and mark source `local`; no present-day remote lookup |
| Usage Git attribution | Same fingerprint, `git:` → `git.local:`; preserve caller, usage, expiry and consent |
| Egress history | New account/settings/proxy revision evidence remains unknown, never reconstructed |
| Usage query columns | Derived from preserved observation JSON; new stable chronological local sequence |
| ACP/user-policy extensions | Preserve data in main baseline; retire only extension metadata |

The first end-to-end fixture caught a real preflight problem: opening an
unsupported DELETE-journal database enabled WAL before the schema transaction
rejected it, changing the file header. Runtime Open now performs a read-only
baseline check before constructing its writer. The fixture verifies that even
an attempted new-Runtime open leaves the rejected source bytes unchanged.

An initial fixture had a known token value without a protocol source; strict
validation correctly rejected it. The fixture was corrected to include the
source, not by weakening validation.

## Verification

Commands completed successfully:

```sh
go test ./tool/convert-v1 ./internal/runtimepersistence ./internal/runtimedata -count=1
go test -race ./tool/convert-v1 -count=1
go test ./...
go run ./cmd/repositorycheck
flutter analyze --no-pub
ui/flutter_app/tool/build_macos_app.sh candidate
```

Conversion checks cover account identity/state/notes/credential references,
Git, enabled usage consent and unknown token values, user policy, ACP records,
sequence watermark 700, private files, successful reopen, uncheckpointed WAL,
13 rejection scenarios (lock/path/cancellation and unexpected/invalid data),
and unchanged source bytes. The entire old SQL fixture is frozen in testdata;
it is not compiled into production conversion or Runtime executables.

## Candidate evidence

Candidate: `dist/candidates/local-mUhpONbO/ViberMate.app`.

- Current checkout `09a01d7938645ef030c616db656aaa5a1f189096`, dirty changes recorded
  in the build manifest; macOS arm64, Flutter 3.41.5, Go 1.25.13.
- Version remains the source's `0.1.15+16`; ad-hoc signed local validation only.
  It is **not** a released upgrade to the installed 0.1.15 (17).
- Live bundle verifier passed both before and after copying to the independent
  candidate directory. Packaged CLI, daemon, Web assets and build manifest exist.
- Packaged daemon SHA-256:
  `9cf6a40856d7b3ca389cc81198afddcebf1662e384a55b6311719d477a31c1d5`.
- `/Applications/ViberMate.app` and existing `dist/ViberMate.app` daemon hashes
  both remain
  `b2d337e74aec550aa5e3b5acdbb0ff1eb398a06b18fea8be5c67285ad7e25eb2`.

Actual packaged-daemon smoke fixture:
`/private/tmp/vibermate-v1-candidate.70sRxy/fixture.mjs`.
It runs the packaged daemon with a new private Server data directory, serves
Web assets from the candidate, creates synthetic credentials/configuration,
and sends requests through CONNECT and strict TLS to a loopback-only provider.
Assertions passed for 125 requests in one run (113 success / 12 intentional
failures), 130 across the exact client session, then 131 after another request.
Candidate Web assets returned HTTP 200. The fixture and child exited normally;
the exact child process was confirmed absent. It did not open a browser or GUI.

## Real-database snapshot rehearsal

A live read-only SQLite backup snapshot was rehearsed after the candidate test.
Conversion correctly rejected its table inventory: two retired
`capture_account_selection*` tables exist, with two selection rows, but neither
the current source nor the installed hotfix source uses them. No row was silently
dropped. The snapshot, private rehearsal backup and partial target were retained;
the original Runtime continued running. This preservation decision was resolved
explicitly rather than weakening the table-inventory check.
Further read-only inspection found both rows belong to finished runs and both
selection arrays are empty. A narrow, verified retirement rule can handle that
case without reviving runtime support; non-empty selections must not be discarded.

The offline command now recognizes only the exact optional pair's definitions,
metadata revision/digest, empty arrays and existing parent assignments. Both
tables must be present together. It reports their retired counts separately;
original and backup retain their bytes. Nine scenarios cover valid empty rows
and empty tables, non-empty data, hash/revision changes, extra columns, partial
pairs and orphan rows. Normal and race conversion tests passed.

A fresh rehearsal completed against the **static DB-only snapshot**, not the
live Runtime or its credential store:

- Source: `/private/tmp/vibermate-v1-rehearsal.DuVFz3/snapshot/runtime.db`.
- Private backup/converted directories: `/private/tmp/vibermate-v1-verified.kRM2cB`.
- Verified preserved values/counts include 6 accounts, 64 runs and assignments,
  6,529 usage observations, 13,186 activity rows, 178,826 connection events,
  22,023 egress attempts and all retained evidence tables.
- Retired only 2 empty selection rows and their 1 metadata row.
- Source and backup SHA-256 both:
  `9d4138b570d46cb347d871c9799de6cc4188e1167e3902566de689acc0085891`.
- Converted database SHA-256:
  `c40df6b4b70becc32fa3f8ae9954a6131b52cf1faee488e758979d0e33d6f0ee`.
- Current-schema integrity/foreign-key/Git/usage checks and account reader reopen
  passed. No credentials were read, no App was started, no live data was switched.

This is not the full private operational backup needed at cutover: the test
source contains only a consistent SQLite snapshot, not CA/config/secret files.

## Full private operational conversion

After confirming the source App, Runtime and managed launchers were stopped and
the source was not open, the authorized complete-directory conversion finished
at `2026-09-28T14:12:03.117454Z`. Unlike the earlier DB-only rehearsal, this copy
includes configuration, CA and file-backed secrets without interpreting them.
Keychain was neither read nor changed.

- Source: `/Users/null/Library/Application Support/io.vibermate.desktop`.
- Private parent: `/Users/null/Library/Application Support/ViberMate-v1-conversion-20260928.qIT1Wt`;
  separate `backup` and `converted` children.
- Source and backup database SHA-256:
  `9e71df98623f9f0f98487709c3a8d04cf73e140bf6a665927524521f148603bc`.
- Converted database SHA-256:
  `c6127d62bc08f8d8ee12e135b2b5200c0880d1c494dd29b37a37fef6e54c22ca`.
- Current baseline SHA-256, including six content foreign-key indexes:
  `aca772a7d57e0a0e22584f7ab9427db9fbe5a57f5edf6f6ba722f212afb72897`.
- All retained fields/counts verified: 6 accounts, 64 runs/assignments, 6,529
  usage observations, 13,186 activities, 178,826 connection events, 22,023
  attempts and all retained content/raw evidence. Only the verified two empty
  legacy selection rows and one metadata row were retired in the new target.

The matching candidate `local-BChEs00i` passed bundle checks and actual isolated
Web acceptance. Its account-column styling successor `local-OaOh98uf` has now
also passed bundle and actual Web checks; CLI/daemon bytes are identical, so no
additional database conversion is needed. No App preference points at the new target.
If the old Runtime writes again before cutover, re-check and take a fresh full
backup/conversion: this target must not replace newer source records.

## Pre-cutover coordination

The final pre-cutover recheck found the source database no longer matched the
14:12 receipt, so that target was not selected. A new complete private conversion
finished at `2026-09-28T15:11:11.631182Z` under
`/Users/null/Library/Application Support/ViberMate-v1-cutover-20260928.h8epWc`
with distinct `backup` and `converted` children. Source/backup SHA-256:
`beb266ec957adc75acba600aa329288461417ba140ed0c7a6f632b314af59a15`;
converted SHA-256:
`bad2327b855d46df821ddde581868a81e16eb7034bd415f5e7db53f8cf38fa80`.
The baseline and all retained counts/value checks above still match. Both older
rehearsals and the original directory remain available; the receipt does not
explain why the old source bytes changed. No cutover occurred. The source must
still be checked again immediately before switching.

The user explicitly approved updating both App locations and selecting the
verified new directory after formal package checks, without launching the App.
Use the existing `vibermate-storage-v1` directory selection; do not set its optional
`previous` fallback, because an older schema requires restoring the matching old
App as well. The native credential service is fixed at `io.vibermate.desktop`,
independent of directory, so moving the data selection neither exports nor
renames Keychain items.

- Web account-column styling, successful quota/sorting/refresh, and account/team/
  project checks are complete and recorded in the capture-overview review. The
  quota-success fixture used production Server composition with process-local
  synthetic trust roots, not a modification to the packaged daemon or system trust.
- Coordinate actual cutover with a still-stopped, unchanged source; preserve the
  original credential store. Conversion is not installation.
- Final clean-source candidate with the intended release/build number, native
  App acceptance, signing/notarization and release/Homebrew verification.

These were pending at that checkpoint; completion evidence follows. Operator
usage and rollback constraints: [conversion instructions](../../tool/convert-v1/README.md).

## Final authorized cutover — 2026-09-29 Asia/Singapore

The old source changed again while formal release checks were running, so neither
earlier target was selected. A final full private conversion completed at
`2026-09-28T15:58:21.711508Z`, with all 107 historical raw-writer sessions closed.
The byte differences did not establish their cause; no cause is inferred here.

- Private parent: `/Users/null/Library/Application Support/ViberMate-v1-final-20260928.Rsrnja`.
- Separate `backup` and `converted` directories; receipt at `converted/conversion.json`.
- Source and backup database SHA-256:
  `7a5de87f31f1f3298bb38b96a89f75c58111af5200920ce83e6337af8953295f`.
- Converted database SHA-256:
  `ed2fb1bb646b1e5e2b65cdcb1de44503d3cdf1228f3e626d3cd21edab9de3b61`.
- Current baseline remains `aca772a7d57e0a0e22584f7ab9427db9fbe5a57f5edf6f6ba722f212afb72897`.
- Preserved counts/values: 6 accounts, 64 runs/assignments, 6,529 usage rows,
  13,186 activities, 178,826 connection events, 22,023 attempts, 6,589 identities,
  and all retained content/raw evidence. Only the previously verified two empty
  selection rows and one metadata row were retired in the new target.

Immediately before replacement, process/open-file checks found no App, daemon or
source database owner, and source/backup hashes still matched. Old Apps were
moved to `apps/Applications-ViberMate.app` and `apps/Dist-ViberMate.app` under the
private parent; the exact signed package replaced `/Applications/ViberMate.app`
and repository `dist/ViberMate.app`. Both installed bundles passed the existing
live bundle verifier and report 0.1.16 build 18.

The mode-0600 `io.vibermate.desktop.storage.json` was atomically installed with
schema `vibermate-storage-v1`, selecting only the final `converted` directory.
It had been absent; no `previous` fallback was added. Post-cutover source,
backup and target hashes still matched the receipt, and process checks confirmed
that no user App or Runtime had been started. Keychain was not read or changed.
The mounted installer and empty staging directories were removed; all backups,
old Apps and original data remain. A private `RESTORE.md` records the coordinated
old-App/old-data rollback boundary; it does not merge future new-format writes.

### Formal artifact and publication evidence

- Source/tag: `v0.1.16` at `7eb2c196a9383790dc9f02800c6d4c3ee3ec2ac1`, merged by
  [PR #24](https://github.com/vibe-agi/vibermate/pull/24).
- [Protected Universal release run](https://github.com/vibe-agi/vibermate/actions/runs/36445659545):
  all five jobs passed, including clean-source/SBOM evidence, Developer ID signing,
  Apple Accepted/stapled notarization and two isolated installed-App launches.
- Local source/payload ledger and signed-installation evidence verifiers passed;
  `stapler validate`, Gatekeeper DMG/App assessment and staged bundle checks passed.
- DMG SHA-256: `505256cd0b7c3d06de4a44ead2b6faf64ee96c2f4bc2ee8e00bb575788cd5e5c`.
- [Linux release run](https://github.com/vibe-agi/vibermate/actions/runs/36448077038):
  x86-64 and ARM64 build/verification passed. Both archive checksums, the DMG and
  SBOM matched their published checksum manifests.
- [v0.1.16 Release](https://github.com/vibe-agi/vibermate/releases/tag/v0.1.16)
  published at `2026-09-28T16:08:49Z`, with six public assets and no private data
  or private signing evidence.
- [Homebrew PR #9](https://github.com/vibe-agi/homebrew-tap/pull/9) passed all four
  jobs in [tap CI](https://github.com/vibe-agi/homebrew-tap/actions/runs/36449002767),
  including strict online cask audit, installation, codesign/Gatekeeper and
  uninstall. It merged as `5546faf2b4f255fa89d32af64d83aa908f387f80` at
  `2026-09-28T16:12:27Z`; the cask pins the published DMG hash and warns existing
  installations to perform the offline conversion before opening the new App.
