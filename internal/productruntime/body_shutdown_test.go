package productruntime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
)

func TestCandidateShutdownDeadlineKeepsDependenciesUntilBodyOwnerDrains(t *testing.T) {
	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	options := testOptions(t, hostcontract.Desktop(), gate)
	policy := longSessionTestPolicy()
	options.Resources = &policy
	options.Lifecycle.ShutdownTimeout = 40 * time.Millisecond
	runtime := startTestRuntime(t, options)
	var disposed atomic.Int32
	runtime.cleanups.entries = append([]cleanupEntry{{name: "test physical completion", run: func(context.Context) error { disposed.Add(1); return nil }}}, runtime.cleanups.entries...)
	lease, err := runtime.bodyAdmission.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var callers sync.WaitGroup
	failures := make(chan error, 3)
	for range 3 {
		callers.Add(1)
		go func() { defer callers.Done(); failures <- runtime.Shutdown(ctx) }()
	}
	callers.Wait()
	close(failures)
	for err := range failures {
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("shutdown did not honor its original budget: %v", err)
		}
	}
	_, readErr := runtime.ExchangeContents().GetProjection(context.Background(), "missing-owned-source", exchangecontent.RequestViewFull)
	if !errors.Is(readErr, exchangecontent.ErrNotFound) {
		t.Fatalf("dependency closed with a live body owner: %v", readErr)
	}
	if disposed.Load() != 0 || runtime.Status().State != RuntimeStateStopFailed {
		t.Fatal("live owner was reported physically stopped")
	}
	lease.Release()
	select {
	case <-runtime.shutdownDone:
	case <-ctx.Done():
		t.Fatal("late owner release did not finish real cleanup")
	}
	if disposed.Load() != 1 {
		t.Fatalf("cleanup executed %d times", disposed.Load())
	}
	_, readErr = runtime.ExchangeContents().GetProjection(context.Background(), "missing-owned-source", exchangecontent.RequestViewFull)
	if !errors.Is(readErr, exchangecontent.ErrRuntimeStopping) {
		t.Fatalf("dependency leaked after real drain: %v", readErr)
	}
	if err := runtime.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) || disposed.Load() != 1 {
		t.Fatalf("repeat shutdown lost failure or repeated cleanup: %v", err)
	}
}

func TestCandidateNormalShutdownDoesNotSignalBudgetExpiry(t *testing.T) {
	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	options := testOptions(t, hostcontract.Desktop(), gate)
	policy := longSessionTestPolicy()
	options.Resources = &policy
	runtime := startTestRuntime(t, options)
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if runtime.Status().State != RuntimeStateStopped {
		t.Fatalf("normal candidate stop failed: %+v", runtime.Status())
	}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
