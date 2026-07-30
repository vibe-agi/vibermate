package access

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
)

var (
	ErrProjectionAlreadyRestored   = errors.New("Access snapshot projection was already restored")
	ErrPublishedRevisionRegression = errors.New("published Access revision would not advance")
)

// ProjectionState is a language-independent health state for the process-local
// Access plan projection.
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

// SnapshotResolver is the only active-plan read boundary.
type SnapshotResolver interface {
	ResolveAccess(AccessID) (AccessPlanSnapshot, error)
}

// ProviderProbeTarget binds an enabled target to the exact Access projection
// from which it was compiled. It contains no secret or mutable authority.
type ProviderProbeTarget struct {
	reference string
	revision  Revision
	planHash  PlanHash
	target    CompiledProviderTarget
}

func (target ProviderProbeTarget) Reference() string {
	return target.reference
}

func (target ProviderProbeTarget) AccessRevision() Revision {
	return target.revision
}

func (target ProviderProbeTarget) PlanHash() PlanHash {
	return target.planHash
}

func (target ProviderProbeTarget) Target() CompiledProviderTarget {
	cloned := target.target
	cloned.target.Capabilities = slices.Clone(target.target.target.Capabilities)
	return cloned
}

// ProviderProbeCatalog exposes only enabled compiled target identities to the
// Offline Hold resume boundary. It is not a mutable routing registry.
type ProviderProbeCatalog interface {
	ActiveProviderProbeTargets() ([]ProviderProbeTarget, error)
}

// SnapshotProjection is the explicit process-local active-plan publication
// dependency. Manager is its only production mutation authority.
type SnapshotProjection interface {
	SnapshotResolver
	IngressResolver
	IngressCatalogReader
	ProviderProbeCatalog
	ProjectionHealthReader
	Restore([]AccessPlanSnapshot) error
	Publish(AccessPlanSnapshot) error
	MarkUnavailable(AccessID)
}

func (p *AtomicSnapshotProjection) ActiveClientAuthorities() ([]string, error) {
	state := p.state.Load()
	authorities := make([]string, 0, len(state.byOrigin))
	for authority, binding := range state.byOrigin {
		if _, unavailable := state.unavailable[binding.AccessID()]; unavailable {
			return nil, fmt.Errorf(
				"%w: accessId=%q",
				ErrProjectionUnavailable,
				binding.AccessID().String(),
			)
		}
		snapshot := state.byAccess[binding.AccessID()]
		if snapshot.Binding().Status == AccessStatusEnabled {
			authorities = append(authorities, authority)
		}
	}
	sort.Strings(authorities)
	return authorities, nil
}

type projectionState struct {
	byAccess    map[AccessID]AccessPlanSnapshot
	byOrigin    map[string]IngressBinding
	byProvider  map[string]providerProbeBinding
	unavailable map[AccessID]struct{}
}

type providerProbeBinding struct {
	accessID AccessID
	revision Revision
	planHash PlanHash
	target   CompiledProviderTarget
}

// AtomicSnapshotProjection publishes complete immutable active-plan catalog
// replacements.
type AtomicSnapshotProjection struct {
	state atomic.Pointer[projectionState]

	restoreMu sync.Mutex
	restored  bool
}

var _ SnapshotProjection = (*AtomicSnapshotProjection)(nil)

func NewSnapshotProjection() *AtomicSnapshotProjection {
	projection := &AtomicSnapshotProjection{}
	projection.state.Store(&projectionState{
		byAccess:    map[AccessID]AccessPlanSnapshot{},
		byOrigin:    map[string]IngressBinding{},
		byProvider:  map[string]providerProbeBinding{},
		unavailable: map[AccessID]struct{}{},
	})
	return projection
}

// Restore installs the complete projection recovered from SQLite exactly once.
func (p *AtomicSnapshotProjection) Restore(snapshots []AccessPlanSnapshot) error {
	next := make(map[AccessID]AccessPlanSnapshot, len(snapshots))
	byOrigin := make(map[string]IngressBinding, len(snapshots))
	byProvider := make(map[string]providerProbeBinding)
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
		cloned := snapshot.clone()
		binding := ingressBindingFromSnapshot(cloned)
		origin := binding.ClientOrigin().EndpointAuthority()
		if existing, exists := byOrigin[origin]; exists {
			return fmt.Errorf(
				"%w: clientOrigin=%q accessId=%q conflictingAccessId=%q",
				ErrAgentEndpointConflict,
				origin,
				binding.AccessID().String(),
				existing.AccessID().String(),
			)
		}
		next[snapshot.AccessID()] = cloned
		byOrigin[origin] = binding
		addProviderProbeBindings(byProvider, cloned)
	}

	p.restoreMu.Lock()
	defer p.restoreMu.Unlock()
	if p.restored {
		return ErrProjectionAlreadyRestored
	}
	p.state.Store(&projectionState{
		byAccess:    next,
		byOrigin:    byOrigin,
		byProvider:  byProvider,
		unavailable: map[AccessID]struct{}{},
	})
	p.restored = true
	return nil
}

