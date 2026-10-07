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
	GetConversationEvidence(context.Context, string, time.Time) (ConversationEvidence, error)
	GetProjection(context.Context, string, time.Time, RequestView) (Projection, error)
	GetPagedProjection(context.Context, string, time.Time, RequestView) (Projection, error)
	GetContentPage(context.Context, string, time.Time, string) (ContentPage, error)
	RequestPreviews(context.Context, []string, time.Time) (map[string]RequestPreview, error)
	AvailableBodies(context.Context, []string, time.Time) (map[string]bool, error)
	PurgeExpired(context.Context, time.Time) (uint64, error)
}

type Recorder interface {
	Record(context.Context, Record) error
}

// SourceRecorder consumes borrowed immutable content synchronously. Returning
// ends the borrow; implementations may not retain Source or its input.
type SourceRecorder interface {
	RecordSource(context.Context, *Source) error
}
type SourceRepository interface {
	PutSource(context.Context, *Source) error
}

type Reader interface {
	Get(context.Context, string) (Record, error)
	GetConversationEvidence(context.Context, string) (ConversationEvidence, error)
	GetProjection(context.Context, string, RequestView) (Projection, error)
	GetPagedProjection(context.Context, string, RequestView) (Projection, error)
	GetContentPage(context.Context, string, string) (ContentPage, error)
	RequestPreviews(context.Context, []string) (map[string]RequestPreview, error)
	AvailableBodies(context.Context, []string) (map[string]bool, error)
}

type Runtime interface {
	Recorder
	Reader
	Shutdown(context.Context) error
}

type Options struct {
	Repository    Repository
	Clock         Clock
	ContentLimits *SourceLimits
}

type Manager struct {
	limits           *SourceLimits
	sourceRepository SourceRepository
	repository       Repository
	clock            Clock

	mu      sync.Mutex
	closing bool
	active  int
	changed chan struct{}
}

func New(ctx context.Context, options Options) (*Manager, error) {
	if ctx == nil || options.Repository == nil || options.Clock == nil {
		return nil, errors.New("Exchange content dependencies are incomplete")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	manager := &Manager{
		repository: options.Repository,
		clock:      options.Clock,
		changed:    make(chan struct{}),
	}
	if options.ContentLimits == nil {
		limits, err := DefaultSourceLimits()
		if err != nil {
			return nil, err
		}
		options.ContentLimits = &limits
	}
	if options.ContentLimits != nil {
		limits := *options.ContentLimits
		if err := limits.Validate(); err != nil {
			return nil, err
		}
		sink, ok := options.Repository.(SourceRepository)
		if !ok {
			return nil, errors.New("candidate content repository lacks synchronous Source support")
		}
		manager.limits, manager.sourceRepository = &limits, sink
	}
	return manager, nil
}

func (manager *Manager) RecordSource(ctx context.Context, source *Source) error {
	if manager == nil || manager.limits == nil || source == nil || source.limits != *manager.limits || !source.metadata.ExpiresAt.After(manager.clock.Now().UTC()) {
		return ErrInvalidEvidence
	}
	operation, finish, err := manager.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	return manager.sourceRepository.PutSource(operation, source)
}

func (manager *Manager) Record(ctx context.Context, record Record) error {
	if manager == nil || manager.limits == nil {
		return ErrInvalidEvidence
	}
	if _, err := SourceFromRecordWithin(*manager.limits, record); err != nil {
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
	// Expiry is enforced on reads; Store maintenance reclaims bytes separately.
	// A recording budget must not include unrelated retention work.
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
	if manager.limits != nil {
		if _, err := SourceFromRecordWithin(*manager.limits, record); err != nil {
			return Record{}, err
		}
	}
	return record.Clone(), nil
}

func (manager *Manager) GetProjection(
	ctx context.Context,
	exchangeID string,
	view RequestView,
) (Projection, error) {
	if !validIdentity(exchangeID, MaxExchangeIDBytes) ||
		(view != RequestViewFull && view != RequestViewIncremental) {
		return Projection{}, ErrInvalidEvidence
	}
	operation, finish, err := manager.begin(ctx)
	if err != nil {
		return Projection{}, err
	}
	defer finish()
	projection, err := manager.repository.GetProjection(
		operation,
		exchangeID,
		manager.clock.Now().UTC(),
		view,
	)
	if err != nil {
		return Projection{}, err
	}
	if err := manager.validateProjection(operation, projection); err != nil {
		return Projection{}, err
	}
	var workspace presentationWorkspace
	if manager.limits != nil {
		workspace, err = presentationWorkspaceFor(projection.Response)
		if err != nil {
			return Projection{}, err
		}
	}
	projection = projection.Clone()
	if projection.Response != nil {
		if manager.limits != nil {
			projection.Response.Blocks, err = foldPresentation(operation, projection.Response.Blocks, workspace.candidates)
			if err != nil {
				return Projection{}, err
			}
		} else {
			projection.Response.Blocks = mergeDuplicateReadableReasoning(
				projection.Response.Blocks,
			)
		}
	}
	if err := manager.validateProjection(operation, projection); err != nil {
		return Projection{}, err
	}
	return projection, nil
}

func (manager *Manager) validateProjection(ctx context.Context, p Projection) error {
	if manager.limits != nil {
		return p.ValidateWithin(ctx, *manager.limits)
	}
	return p.Validate()
}

func (manager *Manager) RequestPreviews(
	ctx context.Context,
	exchangeIDs []string,
) (map[string]RequestPreview, error) {
	if len(exchangeIDs) > MaxRequestPreviewBatch {
		return nil, ErrInvalidEvidence
	}
	wanted := make(map[string]struct{}, len(exchangeIDs))
	unique := make([]string, 0, len(exchangeIDs))
	for _, exchangeID := range exchangeIDs {
		if !validIdentity(exchangeID, MaxExchangeIDBytes) {
			return nil, ErrInvalidEvidence
		}
		if _, exists := wanted[exchangeID]; exists {
			continue
		}
		wanted[exchangeID] = struct{}{}
		unique = append(unique, exchangeID)
	}
	if len(unique) == 0 {
		return map[string]RequestPreview{}, nil
	}
	operation, finish, err := manager.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	previews, err := manager.repository.RequestPreviews(
		operation, unique, manager.clock.Now().UTC(),
	)
	if err != nil {
		return nil, err
	}
	for exchangeID, preview := range previews {
		if _, requested := wanted[exchangeID]; !requested || preview.Validate() != nil {
			return nil, ErrInvalidEvidence
		}
	}
	return previews, nil
}

func (manager *Manager) AvailableBodies(ctx context.Context, exchangeIDs []string) (map[string]bool, error) {
	if len(exchangeIDs) > MaxRequestPreviewBatch {
		return nil, ErrInvalidEvidence
	}
	for _, id := range exchangeIDs {
		if !validIdentity(id, MaxExchangeIDBytes) {
			return nil, ErrInvalidEvidence
		}
	}
	operation, finish, err := manager.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	return manager.repository.AvailableBodies(operation, exchangeIDs, manager.clock.Now().UTC())
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
