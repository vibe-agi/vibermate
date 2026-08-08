package exchangecontent

import (
	"context"
	"errors"
	"sync"
	"time"
)

type Clock interface {
	Now() time.Time
}

type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now().UTC() }

type Repository interface {
	Put(context.Context, Record) error
	Get(context.Context, string, time.Time) (Record, error)
	PurgeExpired(context.Context, time.Time) (uint64, error)
}

type Recorder interface {
	Record(context.Context, Record) error
}

type Reader interface {
	Get(context.Context, string) (Record, error)
}

type Runtime interface {
	Recorder
	Reader
	Shutdown(context.Context) error
}

type Options struct {
	Repository Repository
	Clock      Clock
}

type Manager struct {
	repository Repository
	clock      Clock

	mu      sync.Mutex
	closing bool
	active  int
	changed chan struct{}
}

func New(ctx context.Context, options Options) (*Manager, error) {
	if ctx == nil || options.Repository == nil || options.Clock == nil {
		return nil, errors.New("Exchange content dependencies are incomplete")
	}
	manager := &Manager{
		repository: options.Repository,
		clock:      options.Clock,
		changed:    make(chan struct{}),
	}
	if _, err := options.Repository.PurgeExpired(ctx, options.Clock.Now().UTC()); err != nil {
		return nil, err
	}
	return manager, nil
}

func (manager *Manager) Record(ctx context.Context, record Record) error {
	if err := record.Validate(); err != nil {
		return err
	}
	operation, finish, err := manager.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	now := manager.clock.Now().UTC()
	if !record.ExpiresAt.After(now) {
		return ErrInvalidEvidence
	}
	if _, err := manager.repository.PurgeExpired(operation, now); err != nil {
		return err
	}
	return manager.repository.Put(operation, record.Clone())
}

func (manager *Manager) Get(ctx context.Context, exchangeID string) (Record, error) {
	if !validIdentity(exchangeID, MaxExchangeIDBytes) {
		return Record{}, ErrInvalidEvidence
	}
	operation, finish, err := manager.begin(ctx)
	if err != nil {
		return Record{}, err
	}
	defer finish()
	record, err := manager.repository.Get(operation, exchangeID, manager.clock.Now().UTC())
	if err != nil {
		return Record{}, err
	}
	return record.Clone(), nil
}

func (manager *Manager) Shutdown(ctx context.Context) error {
	if manager == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("Exchange content shutdown context is nil")
	}
	manager.mu.Lock()
	if !manager.closing {
		manager.closing = true
		manager.notifyLocked()
	}
	for manager.active != 0 {
		changed := manager.changed
		manager.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
		manager.mu.Lock()
	}
	manager.mu.Unlock()
	return nil
}

func (manager *Manager) begin(ctx context.Context) (context.Context, func(), error) {
	if manager == nil || ctx == nil {
		return nil, nil, ErrInvalidEvidence
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	manager.mu.Lock()
	if manager.closing {
		manager.mu.Unlock()
		return nil, nil, ErrRuntimeStopping
	}
	manager.active++
	manager.notifyLocked()
	manager.mu.Unlock()
	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			manager.mu.Lock()
			manager.active--
			manager.notifyLocked()
			manager.mu.Unlock()
		})
	}, nil
}

func (manager *Manager) notifyLocked() {
	close(manager.changed)
	manager.changed = make(chan struct{})
}

var _ Runtime = (*Manager)(nil)
