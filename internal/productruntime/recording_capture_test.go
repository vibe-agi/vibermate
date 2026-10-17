package productruntime

import (
	"context"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/recording"
)

type heldCaptureWriter struct {
	entered chan recording.CaptureObservation
	release chan struct{}
}

func (w heldCaptureWriter) PutCaptureObservation(_ context.Context, _ recording.Scope, v recording.CaptureObservation) error {
	w.entered <- v
	<-w.release
	return nil
}
func TestCaptureObservationSharesBudgetUntilActualWriterReturn(t *testing.T) {
	b, _ := recording.NewBudget(recording.Limits{Records: 1, Bytes: 1 << 20})
	r := newObservationRecorder(context.Background(), b, nil, nil)
	w := heldCaptureWriter{make(chan recording.CaptureObservation, 1), make(chan struct{})}
	r.capture = w
	defer r.Shutdown(context.Background())
	defer close(w.release)
	stamp := r.ScopeStamp(captureidentity.Reference{Kind: captureidentity.KindManualCapture, ID: "manual-test"})
	v := recording.CaptureObservation{ObservedAt: time.Now().UTC(), CredentialRevision: 7}
	if got := r.ObserveCapture(stamp, v); got != recording.Accepted {
		t.Fatalf("offer %v", got)
	}
	got := <-w.entered
	if got.CredentialRevision != 7 || !got.ObservedAt.Equal(v.ObservedAt) {
		t.Fatal("frozen authentication facts lost")
	}
	if b.Snapshot().ReservedRecords != 1 {
		t.Fatal("writer lost reservation")
	}
	if got := r.ObserveCapture(stamp, v); got != recording.DroppedCapacity {
		t.Fatalf("separate budget: %v", got)
	}
}
