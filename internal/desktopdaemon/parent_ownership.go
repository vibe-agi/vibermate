package desktopdaemon

import (
	"context"
	"errors"
	"io"
	"sync"
)

// ParentOwnership binds a daemon context to one read-only lifetime descriptor
// inherited from the native shell. The descriptor is not a command channel.
type ParentOwnership struct {
	context  context.Context
	cancel   context.CancelFunc
	lifetime io.ReadCloser
	done     chan struct{}

	closeOnce sync.Once
	closeErr  error
}

func NewParentOwnership(
	parent context.Context,
	lifetime io.ReadCloser,
) (*ParentOwnership, error) {
	if parent == nil || lifetime == nil {
		return nil, errors.New("Desktop parent ownership is unavailable")
	}
	ownedContext, cancel := context.WithCancel(parent)
	ownership := &ParentOwnership{
		context:  ownedContext,
		cancel:   cancel,
		lifetime: lifetime,
		done:     make(chan struct{}),
	}
	go ownership.observe()
	return ownership, nil
}

func (ownership *ParentOwnership) Context() context.Context {
	if ownership == nil || ownership.context == nil {
		closed, cancel := context.WithCancel(context.Background())
		cancel()
		return closed
	}
	return ownership.context
}

func (ownership *ParentOwnership) Close() error {
	if ownership == nil {
		return nil
	}
	ownership.closeOnce.Do(func() {
		ownership.cancel()
		ownership.closeErr = ownership.lifetime.Close()
		<-ownership.done
	})
	return ownership.closeErr
}

func (ownership *ParentOwnership) observe() {
	defer close(ownership.done)
	var probe [1]byte
	for {
		read, err := ownership.lifetime.Read(probe[:])
		if read != 0 || err != nil {
			ownership.cancel()
			return
		}
	}
}
