package workspaceroute

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

type Clock interface {
	Now() time.Time
}

type Manager struct {
	repository Repository
	accesses   access.SnapshotResolver
	clock      Clock
	mu         sync.Mutex
	pins       map[BindingID]map[Revision]int
}

type pinLease struct {
	once sync.Once
	done func()
}

func (lease *pinLease) release() {
	if lease != nil {
		lease.once.Do(lease.done)
	}
}

var _ Controller = (*Manager)(nil)

func New(
	repository Repository,
	accesses access.SnapshotResolver,
	clock Clock,
) (*Manager, error) {
	if repository == nil || accesses == nil || clock == nil {
		return nil, errors.New("workspace route dependencies are incomplete")
	}
	return &Manager{
		repository: repository,
		accesses:   accesses,
		clock:      clock,
		pins:       make(map[BindingID]map[Revision]int),
	}, nil
}

func (manager *Manager) Resolve(
	ctx context.Context,
	snapshot access.AccessPlanSnapshot,
	scope workspaceidentity.Scope,
) (Resolution, error) {
	if manager == nil || ctx == nil {
		return Resolution{}, ErrRouteUnavailable
	}
	if err := ctx.Err(); err != nil {
		return Resolution{}, err
	}
	if err := scope.Validate(); err != nil {
		return Resolution{}, fmt.Errorf("%w: CaptureRun scope is invalid", ErrRouteUnavailable)
	}
	defaultProfile, _, err := routeSetProfiles(snapshot)
	if err != nil {
		return Resolution{}, err
	}
	bindingID, err := BindingIDFor(
		snapshot.AccessID(),
		scope.MachineID(),
		scope.WorkspaceID(),
	)
	if err != nil {
		return Resolution{}, err
	}
	record, err := manager.repository.ResolveOrCreate(ctx, CreateRequest{
		ID:                          bindingID,
		AccessID:                    snapshot.AccessID(),
		MachineID:                   scope.MachineID(),
		WorkspaceID:                 scope.WorkspaceID(),
		MachineRegistrationRevision: scope.RegistrationRevision(),
		WorkspaceLabel:              scope.WorkspaceLabel(),
		WorkspaceEvidence:           scope.Evidence(),
		ProfileID:                   defaultProfile,
		UpdatedAt:                   manager.clock.Now().UTC(),
	})
	if err != nil {
		return Resolution{}, err
	}
	if err := validateRecordKey(record, scope); err != nil ||
		record.AccessID != snapshot.AccessID() || record.ID != bindingID {
		return Resolution{}, fmt.Errorf("%w: persisted binding identity is inconsistent", ErrRouteUnavailable)
	}
	profile, err := selectedProfile(snapshot, record.ProfileID)
	if err != nil {
		return Resolution{}, err
	}
	resolution := Resolution{
		BindingID:       record.ID,
		BindingRevision: record.Revision,
		ProfileID:       profile.ID,
		ProfileRevision: profile.Revision,
	}
	if err := resolution.Validate(); err != nil {
		return Resolution{}, err
	}
	resolution.lease = manager.pin(record.ID, record.Revision)
	return resolution, nil
}

func (manager *Manager) ListBindings(
	ctx context.Context,
	request PageRequest,
) ([]View, error) {
	if manager == nil || ctx == nil {
		return nil, ErrRouteUnavailable
	}
	records, err := manager.repository.List(ctx, request.Normalized())
	if err != nil {
		return nil, err
	}
	views := make([]View, 0, len(records))
	for _, record := range records {
		view, inspectErr := manager.inspect(record)
		if inspectErr != nil {
			return nil, inspectErr
		}
		views = append(views, view)
	}
	return views, nil
}

func (manager *Manager) GetBinding(
	ctx context.Context,
	id BindingID,
) (View, error) {
	if manager == nil || ctx == nil {
		return View{}, ErrRouteUnavailable
	}
	record, err := manager.repository.Get(ctx, id)
	if err != nil {
		return View{}, err
	}
	return manager.inspect(record)
}

func (manager *Manager) UpdateBinding(
	ctx context.Context,
	id BindingID,
	expected Revision,
	profileID access.EndpointProfileID,
) (View, error) {
	if manager == nil || ctx == nil || !expected.Valid() {
		return View{}, ErrInvalidBinding
	}
	record, err := manager.repository.Get(ctx, id)
	if err != nil {
		return View{}, err
	}
	if record.Revision != expected {
		return View{}, ErrRevisionConflict
	}
	snapshot, err := manager.accesses.ResolveAccess(record.AccessID)
	if err != nil {
		return View{}, fmt.Errorf("%w: Access projection cannot be resolved", ErrRouteUnavailable)
	}
	currentProfile, err := selectedProfile(snapshot, record.ProfileID)
	if err != nil {
		return View{}, err
	}
	targetProfile, err := selectedProfile(snapshot, profileID)
	if err != nil {
		return View{}, err
	}
	updated, err := manager.repository.CompareAndSwap(
		ctx,
		UpdateMutation{
			ID:        id,
			Expected:  expected,
			ProfileID: profileID,
			UpdatedAt: manager.clock.Now().UTC(),
			RequireNoActiveCaptureRun: credentialBootstrapSource(currentProfile) !=
				credentialBootstrapSource(targetProfile),
		},
	)
	if err != nil {
		return View{}, err
	}
	return manager.inspect(updated)
}

func credentialBootstrapSource(profile access.EndpointProfile) access.CredentialSource {
	return profile.CredentialSource
}

