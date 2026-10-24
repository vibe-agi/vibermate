// Package lifecyclegate admits a manager's operations until shutdown and lets
// shutdown wait for the admitted ones to finish.
package lifecyclegate

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Gate tracks one manager's in-flight operations. Every operation context is
// canceled with the stopping error once admission closes.
type Gate struct {
	label    string
	invalid  error
	stopping error

	mu        sync.Mutex
	accepting bool
	active    int
	drained   chan struct{}

	ownerContext context.Context
	cancelOwner  context.CancelCauseFunc
}

// New returns an accepting gate. label names the owner in messages; invalid
// wraps nil-context errors and stopping is returned and used as the
// cancellation cause after CloseAdmission, so callers keep their own package
// sentinels for errors.Is.
func New(label string, invalid, stopping error) *Gate {
	ownerContext, cancelOwner := context.WithCancelCause(context.Background())
	return &Gate{
		label:        label,
		invalid:      invalid,
		stopping:     stopping,
		accepting:    true,
		drained:      make(chan struct{}),
		ownerContext: ownerContext,
		cancelOwner:  cancelOwner,
	}
}

// Begin admits one operation. The returned finish function is idempotent and
// must be called once the operation ends.
func (gate *Gate) Begin(
	caller context.Context,
) (context.Context, func(), error) {
	if caller == nil {
		return nil, nil, fmt.Errorf("%w: context is nil", gate.invalid)
	}
	if err := caller.Err(); err != nil {
		return nil, nil, err
	}
	gate.mu.Lock()
	if !gate.accepting {
		gate.mu.Unlock()
		return nil, nil, gate.stopping
	}
	gate.active++
	owner := gate.ownerContext
	gate.mu.Unlock()

	operation, cancel := context.WithCancelCause(caller)
	stop := context.AfterFunc(owner, func() {
		cancel(gate.stopping)
	})
	var once sync.Once
	finish := func() {
		once.Do(func() {
			stop()
			cancel(errors.New(gate.label + " operation finished"))
			gate.finish()
		})
	}
	return operation, finish, nil
}

func (gate *Gate) finish() {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	gate.active--
	if gate.active < 0 {
		panic(gate.label + " lifecycle active operation count became negative")
	}
	if !gate.accepting && gate.active == 0 {
		close(gate.drained)
	}
}

// CloseAdmission refuses new operations and cancels admitted ones.
func (gate *Gate) CloseAdmission() {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if !gate.accepting {
		return
	}
	gate.accepting = false
	gate.cancelOwner(gate.stopping)
	if gate.active == 0 {
		close(gate.drained)
	}
}

// Drain waits until admission is closed and every admitted operation has
// finished, or ctx ends.
func (gate *Gate) Drain(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: drain context is nil", gate.invalid)
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
		return fmt.Errorf("drain %s operations: %w", gate.label, ctx.Err())
	}
}
