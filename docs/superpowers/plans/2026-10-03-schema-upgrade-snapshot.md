# Safe Schema Upgrade Snapshot Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans. Implement the single task with TDD and independent spec/quality review before the separate migration task.

**Goal:** Produce and independently verify a private, complete revision-1 rollback snapshot without changing the source or exporting credentials through the existing ordinary backup API.

**Architecture:** `runtimedata` owns whole-directory snapshots and manifests. `runtimepersistence` owns immutable supported schema inspection and offline SQLite validation. Callers already own `runtime-data.lock`; reuse that exact live guard and the exact host lock, never acquire the same lock twice. No schema upgrade or runtime activation occurs in this task.

**Tech Stack:** Go 1.26.8, current modernc SQLite, instanceguard, existing copyVerified, no new dependencies.

**Spec:** `docs/superpowers/specs/2026-10-03-production-readiness-design.md`; master Task 8. The user explicitly approved safe internal data upgrades, preserving all data, with verified backups and no new-format activation after failure.

## Scope and shared interfaces

- Freeze current revision-1 SQL exactly; do not change `schema.sql` or `schemaRevision` yet. Revision 2 remains unsupported until its concrete DDL and migration are separately adopted and reviewed.
- Development operates on synthetic stopped directories only. Never open or migrate the user's running data directory.
- Ordinary `Backup`/`Restore` retain their existing exclusion of provider credential stores. The private migration snapshot includes every present regular persistent file, including local private secret stores and CA/config files. It is not a portable export; external/keychain credentials remain at their existing locations.
- A snapshot is a new private sibling directory, not a source child or existing target. Lock/SHM files are ephemeral exclusions; committed WAL is retained during copying. Unknown files are copied, not silently dropped. Symlinks/devices/sockets fail closed.
- New behavior uses per-instance private dependencies where fault injection is needed; no mutable package globals, test-only public endpoints, or runtime dependencies.

Proposed public interfaces (implementation may keep helpers private; changes to these contracts require controller review):

```go
// runtimepersistence: read-only identity/revision and known-schema validation.
func InspectSchema(ctx context.Context, path string, timeout time.Duration) (SchemaState, error)
// runtimedata: no schema migration, no Store returned, no source-lock reacquisition.
func CreateSchemaUpgradeSnapshot(ctx context.Context, source, target string,
    owner, hostOwner *instanceguard.Guard, createdAt time.Time) error
func ValidateSchemaUpgradeSnapshot(ctx context.Context, directory string) error
func RestoreSchemaUpgradeSnapshot(ctx context.Context, snapshot, target string) error
```

`owner.Path()` must equal `source/runtime-data.lock` and `OwnsDirectory(source)` must be true. Non-nil `hostOwner` must similarly own `source/server.lock`. If nil, acquire that host lock temporarily and retain it through snapshot completion. Caller retains source ownership throughout; a released/wrong guard is an error. Validation and restore lock their independent snapshot directory. Product startup integration is a later task, not a stub added here.

## Task 1: Verified private rollback snapshot and frozen old schema

**Files:** Create `internal/runtimepersistence/schema/v1.sql`, `internal/runtimepersistence/schema_inspection.go`, `internal/runtimepersistence/schema_inspection_test.go`, `internal/runtimedata/schema_snapshot.go`, `internal/runtimedata/schema_snapshot_test.go`. Modify `schema_revision.go`/`offline_validation.go` only for frozen-schema/version-aware helpers, and `runtimedata/move.go`/`backup.go` only for small shared copy/validation helpers if needed. Add paired snapshot/restore notes to `docs/deployment.md` and `.zh-CN.md`. Large-file splitting must be reported rather than done arbitrarily.

**Consumes:** Current v1 SQL and synthetic repository fixtures, Guard ownership, copyVerified and existing backup validation. **Produces:** The three private-snapshot APIs and read-only schema inspector above. No revision bump, migration SQL, native transport, permissions or UI changes.

