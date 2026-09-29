package productruntime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
)

// flakyCompletionRepository fails the first failures terminal writes, as a
// SQLite writer held by a long transaction would, and then recovers.
type flakyCompletionRepository struct {
	mu        sync.Mutex
	failures  int
	completes int
	appends   int
}

func (repository *flakyCompletionRepository) Append(context.Context, egressaudit.Attempt) (egressaudit.Record, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.appends++
	return egressaudit.Record{}, nil
}

func (repository *flakyCompletionRepository) Complete(_ context.Context, attempt egressaudit.Attempt) (egressaudit.Record, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.failures > 0 {
		repository.failures--
		return egressaudit.Record{}, context.DeadlineExceeded
	}
	repository.completes++
	return egressaudit.Record{Attempt: attempt}, nil
}

func (*flakyCompletionRepository) List(context.Context, egressaudit.PageRequest) (egressaudit.Page, error) {
	return egressaudit.Page{}, nil
}

func (*flakyCompletionRepository) Recover(context.Context, time.Time) (int, error) {
	return 0, nil
}

func (repository *flakyCompletionRepository) counts() (appends, completes int) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return repository.appends, repository.completes
}

// A terminal write that times out behind another writer must not stop the
// Runtime. Without it, no new egress may start; once the terminal is durable,
// the Runtime recovers on its own. Nothing unaudited is ever sent.
func TestCoreAuditTerminalFailureRefusesNewEgressUntilItIsWritten(t *testing.T) {
	t.Parallel()
	delegate := &flakyCompletionRepository{failures: 2}
	tracker := newStatusTracker("instance-audit-breaker", hostcontract.KindDesktop, time.Now().UTC())
	tracker.commitInitialized(25)
	owner, stop := context.WithCancelCause(context.Background())
	defer stop(nil)
	repository := newRuntimeEgressRepository(delegate, tracker, owner,
		LifecycleOptions{RollbackTimeout: time.Second, ShutdownTimeout: time.Second}, stop)
	repository.retryInterval = 5 * time.Millisecond

	if _, err := repository.Complete(context.Background(), egressaudit.Attempt{}); err == nil {
		t.Fatal("a failed terminal write was reported as durable")
	}
	if owner.Err() != nil {
		t.Fatalf("one terminal write failure stopped the Runtime: %v", context.Cause(owner))
	}
	status := tracker.snapshot()
	if status.State != RuntimeStateDegraded || status.Storage != StorageStateUnavailable ||
		status.StorageFailure == nil || status.StorageFailure.Operation != "egress_complete" {
		t.Fatalf("pending audit terminal was not reported: %+v", status)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		appendsBefore, _ := delegate.counts()
		_, err := repository.Append(context.Background(), egressaudit.Attempt{})
		appendsAfter, completes := delegate.counts()
		if err == nil {
			if completes != 1 {
				t.Fatalf("new egress started before the pending terminal was written (completes=%d)", completes)
			}
			break
		}
		if !errors.Is(err, ErrCoreAuditRecovering) || appendsAfter != appendsBefore {
			t.Fatalf("Append while recovering = %v, delegate appends %d -> %d", err, appendsBefore, appendsAfter)
		}
		if time.Now().After(deadline) {
			t.Fatal("the pending terminal was never written")
		}
		time.Sleep(5 * time.Millisecond)
	}
	status = tracker.snapshot()
	if status.State != RuntimeStateInitialized || status.Storage != StorageStateHealthy || status.StorageFailure != nil {
		t.Fatalf("Runtime did not recover after the terminal was written: %+v", status)
	}
	tracker.observeStorage(25, nil)
	if tracker.snapshot().State != RuntimeStateInitialized {
		t.Fatal("health poll re-degraded a recovered Runtime")
	}
}

// A terminal that is still unwritten when the Runtime stops is a durability
// failure of that shutdown, never a clean stop.
func TestCoreAuditTerminalStillPendingAtShutdownFailsTheStop(t *testing.T) {
	t.Parallel()
	delegate := &flakyCompletionRepository{failures: 1 << 30}
	tracker := newStatusTracker("instance-audit-pending-stop", hostcontract.KindDesktop, time.Now().UTC())
	tracker.commitInitialized(25)
	owner, stop := context.WithCancelCause(context.Background())
	repository := newRuntimeEgressRepository(delegate, tracker, owner,
		LifecycleOptions{RollbackTimeout: 50 * time.Millisecond, ShutdownTimeout: 50 * time.Millisecond}, stop)
	repository.retryInterval = 5 * time.Millisecond
	_, _ = repository.Complete(context.Background(), egressaudit.Attempt{})

	shutdown, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	repository.beginShutdown(shutdown)
	stop(errors.New("stopping"))
	<-shutdown.Done()
	repository.finishShutdown()
	if repository.failure() == nil {
		t.Fatal("an unwritten audit terminal did not fail the shutdown")
	}
	tracker.finishStopping(time.Now().UTC(), nil)
	if status := tracker.snapshot(); status.State != RuntimeStateStopFailed {
		t.Fatalf("stop with an unwritten terminal = %+v", status)
	}
}
