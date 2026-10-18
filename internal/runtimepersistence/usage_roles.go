package runtimepersistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/vibe-agi/vibermate/internal/recording"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
)

type usagePolicyRepository struct{ store *Store }

func (s *ControlStore) UsagePolicyRepository() runtimeusage.PolicyRepository {
	return usagePolicyRepository{s.store}
}
func (r usagePolicyRepository) UsagePolicy(ctx context.Context) (runtimeusage.CollectionPolicy, error) {
	return r.store.UsagePolicy(ctx)
}
func (r usagePolicyRepository) SetUsagePolicy(ctx context.Context, p runtimeusage.CollectionPolicy, at time.Time) (runtimeusage.CollectionPolicy, error) {
	return r.store.setUsagePolicy(ctx, p, at, true)
}
func (s *ObservationStore) PutCollectedUsage(ctx context.Context, v runtimeusage.CollectedObservation) (writeErr error) {
	defer func() { writeErr = recordingWriteError(writeErr) }()
	return s.store.PutCollectedUsage(ctx, v)
}

// PutCollectedUsage consumes only frozen trusted collection coordinates. The
// legacy Store method keeps production composition buildable until the owner
// cutover; it no longer has a worker-side control-policy/attribution bridge.
func (s *Store) PutCollectedUsage(ctx context.Context, v runtimeusage.CollectedObservation) (writeErr error) {
	defer func() {
		writeErr = recordingWriteError(writeErr)
		if errors.Is(writeErr, recording.ErrObservationDiscarded) {
			writeErr = errors.Join(runtimeusage.ErrObservationDiscarded, writeErr)
		}
	}()
	if v.Value.Validate() != nil || v.PolicyRevision < 1 || !v.ExpiresAt.After(v.Value.OccurredAt) {
		return errors.New("invalid collected usage")
	}
	if !v.Stamp.Current() {
		return runtimeusage.ErrObservationDiscarded
	}
	scope := v.Stamp.Scope()
	if scope.Validate() != nil {
		return runtimeusage.ErrObservationDiscarded
	}
	switch scope.Capture.Kind {
	case captureidentity.KindManagedRun:
		if v.Value.CaptureRunID != scope.Capture.ID || v.Value.ManualCaptureID != "" {
			return runtimeusage.ErrObservationDiscarded
		}
	case captureidentity.KindManualCapture:
		if v.Value.ManualCaptureID != scope.Capture.ID || v.Value.CaptureRunID != "" {
			return runtimeusage.ErrObservationDiscarded
		}
	default:
		if v.Value.CaptureRunID != "" || v.Value.ManualCaptureID != "" {
			return runtimeusage.ErrObservationDiscarded
		}
	}
	data, err := json.Marshal(v.Value)
	if err != nil {
		return err
	}
	operation, finish, err := s.operations.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	if !v.Stamp.Current() {
		return runtimeusage.ErrObservationDiscarded
	}
	tx, err := beginObservationTransaction(operation, s.database)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if s.observationOnly {
		var enforced bool
		if err = tx.QueryRowContext(operation, `SELECT enforced FROM recording_admission_state WHERE singleton=1`).Scan(&enforced); err != nil {
			return runtimeusage.ErrObservationDiscarded
		}
		if enforced {
			var enabled bool
			var since sql.NullInt64
			var revision int64
			if err = tx.QueryRowContext(operation, `SELECT g.usage_enabled,g.usage_collecting_since_unix_ms,g.usage_policy_revision FROM inspection_mirror_active a JOIN inspection_mirror_generations g USING(generation) WHERE a.singleton=1 AND g.complete=1`).Scan(&enabled, &since, &revision); err != nil {
				return runtimeusage.ErrObservationDiscarded
			}
			if !enabled || !since.Valid || v.Value.StartedAt.Before(time.UnixMilli(since.Int64)) || v.PolicyRevision > revision {
				return runtimeusage.ErrObservationDiscarded
			}
		}
	}
	_, err = tx.ExecContext(operation, `INSERT INTO runtime_usage_observations(exchange_id,capture_run_id,manual_capture_id,runtime_user_id,occurred_at_unix_ms,expires_at_unix_ms,observation_json,policy_revision,recording_scope_kind,recording_scope_id,recording_archive_epoch,recording_capture_generation) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(exchange_id) DO NOTHING`, v.Value.ExchangeID, v.Value.CaptureRunID, v.Value.ManualCaptureID, v.Value.UserID, v.Value.OccurredAt.UnixMilli(), v.ExpiresAt.UnixMilli(), string(data), v.PolicyRevision, scope.Capture.Kind, scope.Capture.ID, scope.Fence.ArchiveEpoch, scope.Fence.CaptureGeneration)
	if err != nil {
		return err
	}
	return tx.Commit()
}
