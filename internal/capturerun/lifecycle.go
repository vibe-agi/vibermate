package capturerun

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

type lifecycleGate struct {
	mu        sync.Mutex
	accepting bool
	active    int
	drained   chan struct{}

	ownerContext context.Context
	cancelOwner  context.CancelCauseFunc
}

func newLifecycleGate() *lifecycleGate {
	ownerContext, cancelOwner := context.WithCancelCause(context.Background())
	return &lifecycleGate{
		accepting:    true,
		drained:      make(chan struct{}),
		ownerContext: ownerContext,
		cancelOwner:  cancelOwner,
	}
}

func (gate *lifecycleGate) begin(
	caller context.Context,
) (context.Context, func(), error) {
	if caller == nil {
		return nil, nil, fmt.Errorf("%w: context is nil", ErrInvalidRequest)
	}
	if err := caller.Err(); err != nil {
		return nil, nil, err
	}
	gate.mu.Lock()
	if !gate.accepting {
		gate.mu.Unlock()
		return nil, nil, ErrRuntimeStopping
	}
	gate.active++
	owner := gate.ownerContext
	gate.mu.Unlock()

	operation, cancel := context.WithCancelCause(caller)
	stop := context.AfterFunc(owner, func() {
		cancel(ErrRuntimeStopping)
	})
	var once sync.Once
	finish := func() {
		once.Do(func() {
			stop()
			cancel(errors.New("CaptureRun operation finished"))
			gate.finish()
		})
	}
	return operation, finish, nil
}

func (gate *lifecycleGate) finish() {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	gate.active--
	if gate.active < 0 {
		panic("CaptureRun lifecycle active operation count became negative")
	}
	if !gate.accepting && gate.active == 0 {
		close(gate.drained)
	}
}

func (gate *lifecycleGate) closeAdmission() {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if !gate.accepting {
		return
	}
	gate.accepting = false
	gate.cancelOwner(ErrRuntimeStopping)
	if gate.active == 0 {
		close(gate.drained)
	}
}

func (gate *lifecycleGate) drain(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: drain context is nil", ErrInvalidRequest)
	}
	gate.mu.Lock()
	if !gate.accepting && gate.active == 0 {
		gate.mu.Unlock()
		return nil
	}
	drained := gate.drained
	gate.mu.Unlock()
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("drain CaptureRun operations: %w", ctx.Err())
	}
}
