[English](backup-and-restore.md) · [简体中文](backup-and-restore.zh-CN.md)

# Backup and restore

ViberMate backups are new local directories with a verified manifest. They are
not copies of an open `runtime.db`, cloud uploads, or credential exports.

## What is included

- required compact control, every declared present observation/legacy archive database,
  and their retained WAL files;
- Runtime configuration stored in the data directory;
- the local Proxy CA, including its private key;
- `storage-layout.json` and recording provisioning/privacy/maintenance metadata;
- `backup-manifest.json`, with schema identities and SHA-256, size, and relative
  path for every included file. Split layouts use `vibermate-data-backup/v2`,
  binding the layout document/ID and each role’s path, identity, revision,
  required flag, status and archive ID. The strict v1 reader remains supported.

Stop the Runtime and attached Server host before creating, validating, moving or
restoring a backup. Validation uses independent database/WAL copies and leaves
the input database bytes unchanged. SQLite SHM files are omitted at declared
role paths; each role’s WAL is retained. Other persistent files keep the normal
backup policy.

A role marked `verified` passed its exact schema, integrity and foreign-key
checks; control also passed required-state invariants, and current observations
passed privacy coverage and known internal stored-format decoders. An existing
legacy role is `unverified`: its schema, inventory and original/maintained
provenance passed, but its optional payload semantics were not exhaustively
reinterpreted. Its bytes are retained. A missing optional role is explicitly
`absent`. Either status sets `historyComplete` to false. Even true means only
that all declared roles passed this backup contract; best-effort recording can
never promise that every activity was captured.

Missing required control, unresolved cleanup intent/claim/receipt or reserved
cleanup artifacts, lost required denial proof, invalid current internal
encodings, and mismatched archive provenance refuse completion. Missing
optional history can be preserved as absent when no required privacy proof
depends on it. Restore retains its provisioning proof and does not silently
create replacement history. Required control can reopen independently.

Cleanup’s payload-free denial/unknown-ownership metadata is retained.
`countsExact=false` in a completed maintenance certificate discloses uncertain
removal totals; it does not mean unfinished work or an exact byte-erasure claim.

The database and Proxy CA are sensitive and are not encrypted by the backup
format. Server access/recovery configuration inside the Runtime directory is
also retained. Protect the backup directory like the original Runtime data
directory.
The manifest detects accidental change and validates internal consistency; it
is not a digital signature and does not establish provenance against an attacker
who can replace both the files and the manifest.

## What is not included

- provider API keys or OAuth tokens in the root `server-secrets` and
  `development-secrets` stores;
- macOS Keychain items;
- certificate/key files passed through `--tls-cert` and `--tls-key`, or files
  managed by an external TLS terminator such as Caddy;
- storage outside the selected Runtime data directory.

On the same Mac, restored database references can continue to use credentials
that still exist in that Mac's Keychain. On another machine, reconnect provider
accounts. For a Server restore, credentials must also be configured again.
External Server HTTPS files must be supplied separately in either case.

The Server credential file is plaintext-equivalent and protected by owner-only
filesystem permissions. On Linux/Unix, a successful credential replacement or
deletion requires a complete temporary-file write, file sync, close, atomic
rename, and parent-directory sync on local persistent storage. Opening the
credential store creates missing components and syncs the exact directory path
and its ancestor entries through the filesystem root, including resolved ancestor
aliases. Every open
repeats these barriers, so retries also certify directories left by a failed
initialization. The Server account needs permission to open and sync this path's
ancestor directories. Existing ancestors above the Server data directory retain
their permissions. Ancestor aliases with noncanonical targets are unavailable;
use the resolved data directory path.
New components and the Server data/credential directories use owner-only
permissions. A failed initialization returns an unavailable-store error and may
leave these directories for the next attempt.

If a credential file commit fails before rename, the confirmed credential remains
usable. If rename succeeds but directory sync fails or the operation is cancelled,
the active credential
store becomes unavailable until reopened; the file may already contain the new
credential. Check storage health and reopen the Server before retrying. Already
issued credential copies retain their existing lifetime. This does not export
credentials into backups or guarantee recovery from failing storage hardware.
Windows compilation compatibility does not establish the same crash-durability
guarantee.

## macOS App

Open **Settings → Safety & data → Evidence storage**. Choose **Create backup**
or **Restore backup**. ViberMate requires captures to be stopped, pauses new
local outbound work, restarts the local Runtime, and shows the exact source and
new target before confirmation.

Restore always creates a new Runtime directory. It never overwrites the current
directory. The App selects the restored directory only after the manifest,
every file hash, SQLite integrity, foreign keys, and schema compatibility pass.
If the restored Runtime cannot start, the existing storage selection is the
rollback source.

## Native or container Server

Stop every Server process that uses the data directory first. Use absolute
paths on local, persistent storage:

```sh
vibermated backup-data \
  --source='/srv/vibermate' \
  --target='/srv/backups/vibermate-2026-09-26'

vibermated verify-backup \
  --source='/srv/backups/vibermate-2026-09-26'

vibermated restore-data \
  --source='/srv/backups/vibermate-2026-09-26' \
  --target='/srv/vibermate-restored'
```

For a container, run the command against mounted host volumes or with a
one-shot container that has both directories mounted. Do not put the active
SQLite directory on a network share or synchronized cloud-drive folder.

Start the Server with the restored directory only after `verify-backup`
succeeds. A missing/changed file, unexpected file, damaged database, broken
foreign key, or incompatible schema fails closed. A failed restore leaves the
existing Runtime directory untouched and never replaces a non-empty or unknown
target. An incomplete newly created target may remain for inspection or manual
removal, but ViberMate never selects it.

For container data volumes and rollback, see [Docker deployment](docker.md).

## Format compatibility and rollback

Published monolithic `vibermate-runtime` revisions 1, 2 and 3 remain readable,
including actual Standard/Blue/Red account preferences. Their public exports
remain v1. Public restore verifies the old copied inventory before performing
owned storage cutover only in the new destination, with both directory guards.
The backup source is never upgraded. The restored layout retains the original
monolith as an archive beside required `vibermate-control/3`; current optional
recording uses `vibermate-observations/1`.

Split-layout public exports use v2. Their restore reproduces the declared role
layout and facts after validation. Current observation bytes are checked as a
stopped snapshot; a historical maintenance hash is not treated as their current
hash. Maintained archives must match their latest certificate while retaining
the unchanged original provenance manifest.

Older releases cannot use split layouts or v2 backups as rollback inputs.
Retain the independent private snapshot from before the transition. Old private
`runtime-schema-upgrade/v1` snapshots restore faithfully at their original
published runtime1/2/3 schema and credentials; a later Runtime admission owns any
upgrade/cutover. Split private snapshots use the separate strict
`runtime-schema-upgrade/v2` format and reproduce that recorded layout, without
implicit cutover. They refuse unresolved split recovery work. Neither private
format is an incomplete recovery export or an in-place downgrade.

An unresolved schema-upgrade receipt blocks normal startup and public backup or
move. Keep it with the source and private snapshot for guarded reconciliation;
do not delete it to force startup. Public backups retain the exclusions above;
private rollback snapshots preserve actual local credential files, but never
export external Keychain entries. See [private rollback snapshots](deployment.md#private-rollback-snapshots).
