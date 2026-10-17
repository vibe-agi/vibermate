package productruntime

import (
	"context"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
)

func TestExchangeObservationFailureDoesNotReachProducer(t *testing.T) {
	b, _ := recording.NewBudget(recording.Limits{Records: 2, Bytes: 64 << 20})
	q := newObservationRecorder(context.Background(), b, nil, nil)
	defer q.Shutdown(context.Background())
	writer := &failingPreparedActivity{writes: make(chan activity.Record, 1)}
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
