package runtimepersistence

import (
	"context"
	"database/sql"
	"github.com/vibe-agi/vibermate/internal/recording"
)

// Table and identity column are closed constants at the two internal callers.
func readRecordingScope(ctx context.Context, tx *sql.Tx, table, column, id string) (recording.Scope, error) {
	var s recording.Scope
	err := tx.QueryRowContext(ctx, "SELECT recording_scope_kind,recording_scope_id,recording_archive_epoch,recording_capture_generation FROM "+table+" WHERE "+column+"=? LIMIT 1", id).Scan(&s.Capture.Kind, &s.Capture.ID, &s.Fence.ArchiveEpoch, &s.Fence.CaptureGeneration)
	return s, err
}
