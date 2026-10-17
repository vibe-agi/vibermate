package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

// Only the two insertion guards that reject valid final-row import are deferred.
// Other lifecycle/identity guards remain installed. No runtime option exposes it.
func deferExtractionTriggers(ctx context.Context, tx *sql.Tx) ([]string, error) {
	var statements []string
	for _, name := range []string{"native_tool_operation_received", "native_tool_binding_egress_insert_guard"} {
		var statement string
		if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type='trigger' AND name=?`, name).Scan(&statement); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `DROP TRIGGER `+name); err != nil {
			return nil, err
		}
		statements = append(statements, statement)
	}
	return statements, nil
}

func validateExtractedNative(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT operation_id FROM native_tool_operations ORDER BY operation_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return err
		}
		id, err := nativetool.NewID(raw)
		if err != nil {
			return err
		}
		op, err := loadNativeOperation(ctx, tx, id)
		if err != nil {
			return fmt.Errorf("native current projection: %w", err)
		}
		var claims int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM native_tool_once WHERE operation_id=?`, id).Scan(&claims); err != nil {
			return err
		}
		if claims != 0 || op.State == "started" || op.State == "completed" || op.State == "uncertain" {
			if _, _, err = nativeAttemptFacts(ctx, tx, op); err != nil {
				return fmt.Errorf("native delivery proof: %w", err)
			}
		}
	}
	return errors.Join(rows.Err(), rows.Close())
}

func validateExtractionDeliveryConstants(ctx context.Context, tx *sql.Tx) error {
	// Locate by the required claim, never by labels that could hide corruption.
	// Retired barriers and unclaimed audit rows remain independent archive facts.
	return extractionNoRows(ctx, tx, `SELECT 1 FROM native_tool_once w
 JOIN native_tool_operations o ON o.operation_id=w.operation_id
 JOIN runtime_egress_attempts a ON a.attempt_id=w.attempt_id
 WHERE a.purpose IS NOT 'client_tool_service' OR a.parent_kind IS NOT 'client_operation'
 OR a.parent_exchange_id IS NOT '' OR a.caller_kind IS NOT 'core' OR a.caller_id IS NOT ''
 OR a.policy_authority IS NOT 'environment' OR a.account_id IS NOT '' OR a.account_settings_revision IS NOT 0
 OR a.reused_transport IS NOT 0 OR a.parent_id IS NOT o.operation_id`)
}
