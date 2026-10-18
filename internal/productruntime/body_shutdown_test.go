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
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
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
	waitRecording(t, runtime)
	content := runtime.recordingStorage.current.Load().content
	var disposed atomic.Int32
	runtime.cleanups.entries = append([]cleanupEntry{{name: "test physical completion", run: func(context.Context) error { disposed.Add(1); return nil }}}, runtime.cleanups.entries...)
	lease, err := runtime.bodyAdmission.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		lease.Release()
		_ = runtime.Shutdown(context.Background())
		select {
		case <-runtime.ShutdownDone():
		case <-time.After(3 * time.Second):
			t.Error("body owner cleanup did not actually finish")
		}
	}()
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
	if _, err := runtime.storage.SchemaStateReader().ReadSchemaState(context.Background()); err != nil {
		t.Fatalf("required control closed with a live body owner: %v", err)
	}
	_, readErr := content.GetProjection(context.Background(), "missing-owned-source", exchangecontent.RequestViewFull)
	if !errors.Is(readErr, exchangecontent.ErrNotFound) {
		t.Fatalf("actual content owner closed with a live body owner: %v", readErr)
	}
	// Public inspection is independently unavailable until Task5's mirror exists.
	if _, err := runtime.ExchangeContents().GetProjection(context.Background(), "missing-owned-source", exchangecontent.RequestViewFull); !errors.Is(err, recording.ErrInspectionUnavailable) {
		t.Fatalf("missing mirror fabricated public inspection: %v", err)
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
	_, readErr = content.GetProjection(context.Background(), "missing-owned-source", exchangecontent.RequestViewFull)
	if !errors.Is(readErr, exchangecontent.ErrRuntimeStopping) {
		t.Fatalf("dependency leaked after real drain: %v", readErr)
	}
	if _, err := runtime.storage.SchemaStateReader().ReadSchemaState(context.Background()); !errors.Is(err, runtimepersistence.ErrStoreClosing) {
		t.Fatalf("required control leaked after real drain: %v", err)
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
