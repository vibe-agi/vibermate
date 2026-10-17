package runtimepersistence

import (
	"context"
	"encoding/json"
	"errors"
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
func (s *ObservationStore) PutCollectedUsage(ctx context.Context, v runtimeusage.CollectedObservation) error {
	return s.store.PutCollectedUsage(ctx, v)
}

// PutCollectedUsage consumes only frozen trusted collection coordinates. The
// legacy Store method keeps production composition buildable until the owner
// cutover; it no longer has a worker-side control-policy/attribution bridge.
func (s *Store) PutCollectedUsage(ctx context.Context, v runtimeusage.CollectedObservation) error {
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
	_, err = s.database.ExecContext(operation, `INSERT INTO runtime_usage_observations(exchange_id,capture_run_id,manual_capture_id,runtime_user_id,occurred_at_unix_ms,expires_at_unix_ms,observation_json,policy_revision,recording_scope_kind,recording_scope_id,recording_archive_epoch,recording_capture_generation) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(exchange_id) DO NOTHING`, v.Value.ExchangeID, v.Value.CaptureRunID, v.Value.ManualCaptureID, v.Value.UserID, v.Value.OccurredAt.UnixMilli(), v.ExpiresAt.UnixMilli(), string(data), v.PolicyRevision, scope.Capture.Kind, scope.Capture.ID, scope.Fence.ArchiveEpoch, scope.Fence.CaptureGeneration)
	return err
}
