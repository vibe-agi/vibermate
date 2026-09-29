# One schema revision, without migrations

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
