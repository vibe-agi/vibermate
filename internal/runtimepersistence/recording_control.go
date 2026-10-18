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
type RecordingCleanupState struct {
	RequestedRevision, CompletedRevision, SourceGeneration, ActiveRevision uint64
	CutoffMillis, ActiveCutoffMillis                                       int64
}

func (s *ControlStore) RecordingCleanupState(ctx context.Context) (state RecordingCleanupState, err error) {
	operation, finish, err := s.store.operations.begin(ctx)
	if err != nil {
		return state, err
	}
	defer finish()
	err = s.store.reads.QueryRowContext(operation, `SELECT requested_revision,completed_revision,source_generation,active_revision,cutoff_unix_ms,active_cutoff_unix_ms FROM recording_cleanup_state WHERE singleton=1`).Scan(&state.RequestedRevision, &state.CompletedRevision, &state.SourceGeneration, &state.ActiveRevision, &state.CutoffMillis, &state.ActiveCutoffMillis)
	return state, err
}
func requestRecordingCleanup(ctx context.Context, tx *sql.Tx, now time.Time) error {
	result, err := tx.ExecContext(ctx, `UPDATE recording_cleanup_state SET requested_revision=requested_revision+1,cutoff_unix_ms=max(cutoff_unix_ms,?) WHERE singleton=1`, now.UTC().UnixMilli())
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrInspectionUnavailable
	}
	return nil
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
	if err = requestRecordingCleanup(operation, tx, time.Now().UTC()); err != nil {
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

// RequestRecordingCleanup publishes explicit/routine retention intent only.
// It does not open optional sources or wait for the physical owner.
func (s *ControlStore) RequestRecordingCleanup(ctx context.Context, now time.Time) error {
	if now.IsZero() || now.UnixMilli() <= 0 {
		return ErrInspectionUnavailable
	}
	operation, finish, err := s.store.operations.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	tx, err := s.store.database.BeginTx(operation, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = requestRecordingCleanup(operation, tx, now); err != nil {
		return err
	}
	return tx.Commit()
}

// ClaimRecordingCleanup returns target, catalog generation and frozen cutoff.
// A resumed active claim retains exactly those coordinates across newer intent.
func (s *ControlStore) ClaimRecordingCleanup(ctx context.Context) (target, generation uint64, cutoff int64, result error) {
	operation, finish, err := s.store.operations.begin(ctx)
	if err != nil {
		return 0, 0, 0, err
	}
	defer finish()
	result = fullSynchronousTransaction(operation, s.store.database, func(tx *sql.Tx) error {
		var requested, completed uint64
		var wantedCutoff int64
		if err := tx.QueryRowContext(operation, `SELECT requested_revision,completed_revision,source_generation,cutoff_unix_ms,active_revision,active_cutoff_unix_ms FROM recording_cleanup_state WHERE singleton=1`).Scan(&requested, &completed, &generation, &wantedCutoff, &target, &cutoff); err != nil {
			return err
		}
		if target != 0 || requested == completed {
			return nil
		}
		target, cutoff = requested, wantedCutoff
		generation++
		_, err := tx.ExecContext(operation, `UPDATE recording_cleanup_state SET active_revision=?,active_cutoff_unix_ms=?,source_generation=? WHERE singleton=1 AND active_revision=0`, target, cutoff, generation)
		return err
	})
	return
}

// CompleteRecordingCleanup is called only after the owned receipt and every
// declared current/legacy file have been physically verified. It cannot erase
// newer desired work or acknowledge a different source owner.
func (s *ControlStore) CompleteRecordingCleanup(ctx context.Context, target, generation uint64) error {
	if target == 0 || generation < 2 {
		return ErrInspectionChanged
	}
	operation, finish, err := s.store.operations.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	return fullSynchronousTransaction(operation, s.store.database, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(operation, `UPDATE recording_cleanup_state SET completed_revision=?,active_revision=0,active_cutoff_unix_ms=0 WHERE singleton=1 AND active_revision=? AND source_generation=?`, target, target, generation)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrInspectionChanged
		}
		return nil
	})
}
