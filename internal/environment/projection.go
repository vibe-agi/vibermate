package environment

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/vibe-agi/vibermate/internal/originidentity"
)

type ProjectionState string

const (
	ProjectionStateUnrestored  ProjectionState = "unrestored"
	ProjectionStateHealthy     ProjectionState = "healthy"
	ProjectionStateUnavailable ProjectionState = "unavailable"
)

type ProjectionHealth struct {
	State                   ProjectionState `json:"state"`
	UnavailableEnvironments []EnvironmentID `json:"unavailableEnvironments"`
}

type SnapshotResolver interface {
	Resolve(EnvironmentID) (EnvironmentSnapshot, error)
	ResolveRevision(context.Context, EnvironmentID, Revision) (EnvironmentSnapshot, error)
	ResolveClientOrigin(EnvironmentID, originidentity.ClientOrigin) (ClientEndpointSnapshot, error)
}

type SnapshotProjection interface {
	SnapshotResolver
	Restore(EnvironmentSnapshot, []EnvironmentSnapshot) error
	Publish(EnvironmentSnapshot) error
	Retire(EnvironmentID) error
	MarkUnavailable(EnvironmentID)
	Health() ProjectionHealth
}

type projectionData struct {
	restored  bool
	byID      map[EnvironmentID]EnvironmentSnapshot
	revisions map[EnvironmentID]Revision
	poisoned  map[EnvironmentID]struct{}
}

// AtomicProjection publishes a whole copy-on-write catalog. Readers take one
// atomic pointer and never observe a partially replaced Environment.
type AtomicProjection struct {
	writes sync.Mutex
	state  atomic.Pointer[projectionData]
}

var _ SnapshotProjection = (*AtomicProjection)(nil)

func NewAtomicProjection() *AtomicProjection {
	projection := &AtomicProjection{}
	projection.state.Store(&projectionData{
		byID:      map[EnvironmentID]EnvironmentSnapshot{},
		revisions: map[EnvironmentID]Revision{},
		poisoned:  map[EnvironmentID]struct{}{},
	})
	return projection
}

func (projection *AtomicProjection) Restore(
	system EnvironmentSnapshot,
	snapshots []EnvironmentSnapshot,
) error {
	if projection == nil {
		return ErrProjectionUnavailable
	}
	projection.writes.Lock()
	defer projection.writes.Unlock()
	if projection.state.Load().restored {
		return ErrProjectionRestored
	}
	if !system.SystemOwned() || system.ID() != SystemTransparentID ||
		system.validate() != nil {
		return ErrSystemEnvironment
	}
	next := &projectionData{
		restored:  true,
		byID:      make(map[EnvironmentID]EnvironmentSnapshot, len(snapshots)+1),
		revisions: make(map[EnvironmentID]Revision, len(snapshots)+1),
		poisoned:  make(map[EnvironmentID]struct{}),
	}
	next.byID[SystemTransparentID] = system.clone()
	next.revisions[SystemTransparentID] = system.Revision()
	for _, snapshot := range snapshots {
		if snapshot.ID() == SystemTransparentID || snapshot.SystemOwned() {
			return ErrSystemEnvironment
		}
		if err := snapshot.validate(); err != nil {
			return err
		}
		if _, exists := next.byID[snapshot.ID()]; exists {
			return fmt.Errorf("%w: duplicate Environment %q", ErrInvalidRepositoryState, snapshot.ID())
		}
		next.byID[snapshot.ID()] = snapshot.clone()
		next.revisions[snapshot.ID()] = snapshot.Revision()
	}
	projection.state.Store(next)
	return nil
}

func (projection *AtomicProjection) Publish(snapshot EnvironmentSnapshot) error {
	if projection == nil {
		return ErrProjectionUnavailable
	}
	if snapshot.ID() == SystemTransparentID || snapshot.SystemOwned() {
		return ErrSystemEnvironment
	}
	if err := snapshot.validate(); err != nil {
		return err
	}
	projection.writes.Lock()
	defer projection.writes.Unlock()
	current := projection.state.Load()
	if !current.restored {
		return ErrProjectionNotRestored
	}
	if _, poisoned := current.poisoned[snapshot.ID()]; poisoned {
		return fmt.Errorf("%w: environmentId=%q", ErrProjectionUnavailable, snapshot.ID())
	}
	last, exists := current.revisions[snapshot.ID()]
	if (!exists && snapshot.Revision() != 1) ||
		(exists && (last >= MaxRevision || snapshot.Revision() != last+1)) {
		return fmt.Errorf("%w: revision %d does not continuously advance %d", ErrRevisionConflict, snapshot.Revision(), last)
	}
	next := cloneProjection(current)
	next.byID[snapshot.ID()] = snapshot.clone()
	next.revisions[snapshot.ID()] = snapshot.Revision()
	projection.state.Store(next)
	return nil
}

