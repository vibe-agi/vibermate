package connectionpolicy

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Clock is the runtime's time source. Storage never reads the wall clock on
// its own, so a caller always says when a change happened.
type Clock interface {
	Now() time.Time
}

// Manager owns the rules a person edits: it loads them at startup, seeds a
// fresh store with the shipped set, and swaps them while the runtime runs.
//
// The set in force is only ever replaced by one that has already been stored,
// so a runtime cannot end up evaluating rules that were refused, and it cannot
// end up evaluating rules that a restart would not bring back.
type Manager struct {
	repository Repository
	clock      Clock
	live       *Live

	mu      sync.Mutex
	current Snapshot
}

type ManagerOptions struct {
	Repository Repository
	Clock      Clock
	// Shipped is written only when nothing is stored yet.
	Shipped Snapshot
}

func NewManager(
	ctx context.Context,
	options ManagerOptions,
) (*Manager, error) {
	if options.Repository == nil || options.Clock == nil {
		return nil, errors.New("connection rule manager dependencies are incomplete")
	}
	stored, err := options.Repository.Load(ctx)
	if errors.Is(err, ErrNoRuleSet) {
		stored, err = options.Repository.Seed(
			ctx,
			options.Shipped,
			options.Clock.Now().UTC(),
		)
	}
	if err != nil {
		return nil, err
	}
	compiled, err := stored.Compile()
	if err != nil {
		return nil, err
	}
	return &Manager{
		repository: options.Repository,
		clock:      options.Clock,
		live:       NewLive(compiled),
		current:    stored,
	}, nil
}

// Source is what the proxy reads per connection.
func (manager *Manager) Source() Source { return manager.live }

// Current is the stored set as a person would read it back.
func (manager *Manager) Current() Snapshot {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.current
}

// Replace stores a whole new set and puts it in force. A set that will not
// construct, or that was prepared against a revision that has since moved, is
// refused and leaves the rules in force untouched.
func (manager *Manager) Replace(
	ctx context.Context,
	expectedRevision uint64,
	rules []Rule,
	fallback Rule,
) (Snapshot, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.current.Revision != expectedRevision {
		return manager.current, ErrRevisionConflict
	}
	proposed := Snapshot{
		Revision: manager.current.Revision + 1,
		Rules:    rules,
		Default:  fallback,
	}
	compiled, err := proposed.Compile()
	if err != nil {
		return manager.current, err
	}
	stored, err := manager.repository.Replace(
		ctx,
		expectedRevision,
		proposed,
		manager.clock.Now().UTC(),
	)
	if err != nil {
		return manager.current, err
	}
	manager.current = stored
	manager.live.Adopt(compiled)
	return stored, nil
}
