package runtimepersistence

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

var ErrStoreClosing = errors.New("runtime store is closing")

type operationGate struct {
	mu        sync.Mutex
	accepting bool
	active    int
	drained   chan struct{}

	ownerContext context.Context
	cancelOwner  context.CancelCauseFunc
}

type operationPermit struct {
	context      context.Context
	ownerContext context.Context
	finish       func()
}

func newOperationGate() *operationGate {
	ownerContext, cancelOwner := context.WithCancelCause(context.Background())
	return &operationGate{
		accepting:    true,
		drained:      make(chan struct{}),
		ownerContext: ownerContext,
		cancelOwner:  cancelOwner,
	}
}

func (g *operationGate) begin(
	callerContext context.Context,
) (context.Context, func(), error) {
	permit, err := g.admit(callerContext)
	if err != nil {
		return nil, nil, err
	}
	return permit.context, permit.finish, nil
}

func (g *operationGate) admit(
	callerContext context.Context,
) (*operationPermit, error) {
	if callerContext == nil {
		return nil, errors.New("database operation context is nil")
	}
	if err := callerContext.Err(); err != nil {
		return nil, fmt.Errorf("begin database operation: %w", err)
	}

	g.mu.Lock()
	if !g.accepting {
		g.mu.Unlock()
		return nil, ErrStoreClosing
	}
	g.active++
	ownerContext := g.ownerContext
	g.mu.Unlock()

	operationContext, cancelOperation := context.WithCancelCause(callerContext)
	stopOwnerCancellation := context.AfterFunc(ownerContext, func() {
		cancelOperation(ErrStoreClosing)
	})

	var finishOnce sync.Once
	finish := func() {
		finishOnce.Do(func() {
			stopOwnerCancellation()
			cancelOperation(context.Canceled)
			g.finish()
		})
	}
	return &operationPermit{
		context:      operationContext,
		ownerContext: ownerContext,
		finish:       finish,
	}, nil
}

func (g *operationGate) closeAdmission() {
	g.mu.Lock()
	if !g.accepting {
		g.mu.Unlock()
		return
	}
	g.accepting = false
	g.cancelOwner(ErrStoreClosing)
	if g.active == 0 {
		close(g.drained)
	}
	g.mu.Unlock()
}

func (g *operationGate) finish() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.active--
	if g.active < 0 {
		panic("database operation gate active count became negative")
	}
	if !g.accepting && g.active == 0 {
		close(g.drained)
	}
}

func (g *operationGate) drain(ctx context.Context) error {
	if ctx == nil {
		return errors.New("database drain context is nil")
	}

	g.mu.Lock()
	if !g.accepting && g.active == 0 {
		g.mu.Unlock()
		return nil
	}
	drained := g.drained
	g.mu.Unlock()

	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("drain database operations: %w", ctx.Err())
	}
}
