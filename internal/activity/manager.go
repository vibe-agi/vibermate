package activity

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

const activityIDBytes = 20

type Clock interface {
	Now() time.Time
}

type SystemClock struct{}

func (SystemClock) Now() time.Time {
	return time.Now().UTC()
}

type Options struct {
	Repository Repository
	Clock      Clock
	Random     io.Reader
}

func DefaultOptions(repository Repository) Options {
	return Options{
		Repository: repository,
		Clock:      SystemClock{},
		Random:     rand.Reader,
	}
}

type Manager struct {
	repository Repository
	clock      Clock
	random     io.Reader

	mu      sync.Mutex
	closing bool
	active  int
	changed chan struct{}
}

func New(options Options) (*Manager, error) {
	if options.Repository == nil ||
		options.Clock == nil ||
		options.Random == nil {
		return nil, errors.New("Activity dependencies are incomplete")
	}
	return &Manager{
		repository: options.Repository,
		clock:      options.Clock,
		random:     options.Random,
		changed:    make(chan struct{}),
	}, nil
}

func (manager *Manager) Record(
	ctx context.Context,
	event Event,
) (Record, error) {
	if err := event.Validate(); err != nil {
		return Record{}, err
	}
	operation, finish, err := manager.begin(ctx)
	if err != nil {
		return Record{}, err
	}
	defer finish()
	identifier := make([]byte, activityIDBytes)
	if _, err := io.ReadFull(manager.random, identifier); err != nil {
		return Record{}, fmt.Errorf("generate Activity ID: %w", err)
	}
	record := Record{
		ID:         base64.RawURLEncoding.EncodeToString(identifier),
		OccurredAt: manager.clock.Now().UTC(),
		Kind:       event.Kind,
		AccessID:   event.AccessID.String(),
		SubjectID:  event.SubjectID,
		Status:     event.Status,
		ReasonCode: event.ReasonCode,
	}
	if !event.Diagnosis.Empty() {
		diagnosis := event.Diagnosis
		record.Diagnosis = &diagnosis
	}
	if event.Transport != nil {
		transport := event.Transport.Clone()
		record.Transport = &transport
	}
	return manager.repository.Append(operation, record)
}

func (manager *Manager) List(
	ctx context.Context,
	request PageRequest,
) (Page, error) {
	if manager == nil {
		return Page{}, ErrInvalidEvent
	}
	return manager.list(ctx, request, manager.repository.List)
}

// GetExchange returns the one durable terminal record for a logical Exchange.
// A duplicate terminal is a storage contradiction and is rejected by the
// repository rather than selecting whichever row happened to sort first.
func (manager *Manager) GetExchange(
	ctx context.Context,
	exchangeID string,
) (Record, error) {
	if manager == nil || validateIdentity("Exchange ID", exchangeID, false) != nil {
		return Record{}, ErrInvalidEvent
	}
	operation, finish, err := manager.begin(ctx)
	if err != nil {
		return Record{}, err
	}
	defer finish()
	record, err := manager.repository.GetExchange(operation, exchangeID)
	if err != nil {
		return Record{}, err
	}
	if record.Transport != nil {
		transport := record.Transport.Clone()
		record.Transport = &transport
	}
	return record, nil
}

// ListExchanges returns only durable Exchange terminal records. Keeping this
// read distinct from List prevents a control-surface projection from filtering
// a mixed page after pagination has already been applied.
func (manager *Manager) ListExchanges(
	ctx context.Context,
	request PageRequest,
) (Page, error) {
	if manager == nil {
		return Page{}, ErrInvalidEvent
	}
	return manager.list(ctx, request, manager.repository.ListExchanges)
}

func (manager *Manager) list(
	ctx context.Context,
	request PageRequest,
	read func(context.Context, PageRequest) (Page, error),
) (Page, error) {
	if err := request.Validate(); err != nil {
		return Page{}, err
	}
	operation, finish, err := manager.begin(ctx)
	if err != nil {
		return Page{}, err
	}
	defer finish()
	page, err := read(operation, request)
	if err != nil {
		return Page{}, err
	}
	page.Items = append([]Record{}, page.Items...)
	for index := range page.Items {
		if page.Items[index].Transport != nil {
			transport := page.Items[index].Transport.Clone()
			page.Items[index].Transport = &transport
		}
	}
	return page, nil
}

func (manager *Manager) Shutdown(ctx context.Context) error {
	if manager == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("Activity shutdown context is nil")
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

func (manager *Manager) begin(
	ctx context.Context,
) (context.Context, func(), error) {
	if manager == nil || ctx == nil {
		return nil, nil, ErrInvalidEvent
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
