package productruntime

import (
	"context"
	"errors"
	"fmt"
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

func TestStorageHealthMonitorOwnsAndCancelsRetentionCleanup(t *testing.T) {
	entered, exited := make(chan struct{}), make(chan struct{})
	monitor, err := newStorageHealthMonitor(monitorBuildRequest{
		ownerContext: context.Background(),
		reader:       &cancellableRepository{entered: make(chan struct{}), exited: make(chan struct{})},
		interval:     time.Hour,
		observe:      func(runtimepersistence.SchemaState, error) {},
		cleanup: func(ctx context.Context) error {
			close(entered)
			<-ctx.Done()
			close(exited)
			return ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("retention cleanup did not run without request traffic")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := monitor.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	default:
		t.Fatal("cleanup escaped the Runtime lifecycle")
	}
}

func TestRetentionBudgetDoesNotDeclareStorageUnavailable(t *testing.T) {
	for _, test := range []struct {
		name     string
		err      error
		degraded bool
	}{
		{"budget", fmt.Errorf("cleanup: %w", context.DeadlineExceeded), false},
		{"shutdown", context.Canceled, false},
		{"database failure", errors.New("database write failed"), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &cancellableRepository{entered: make(chan struct{}), exited: make(chan struct{})}
			observed := make(chan error, 2)
			monitor, err := newStorageHealthMonitor(monitorBuildRequest{
				ownerContext: context.Background(), reader: repository, interval: time.Millisecond,
				cleanup: func(context.Context) error { return test.err },
				observe: func(_ runtimepersistence.SchemaState, err error) { observed <- err },
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := monitor.Shutdown(ctx); err != nil {
					t.Error(err)
				}
			})
			select {
			case <-repository.entered: // cleanup finished; the independent health read is still pending
			case <-time.After(time.Second):
				t.Fatal("health checks did not continue after maintenance")
			}
			select {
			case err := <-observed:
				if !test.degraded || !errors.Is(err, test.err) {
					t.Fatalf("maintenance incorrectly changed storage health: %v", err)
				}
			default:
				if test.degraded {
					t.Fatal("database maintenance failure was hidden")
				}
			}
		})
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
