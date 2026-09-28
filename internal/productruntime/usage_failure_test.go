package productruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
)

func TestExchangeObservationFailureWarnsWithoutRevokingReadiness(t *testing.T) {
	failure := errors.New("fixture write failure")
	for _, stage := range []string{"start", "terminal", "usage", "success"} {
		t.Run(stage, func(t *testing.T) {
			tracker := newStatusTracker("fixture", hostcontract.KindDesktop, time.Now())
			tracker.commitInitialized(1)
			activityWriter, usageWriter := &observationActivityWriter{}, &observationUsageWriter{}
			if stage == "start" || stage == "terminal" {
				activityWriter.err = failure
			}
			if stage == "usage" {
				usageWriter.err = failure
			}
			reported := 0
			observer := activityAttemptObserver{recorder: activityWriter, usage: usageWriter, reportFailure: func(operation string, err error) {
				if !errors.Is(err, failure) {
					t.Errorf("lost write cause: %v", err)
				}
				reported++
				tracker.failRecording(operation, err)
			}}
			var err error
			if stage == "start" {
				err = observer.ObserveStart(context.Background(), exchange.StartObservation{})
			} else {
				err = observer.ObserveTerminal(context.Background(), exchange.AttemptObservation{Outcome: exchange.AttemptSucceeded, StartedAt: time.Now()})
			}
			tracker.observeStorage(1, nil)
			status := tracker.snapshot()
			if stage == "success" {
				if err != nil || reported != 0 || status.RecordingFailure != nil || status.Storage != StorageStateHealthy {
					t.Fatalf("successful observation degraded storage: %+v %v", status, err)
				}
			} else if !errors.Is(err, failure) || reported != 1 || status.State != RuntimeStateInitialized || status.Storage != StorageStateHealthy || status.RecordingFailure == nil || !status.RecordingFailure.Valid() {
				t.Fatalf("recording gap lost or revoked runtime readiness: %+v reported=%d err=%v", status, reported, err)
			} else if stage == "usage" && status.RecordingFailure.Operation != "usage" {
				t.Fatalf("usage failure conflated with activity: %+v", status.RecordingFailure)
			}
			tracker.finishStopping(time.Now(), nil)
			if tracker.snapshot().State != RuntimeStateStopped {
				t.Fatal("optional observation gap prevented clean shutdown")
			}
		})
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
