# Offline v1 conversion

This operator-only command converts the exact released 0.1.15 or 0.1.16 baseline
into the current v1 schema. It is not shipped in `vibermate` or `vibermated`, is never run
at startup, and is not a general migration framework. Unknown shapes fail.

Stop the App, its Runtime and all managed Agents first. Use three **absolute,
canonical, non-overlapping paths** with existing parents. Backup and target must
not exist. On macOS, resolve `/var` to `/private/var` before passing paths.

```sh
go run ./tool/convert-v1 \
  --source /absolute/path/runtime-data \
  --backup /absolute/path/runtime-before-conversion \
  --target /absolute/path/runtime-converted
```

The command:

1. Uses the existing Runtime/server locks and verified copy operation to make a
   complete private backup, including any durable WAL, configuration, CA and
   file-backed secrets. It does not read or modify the system Keychain.
2. Copies that backup into a separate working directory; opens the backup
   database read-only; creates the current schema in a private staging directory.
3. Copies every table in one transaction, checks row counts **and all preserved
   field values**, retains AUTOINCREMENT high watermarks, validates Git/usage
   records, current account decoding, foreign keys and database integrity.
4. Promotes only the verified staged database into the **new target**, then
   writes `conversion.json` with schema/file hashes and verified row counts.
   Neither the source nor the backup is overwritten. Partial failed targets are
   retained for inspection; never select a target without a successful command
   and receipt. A retry needs fresh backup/target paths.

This full-copy backup is **not** the credential-excluding `backup-data` export.
It contains sensitive data and must stay private. A Mac Keychain credential
remains on the original Mac; copying this directory does not export it or make
it usable on another host. External TLS files are not copied.

## Explicit conversion decisions

The field conversions below apply to 0.1.15; 0.1.16 already carries those fields
and they are copied unchanged. Both baselines gain verified content-reference
indexes and an empty usage-retention cap table: their existing observation
deadlines already embody all previously consented restrictions.

- Existing OAuth accounts retain the previously active automatic refresh
  behavior as an explicit setting; other accounts remain off. New imports are
  still off by default in the new Runtime. No credential is refreshed.
- Account egress starts at explicit inheritance with settings revision 1.
  Identity, note, credential reference, credential epoch and state are unchanged.
- Old Git snapshots are marked `local`, keeping their original clone fingerprint,
  name and launch branch. Old `git:` grouping IDs become `git.local:` with the
  same fingerprint. No remote is read and no historical project is claimed to
  be shared across machines.
- Historical egress attempts have no observed account settings or proxy revision;
  those fields remain absent/zero. Present account settings are not backfilled.
- Usage consent, retention, expiry, timestamps, tokens and missing values are
  preserved. New query columns are generated from those records. Conversion
  neither enables collection nor resurrects expired/deleted observations.
- ACP, user-policy, launch-Git and usage extension metadata are retired. Their
  data lives in the one current v1 baseline; no extension initializer remains.
- The optional abandoned `capture_account_selection*` pair is retired **only**
  when both table definitions and metadata match the known shape, every
  selection is an empty array, and every row has its original assignment.
  The receipt explicitly counts these retired rows; source and backup retain
  them. Non-empty selections, orphan rows or changed/partial shapes fail closed.

## Selection and rollback

The tool does **not** change App preferences, install/start an App, select the
converted directory, or remove any original data. Inspect the receipt, keep
the Runtime stopped, and validate a candidate against an isolated copy before
coordinating the actual switch. Do not run two Runtime instances against one
directory or one file-backed credential store. If the old Runtime is restarted
and writes new data, discard the proposed cutover and take a fresh backup.

Before any new Runtime writes, rollback means selecting the original directory
with its original App. After new writes, an old backup omits those new writes:
do not switch back silently or feed a converted directory to the old App.

Checks:

```sh
go test ./tool/convert-v1 ./internal/runtimepersistence ./internal/runtimedata -count=1
go test -race ./tool/convert-v1 -count=1
```

The frozen SQL under `testdata/` is test input from released commits `09a01d7`
and `7eb2c19`. Production conversion does not embed it, read Git history, or add
old-schema readers to the Runtime.
