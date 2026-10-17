package productruntime

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/acpobservation"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/recording"
	"testing"
	"time"
)

type heldACPWriter struct {
	entered chan struct{}
	release chan struct{}
	values  chan acpobservation.Record
}

func (w heldACPWriter) PutACP(_ context.Context, r acpobservation.Record) error {
	w.entered <- struct{}{}
	<-w.release
	w.values <- r
	return nil
}
func TestACPRecordingOwnsAllSnapshotFieldsUntilWriterReturns(t *testing.T) {
	b, _ := recording.NewBudget(recording.Limits{Records: 1, Bytes: 64 << 20})
	q := newObservationRecorder(context.Background(), b, nil, nil)
	defer q.Shutdown(context.Background())
	w := heldACPWriter{make(chan struct{}, 1), make(chan struct{}), make(chan acpobservation.Record, 1)}
	q.acp = w
	s := q.ScopeStamp(captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "run-acp"})
	exit := 0
	r := acpobservation.Record{RunID: "run-acp", Scope: s.Scope(), Policy: environment.ContentRecordingPolicy{Mode: environment.ContentRecordingFull, RetentionDays: 30}, ExpiresAtMillis: time.Now().Add(time.Hour).UnixMilli(), Snapshot: acpobservation.Snapshot{Revision: 2, Final: true, ExitCode: &exit, Sessions: []acpobservation.Session{{ID: "session", CWD: "/original", Operation: "new"}}, Prompts: []acpobservation.Prompt{{Sequence: 1, SessionID: "session", State: "completed", UserText: "original"}}}}
	if result := q.ObserveACP(s, r); result != recording.Accepted {
		t.Fatalf("offer %v", result)
	}
	<-w.entered
	if result := q.ObserveACP(s, r); result != recording.DroppedCapacity {
		t.Errorf("shared capacity: %v", result)
	}
	r.Snapshot.Sessions[0].CWD = "/changed"
	r.Snapshot.Prompts[0].UserText = "changed"
	exit = 23
	if b.Snapshot().ReservedRecords != 1 {
		t.Error("reservation released before writer return")
	}
	close(w.release)
	got := <-w.values
	if got.Snapshot.Sessions[0].CWD != "/original" || got.Snapshot.Prompts[0].UserText != "original" || *got.Snapshot.ExitCode != 0 {
		t.Fatalf("producer aliases survived: %+v", got)
	}
	if err := q.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b.Snapshot().ReservedRecords != 0 {
		t.Fatal("reservation leaked")
	}
}
