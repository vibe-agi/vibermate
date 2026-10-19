package productruntime

import (
	"context"
	"errors"
	"github.com/vibe-agi/vibermate/internal/acpobservation"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"path/filepath"
	"reflect"
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

type observedACPWriter func(context.Context, acpobservation.Record) error

func (f observedACPWriter) PutACP(ctx context.Context, r acpobservation.Record) error {
	return f(ctx, r)
}

func TestACPFinalSnapshotSurvivesEarlierMirrorGapDrops(t *testing.T) {
	for _, mode := range []environment.ContentRecordingMode{environment.ContentRecordingMetadataOnly, environment.ContentRecordingFull} {
		t.Run(string(mode), func(t *testing.T) {
			ctx := t.Context()
			root := t.TempDir()
			control, err := runtimepersistence.OpenControl(ctx, runtimepersistence.ControlOptions{DatabasePath: filepath.Join(root, "runtime.db"), LayoutID: "acp-mirror-gap", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer control.Shutdown(context.Background())
			observations, err := runtimepersistence.OpenObservations(ctx, runtimepersistence.ObservationOptions{DatabasePath: filepath.Join(root, "observations.db"), LayoutID: "acp-mirror-gap", ArchiveID: "current", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer observations.Shutdown(context.Background())
			if err = observations.PublishInspectionMirror(ctx, control); err != nil {
				t.Fatal(err)
			}
			runs, err := capturerun.NewManager(ctx, capturerun.DefaultOptions(control.CaptureRunRepository()))
			if err != nil {
				t.Fatal(err)
			}
			defer runs.Shutdown(context.Background())
			grant, err := runs.Create(ctx, capturerun.CreateCommand{CWD: "/fixture", CanonicalExecutablePath: "/bin/fixture", ExecutableLabel: "fixture", CatalogRevision: 1, Lifetime: time.Minute})
			if err != nil {
				t.Fatal(err)
			}
			registration, err := control.ACPRegistrations().Register(ctx, acpobservation.Registration{RunID: grant.Run.ID, Policy: environment.ContentRecordingPolicy{Mode: mode, RetentionDays: 30}, ExpiresAtMillis: time.Now().Add(time.Hour).UnixMilli()}, time.Now().UnixMilli())
			if err != nil {
				t.Fatal(err)
			}
			budget, _ := recording.NewBudget(recording.Limits{Records: 4, Bytes: 64 << 20})
			queue := newObservationRecorder(ctx, budget, nil, nil)
			defer queue.Shutdown(context.Background())
			outcomes := make(chan error, 3)
			queue.acp = observedACPWriter(func(ctx context.Context, value acpobservation.Record) error {
				err := observations.PutACP(ctx, value)
				t.Logf("actual ACP append revision=%d final=%t originalFence=%+v err=%v", value.Snapshot.Revision, value.Snapshot.Final, value.Scope.Fence, err)
				outcomes <- err
				return err
			})
			capture := captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: grant.Run.ID}
			stamp := queue.ScopeStamp(capture)
			record := acpobservation.Record{RunID: grant.Run.ID, Scope: recording.Scope{Capture: capture, Fence: registration.Fence}, Policy: registration.Policy, ExpiresAtMillis: registration.ExpiresAtMillis, Snapshot: acpobservation.Snapshot{Revision: 1}}
			if stamp.Scope() != record.Scope {
				t.Fatal("fixture did not retain registered original scope")
			}
			if got := queue.ObserveACP(stamp, record); got != recording.Accepted {
				t.Fatal("initial offer", got)
			}
			exitCode := 0
			record.Snapshot = acpobservation.Snapshot{Revision: 2, Final: true, ExitCode: &exitCode, Sessions: []acpobservation.Session{{ID: "session", Operation: "new"}}, Prompts: []acpobservation.Prompt{{Sequence: 1, SessionID: "session", State: "completed"}}}
			if mode == environment.ContentRecordingFull {
				record.Snapshot.Prompts[0].UserText = "question"
				record.Snapshot.Prompts[0].AgentText = "answer"
			}
			if got := queue.ObserveACP(stamp, record); got != recording.Accepted {
				t.Fatal("final offer", got)
			}
			if err = queue.Flush(ctx); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err = <-outcomes; !errors.Is(err, recording.ErrObservationDiscarded) {
					t.Fatal("unmirrored original scope did not fail closed", err)
				}
			}
			stats := budget.Snapshot().ByKind[recording.ACP]
			if stats.Offers[recording.Accepted].Records != 2 || stats.Settlements[recording.Discarded].Records != 2 || stats.Settlements[recording.Unconfirmed].Records != 0 {
				t.Fatal("unexpected actual ACP drop classification", stats)
			}
			if _, err = observations.ACPSnapshots().Read(ctx, grant.Run.ID, time.Now().UnixMilli()); !errors.Is(err, acpobservation.ErrNotFound) {
				t.Fatal("unmirrored snapshot became visible", err)
			}
			if err = runs.Finish(ctx, grant.Run.ID, grant.ControlCapability); err != nil {
				t.Fatal(err)
			}
			if err = observations.PublishInspectionMirror(ctx, control); err != nil {
				t.Fatal(err)
			}
			// Replay only the optional snapshot in this causal probe. Required Capture
			// actions are not retried, and no original stamp or privacy guard changes.
			if got := queue.ObserveACP(stamp, record); got != recording.Accepted {
				t.Fatal("mirrored final offer", got)
			}
			if err = queue.Flush(ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-outcomes; err != nil {
				t.Fatal(err)
			}
			retained, err := observations.ACPSnapshots().Read(ctx, grant.Run.ID, time.Now().UnixMilli())
			if err != nil || !reflect.DeepEqual(retained, record) {
				t.Fatalf("final original snapshot not complete: %+v %v", retained, err)
			}
			stats = budget.Snapshot().ByKind[recording.ACP]
			if stats.Settlements[recording.Discarded].Records != 2 || stats.Settlements[recording.Persisted].Records != 1 || budget.Snapshot().ReservedRecords != 0 {
				t.Fatal("known prior drops erased or ownership leaked", stats)
			}
		})
	}
}
