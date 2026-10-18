package productruntime

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
	"reflect"
	"time"
)

// This wrapper publishes only successful required-setting commits. It does not
// add a query to any observation producer.
type recordingUsageRepository struct {
	runtimeusage.PolicyRepository
	recorder *observationRecorder
}

func (r recordingUsageRepository) SetUsagePolicy(ctx context.Context, p runtimeusage.CollectionPolicy, now time.Time) (runtimeusage.CollectionPolicy, error) {
	updated, err := r.PolicyRepository.SetUsagePolicy(ctx, p, now)
	if err == nil {
		r.recorder.publishUsagePolicy(updated)
	}
	return updated, err
}
func (r *observationRecorder) publishUsagePolicy(p runtimeusage.CollectionPolicy) {
	if p.Validate() != nil {
		return
	}
	if p.CollectingSince != nil {
		t := p.CollectingSince.UTC()
		p.CollectingSince = &t
	}
	for {
		previous := r.usagePolicy.Load()
		if previous != nil && previous.Revision >= p.Revision {
			return
		}
		if r.usagePolicy.CompareAndSwap(previous, &p) {
			return
		}
	}
}

// Called only after metadata preparation has reserved its owned overlap.
func (o activityAttemptObserver) collectUsage(observation exchange.AttemptObservation, status activity.Status) *runtimeusage.CollectedObservation {
	policy := o.queue.usagePolicy.Load()
	if policy == nil || !policy.Enabled || policy.CollectingSince == nil || observation.StartedAt.Before(*policy.CollectingSince) {
		return nil
	}
	if o.usage == nil {
		return nil
	}
	v := runtimeusage.Observation{ExchangeID: observation.ExchangeID, StartedAt: observation.StartedAt, OccurredAt: observation.OccurredAt, Status: status, EnvironmentID: string(observation.EnvironmentID), EnvironmentRevision: uint64(observation.EnvironmentRevision), EnvironmentName: observation.EnvironmentName, AccountID: observation.AccountID, AccountName: observation.AccountName, RequestedModel: observation.RequestedModel, UpstreamModel: observation.UpstreamModel, Usage: observation.Usage, Source: "proxy"}
	if observation.HasAdmission {
		a := observation.Admission
		switch a.Kind() {
		case captureadmission.KindManagedRun:
			v.CaptureRunID, _ = a.CaptureRunID()
			v.UserID, _ = a.RuntimeUserID()
			v.Source = "local"
			if v.UserID != "" {
				v.Source = "member"
			}
			username, _ := a.RuntimeUsername()
			runtime, _ := a.RuntimeMetadata()
			machine, _ := a.MachineID()
			v.Attribution = runtimeusage.CaptureAttribution(capturerun.View{RuntimeUserID: v.UserID, RuntimeUsername: username, LocalUserLabel: runtime.LocalUserName, MachineID: machine.String(), Runtime: runtime})
		case captureadmission.KindManual:
			v.ManualCaptureID, _ = a.ManualCaptureID()
			v.Source = "manual"
		}
	}
	if identity, ok := agentconversation.ClientIdentityFromProtocolEvidence(observation.ClientProtocolEvidence, observation.ProviderResponseID, observation.OccurredAt); ok {
		v.Client, v.SessionID = identity.Client, identity.SessionID
	}
	if v.Validate() != nil {
		return nil
	}
	cloneSnapshot(reflect.ValueOf(&v).Elem())
	return &runtimeusage.CollectedObservation{Value: v, PolicyRevision: policy.Revision, ExpiresAt: v.OccurredAt.Add(time.Duration(policy.RetentionDays) * 24 * time.Hour), Stamp: observation.ObservationStamp}
}
