package productruntime

import (
	"context"
	"errors"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/recording"
	"testing"
	"time"
)

func TestOrdinaryAuditLossDoesNotDisableRuntime(t *testing.T) {
	owner, stop := context.WithCancelCause(context.Background())
	defer stop(nil)
	tracker := newStatusTracker("optional-audit", hostcontract.KindDesktop, time.Now())
	tracker.commitInitialized(25)
	budget, _ := recording.NewBudget(recording.Limits{Records: 8, Bytes: 1 << 20})
	sink := &blockedObservationStore{make(chan struct{}, 1), make(chan struct{})}
	close(sink.release)
	recorder := newObservationRecorder(owner, budget, sink, sink)
	defer recorder.Shutdown(context.Background())
	repo := newRuntimeEgressRepository(nil, tracker, owner, LifecycleOptions{RollbackTimeout: time.Second, ShutdownTimeout: time.Second}, stop)
	repo.recorder = recorder
	for _, id := range []string{"first", "second"} {
		a := runtimeProviderAttempt(t, id)
		if repo.ObserveEgress(repo.ScopeStamp(captureidentity.Reference{}), a) != recording.Accepted {
			t.Fatal("ordinary loss refused subsequent offer")
		}
		if err := recorder.Flush(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	repo.ReportTerminalFailure(errors.New("malformed optional terminal"))
	repo.finishShutdown()
	if owner.Err() != nil || repo.failure() != nil || tracker.snapshot().State != RuntimeStateInitialized || tracker.snapshot().Storage != StorageStateHealthy {
		t.Fatal("optional audit loss poisoned owner/readiness")
	}
	if budget.Snapshot().ByKind[recording.Egress].Settlements[recording.Unconfirmed].Records != 2 {
		t.Fatal("loss not counted")
	}
}
