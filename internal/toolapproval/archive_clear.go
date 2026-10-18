package toolapproval

import (
	"context"
	"math"
	"sync"
)

// ArchiveClearLease owns the required approval publication boundary until its
// caller has the actual outcome of the required archive-clear transaction.
// Complete(true) publishes that durable clear fact to existing pending waiters;
// Complete(false) releases admission without inventing a cancellation.
type ArchiveClearLease interface{ Complete(committed bool) }
type ArchiveClearAuthority interface {
	BeginArchiveClear(context.Context) (ArchiveClearLease, error)
}
type archiveClearLease struct {
	authority *Authority
	finish    func()
	once      sync.Once
}

func (l *archiveClearLease) Complete(committed bool) {
	l.once.Do(func() {
		if committed {
			// Every creation/decision publication admitted before us has returned. A
			// delivered decision has already left the registry and stays historical.
			l.authority.waiters.resolveArchiveClear()
			l.authority.mu.Lock()
			clear(l.authority.ephemeralSubjectLabels)
			l.authority.notifyLocked()
			l.authority.mu.Unlock()
		}
		l.authority.publication.Release(math.MaxInt64)
		l.finish()
	})
}
func (authority *Authority) BeginArchiveClear(ctx context.Context) (ArchiveClearLease, error) {
	operation, finish, err := authority.begin(ctx)
	if err != nil {
		return nil, err
	}
	if err = authority.publication.Acquire(operation, math.MaxInt64); err != nil {
		finish()
		return nil, err
	}
	authority.mu.Lock()
	closing := authority.closing
	authority.mu.Unlock()
	if closing {
		authority.publication.Release(math.MaxInt64)
		finish()
		return nil, ErrRuntimeStopping
	}
	return &archiveClearLease{authority: authority, finish: finish}, nil
}

// Only short SQL/create/publication sections use this gate. Human answers and
// optional recording never retain a shared slot, and clear takes every slot.
func (authority *Authority) acquirePublication(ctx context.Context) (func(), error) {
	if err := authority.publication.Acquire(ctx, 1); err != nil {
		return nil, err
	}
	authority.mu.Lock()
	closing := authority.closing
	authority.mu.Unlock()
	if closing {
		authority.publication.Release(1)
		return nil, ErrRuntimeStopping
	}
	var once sync.Once
	return func() { once.Do(func() { authority.publication.Release(1) }) }, nil
}

func (registry *waiterRegistry) resolveArchiveClear() {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	for aggregate, entry := range registry.byAggregate {
		fact := Record{ID: entry.recordID, State: StateCanceled, DecisionReason: "archive_cleared"}
		for _, pending := range entry.waiters {
			select {
			case pending.result <- fact:
			default:
			}
		}
		entry.waiters = nil
		delete(registry.byRecord, entry.recordID)
		delete(registry.byAggregate, aggregate)
	}
}
