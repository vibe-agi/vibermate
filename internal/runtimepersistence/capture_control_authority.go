package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturerun"
)

func (repository *captureRunRepository) AuthorizeControl(ctx context.Context, id string, digest capturerun.CapabilityDigest, now time.Time) (capturerun.DurableRecord, error) {
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return capturerun.DurableRecord{}, err
	}
	defer finish()
	record, err := scanCaptureRun(repository.database.QueryRowContext(operation, `SELECT `+captureRunColumns+` FROM capture_runs WHERE run_id=? AND control_capability_hash=? AND state IN ('created','attached') AND expires_at_unix_ms>? AND `+captureRunIdentityActive, id, digest[:], toUnixMillis(now), toUnixMillis(now)))
	if errors.Is(err, sql.ErrNoRows) {
		return capturerun.DurableRecord{}, capturerun.ErrCapabilityRejected
	}
	if err != nil {
		return capturerun.DurableRecord{}, errors.New("Capture control authority unavailable")
	}
	return record, nil
}
