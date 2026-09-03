package captureassignment

import (
	"context"
	"sync"
)

type lifecycleGate struct {
	mu     sync.Mutex
	open   bool
	active int
	zero   chan struct{}
}

func newLifecycleGate() *lifecycleGate {
	zero := make(chan struct{})
	close(zero)
	return &lifecycleGate{open: true, zero: zero}
}

func (gate *lifecycleGate) begin(ctx context.Context) (func(), error) {
	if ctx == nil {
		return nil, ErrInvalidAssignment
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if !gate.open {
		return nil, ErrRuntimeStopping
	}
	if gate.active == 0 {
		gate.zero = make(chan struct{})
	}
	gate.active++
	var once sync.Once
	return func() {
		once.Do(func() {
			gate.mu.Lock()
			gate.active--
			if gate.active == 0 {
				close(gate.zero)
			}
			gate.mu.Unlock()
		})
	}, nil
}

func (gate *lifecycleGate) closeAdmission() {
	gate.mu.Lock()
	gate.open = false
	gate.mu.Unlock()
}

func (gate *lifecycleGate) drain(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidAssignment
	}
	gate.mu.Lock()
	zero := gate.zero
	gate.mu.Unlock()
	select {
	case <-zero:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
