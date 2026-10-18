[English](backup-and-restore.md) · [简体中文](backup-and-restore.zh-CN.md)

# Backup and restore

ViberMate backups are new local directories with a verified manifest. They are
not copies of an open `runtime.db`, cloud uploads, or credential exports.

## What is included

- the consistent SQLite Runtime database, including retained evidence;
- Runtime configuration stored in the data directory;
- the local Proxy CA, including its private key;
- `backup-manifest.json`, with the Runtime schema identity and SHA-256, size,
  and relative path for every included file.

The database and Proxy CA are sensitive and are not encrypted by the backup
format. Server access/recovery configuration inside the Runtime directory is
also retained. Protect the backup directory like the original Runtime data
directory.
The manifest detects accidental change and validates internal consistency; it
is not a digital signature and does not establish provenance against an attacker
who can replace both the files and the manifest.

## What is not included

- provider API keys, OAuth tokens, or the Server `server-secrets` store;
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

Version 0.1.30 reads exact published `vibermate-runtime` revisions 1, 2 and 3.
Backup, verification, move and restore preserve the source revision and values;
restoring does not modify or upgrade the backup source. Starting the new Runtime
on the restored destination performs any required owned upgrade: 1→2→3 or 2→3,
with a separate private snapshot for each transition and no normal writers
between them. Current revision 3 preserves actual Standard/Blue/Red account preferences.

A revision 3 backup requires ViberMate 0.1.30 or a later compatible reader.
Older releases refuse revision 3 unchanged; a post-upgrade backup is not an old-reader
rollback artifact. To return to the old format, stop all owners and use the
independently verified private snapshot from before that transition, restoring
into a new directory. A revision1 snapshot remains revision 1; a revision2 snapshot remains revision 2.
Retain the original source and snapshot until the chosen restored Runtime is
verified. There is no in-place downgrade.

An unresolved schema-upgrade receipt blocks normal startup and public backup or
move. Keep it with the source and private snapshot for guarded reconciliation;
do not delete it to force startup. Public backups retain the exclusions above;
private rollback snapshots preserve actual local credential files, but never
export external Keychain entries. See [private rollback snapshots](deployment.md#private-rollback-snapshots).
