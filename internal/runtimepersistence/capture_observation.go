package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/recording"
)

func validCaptureObservation(scope recording.Scope, revision uint64) bool {
	return scope.Validate() == nil && scope.Capture != (captureidentity.Reference{}) && revision <= math.MaxInt64 && ((scope.Capture.Kind == captureidentity.KindManagedRun && revision == 0) || (scope.Capture.Kind == captureidentity.KindManualCapture && revision > 0))
}
func (s *ObservationStore) PutCaptureObservation(ctx context.Context, scope recording.Scope, v recording.CaptureObservation) (writeErr error) {
	defer func() { writeErr = recordingWriteError(writeErr) }()
	if !validCaptureObservation(scope, v.CredentialRevision) || v.ObservedAt.UnixMilli() <= 0 {
		return errors.New("invalid Capture observation")
	}
	operation, finish, err := s.store.operations.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	_, err = s.store.database.ExecContext(operation, `INSERT INTO capture_observations(capture_kind,capture_id,archive_epoch,capture_generation,credential_revision,first_observed_at_unix_ms,last_observed_at_unix_ms) VALUES(?,?,?,?,?,?,?)
 ON CONFLICT(capture_kind,capture_id,archive_epoch,capture_generation,credential_revision) DO UPDATE SET first_observed_at_unix_ms=MIN(first_observed_at_unix_ms,excluded.first_observed_at_unix_ms),last_observed_at_unix_ms=MAX(last_observed_at_unix_ms,excluded.last_observed_at_unix_ms)`, scope.Capture.Kind, scope.Capture.ID, scope.Fence.ArchiveEpoch, scope.Fence.CaptureGeneration, v.CredentialRevision, v.ObservedAt.UnixMilli(), v.ObservedAt.UnixMilli())
	return err
}

// CaptureObservation is inspection only. A missing row is unknown evidence,
// never proof that this Capture did not authenticate or send traffic.
func (s *ObservationStore) CaptureObservation(ctx context.Context, scope recording.Scope, revision uint64) (recording.CaptureTimes, error) {
	if !validCaptureObservation(scope, revision) {
		return recording.CaptureTimes{}, errors.New("invalid Capture observation scope")
	}
	operation, finish, err := s.store.operations.begin(ctx)
	if err != nil {
		return recording.CaptureTimes{}, err
	}
	defer finish()
	var first, last int64
	err = s.store.reads.QueryRowContext(operation, `SELECT first_observed_at_unix_ms,last_observed_at_unix_ms FROM capture_observations WHERE capture_kind=? AND capture_id=? AND archive_epoch=? AND capture_generation=? AND credential_revision=?`, scope.Capture.Kind, scope.Capture.ID, scope.Fence.ArchiveEpoch, scope.Fence.CaptureGeneration, revision).Scan(&first, &last)
	if err != nil {
		return recording.CaptureTimes{}, err
	}
	return recording.CaptureTimes{First: time.UnixMilli(first).UTC(), Last: time.UnixMilli(last).UTC()}, nil
}

// Called after successful admission/commit with already loaded facts. This
// pure recording port performs no storage work and its refusal is irrelevant
// to the authority result.
func observeCapture(observer recording.CaptureObserver, scopes recording.ScopeIssuer, capture captureidentity.Reference, revision uint64, at time.Time, original recording.Fence) {
	if observer != nil && scopes != nil {
		stamp := recording.StampOriginal(scopes, recording.Scope{Capture: capture, Fence: original})
		if !stamp.Current() || stamp.Fence() != original || !stamp.MatchesCapture(capture) {
			return
		}
		observer.ObserveCapture(stamp, recording.CaptureObservation{ObservedAt: at, CredentialRevision: revision})
	}
}

// The same required authority SELECT freezes these scalar coordinates. Missing
// optional coordinates suppress evidence; they do not create authority.
type captureObservationScan struct {
	row   *sql.Row
	fence *recording.Fence
}

func (s captureObservationScan) Scan(dest ...any) error {
	return s.row.Scan(append(dest, &s.fence.ArchiveEpoch, &s.fence.CaptureGeneration)...)
}

const captureObservationEpoch = `COALESCE((SELECT archive_epoch FROM runtime_storage_state WHERE singleton=1),0)`
