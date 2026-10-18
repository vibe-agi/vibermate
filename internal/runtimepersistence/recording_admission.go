package runtimepersistence

import (
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
