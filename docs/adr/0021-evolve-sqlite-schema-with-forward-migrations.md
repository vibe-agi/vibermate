# Evolve the SQLite schema with forward migrations

The Runtime database schema is defined only by numbered migrations embedded
in the binary (`internal/runtimepersistence/migrations/NNNN_name.sql`). A new
database applies every migration in order; an existing database applies the
migrations after its recorded `schema_revision` in one transaction when the
store opens. A file that is not a ViberMate database, or whose revision is
newer than the build, is refused before any write.

This replaces the earlier single baseline whose full-text SHA-256 had to match
exactly, together with the stop-and-convert tool that every schema change
required (`tool/convert-v1`, retained in the v0.1.16 tag). That design turned
each schema edit, even a comment or an index, into a release-time data
conversion that users had to run by hand, and each converter only understood
one previous baseline.

Rules:

- A shipped migration file is never edited; a change is a new, higher number.
- Migrations run with foreign keys enforced. A table rebuild defers foreign-key
  checks for its own statement group and must preserve every row.
- The runtime reads only the latest structure. Migrations may backfill or
  reshape data, but no code path reads an older layout.
- Backup and restore accept any revision up to the build's latest; restoring an
  older backup migrates it on the next open.

The migration chain starts at revision 1 with the complete current schema.
Databases created by v0.1.16 or earlier have a different identity and are not
opened by this source; there were no external users to carry forward.
