package access

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

var (
	ErrProjectionAlreadyRestored   = errors.New("Access snapshot projection was already restored")
	ErrPublishedRevisionRegression = errors.New("published Access revision would not advance")
)

// ProjectionState is a language-independent health state for the process-local
// Access projection.
type ProjectionState string

const (
	ProjectionStateHealthy     ProjectionState = "healthy"
	ProjectionStateUnavailable ProjectionState = "unavailable"
)

// ProjectionHealth is an immutable summary. UnavailableAccessCount does not
// expose the identities of aggregates whose durable and projected state may
// differ.
type ProjectionHealth struct {
	State                  ProjectionState `json:"state"`
	UnavailableAccessCount int             `json:"unavailableAccessCount"`
}

// ProjectionHealthReader exposes projection trust independently from product
// readiness.
type ProjectionHealthReader interface {
	ProjectionHealth() ProjectionHealth
}

// SnapshotResolver resolves the immutable snapshot for one typed AccessID.
type SnapshotResolver interface {
	ResolveAccess(AccessID) (Snapshot, error)
}

// SnapshotProjection is the explicit process-local publication dependency.
// Manager is its only production mutation authority.
type SnapshotProjection interface {
	SnapshotResolver
	ProjectionHealthReader
	Restore([]Snapshot) error
	Publish(Snapshot) error
	MarkUnavailable(AccessID)
}

type projectionState struct {
	byAccess    map[AccessID]Snapshot
	unavailable map[AccessID]struct{}
}

// AtomicSnapshotProjection publishes complete immutable catalog replacements.
type AtomicSnapshotProjection struct {
	state atomic.Pointer[projectionState]

	restoreMu sync.Mutex
	restored  bool
}

var _ SnapshotProjection = (*AtomicSnapshotProjection)(nil)

func NewSnapshotProjection() *AtomicSnapshotProjection {
	projection := &AtomicSnapshotProjection{}
	projection.state.Store(&projectionState{
		byAccess:    map[AccessID]Snapshot{},
		unavailable: map[AccessID]struct{}{},
	})
	return projection
}

// Restore installs the complete projection recovered from SQLite exactly once.
func (p *AtomicSnapshotProjection) Restore(snapshots []Snapshot) error {
	next := make(map[AccessID]Snapshot, len(snapshots))
	for _, snapshot := range snapshots {
		if err := snapshot.validate(); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidRepositoryState, err)
		}
		if _, exists := next[snapshot.AccessID()]; exists {
			return fmt.Errorf(
				"%w: duplicate Access ID %q",
				ErrInvalidRepositoryState,
				snapshot.AccessID().String(),
			)
		}
		next[snapshot.AccessID()] = snapshot
	}

	p.restoreMu.Lock()
	defer p.restoreMu.Unlock()
	if p.restored {
		return ErrProjectionAlreadyRestored
	}
	p.state.Store(&projectionState{
		byAccess:    next,
		unavailable: map[AccessID]struct{}{},
	})
	p.restored = true
	return nil
}

// Publish atomically installs one strictly newer aggregate snapshot.
func (p *AtomicSnapshotProjection) Publish(candidate Snapshot) error {
	if err := candidate.validate(); err != nil {
		return err
	}

	p.restoreMu.Lock()
	restored := p.restored
	p.restoreMu.Unlock()
	if !restored {
		return errors.New("Access snapshot projection is not restored")
	}

	for {
		current := p.state.Load()
		if _, unavailable := current.unavailable[candidate.AccessID()]; unavailable {
			return fmt.Errorf(
				"%w: accessId=%q",
				ErrProjectionUnavailable,
				candidate.AccessID().String(),
			)
		}
		if published, exists := current.byAccess[candidate.AccessID()]; exists &&
			candidate.Revision() <= published.Revision() {
			return fmt.Errorf(
				"%w: accessId=%q published=%d candidate=%d",
				ErrPublishedRevisionRegression,
				candidate.AccessID().String(),
				published.Revision(),
				candidate.Revision(),
			)
		}

		next := make(map[AccessID]Snapshot, len(current.byAccess)+1)
		for accessID, snapshot := range current.byAccess {
			next[accessID] = snapshot
		}
		next[candidate.AccessID()] = candidate
		unavailable := cloneUnavailable(current.unavailable, 0)
		if p.state.CompareAndSwap(current, &projectionState{
			byAccess:    next,
			unavailable: unavailable,
		}) {
			return nil
		}
	}
}

// MarkUnavailable prevents new resolutions and writes from treating a stale
// process projection as authoritative. Existing Snapshot values remain valid
// immutable handles. Startup recovery installs a fresh projection.
func (p *AtomicSnapshotProjection) MarkUnavailable(accessID AccessID) {
	for {
		current := p.state.Load()
		if _, exists := current.unavailable[accessID]; exists {
			return
		}
		unavailable := cloneUnavailable(current.unavailable, 1)
		unavailable[accessID] = struct{}{}
		if p.state.CompareAndSwap(current, &projectionState{
			byAccess:    current.byAccess,
			unavailable: unavailable,
		}) {
			return
		}
	}
}

func (p *AtomicSnapshotProjection) ProjectionHealth() ProjectionHealth {
	unavailableCount := len(p.state.Load().unavailable)
	if unavailableCount > 0 {
		return ProjectionHealth{
			State:                  ProjectionStateUnavailable,
			UnavailableAccessCount: unavailableCount,
		}
	}
	return ProjectionHealth{State: ProjectionStateHealthy}
}

func (p *AtomicSnapshotProjection) ResolveAccess(accessID AccessID) (Snapshot, error) {
	if err := accessID.validate(); err != nil {
		return Snapshot{}, newFailure(
			ReasonInvalidAccess,
			ErrInvalidAccess,
			accessID,
			0,
			0,
			err,
		)
	}
	state := p.state.Load()
	snapshot, exists := state.byAccess[accessID]
	if _, unavailable := state.unavailable[accessID]; unavailable {
		actualRevision := Revision(0)
		if exists {
			actualRevision = snapshot.Revision()
		}
		return Snapshot{}, newFailure(
			ReasonProjectionUnavailable,
			ErrProjectionUnavailable,
			accessID,
			0,
			actualRevision,
			nil,
		)
	}
	if !exists {
		return Snapshot{}, newFailure(
			ReasonAccessNotConfigured,
			ErrAccessNotConfigured,
			accessID,
			0,
			0,
			nil,
		)
	}
	return snapshot, nil
}

func cloneUnavailable(
	current map[AccessID]struct{},
	additionalCapacity int,
) map[AccessID]struct{} {
	next := make(map[AccessID]struct{}, len(current)+additionalCapacity)
	for accessID := range current {
		next[accessID] = struct{}{}
	}
	return next
}