- [ ] Write RED `TestSchemaUpgradeSnapshotUsesHeldOwnershipAndPreservesPrivateFiles`: seed a real v1 repository fixture with account/config/history/evidence and synthetic private credential/CA files. Stop Store, acquire both exact guards, take a snapshot, independently validate and restore it into another new sibling. Assert every present persistent file and all repository rows survive, private files are 0600/directories 0700, and the source is unchanged. Public Backup still excludes credential stores and its manifest scope remains unchanged.
- [ ] Run the focused failing test before implementation; record actual missing API/behavior failure, not an invented expected result.
- [ ] Freeze exact current schema.sql in schema/v1.sql with embedded v1 SQL. `TestFrozenRevision1SchemaBytes` asserts SHA-256 `393b7d67f8ffd1952fc516a150e1a2a68707b67536fbc2e7f5a7f17208424cb1`. Keep bootstrap revision 1 unchanged. Update comments that formerly forbade any future migration; no claim that upgrade is implemented yet.
- [ ] Implement InspectSchema with the existing read connector, no WAL/journal-mode changes, no path/file creation. Absent/empty DB returns revision 0; valid supported v1 returns exact SchemaState; foreign, unknown revisions and malformed known schema fail closed. Check required sqlite_schema against the frozen schema, not merely a forgeable metadata row. Source inspections must not mutate DB/WAL/file modes. Close rows before subsequent queries on a single connection.
- [ ] Factor supported-version validation so backup/offline validation can later admit a concretely known v2, but this task admits only 1. Unknown revision 2/99 remains rejected. Offline WAL recovery applies only to the independent copied/restored database, not the source inspection. Keep existing backup behavior and collection defaults.
- [ ] Implement exclusive owned snapshot copying without reentrant Acquire(source). Validate clean absolute roots, resolved physical parents, disjoint new target, UTC creation time, live exact guards and cancellation before target creation. Retain an incomplete target on failure with no valid completion manifest. Preserve source authority; do not automatically delete/restore/modify it.
- [ ] Use a distinct manifest `runtime-schema-upgrade/v1`: explicit source schema/version, creation time and sorted complete persistent-file inventory with relative path, size and SHA-256. Include local secrets but never print their bytes or hashes in errors/telemetry. Bounds and strict parsing reject traversal, duplicate/extra/missing entries, links and an ordinary backup pretending to be a migration snapshot. Exclude only documented ephemeral locks/SHM and the manifest itself. No ordinary backup/download API exposes the private snapshot.
- [ ] Copy and byte-verify every source file (including WAL). Validate copied SQLite integrity/FKs and schema before finalizing the inventory because recovery may checkpoint the independent copy. Verify source inventory/contents unchanged across the copy; actual logical row comparison proves committed WAL data survived independently of file-layout changes. Sync copied files and directory entries; write/sync manifest last, then sync target and its parent before success. A valid manifest alone is insufficient if any file differs.
- [ ] ValidateSchemaUpgradeSnapshot independently reopens the copy, verifies the entire manifest inventory, revision/schema and integrity/FKs, and retains exclusive ownership through validation. It must detect tampered private files and unsupported DBs, not only CA/runtime.db changes. Restoration first verifies the snapshot, copies to a new disjoint target, strips the snapshot manifest, preserves all private files and independently verifies restored data; it never overwrites an existing directory.
- [ ] Add named cases for committed uncheckpointed WAL; wrong/released source guard; missing host guard with a live attached server; correct already-held host guard; unknown/foreign schema; tampering/missing/extra file; source mutation during copy; copy/write/sync/cancellation failure; invalid/existing/nested/alias targets; busy snapshot during validate/restore. Inject faults through a private per-operation runner, with deterministic barriers where needed. Assert no source writes and no successful-completion manifest after failures, and that locks are not leaked.
- [ ] Include a real restore proof: original Environment canonical bytes/digest, history/evidence and private secret bytes survive; original account secret references still resolve under the unchanged secretstore format. No reauthorization/account relinking is required for locally stored synthetic credentials. External/keychain credentials are explicitly not exported.
- [ ] Document EN/zh-CN: private snapshot contains secrets; keep permissions/private location; stop every host before restore; validate and restore into a new directory; retain original source; external credentials stay on their original host. Do not expose an unimplemented upgrade command or promise portable keychain export.
- [ ] GREEN once at final task SHA:

```sh
DEVELOPER_DIR=/Library/Developer/CommandLineTools go test ./internal/runtimedata ./internal/runtimepersistence ./internal/instanceguard -count=1
DEVELOPER_DIR=/Library/Developer/CommandLineTools go test -race ./internal/runtimedata ./internal/runtimepersistence ./internal/instanceguard -count=1
DEVELOPER_DIR=/Library/Developer/CommandLineTools go run ./cmd/repositorycheck
DEVELOPER_DIR=/Library/Developer/CommandLineTools GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -tags vibermate_native_secrets ./...
```

- [ ] Commit only this task's files, self-review, write the task report with actual RED/GREEN outputs, affected commands, source SHA and concerns. Controller performs independent spec/security/durability review. Native Linux qualification remains a later actual OS gate; Windows build is not crash-durability evidence.

## Controller self-review / next dependency

This bounded task is executable against the current v1 source, without waiting for native DDL. It neither accepts nonexistent v2 schemas nor calls an incomplete migration API. Whole-directory manifest ownership remains in runtimedata; future runtimepersistence migration receipts cover DB preservation only, avoiding an import cycle. The later transaction/startup plan consumes this verified snapshot plus independently adopted exact schema-2 bootstrap/delta; its commit reconciliation and native once/approval recovery are separate mandatory gates. Snapshot success alone does not establish migration or MCP support.
