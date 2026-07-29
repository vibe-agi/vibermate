package productruntime

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

func TestStorageHealthMonitorCancelsActiveRepositoryObservation(t *testing.T) {
	t.Parallel()

	repository := &cancellableRepository{
		entered: make(chan struct{}),
		exited:  make(chan struct{}),
	}
	ownerContext, cancelOwner := context.WithCancel(context.Background())
	defer cancelOwner()

	monitor, err := newStorageHealthMonitor(monitorBuildRequest{
		ownerContext: ownerContext,
		reader:       repository,
		interval:     time.Millisecond,
		observe:      func(runtimepersistence.SchemaState, error) {},
	})
	if err != nil {
		t.Fatalf("start storage health monitor: %v", err)
	}

	select {
	case <-repository.entered:
	case <-time.After(time.Second):
		t.Fatal("monitor did not enter the repository observation")
	}

	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
	defer cancelShutdown()
	if err := monitor.Shutdown(shutdownContext); err != nil {
		t.Fatalf("shutdown storage health monitor: %v", err)
	}
	select {
	case <-repository.exited:
	default:
		t.Fatal("monitor shutdown did not cancel the active repository observation")
	}
}

type cancellableRepository struct {
	entered chan struct{}
	exited  chan struct{}

	enterOnce sync.Once
	exitOnce  sync.Once
}

func (r *cancellableRepository) ReadSchemaState(
	ctx context.Context,
) (runtimepersistence.SchemaState, error) {
	r.enterOnce.Do(func() {
		close(r.entered)
	})
	<-ctx.Done()
	r.exitOnce.Do(func() {
		close(r.exited)
	})
	return runtimepersistence.SchemaState{}, ctx.Err()
}
