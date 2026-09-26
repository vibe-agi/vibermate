package runtimepersistence

import (
	"context"
	"database/sql"
	"fmt"
)

// v0.1.13 is the exact published SQLite source. Only that digest is eligible
// for this in-place, transactional egress-purpose expansion.
const published013SchemaDigest = "3b9f6827aacfdbc213fabcda34f4b9c37e94402dab176ad45ff922c527a7d142"

func widenReleasedAccountAction(ctx context.Context, tx *sql.Tx, digest string) error {
	var identity, previous string
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT schema_identity, schema_revision, schema_source_sha256 FROM runtime_metadata WHERE singleton = 1`).Scan(&identity, &revision, &previous); err != nil {
		return fmt.Errorf("read released schema binding: %w", err)
	}
	if identity != currentSchemaIdentity || revision != currentSchemaRevision || previous != published013SchemaDigest {
		return nil
	}
	if err := widenEgressPurposeCatalog(ctx, tx); err != nil {
		return fmt.Errorf("preserve released egress audit: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runtime_metadata SET schema_source_sha256 = ? WHERE singleton = 1`, digest); err != nil {
		return fmt.Errorf("record widened released schema: %w", err)
	}
	return nil
}
