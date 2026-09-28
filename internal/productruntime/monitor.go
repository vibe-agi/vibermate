package productruntime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

var ErrStorageMonitorStopped = errors.New("storage health monitor stopped")

type storageHealthMonitor struct {
	cancel context.CancelCauseFunc
	done   chan struct{}

	shutdownOnce sync.Once
}

func newStorageHealthMonitor(request monitorBuildRequest) (*storageHealthMonitor, error) {
	if request.ownerContext == nil {
		return nil, errors.New("storage health monitor owner context is nil")
	}
	if request.reader == nil {
		return nil, errors.New("storage health monitor schema reader is nil")
	}
	if request.interval <= 0 {
		return nil, errors.New("storage health monitor interval must be positive")
	}
	if request.observe == nil {
		return nil, errors.New("storage health monitor observer is nil")
	}

	monitorContext, cancel := context.WithCancelCause(request.ownerContext)
	monitor := &storageHealthMonitor{
		cancel: cancel,
		done:   make(chan struct{}),
	}
	go monitor.run(monitorContext, request)
	return monitor, nil
}

func (m *storageHealthMonitor) run(ctx context.Context, request monitorBuildRequest) {
	defer close(m.done)
	ticker := time.NewTicker(request.interval)
	defer ticker.Stop()
	maintenance := time.NewTicker(time.Minute)
	defer maintenance.Stop()
	cleanup := func() {
		if request.cleanup == nil {
			return
		}
		call, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		// Expiry is already enforced by readers. Exhausting a maintenance budget
		// means continue next pass, not that the database or proxy is unavailable.
		if err := request.cleanup(call); err != nil &&
			!errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			request.observe(runtimepersistence.SchemaState{}, err)
		}
	}
	cleanup()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			state, err := request.reader.ReadSchemaState(ctx)
			request.observe(state, err)
		case <-maintenance.C:
			cleanup()
		}
	}
}

func (m *storageHealthMonitor) Shutdown(ctx context.Context) error {
	m.shutdownOnce.Do(func() {
		m.cancel(ErrStorageMonitorStopped)
	})
	select {
	case <-m.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("drain storage health monitor: %w", ctx.Err())
	}
}