// Retire removes an Environment from the live data-plane catalog while
// preserving its last revision watermark. Immutable historical revisions are
// resolved by the repository; keeping the watermark here prevents a retired
// stable ID from silently restarting its revision sequence if it is reused.
func (projection *AtomicProjection) Retire(id EnvironmentID) error {
	if projection == nil {
		return ErrProjectionUnavailable
	}
	if id == SystemTransparentID {
		return ErrSystemEnvironment
	}
	if err := validateID("Environment ID", id.String()); err != nil {
		return err
	}
	projection.writes.Lock()
	defer projection.writes.Unlock()
	current := projection.state.Load()
	if !current.restored {
		return ErrProjectionNotRestored
	}
	if _, exists := current.byID[id]; !exists {
		return fmt.Errorf("%w: environmentId=%q", ErrEnvironmentNotFound, id)
	}
	next := cloneProjection(current)
	delete(next.byID, id)
	delete(next.poisoned, id)
	projection.state.Store(next)
	return nil
}

func (projection *AtomicProjection) MarkUnavailable(id EnvironmentID) {
	if projection == nil || id == SystemTransparentID {
		return
	}
	projection.writes.Lock()
	defer projection.writes.Unlock()
	current := projection.state.Load()
	next := cloneProjection(current)
	next.poisoned[id] = struct{}{}
	projection.state.Store(next)
}

func (projection *AtomicProjection) Resolve(id EnvironmentID) (EnvironmentSnapshot, error) {
	if projection == nil {
		return EnvironmentSnapshot{}, ErrProjectionUnavailable
	}
	if err := validateID("Environment ID", id.String()); err != nil {
		return EnvironmentSnapshot{}, err
	}
	state := projection.state.Load()
	if !state.restored {
		return EnvironmentSnapshot{}, ErrProjectionNotRestored
	}
	if _, unavailable := state.poisoned[id]; unavailable {
		return EnvironmentSnapshot{}, fmt.Errorf("%w: environmentId=%q", ErrProjectionUnavailable, id)
	}
	snapshot, exists := state.byID[id]
	if !exists {
		return EnvironmentSnapshot{}, fmt.Errorf("%w: environmentId=%q", ErrEnvironmentNotFound, id)
	}
	if snapshot.State() != StateActive {
		return EnvironmentSnapshot{}, fmt.Errorf("%w: environmentId=%q revision=%d", ErrEnvironmentDisabled, id, snapshot.Revision())
	}
	return snapshot.clone(), nil
}

func (projection *AtomicProjection) ResolveClientOrigin(id EnvironmentID, origin originidentity.ClientOrigin) (ClientEndpointSnapshot, error) {
	snapshot, err := projection.Resolve(id)
	if err != nil {
		return ClientEndpointSnapshot{}, err
	}
	endpoint, exists := snapshot.LookupClientOrigin(origin)
	if !exists {
		return ClientEndpointSnapshot{}, fmt.Errorf("%w: environmentId=%q clientOrigin=%q", ErrEnvironmentNotFound, id, origin.String())
	}
	return endpoint, nil
}

// ResolveRevision serves the exact revision currently held by this projection.
// Historical resolution belongs to Manager, whose repository retains every
// published revision. Keeping the method here makes a projection useful in
// bounded tests without pretending it is a historical archive.
func (projection *AtomicProjection) ResolveRevision(
	ctx context.Context,
	id EnvironmentID,
	revision Revision,
) (EnvironmentSnapshot, error) {
	if ctx == nil || revision == 0 {
		return EnvironmentSnapshot{}, ErrInvalidEnvironment
	}
	if err := ctx.Err(); err != nil {
		return EnvironmentSnapshot{}, err
	}
	snapshot, err := projection.Resolve(id)
	if err != nil {
		return EnvironmentSnapshot{}, err
	}
	if snapshot.Revision() != revision {
		return EnvironmentSnapshot{}, fmt.Errorf(
			"%w: environmentId=%q revision=%d",
			ErrEnvironmentNotFound,
			id,
			revision,
		)
	}
	return snapshot, nil
}

func (projection *AtomicProjection) Health() ProjectionHealth {
	if projection == nil {
		return ProjectionHealth{State: ProjectionStateUnavailable}
	}
	state := projection.state.Load()
	if !state.restored {
		return ProjectionHealth{State: ProjectionStateUnrestored}
	}
	ids := make([]EnvironmentID, 0, len(state.poisoned))
	for id := range state.poisoned {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(left, right int) bool { return ids[left] < ids[right] })
	if len(ids) != 0 {
		return ProjectionHealth{State: ProjectionStateUnavailable, UnavailableEnvironments: ids}
	}
	return ProjectionHealth{State: ProjectionStateHealthy}
}

func cloneProjection(current *projectionData) *projectionData {
	next := &projectionData{
		restored:  current.restored,
		byID:      make(map[EnvironmentID]EnvironmentSnapshot, len(current.byID)),
		revisions: make(map[EnvironmentID]Revision, len(current.revisions)),
		poisoned:  make(map[EnvironmentID]struct{}, len(current.poisoned)),
	}
	for id, snapshot := range current.byID {
		next.byID[id] = snapshot
	}
	for id, revision := range current.revisions {
		next.revisions[id] = revision
	}
	for id := range current.poisoned {
		next.poisoned[id] = struct{}{}
	}
	return next
}
