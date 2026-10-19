package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/vibe-agi/vibermate/internal/recording"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// The fixed trigger marker is a local optional-discard outcome, never a generic
// SQL constraint failure. Preserve other errors for actual failure reporting.
func recordingWriteError(err error) error {
	if errors.Is(err, recording.ErrObservationDiscarded) {
		return err
	}
	var sqlError *sqlite.Error
	if errors.As(err, &sqlError) && sqlError.Code() == sqlite3.SQLITE_CONSTRAINT_TRIGGER && strings.Contains(sqlError.Error(), "recording_scope_discarded") {
		return errors.Join(recording.ErrObservationDiscarded, err)
	}
	return err
}

// Exact-retry paths may perform no INSERT/UPDATE, so they must check the same
// private writer certificate and original scope explicitly in their writer
// transaction. This is bounded indexed history SQL, never a control query.
func checkRecordingReplayAdmission(ctx context.Context, tx *sql.Tx, scope recording.Scope) error {
	if scope.Validate() != nil {
		return recording.ErrObservationDiscarded
	}
	var private bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM temp.sqlite_schema WHERE name='recording_writer_coverage' AND type='table')`).Scan(&private); err != nil {
		return err
	}
	if !private {
		var enforced bool
		if err := tx.QueryRowContext(ctx, `SELECT enforced FROM recording_admission_state WHERE singleton=1`).Scan(&enforced); err != nil || enforced {
			return recording.ErrObservationDiscarded
		}
		// Standalone fixtures are unarmed. Production exposes a writer only after
		// full publication installed the private connection-bound certificate.
		return nil
	}
	var admitted bool
	err := tx.QueryRowContext(ctx, `SELECT
 EXISTS(SELECT 1 FROM temp.recording_writer_coverage c WHERE c.data_version=(SELECT data_version FROM pragma_data_version) AND (c.identity_count=0 OR EXISTS(SELECT 1 FROM recording_denials d WHERE d.sequence=c.identity_count AND d.chain_hash=c.chain_hash)))
 AND EXISTS(SELECT 1 FROM inspection_mirror_active a JOIN inspection_mirror_generations g USING(generation) WHERE a.singleton=1 AND g.complete=1 AND g.archive_epoch=? AND ((?='' AND ?='' AND ?=0) OR EXISTS(SELECT 1 FROM inspection_mirror_fences f WHERE f.mirror_generation=g.generation AND f.capture_kind=? AND f.capture_id=? AND f.capture_generation=? AND f.deleted=0)))`, scope.Fence.ArchiveEpoch, scope.Capture.Kind, scope.Capture.ID, scope.Fence.CaptureGeneration, scope.Capture.Kind, scope.Capture.ID, scope.Fence.CaptureGeneration).Scan(&admitted)
	if err != nil {
		return err
	}
	if !admitted {
		return recording.ErrObservationDiscarded
	}
	return nil
}