// Publish atomically installs one strictly newer compiled Access plan.
func (p *AtomicSnapshotProjection) Publish(candidate AccessPlanSnapshot) error {
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

		next := make(map[AccessID]AccessPlanSnapshot, len(current.byAccess)+1)
		for accessID, snapshot := range current.byAccess {
			next[accessID] = snapshot
		}
		byOrigin := make(map[string]IngressBinding, len(current.byOrigin)+1)
		for origin, binding := range current.byOrigin {
			if binding.AccessID() != candidate.AccessID() {
				byOrigin[origin] = binding
			}
		}
		byProvider := make(
			map[string]providerProbeBinding,
			len(current.byProvider)+len(candidate.compiledTargets),
		)
		for reference, provider := range current.byProvider {
			if provider.accessID != candidate.AccessID() {
				byProvider[reference] = provider
			}
		}
		cloned := candidate.clone()
		binding := ingressBindingFromSnapshot(cloned)
		origin := binding.ClientOrigin().EndpointAuthority()
		if existing, exists := byOrigin[origin]; exists {
			return fmt.Errorf(
				"%w: clientOrigin=%q accessId=%q conflictingAccessId=%q",
				ErrAgentEndpointConflict,
				origin,
				binding.AccessID().String(),
				existing.AccessID().String(),
			)
		}
		next[candidate.AccessID()] = cloned
		byOrigin[origin] = binding
		addProviderProbeBindings(byProvider, cloned)
		unavailable := cloneUnavailable(current.unavailable, 0)
		if p.state.CompareAndSwap(current, &projectionState{
			byAccess:    next,
			byOrigin:    byOrigin,
			byProvider:  byProvider,
			unavailable: unavailable,
		}) {
			return nil
		}
	}
}

// MarkUnavailable prevents new resolutions and writes from treating a stale
// process projection as authoritative. Existing AccessPlanSnapshot values
// remain valid immutable handles. Startup recovery installs a fresh projection.
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
			byOrigin:    current.byOrigin,
			byProvider:  current.byProvider,
			unavailable: unavailable,
		}) {
			return
		}
	}
}

func (p *AtomicSnapshotProjection) ActiveProviderProbeTargets() (
	[]ProviderProbeTarget,
	error,
) {
	state := p.state.Load()
	targets := make([]ProviderProbeTarget, 0, len(state.byProvider))
	for reference, binding := range state.byProvider {
		if _, unavailable := state.unavailable[binding.accessID]; unavailable {
			return nil, fmt.Errorf(
				"%w: accessId=%q",
				ErrProjectionUnavailable,
				binding.accessID.String(),
			)
		}
		if state.byAccess[binding.accessID].Binding().Status == AccessStatusEnabled {
			targets = append(targets, ProviderProbeTarget{
				reference: reference,
				revision:  binding.revision,
				planHash:  binding.planHash,
				target:    binding.target,
			})
		}
	}
	sort.Slice(targets, func(left, right int) bool {
		return targets[left].reference < targets[right].reference
	})
	return targets, nil
}

func addProviderProbeBindings(
	destination map[string]providerProbeBinding,
	snapshot AccessPlanSnapshot,
) {
	for _, target := range snapshot.compiledTargets {
		resource := target.Target()
		reference := ProviderTargetReference(snapshot.AccessID(), resource.ID)
		destination[reference] = providerProbeBinding{
			accessID: snapshot.AccessID(),
			revision: snapshot.Revision(),
			planHash: snapshot.PlanHash(),
			target:   target,
		}
	}
}

func (p *AtomicSnapshotProjection) ResolveClientOrigin(
	origin ClientOrigin,
) (IngressBinding, error) {
	if err := origin.validate(); err != nil {
		return IngressBinding{}, err
	}
	state := p.state.Load()
	binding, exists := state.byOrigin[origin.EndpointAuthority()]
	if !exists {
		return IngressBinding{}, fmt.Errorf(
			"%w: clientOrigin=%q",
			ErrAgentEndpointNotConfigured,
			origin.String(),
		)
	}
	if _, unavailable := state.unavailable[binding.AccessID()]; unavailable {
		return IngressBinding{}, newFailure(
			ReasonProjectionUnavailable,
			ErrProjectionUnavailable,
			binding.AccessID(),
			0,
			binding.AccessRevision(),
			nil,
		)
	}
	snapshot := state.byAccess[binding.AccessID()]
	if snapshot.Binding().Status != AccessStatusEnabled {
		return IngressBinding{}, fmt.Errorf(
			"%w: clientOrigin=%q",
			ErrAgentEndpointNotConfigured,
			origin.String(),
		)
	}
	return binding, binding.validate()
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

func (p *AtomicSnapshotProjection) ResolveAccess(
	accessID AccessID,
) (AccessPlanSnapshot, error) {
	if err := accessID.validate(); err != nil {
		return AccessPlanSnapshot{}, newFailure(
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
		return AccessPlanSnapshot{}, newFailure(
			ReasonProjectionUnavailable,
			ErrProjectionUnavailable,
			accessID,
			0,
			actualRevision,
			nil,
		)
	}
	if !exists {
		return AccessPlanSnapshot{}, newFailure(
			ReasonAccessNotConfigured,
			ErrAccessNotConfigured,
			accessID,
			0,
			0,
			nil,
		)
	}
	return snapshot.clone(), nil
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
