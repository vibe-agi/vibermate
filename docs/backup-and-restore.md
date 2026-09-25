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
