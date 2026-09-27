package runtimepersistence

import (
	"context"
	"database/sql"
	"fmt"
)

// The exact pre-change baseline. Preserve existing accounts and evidence in
// the schema transaction; unfamiliar databases still fail the baseline check.
const beforeProviderErrorCodeDigest = "48c1565cf70204fb998d44969beb3aa0c0ba5696dcaa45f4265845d70bd968f1"
const providerErrorCodeColumnSQL = "  provider_error_code TEXT NOT NULL DEFAULT '' CHECK(length(provider_error_code) <= 128),\n"

func addProviderErrorCode(ctx context.Context, tx *sql.Tx, digest string) error {
	var identity, previous string
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT schema_identity, schema_revision, schema_source_sha256 FROM runtime_metadata WHERE singleton = 1`).Scan(&identity, &revision, &previous); err != nil {
		return err
	}
	if identity != currentSchemaIdentity || revision != currentSchemaRevision || previous != beforeProviderErrorCodeDigest {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE runtime_activities ADD COLUMN provider_error_code TEXT NOT NULL DEFAULT '' CHECK(length(provider_error_code) <= 128)`); err != nil {
		return fmt.Errorf("add structural provider error code: %w", err)
	}
	_, err := tx.ExecContext(ctx, `UPDATE runtime_metadata SET schema_source_sha256 = ? WHERE singleton = 1`, digest)
	return err
}