func (manager *Manager) inspect(record Record) (View, error) {
	if err := record.Validate(); err != nil {
		return View{}, err
	}
	snapshot, err := manager.accesses.ResolveAccess(record.AccessID)
	if err != nil {
		return View{
			Record:             record,
			State:              StateUnavailable,
			PinnedRequestCount: manager.olderPinCount(record.ID, record.Revision),
		}, nil
	}
	_, candidates, routeErr := routeSetProfiles(snapshot)
	if routeErr != nil {
		return View{
			Record:             record,
			State:              StateUnavailable,
			PinnedRequestCount: manager.olderPinCount(record.ID, record.Revision),
		}, nil
	}
	profiles, err := profileOptions(snapshot, candidates)
	if err != nil {
		return View{
			Record:             record,
			State:              StateUnavailable,
			PinnedRequestCount: manager.olderPinCount(record.ID, record.Revision),
		}, nil
	}
	state := StateActive
	if !slices.Contains(candidates, record.ProfileID) {
		// Keep the current approved choices visible so the operator can repair a
		// binding whose previously selected profile left the active Access plan.
		state = StateUnavailable
	}
	view := View{
		Record:             record,
		State:              state,
		Profiles:           profiles,
		PinnedRequestCount: manager.olderPinCount(record.ID, record.Revision),
	}
	return view, view.Validate()
}

func (manager *Manager) pin(id BindingID, revision Revision) *pinLease {
	manager.mu.Lock()
	if manager.pins[id] == nil {
		manager.pins[id] = make(map[Revision]int)
	}
	manager.pins[id][revision]++
	manager.mu.Unlock()
	return &pinLease{done: func() {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		byRevision := manager.pins[id]
		byRevision[revision]--
		if byRevision[revision] == 0 {
			delete(byRevision, revision)
		}
		if len(byRevision) == 0 {
			delete(manager.pins, id)
		}
	}}
}

func (manager *Manager) olderPinCount(id BindingID, current Revision) int {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	total := 0
	for revision, count := range manager.pins[id] {
		if revision != current {
			total += count
		}
	}
	return total
}

func routeSetProfiles(
	snapshot access.AccessPlanSnapshot,
) (access.EndpointProfileID, []access.EndpointProfileID, error) {
	binding := snapshot.Binding()
	if binding.Status != access.AccessStatusEnabled ||
		binding.ID != snapshot.AccessID() {
		return access.EndpointProfileID{}, nil, ErrRouteUnavailable
	}
	var candidates []access.EndpointProfileID
	for _, routeSet := range snapshot.RouteSets() {
		if routeSet.ID != binding.DefaultRouteSetID {
			continue
		}
		if candidates != nil {
			return access.EndpointProfileID{}, nil, fmt.Errorf("%w: default RouteSet is duplicated", ErrRouteUnavailable)
		}
		candidates = slices.Clone(routeSet.CandidateProfileIDs)
	}
	if len(candidates) == 0 {
		return access.EndpointProfileID{}, nil, fmt.Errorf("%w: default RouteSet has no candidates", ErrRouteUnavailable)
	}
	return candidates[0], candidates, nil
}

func selectedProfile(
	snapshot access.AccessPlanSnapshot,
	profileID access.EndpointProfileID,
) (access.EndpointProfile, error) {
	_, candidates, err := routeSetProfiles(snapshot)
	if err != nil || !slices.Contains(candidates, profileID) {
		return access.EndpointProfile{}, fmt.Errorf("%w: profile is outside the current RouteSet", ErrRouteUnavailable)
	}
	var selected access.EndpointProfile
	found := false
	for _, profile := range snapshot.EndpointProfiles() {
		if profile.ID != profileID {
			continue
		}
		if found || profile.AccessID != snapshot.AccessID() {
			return access.EndpointProfile{}, fmt.Errorf("%w: profile ownership is inconsistent", ErrRouteUnavailable)
		}
		selected = profile
		found = true
	}
	if !found {
		return access.EndpointProfile{}, fmt.Errorf("%w: selected profile is missing", ErrRouteUnavailable)
	}
	return selected, nil
}

func profileOptions(
	snapshot access.AccessPlanSnapshot,
	candidates []access.EndpointProfileID,
) ([]ProfileOption, error) {
	accounts := snapshot.AccountBindings()
	options := make([]ProfileOption, 0, len(candidates))
	for _, profileID := range candidates {
		profile, err := selectedProfile(snapshot, profileID)
		if err != nil {
			return nil, err
		}
		model := string(profile.DefaultModelPolicy.Mode)
		if profile.DefaultModelPolicy.Mode == access.ModelPolicyModeFixed {
			model = profile.DefaultModelPolicy.FixedModel.String()
		}
		authPresentation := AuthClient
		authLabel := "Current client login"
		if profile.Kind == access.EndpointProfileManaged {
			var account access.ProviderAccountBinding
			foundAccount := false
			for _, candidate := range accounts {
				if candidate.ID != profile.DefaultAccountBindingID {
					continue
				}
				if foundAccount || candidate.ProfileID != profile.ID || !candidate.Enabled {
					return nil, ErrRouteUnavailable
				}
				account = candidate
				foundAccount = true
			}
			if !foundAccount {
				return nil, ErrRouteUnavailable
			}
			authPresentation = AuthVibeMateAccount
			authLabel = account.Label
		} else if profile.Kind != access.EndpointProfileOriginalPassthrough {
			return nil, ErrRouteUnavailable
		}
		options = append(options, ProfileOption{
			ProfileID:         profile.ID,
			ProfileRevision:   profile.Revision,
			Kind:              profile.Kind,
			Label:             profile.Name,
			ModelPresentation: model,
			AuthPresentation:  authPresentation,
			AuthLabel:         authLabel,
			Available:         true,
		})
	}
	return options, nil
}
