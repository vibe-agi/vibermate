package runtimepersistence

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
)

//go:embed runtime_user_policy_schema.sql
var runtimeUserPolicySchemaSQL string

// This additive extension is initialized only after the released base schema
// is verified. Existing Runtime Users without a row retain the explicit default:
// every published Environment is allowed and usage warnings are disabled.
func initializeRuntimeUserPolicySchema(ctx context.Context, database *sql.DB) error {
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	digest := sha256.Sum256([]byte(runtimeUserPolicySchemaSQL))
	expected := hex.EncodeToString(digest[:])
	var count int
	if err := transaction.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE name='runtime_user_policy_schema_metadata'`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		if _, err := transaction.ExecContext(ctx, runtimeUserPolicySchemaSQL); err != nil {
			return errors.New("Runtime User policy schema extension could not be initialized")
		}
		if _, err := transaction.ExecContext(ctx, `INSERT INTO runtime_user_policy_schema_metadata VALUES (1,1,?)`, expected); err != nil {
			return err
		}
	} else {
		var revision int
		var digest string
		if err := transaction.QueryRowContext(ctx, `SELECT revision,source_sha256 FROM runtime_user_policy_schema_metadata WHERE singleton=1`).Scan(&revision, &digest); err != nil || revision != 1 || digest != expected {
			return errors.New("unsupported Runtime User policy schema extension")
		}
	}
	return transaction.Commit()
}
