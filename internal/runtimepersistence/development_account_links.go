package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// These are the local OAuth work-in-progress shapes, not public schema
// versions. Preserve their test accounts, secret refs and audit evidence while
// completing the independent-account baseline. Unknown schemas still fail
// closed; the public schema remains v1.
const ownedAccountDevelopmentDigest = "4fe2e3bc8d4f049074ad0296f5d342c59287bd01ba0dfb1558c020a1259d2492"
const independentAccountDevelopmentDigest = "c195ca944ba7cd585fe761499120b191af6856c29576e8e4dee2ec112e96f0b1"
const accountNotesDevelopmentDigest = "897a6ec14c0a60c8809b2de4972e82dc6447ed47bcef5945f2dd1a4c08657db5"

const accountNoteColumnsSQL = "  note TEXT NOT NULL DEFAULT '' CHECK(length(note) <= 256),\n  note_revision INTEGER NOT NULL DEFAULT 0 CHECK(note_revision BETWEEN 0 AND 9223372036854775807),\n"

func detachDevelopmentAccounts(ctx context.Context, tx *sql.Tx, digest string) error {
	var identity, previous string
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT schema_identity, schema_revision, schema_source_sha256 FROM runtime_metadata WHERE singleton = 1`).Scan(&identity, &revision, &previous); err != nil {
		return fmt.Errorf("%w: read account schema binding: %v", ErrSchemaBaselineMismatch, err)
	}
	if identity != currentSchemaIdentity || revision != currentSchemaRevision ||
		(previous != ownedAccountDevelopmentDigest && previous != independentAccountDevelopmentDigest && previous != accountNotesDevelopmentDigest) {
		return nil // The normal baseline check remains authoritative.
	}
	if previous == ownedAccountDevelopmentDigest {
		// Use the current table definition so this narrow conversion cannot drift
		// from clean installations. The enclosing transaction also covers metadata.
		const tableStart = `CREATE TABLE "provider_accounts"(`
		_, definition, found := strings.Cut(schemaSQL, tableStart)
		definition, _, terminated := strings.Cut(definition, ") STRICT;")
		if !found || !terminated {
			return fmt.Errorf("%w: account table definition missing", ErrSchemaBaselineMismatch)
		}
		if _, err := tx.ExecContext(ctx, `CREATE TABLE provider_accounts_detached(`+definition+") STRICT;"); err != nil {
			return fmt.Errorf("prepare independent accounts: %w", err)
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO provider_accounts_detached(
		account_id, display_name, credential_origin, endpoint_associations, association_revision,
		realm_id, driver_ref, secret_reference, state, revision, created_at_unix_ms, updated_at_unix_ms)
		SELECT a.account_id, a.display_name, e.origin, json_array(a.upstream_endpoint_id), 1,
		a.realm_id, a.driver_ref, a.secret_reference, a.state, a.revision, a.created_at_unix_ms, a.updated_at_unix_ms
		FROM provider_accounts a JOIN upstream_endpoints e ON e.endpoint_id = a.upstream_endpoint_id`)
		if err != nil {
			return fmt.Errorf("preserve independent accounts: %w", err)
		}
		var count int64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM provider_accounts`).Scan(&count); err != nil {
			return err
		}
		copied, err := result.RowsAffected()
		if err != nil || copied != count {
			return fmt.Errorf("%w: account preservation count mismatch", ErrSchemaBaselineMismatch)
		}
		if _, err := tx.ExecContext(ctx, `DROP TABLE provider_accounts;
		ALTER TABLE provider_accounts_detached RENAME TO provider_accounts;
		CREATE INDEX provider_accounts_origin_state ON provider_accounts(credential_origin, state, account_id);`); err != nil {
			return fmt.Errorf("finish independent accounts: %w", err)
		}
	} else {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE provider_accounts ADD COLUMN note TEXT NOT NULL DEFAULT '' CHECK(length(note) <= 256);
		ALTER TABLE provider_accounts ADD COLUMN note_revision INTEGER NOT NULL DEFAULT 0 CHECK(note_revision BETWEEN 0 AND 9223372036854775807);`); err != nil {
			return fmt.Errorf("add account notes: %w", err)
		}
	}
	if previous != accountNotesDevelopmentDigest {
		if err := widenEgressPurposeCatalog(ctx, tx); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runtime_metadata SET schema_source_sha256 = ? WHERE singleton = 1`, digest); err != nil {
		return fmt.Errorf("record independent account schema: %w", err)
	}
	return nil
}

// Keep local unreleased evidence and its pagination sequence when adding the
// two account-related purposes. This stays inside the same all-or-nothing
// development-baseline conversion; arbitrary schemas are never accepted.
func widenEgressPurposeCatalog(ctx context.Context, tx *sql.Tx) error {
	const tableStart = `CREATE TABLE runtime_egress_attempts(`
	_, definition, found := strings.Cut(schemaSQL, tableStart)
	definition, _, terminated := strings.Cut(definition, ") STRICT;")
	if !found || !terminated {
		return fmt.Errorf("%w: egress table definition missing", ErrSchemaBaselineMismatch)
	}
	var sequence int64
	err := tx.QueryRowContext(ctx, `SELECT seq FROM sqlite_sequence WHERE name = 'runtime_egress_attempts'`).Scan(&sequence)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE runtime_egress_accounts(`+definition+") STRICT;"); err != nil {
		return err
	}
	var count int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runtime_egress_attempts`).Scan(&count); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO runtime_egress_accounts SELECT * FROM runtime_egress_attempts`)
	if err != nil {
		return err
	}
	copied, err := result.RowsAffected()
	if err != nil || copied != count {
		return fmt.Errorf("%w: egress preservation count mismatch", ErrSchemaBaselineMismatch)
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE runtime_egress_attempts;
		ALTER TABLE runtime_egress_accounts RENAME TO runtime_egress_attempts;`); err != nil {
		return err
	}
	for _, statement := range strings.Split(schemaSQL, ";") {
		if strings.HasPrefix(strings.TrimSpace(statement), "CREATE INDEX runtime_egress_attempts_") {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sqlite_sequence SET seq = max(seq, ?) WHERE name = 'runtime_egress_attempts'`, sequence); err != nil {
		return err
	}
	return nil
}
