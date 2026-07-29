package productruntime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
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

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			state, err := request.reader.ReadSchemaState(ctx)
			request.observe(state, err)
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
