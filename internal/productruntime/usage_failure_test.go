package productruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
)

func TestExchangeObservationFailureDoesNotReachProducer(t *testing.T) {
	b, _ := recording.NewBudget(recording.Limits{Records: 2, Bytes: 64 << 20})
	q := newObservationRecorder(context.Background(), b, nil, nil)
	defer q.Shutdown(context.Background())
	writer := &warningPreparedActivity{failingPreparedActivity{writes: make(chan activity.Record, 1)}}
	observer := activityAttemptObserver{queue: q, recorder: writer}
	if err := observer.ObserveStart(context.Background(), exchange.StartObservation{ObservationStamp: q.ScopeStamp(captureidentity.Reference{})}); err != nil {
		t.Fatal(err)
	}
	if err := q.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b.Snapshot().ByKind[recording.Activity].Settlements[recording.Unconfirmed].Records != 1 {
		t.Fatal("lost writer failure")
	}
}

type observationActivityWriter struct{ err error }

func (writer *observationActivityWriter) Record(context.Context, activity.Event) (activity.Record, error) {
	return activity.Record{OccurredAt: time.Now()}, writer.err
}

type observationUsageWriter struct{ err error }

func (writer *observationUsageWriter) RecordUsage(context.Context, runtimeusage.Observation) error {
	return writer.err
}

// Complements the actual SQL append-fault matrix in runtimepersistence and
// the real host forwarding/restart test: every async failure stage keeps its
// public warning without changing required readiness or actual owner drain.
func TestQueuedObservationWarningPreservesReadinessAndCleanShutdown(t *testing.T) {
	for _, stage := range []string{"activity_start", "activity_terminal", "usage"} {
		t.Run(stage, func(t *testing.T) {
			tracker := newStatusTracker("warning", hostcontract.KindDesktop, time.Now())
			tracker.commitInitialized(3)
			budget, _ := recording.NewBudget(recording.Limits{Records: 2, Bytes: 64 << 20})
			q := newObservationRecorder(t.Context(), budget, nil, nil)
			q.reportFailure = tracker.failRecording
			defer q.Shutdown(context.Background())
			writer := &warningPreparedActivity{failingPreparedActivity{writes: make(chan activity.Record, 1)}}
			observer := activityAttemptObserver{queue: q, recorder: writer}
			now := time.Now().UTC()
			since := now.Add(-time.Hour)
			q.publishUsagePolicy(runtimeusage.CollectionPolicy{Revision: 1, Enabled: true, RetentionDays: 7, CollectingSince: &since})
			if stage == "usage" {
				observer.recorder = &successfulWarningActivity{warningPreparedActivity: *writer}
				observer.usage = &collectedUsageSink{values: make(chan runtimeusage.CollectedObservation, 1), err: errors.New("selected Usage append failure")}
			}
			stamp := q.ScopeStamp(captureidentity.Reference{})
			var err error
			if stage == "activity_start" {
				err = observer.ObserveStart(t.Context(), exchange.StartObservation{ObservationStamp: stamp})
			} else {
				err = observer.ObserveTerminal(t.Context(), exchange.AttemptObservation{ObservationStamp: stamp, ExchangeID: "warning-exchange", Outcome: exchange.AttemptSucceeded, StartedAt: now, OccurredAt: now})
			}
			if err != nil {
				t.Fatal("optional failure reached producer", err)
			}
			if err := q.Flush(t.Context()); err != nil {
				t.Fatal(err)
			}
			status := tracker.snapshot()
			if status.State != RuntimeStateInitialized || status.Storage != StorageStateHealthy || status.RecordingFailure == nil || status.RecordingFailure.Operation != stage || !status.RecordingFailure.Valid() {
				t.Fatalf("asynchronous warning lost or authority failed: %+v", status)
			}
			if stats := budget.Snapshot(); stats.ReservedRecords != 0 || stats.ReservedBytes != 0 {
				t.Fatal("settled warning leaked owner", stats)
			}
			tracker.beginStopping()
			err = q.Shutdown(t.Context())
			tracker.finishStopping(time.Now(), err)
			if tracker.snapshot().State != RuntimeStateStopped {
				t.Fatal("optional loss prevented clean shutdown")
			}
		})
	}
}

type successfulWarningActivity struct{ warningPreparedActivity }

func (s *successfulWarningActivity) RecordScopedPrepared(_ context.Context, _ recording.Scope, r activity.Record) (activity.Record, error) {
	return r, nil
}

type discardedPreparedActivity struct{ failingPreparedActivity }

func (s *discardedPreparedActivity) RecordScopedPrepared(_ context.Context, _ recording.Scope, r activity.Record) (activity.Record, error) {
	return r, recording.ErrObservationDiscarded
}
func TestQueuedIntentionalDiscardIsNotWriterFailure(t *testing.T) {
	tracker := newStatusTracker("discard", hostcontract.KindDesktop, time.Now())
	tracker.commitInitialized(3)
	budget, _ := recording.NewBudget(recording.Limits{Records: 2, Bytes: 64 << 20})
	q := newObservationRecorder(context.Background(), budget, nil, nil)
	q.reportFailure = tracker.failRecording
	defer q.Shutdown(context.Background())
	observer := activityAttemptObserver{queue: q, recorder: &discardedPreparedActivity{}}
	if err := observer.ObserveStart(context.Background(), exchange.StartObservation{ObservationStamp: q.ScopeStamp(captureidentity.Reference{})}); err != nil {
		t.Fatal(err)
	}
	if err := q.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if tracker.snapshot().RecordingFailure != nil || budget.Snapshot().ByKind[recording.Activity].Settlements[recording.Unconfirmed].Records != 0 {
		t.Fatal("intentional stale/expiry discard was reported as failed write")
	}
}

type warningPreparedActivity struct{ failingPreparedActivity }

func (s *warningPreparedActivity) Prepare(e activity.Event) (activity.Record, error) {
	r, err := s.failingPreparedActivity.Prepare(e)
	r.Kind = e.Kind
	return r, err
}
