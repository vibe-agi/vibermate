# One schema revision, without migrations

Current status (2026-10-16): the no-migrations restriction below is historical.
The supported revision-1-to-revision-2 startup upgrade now preserves existing
data through a verified private snapshot and a transactional database upgrade.
See the current [upgrade and private rollback contract](../deployment.md#private-rollback-snapshots).
Unsupported or unrecognized schemas still fail closed. This clarification
records the implemented behavior; it introduces no new schema or migration.
The original decision follows unchanged.

The Runtime database has one schema, `internal/runtimepersistence/schema.sql`,
identified by the integer `schemaRevision`. A new data directory is created
at that revision. A file with any other revision, or one that is not a
ViberMate database, is refused before any write and left unchanged; the user
starts a new data directory.

There are no migrations and no conversion tools. ViberMate has no external
users, so carrying data between development revisions would add a second
code path for every schema change without anyone to benefit. The earlier
full-text schema digest and the hand-run `tool/convert-v1` converter (retained
in the v0.1.16 tag) are removed.

Any edit to `schema.sql` bumps `schemaRevision`. A test pins the file's
digest to the revision, so an edit without a bump fails in CI instead of
silently opening an old file with a new structure.

Forward migrations become necessary once there are users whose data must
survive an upgrade. That change will be a new decision, starting its chain
from the schema that ships then.
