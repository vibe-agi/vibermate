package runtimepersistence

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

func approvalOwnerActive(ctx context.Context, tx *sql.Tx, r toolapproval.Record, now time.Time) (bool, error) {
	if r.OwnerKind == toolapproval.OwnershipUnknown || r.OwnerKind == toolapproval.OwnershipShared {
		return true, nil
	}
	query := `SELECT EXISTS(SELECT 1 FROM runtime_storage_state WHERE singleton=1 AND archive_epoch=?)`
	args := []any{r.OwnerScope.Fence.ArchiveEpoch}
	if r.OwnerKind == toolapproval.OwnershipCapture {
		query += ` AND EXISTS(SELECT 1 FROM recording_capture_fences WHERE capture_kind=? AND capture_id=? AND generation=? AND deleted=0)`
		args = append(args, r.OwnerScope.Capture.Kind, r.OwnerScope.Capture.ID, r.OwnerScope.Fence.CaptureGeneration)
		if r.OwnerScope.Capture.Kind == captureidentity.KindManualCapture {
			query += ` AND EXISTS(SELECT 1 FROM manual_captures WHERE capture_id=? AND state='active' AND (expires_at_unix_ms IS NULL OR expires_at_unix_ms>?))`
		} else {
			query += ` AND EXISTS(SELECT 1 FROM capture_runs WHERE run_id=? AND state IN('created','attached') AND expires_at_unix_ms>?)`
		}
		args = append(args, r.OwnerScope.Capture.ID, now.UnixMilli())
	}
	var allowed bool
	err := tx.QueryRowContext(ctx, query, args...).Scan(&allowed)
	return allowed, err
}
func (repository *toolApprovalRepository) CancelOwnedCapture(ctx context.Context, capture captureidentity.Reference, reason string, now time.Time, limit int) ([]toolapproval.Record, error) {
	if capture.Validate() != nil || reason == "" || now.IsZero() || limit < 1 || limit > 100 {
		return nil, toolapproval.ErrInvalidApproval
	}
	if !repository.controlOwnership {
		return nil, nil
	}
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	tx, err := repository.database.BeginTx(operation, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(operation, `SELECT approval_id FROM tool_approvals WHERE owner_kind='capture' AND owner_capture_kind=? AND owner_capture_id=? AND state='pending' ORDER BY approval_id LIMIT ?`, capture.Kind, capture.ID, limit)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	records := make([]toolapproval.Record, 0, len(ids))
	for _, id := range ids {
		if _, err = tx.ExecContext(operation, `UPDATE tool_approvals SET state='canceled',revision=revision+1,decision_reason=?,resolved_at_unix_ms=max(?,created_at_unix_ms) WHERE approval_id=? AND state='pending'`, reason, now.UnixMilli(), id); err != nil {
			return nil, err
		}
		record, err := repository.get(operation, tx, id)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("cancel owned approvals: %w", err)
	}
	return records, nil
}
