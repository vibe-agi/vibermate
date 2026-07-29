package access

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

func (g *lifecycleGate) begin(
	callerContext context.Context,
) (context.Context, func(), error) {
	if callerContext == nil {
		return nil, nil, errors.New("Access operation context is nil")
	}
	if err := callerContext.Err(); err != nil {
		return nil, nil, fmt.Errorf("begin Access operation: %w", err)
	}

	g.mu.Lock()
	if !g.accepting {
		g.mu.Unlock()
		return nil, nil, ErrAccessRuntimeStopping
	}
	g.active++
	ownerContext := g.ownerContext
	g.mu.Unlock()

	operationContext, cancelOperation := context.WithCancelCause(callerContext)
	stopOwnerCancellation := context.AfterFunc(ownerContext, func() {
		cancelOperation(ErrAccessRuntimeStopping)
	})

	var finishOnce sync.Once
	finish := func() {
		finishOnce.Do(func() {
			stopOwnerCancellation()
			cancelOperation(context.Canceled)
			g.finish()
		})
	}
	return operationContext, finish, nil
}

func (g *lifecycleGate) finish() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.active--
	if g.active < 0 {
		panic("Access lifecycle active operation count became negative")
	}
	if !g.accepting && g.active == 0 {
		close(g.drained)
	}
}

func (g *lifecycleGate) closeAndDrain(ctx context.Context) error {
	if ctx == nil {
		return errors.New("Access shutdown context is nil")
	}

	g.mu.Lock()
	if g.accepting {
		g.accepting = false
		g.cancelOwner(ErrAccessRuntimeStopping)
		if g.active == 0 {
			close(g.drained)
		}
	}
	if g.active == 0 {
		g.mu.Unlock()
		return nil
	}
	drained := g.drained
	g.mu.Unlock()

	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("drain Access operations: %w", ctx.Err())
	}
}
