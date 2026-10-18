package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/resourcedeletion"
)

// RecordingCleanupState coalesces arbitrary deletion/retention requests into
// one full-inventory obligation. A completion may acknowledge only its captured
// target, never a newer request. SourceGeneration belongs to physical ownership.
type RecordingCleanupState struct{ RequestedRevision, CompletedRevision, SourceGeneration uint64 }

func (s *ControlStore) RecordingCleanupState(ctx context.Context) (state RecordingCleanupState, err error) {
	operation, finish, err := s.store.operations.begin(ctx)
	if err != nil {
		return state, err
	}
	defer finish()
	err = s.store.reads.QueryRowContext(operation, `SELECT requested_revision,completed_revision,source_generation FROM recording_cleanup_state WHERE singleton=1`).Scan(&state.RequestedRevision, &state.CompletedRevision, &state.SourceGeneration)
	return state, err
}
func requestRecordingCleanup(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `UPDATE recording_cleanup_state SET requested_revision=requested_revision+1 WHERE singleton=1`)
	return err
}

// DeleteCapture commits required authority removal, its permanent original-
// generation tombstone and a cleanup obligation together. It never touches an
// observation handle. Released reports only these committed required rows;
// optional physical removal is acknowledged separately by the cleanup owner.
func (s *ControlStore) DeleteCapture(ctx context.Context, kind, id string) (released resourcedeletion.Released, result error) {
	ref := captureidentity.Reference{Kind: captureidentity.Kind(kind), ID: id}
	if s == nil || ctx == nil || ref.Validate() != nil {
		return released, ErrInvalidCaptureTarget
	}
	operation, finish, err := s.store.operations.begin(ctx)
	if err != nil {
		return released, err
	}
	defer finish()
	tx, err := s.store.database.BeginTx(operation, nil)
	if err != nil {
		return released, err
	}
	defer tx.Rollback()
	table, column := "capture_runs", "run_id"
	if ref.Kind == captureidentity.KindManualCapture {
		table, column = "manual_captures", "capture_id"
	}
	var exists int
	if err = tx.QueryRowContext(operation, `SELECT EXISTS(SELECT 1 FROM `+table+` WHERE `+column+`=?)`, id).Scan(&exists); err != nil {
		return released, err
	}
	if exists != 1 {
		return released, resourcedeletion.ErrTargetNotFound
	}
	fence, err := tx.ExecContext(operation, `UPDATE recording_capture_fences SET deleted=1 WHERE capture_kind=? AND capture_id=? AND deleted=0`, kind, id)
	if err != nil {
		return released, err
	}
	count, err := fence.RowsAffected()
	if err != nil || count != 1 {
		return released, errors.Join(ErrInspectionUnavailable, err)
	}
	if _, err = tx.ExecContext(operation, `UPDATE runtime_storage_state SET visibility_revision=visibility_revision+1 WHERE singleton=1`); err != nil {
		return released, err
	}
	if err = requestRecordingCleanup(operation, tx); err != nil {
		return released, err
	}
	if err = revokeNativeScope(operation, tx, ref, time.Now().UTC()); err != nil {
		return released, err
	}
	if released.Approvals, err = deleteCounted(operation, tx, `DELETE FROM native_tool_approvals WHERE operation_id IN(SELECT operation_id FROM native_tool_operations WHERE capture_kind=? AND capture_id=?)`, kind, id); err != nil {
		return resourcedeletion.Released{}, err
	}
	if _, err = tx.ExecContext(operation, `DELETE FROM native_tool_operations WHERE capture_kind=? AND capture_id=?`, kind, id); err != nil {
		return resourcedeletion.Released{}, err
	}
	if _, err = tx.ExecContext(operation, `UPDATE native_tool_scopes SET state='closed',replay_barrier=1 WHERE capture_kind=? AND capture_id=?`, kind, id); err != nil {
		return resourcedeletion.Released{}, err
	}
	if released.Assignments, err = deleteCounted(operation, tx, `DELETE FROM capture_environment_assignments WHERE capture_kind=? AND capture_id=?`, kind, id); err != nil {
		return resourcedeletion.Released{}, err
	}
	if released.Captures, err = deleteCounted(operation, tx, `DELETE FROM `+table+` WHERE `+column+`=?`, id); err != nil {
		return resourcedeletion.Released{}, err
	}
	if err = tx.Commit(); err != nil {
		return resourcedeletion.Released{}, err
	}
	return released, nil
}
