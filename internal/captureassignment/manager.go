package captureassignment

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"golang.org/x/sync/semaphore"
)

const environmentGateCapacity int64 = 1 << 30

type Options struct {
	Repository           Repository
	Environments         environment.SnapshotResolver
	LeafCacheInvalidator LeafCacheInvalidator
	Clock                Clock
}

func DefaultOptions(repository Repository, environments environment.SnapshotResolver) Options {
	return Options{Repository: repository, Environments: environments, Clock: SystemClock{}}
}

type connectionRecord struct {
	binding        environment.ConnectionBinding
	snapshot       environment.EnvironmentSnapshot
	closeHandle    ConnectionCloseHandle
	finish         func()
	activeRequests int
	zero           chan struct{}
	closing        bool
}

type captureState struct {
	reference captureidentity.Reference
	writer    sync.Mutex
	mu        sync.Mutex

	poisoned bool
	shutdown bool
	blocked  map[string]struct{}

	connections map[string]*connectionRecord
}

func newCaptureState(reference captureidentity.Reference) *captureState {
	return &captureState{
		reference:   reference,
		blocked:     make(map[string]struct{}),
		connections: make(map[string]*connectionRecord),
	}
}

type Manager struct {
	repository   Repository
	environments environment.SnapshotResolver
	clock        Clock
	leafCache    LeafCacheInvalidator
	lifecycle    *lifecycleGate

	statesMu sync.Mutex
	states   map[string]*captureState

	environmentGatesMu sync.Mutex
	environmentGates   map[environment.EnvironmentID]*semaphore.Weighted
}

var (
	_ Controller                               = (*Manager)(nil)
	_ environment.CaptureInspector             = (*Manager)(nil)
	_ environment.CaptureTransitionCoordinator = (*Manager)(nil)
)

func NewManager(options Options) (*Manager, error) {
	if options.Repository == nil || options.Environments == nil || options.Clock == nil {
		return nil, errors.New("Capture assignment dependencies are incomplete")
	}
	return &Manager{
		repository: options.Repository, environments: options.Environments,
		clock:     options.Clock,
		leafCache: options.LeafCacheInvalidator,
		lifecycle: newLifecycleGate(), states: make(map[string]*captureState),
		environmentGates: make(map[environment.EnvironmentID]*semaphore.Weighted),
	}, nil
}

func (manager *Manager) environmentGate(id environment.EnvironmentID) *semaphore.Weighted {
	manager.environmentGatesMu.Lock()
	defer manager.environmentGatesMu.Unlock()
	gate := manager.environmentGates[id]
	if gate == nil {
		gate = semaphore.NewWeighted(environmentGateCapacity)
		manager.environmentGates[id] = gate
	}
	return gate
}

func (manager *Manager) lockEnvironmentReads(ctx context.Context, ids ...environment.EnvironmentID) (func(), error) {
	ids = slices.Clone(ids)
	sort.Slice(ids, func(left, right int) bool { return ids[left] < ids[right] })
	ids = slices.Compact(ids)
	gates := make([]*semaphore.Weighted, 0, len(ids))
	for _, id := range ids {
		gate := manager.environmentGate(id)
		if err := gate.Acquire(ctx, 1); err != nil {
			for index := len(gates) - 1; index >= 0; index-- {
				gates[index].Release(1)
			}
			return nil, err
		}
		gates = append(gates, gate)
	}
	return func() {
		for index := len(gates) - 1; index >= 0; index-- {
			gates[index].Release(1)
		}
	}, nil
}

func (manager *Manager) state(reference captureidentity.Reference) *captureState {
	key := reference.Key()
	manager.statesMu.Lock()
	defer manager.statesMu.Unlock()
	state := manager.states[key]
	if state == nil {
		state = newCaptureState(reference)
		manager.states[key] = state
	}
	return state
}

func (manager *Manager) Create(ctx context.Context, command CreateCommand) (Assignment, error) {
	finish, err := manager.lifecycle.begin(ctx)
	if err != nil {
		return Assignment{}, err
	}
	defer finish()
	if command.Capture.Validate() != nil || !command.Source.valid() {
		return Assignment{}, ErrInvalidAssignment
	}
	state := manager.state(command.Capture)
	state.writer.Lock()
	defer state.writer.Unlock()
	unlockEnvironment, err := manager.lockEnvironmentReads(ctx, command.EnvironmentID)
	if err != nil {
		return Assignment{}, err
	}
	defer unlockEnvironment()
	snapshot, err := manager.resolveActive(command.EnvironmentID)
	if err != nil {
		return Assignment{}, err
	}
	launchAuthority, err := environment.NewLaunchAuthorityBoundary(snapshot)
	if err != nil {
		return Assignment{}, err
	}
	candidate := Assignment{
		Capture: command.Capture, EnvironmentID: command.EnvironmentID, Revision: 1,
		Source: command.Source, LaunchAuthority: launchAuthority,
		UpdatedAt: canonicalTime(manager.clock.Now()),
	}
	result, writeErr := manager.repository.Write(ctx, 0, candidate)
	return manager.finishWrite(state, candidate, result, writeErr)
}

func (manager *Manager) Resolve(ctx context.Context, reference captureidentity.Reference) (Assignment, error) {
	finish, err := manager.lifecycle.begin(ctx)
	if err != nil {
		return Assignment{}, err
	}
	defer finish()
	if reference.Validate() != nil {
		return Assignment{}, ErrInvalidAssignment
	}
	state := manager.state(reference)
	state.mu.Lock()
	poisoned := state.poisoned
	state.mu.Unlock()
	if poisoned {
		return Assignment{}, ErrAssignmentUnavailable
	}
	assignment, exists, err := manager.repository.Load(ctx, reference)
	if err != nil {
		return Assignment{}, err
	}
	if !exists {
		return Assignment{}, ErrAssignmentNotFound
	}
	if assignment.Validate() != nil || assignment.Capture != reference {
		return Assignment{}, ErrAssignmentUnavailable
	}
	return assignment, nil
}

func (manager *Manager) Switch(ctx context.Context, command SwitchCommand) (SwitchResult, error) {
	finish, err := manager.lifecycle.begin(ctx)
	if err != nil {
		return SwitchResult{}, err
	}
	defer finish()
	if command.Capture.Validate() != nil || command.ExpectedRevision == 0 ||
		command.ExpectedRevision >= MaxRevision || command.Source != SourceOperatorSwitch {
		return SwitchResult{}, ErrInvalidAssignment
	}
	state := manager.state(command.Capture)
	state.writer.Lock()
	defer state.writer.Unlock()

	current, exists, err := manager.repository.Load(ctx, command.Capture)
	if err != nil {
		return SwitchResult{}, err
	}
	if !exists {
		return SwitchResult{}, ErrAssignmentNotFound
	}
	if current.Validate() != nil || current.Capture != command.Capture {
		return SwitchResult{}, ErrAssignmentUnavailable
	}
	if current.Revision != command.ExpectedRevision {
		return SwitchResult{Assignment: current}, ErrAssignmentConflict
	}
	if current.EnvironmentID == command.TargetEnvironmentID {
		return SwitchResult{Assignment: current, Boundary: BoundaryNoChange}, nil
	}
	unlockEnvironments, err := manager.lockEnvironmentReads(ctx, current.EnvironmentID, command.TargetEnvironmentID)
	if err != nil {
		return SwitchResult{}, err
	}
	defer unlockEnvironments()
	target, err := manager.resolveActive(command.TargetEnvironmentID)
	if err != nil {
		return SwitchResult{}, err
	}
	if err := current.LaunchAuthority.Covers(target); err != nil {
		if errors.Is(err, environment.ErrLaunchAuthorityRestartRequired) {
			return SwitchResult{
				Assignment: current,
				Boundary:   BoundaryRestartRequired,
			}, ErrLaunchRestartRequired
		}
		return SwitchResult{}, err
	}

	state.mu.Lock()
	if state.poisoned {
		state.mu.Unlock()
		return SwitchResult{}, ErrAssignmentUnavailable
	}
	connectionIDs := make([]string, 0, len(state.connections))
	connectionZeros := make(map[string]<-chan struct{}, len(state.connections))
	for id, connection := range state.connections {
		classification, classifyErr := environment.ClassifyConnectionTransition(connection.snapshot, target, connection.binding)
		if classifyErr != nil {
			state.mu.Unlock()
			return SwitchResult{}, fmt.Errorf("classify connection %q: %w", id, classifyErr)
		}
		if classification == environment.CompatibilityReconnectRequired {
			connectionIDs = append(connectionIDs, id)
			connectionZeros[id] = connection.zero
		}
	}
	sort.Strings(connectionIDs)
	for _, id := range connectionIDs {
		state.blocked[id] = struct{}{}
	}
	state.mu.Unlock()

	if len(connectionIDs) != 0 {
		for _, id := range connectionIDs {
			select {
			case <-connectionZeros[id]:
			case <-ctx.Done():
				manager.reopen(state, connectionIDs)
				return SwitchResult{}, ctx.Err()
			}
		}
		for _, id := range connectionIDs {
			if err := manager.closeConnection(ctx, command.Capture, id); err != nil {
				manager.reopen(state, connectionIDs)
				return SwitchResult{}, errors.Join(ErrReconnectUnavailable, err)
			}
		}
	}

	candidate := Assignment{
		Capture: command.Capture, EnvironmentID: command.TargetEnvironmentID,
		Revision: current.Revision + 1, Source: command.Source,
		LaunchAuthority: current.LaunchAuthority,
		UpdatedAt:       canonicalTime(manager.clock.Now()),
	}
	result, writeErr := manager.repository.Write(ctx, current.Revision, candidate)
	assignment, finishErr := manager.finishWrite(state, candidate, result, writeErr)
	if finishErr != nil {
		manager.reopen(state, connectionIDs)
		return SwitchResult{Assignment: assignment, ClosedConnections: slices.Clone(connectionIDs)}, finishErr
	}
	boundary := BoundaryHotSwitch
	if len(connectionIDs) != 0 {
		boundary = BoundaryReconnectRequired
	}
	manager.reopen(state, connectionIDs)
	return SwitchResult{Assignment: assignment, Boundary: boundary, ClosedConnections: slices.Clone(connectionIDs)}, nil
}

func (manager *Manager) finishWrite(state *captureState, expected Assignment, result CommitResult, writeErr error) (Assignment, error) {
	switch result.Outcome {
	case CommitOutcomeCommitted:
		if writeErr != nil || result.Assignment.Validate() != nil || result.Assignment != expected ||
			result.Actual != result.Assignment.Revision {
			state.mu.Lock()
			state.poisoned = true
			state.mu.Unlock()
			return result.Assignment, errors.Join(ErrCommitOutcomeUnknown, writeErr)
		}
		return result.Assignment, nil
	case CommitOutcomeConflict:
		return result.Assignment, errors.Join(ErrAssignmentConflict, writeErr)
	case CommitOutcomeIndeterminate:
		state.mu.Lock()
		state.poisoned = true
		state.mu.Unlock()
		return result.Assignment, errors.Join(ErrCommitOutcomeUnknown, writeErr)
	default:
		return result.Assignment, errors.Join(ErrWriteNotCommitted, writeErr)
	}
}

func (manager *Manager) resolveActive(id environment.EnvironmentID) (environment.EnvironmentSnapshot, error) {
	snapshot, err := manager.environments.Resolve(id)
	if err != nil {
		return environment.EnvironmentSnapshot{}, err
	}
	if snapshot.State() != environment.StateActive {
		return environment.EnvironmentSnapshot{}, environment.ErrEnvironmentDisabled
	}
	return snapshot, nil
}

func (manager *Manager) reopen(state *captureState, connectionIDs []string) {
	state.mu.Lock()
	for _, id := range connectionIDs {
		delete(state.blocked, id)
	}
	state.mu.Unlock()
}

type ConnectionLease struct {
	manager     *Manager
	capture     captureidentity.Reference
	id          string
	assignment  Assignment
	environment environment.EnvironmentSnapshot
	binding     environment.ConnectionBinding
	once        sync.Once
}

func (lease *ConnectionLease) Assignment() Assignment {
	if lease == nil {
		return Assignment{}
	}
	return lease.assignment
}

// Environment returns the immutable Environment revision that admitted this
// connection. Callers may inspect it, but request admission still belongs to
// Manager.BeginRequest so a hot switch cannot accidentally reuse this value as
// current request authority.
func (lease *ConnectionLease) Environment() environment.EnvironmentSnapshot {
	if lease == nil {
		return environment.EnvironmentSnapshot{}
	}
	return lease.environment
}

func (lease *ConnectionLease) Binding() environment.ConnectionBinding {
	if lease == nil {
		return environment.ConnectionBinding{}
	}
	return lease.binding
}

func (manager *Manager) RegisterConnection(
	ctx context.Context,
	capture captureidentity.Reference,
	id string,
	origin originidentity.ClientOrigin,
	closeHandle ConnectionCloseHandle,
) (*ConnectionLease, error) {
	finish, err := manager.lifecycle.begin(ctx)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*ConnectionLease, error) { finish(); return nil, err }
	if capture.Validate() != nil || !validConnectionID(id) || origin.Validate() != nil || closeHandle == nil {
		return fail(ErrInvalidAssignment)
	}
	state := manager.state(capture)
	state.writer.Lock()
	defer state.writer.Unlock()
	assignment, exists, err := manager.repository.Load(ctx, capture)
	if err != nil {
		return fail(err)
	}
	if !exists {
		return fail(ErrAssignmentNotFound)
	}
	if assignment.Validate() != nil || assignment.Capture != capture {
		return fail(ErrAssignmentUnavailable)
	}
	unlockEnvironment, err := manager.lockEnvironmentReads(ctx, assignment.EnvironmentID)
	if err != nil {
		return fail(err)
	}
	defer unlockEnvironment()
	snapshot, err := manager.resolveActive(assignment.EnvironmentID)
	if err != nil {
		return fail(err)
	}
	binding, err := snapshot.BeginConnection(origin)
	if err != nil {
		return fail(err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.shutdown || state.poisoned {
		return fail(ErrOperationInProgress)
	}
	if _, exists := state.connections[id]; exists {
		return fail(ErrOperationInProgress)
	}
	zero := make(chan struct{})
	close(zero)
	state.connections[id] = &connectionRecord{
		binding: binding, snapshot: snapshot, closeHandle: closeHandle,
		finish: finish, zero: zero,
	}
	return &ConnectionLease{
		manager: manager, capture: capture, id: id,
		assignment: assignment, environment: snapshot, binding: binding,
	}, nil
}

func (lease *ConnectionLease) Close() {
	if lease == nil || lease.manager == nil {
		return
	}
	lease.once.Do(func() { lease.manager.removeConnection(lease.capture, lease.id) })
}

func (manager *Manager) removeConnection(capture captureidentity.Reference, id string) {
	state := manager.state(capture)
	state.mu.Lock()
	record, exists := state.connections[id]
	if exists {
		delete(state.connections, id)
	}
	state.mu.Unlock()
	if exists {
		record.finish()
	}
}

func (manager *Manager) closeConnection(ctx context.Context, capture captureidentity.Reference, id string) error {
	state := manager.state(capture)
	state.mu.Lock()
	record := state.connections[id]
	if record == nil || record.closing {
		state.mu.Unlock()
		return nil
	}
	record.closing = true
	state.mu.Unlock()
	if record.closeHandle == nil {
		state.mu.Lock()
		if current := state.connections[id]; current == record {
			current.closing = false
		}
		state.mu.Unlock()
		return ErrReconnectUnavailable
	}
	if err := record.closeHandle.Close(ctx); err != nil {
		state.mu.Lock()
		if current := state.connections[id]; current == record {
			current.closing = false
		}
		state.mu.Unlock()
		return err
	}
	manager.removeConnection(capture, id)
	return nil
}

type RequestLease struct {
	assignment Assignment
	plan       environment.RequestPlan
	finish     func()
	once       sync.Once
}

func (lease *RequestLease) Assignment() Assignment { return lease.assignment }
func (lease *RequestLease) Plan() environment.RequestPlan {
	if lease == nil {
		return environment.RequestPlan{}
	}
	return lease.plan
}

func (manager *Manager) BeginRequest(
	ctx context.Context,
	capture captureidentity.Reference,
	connectionID string,
	facts environment.RequestFacts,
) (*RequestLease, error) {
	finish, err := manager.lifecycle.begin(ctx)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*RequestLease, error) { finish(); return nil, err }
	if capture.Validate() != nil || !validConnectionID(connectionID) {
		return fail(ErrInvalidAssignment)
	}
	assignment, unlockEnvironment, err := manager.loadAssignmentUnderEnvironmentGate(ctx, capture)
	if err != nil {
		return fail(err)
	}
	defer unlockEnvironment()
	snapshot, err := manager.resolveActive(assignment.EnvironmentID)
	if err != nil {
		return fail(err)
	}
	state := manager.state(capture)
	state.mu.Lock()
	connection, exists := state.connections[connectionID]
	if !exists {
		state.mu.Unlock()
		return fail(ErrConnectionNotFound)
	}
	_, blocked := state.blocked[connectionID]
	if state.shutdown || state.poisoned || blocked || connection.closing {
		state.mu.Unlock()
		return fail(ErrOperationInProgress)
	}
	if err := environment.ValidateConnectionBinding(snapshot, connection.binding); err != nil {
		state.mu.Unlock()
		return fail(err)
	}
	if connection.binding.Mode != environment.ConnectionModeSemantic {
		state.mu.Unlock()
		return fail(environment.ErrClientProtocolNotMatched)
	}
	if connection.activeRequests == 0 {
		connection.zero = make(chan struct{})
	}
	connection.activeRequests++
	state.mu.Unlock()
	release := func() {
		state.mu.Lock()
		connection.activeRequests--
		if connection.activeRequests == 0 {
			close(connection.zero)
		}
		state.mu.Unlock()
		finish()
	}
	plan, err := snapshot.ResolveRequest(connection.binding.ClientOrigin, facts)
	if err != nil {
		release()
		return nil, err
	}
	return &RequestLease{assignment: assignment, plan: plan, finish: release}, nil
}

func (manager *Manager) loadAssignmentUnderEnvironmentGate(ctx context.Context, capture captureidentity.Reference) (Assignment, func(), error) {
	for attempt := 0; attempt < 3; attempt++ {
		assignment, exists, err := manager.repository.Load(ctx, capture)
		if err != nil {
			return Assignment{}, nil, err
		}
		if !exists {
			return Assignment{}, nil, ErrAssignmentNotFound
		}
		if assignment.Validate() != nil || assignment.Capture != capture {
			return Assignment{}, nil, ErrAssignmentUnavailable
		}
		unlockEnvironment, gateErr := manager.lockEnvironmentReads(ctx, assignment.EnvironmentID)
		if gateErr != nil {
			return Assignment{}, nil, gateErr
		}
		confirmed, confirmedExists, confirmErr := manager.repository.Load(ctx, capture)
		if confirmErr != nil {
			unlockEnvironment()
			return Assignment{}, nil, confirmErr
		}
		if confirmedExists && confirmed == assignment {
			return assignment, unlockEnvironment, nil
		}
		unlockEnvironment()
	}
	return Assignment{}, nil, ErrOperationInProgress
}

func (lease *RequestLease) Release() {
	if lease == nil || lease.finish == nil {
		return
	}
	lease.once.Do(lease.finish)
}

func (manager *Manager) AffectedCaptures(ctx context.Context, environmentID environment.EnvironmentID, limit int) ([]environment.CaptureReference, error) {
	finish, err := manager.lifecycle.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	return manager.affectedCaptures(ctx, environmentID, limit)
}

func (manager *Manager) affectedCaptures(ctx context.Context, environmentID environment.EnvironmentID, limit int) ([]environment.CaptureReference, error) {
	if limit <= 0 || limit > MaxListLimit {
		return nil, ErrInvalidAssignment
	}
	assignments, err := manager.repository.ListByEnvironment(ctx, environmentID, limit)
	if err != nil {
		return nil, err
	}
	result := make([]environment.CaptureReference, 0, len(assignments))
	for _, assignment := range assignments {
		if assignment.Validate() != nil || assignment.EnvironmentID != environmentID {
			return nil, ErrAssignmentUnavailable
		}
		state := manager.state(assignment.Capture)
		state.mu.Lock()
		bindings := make([]environment.ConnectionBinding, 0, len(state.connections))
		for _, connection := range state.connections {
			bindings = append(bindings, connection.binding)
		}
		state.mu.Unlock()
		sort.Slice(bindings, func(left, right int) bool {
			if bindings[left].Mode != bindings[right].Mode {
				return bindings[left].Mode < bindings[right].Mode
			}
			if bindings[left].ClientOrigin != bindings[right].ClientOrigin {
				return bindings[left].ClientOrigin.String() < bindings[right].ClientOrigin.String()
			}
			if bindings[left].ClientEndpointID != bindings[right].ClientEndpointID {
				return bindings[left].ClientEndpointID < bindings[right].ClientEndpointID
			}
			return bindings[left].CompatibilityDigest.String() < bindings[right].CompatibilityDigest.String()
		})
		bindings = slices.Compact(bindings)
		result = append(result, environment.CaptureReference{
			Capture: assignment.Capture, LaunchAuthority: assignment.LaunchAuthority,
			Bindings: bindings,
		})
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].Capture.Key() < result[right].Capture.Key()
	})
	return result, nil
}

type environmentTransitionTarget struct {
	capture captureidentity.Reference
	state   *captureState
	id      string
	zero    <-chan struct{}
}

type environmentTransitionLease struct {
	commitOnce    sync.Once
	releaseOnce   sync.Once
	manager       *Manager
	gate          *semaphore.Weighted
	blocked       map[*captureState][]string
	invalidations []LeafCacheInvalidation
	finish        func()
}

func (lease *environmentTransitionLease) Commit() {
	if lease == nil {
		return
	}
	lease.commitOnce.Do(func() {
		if lease.manager.leafCache == nil {
			return
		}
		for _, invalidation := range lease.invalidations {
			lease.manager.leafCache.InvalidateLeafCache(invalidation)
		}
	})
}

func (lease *environmentTransitionLease) Release() {
	if lease == nil {
		return
	}
	lease.releaseOnce.Do(func() {
		for state, ids := range lease.blocked {
			lease.manager.reopen(state, ids)
		}
		lease.gate.Release(environmentGateCapacity)
		lease.finish()
	})
}

func (manager *Manager) PrepareEnvironmentTransition(ctx context.Context, transition environment.EnvironmentTransition) (environment.EnvironmentTransitionLease, error) {
	finish, err := manager.lifecycle.begin(ctx)
	if err != nil {
		return nil, err
	}
	failBeforeGate := func(err error) (environment.EnvironmentTransitionLease, error) {
		finish()
		return nil, err
	}
	if transition.Candidate.ID() == "" || transition.Candidate.ID() != transition.Expected.EnvironmentID ||
		transition.Candidate.Revision() != transition.Expected.BaseRevision+1 ||
		transition.Candidate.Digest() != transition.Expected.CandidateDigest {
		return failBeforeGate(environment.ErrPreviewStale)
	}
	if transition.ActiveExists {
		if transition.Active.ID() != transition.Expected.EnvironmentID ||
			transition.Active.Revision() != transition.Expected.BaseRevision {
			return failBeforeGate(environment.ErrPreviewStale)
		}
	} else if transition.Expected.BaseRevision != 0 {
		return failBeforeGate(environment.ErrPreviewStale)
	}

	gate := manager.environmentGate(transition.Expected.EnvironmentID)
	if err := gate.Acquire(ctx, environmentGateCapacity); err != nil {
		return failBeforeGate(err)
	}
	blocked := make(map[*captureState][]string)
	release := func() {
		for state, ids := range blocked {
			manager.reopen(state, ids)
		}
		gate.Release(environmentGateCapacity)
		finish()
	}

	references, err := manager.affectedCaptures(ctx, transition.Expected.EnvironmentID, environment.MaxCaptureImpact)
	if err != nil {
		release()
		return nil, err
	}
	actualImpacts := make([]environment.ImpactReference, 0, len(references))
	targets := make([]environmentTransitionTarget, 0)
	for _, reference := range references {
		classification := environment.CompatibilityHotSwitch
		if err := reference.LaunchAuthority.Covers(transition.Candidate); err != nil {
			if errors.Is(err, environment.ErrLaunchAuthorityRestartRequired) {
				classification = environment.CompatibilityRestartRequired
			} else {
				release()
				return nil, err
			}
		}
		if !transition.ActiveExists || transition.Active.State() != environment.StateActive ||
			transition.Candidate.State() != environment.StateActive {
			classification = environment.CompatibilityReconnectRequired
		} else if classification != environment.CompatibilityRestartRequired {
			for _, binding := range reference.Bindings {
				value, classifyErr := environment.ClassifyConnectionTransition(transition.Active, transition.Candidate, binding)
				if classifyErr != nil || value == environment.CompatibilityReconnectRequired {
					classification = environment.CompatibilityReconnectRequired
					break
				}
			}
		}
		state := manager.state(reference.Capture)
		state.mu.Lock()
		if state.shutdown || state.poisoned {
			state.mu.Unlock()
			release()
			return nil, ErrAssignmentUnavailable
		}
		for id, connection := range state.connections {
			value, classifyErr := environment.ClassifyConnectionTransition(connection.snapshot, transition.Candidate, connection.binding)
			if classifyErr != nil {
				state.mu.Unlock()
				release()
				return nil, classifyErr
			}
			if value == environment.CompatibilityReconnectRequired {
				classification = environment.CompatibilityReconnectRequired
				targets = append(targets, environmentTransitionTarget{
					capture: reference.Capture, state: state, id: id, zero: connection.zero,
				})
			}
		}
		state.mu.Unlock()
		actualImpacts = append(actualImpacts, environment.ImpactReference{Capture: reference, Classification: classification})
	}
	if !sameTransitionImpacts(transition.Expected.Affected, actualImpacts) {
		release()
		return nil, environment.ErrPreviewStale
	}
	for _, impact := range actualImpacts {
		if impact.Classification == environment.CompatibilityRestartRequired {
			release()
			return nil, environment.ErrLaunchAuthorityRestartRequired
		}
	}

	sort.Slice(targets, func(left, right int) bool {
		if targets[left].capture.Key() != targets[right].capture.Key() {
			return targets[left].capture.Key() < targets[right].capture.Key()
		}
		return targets[left].id < targets[right].id
	})
	for _, target := range targets {
		target.state.mu.Lock()
		if _, exists := target.state.connections[target.id]; exists {
			target.state.blocked[target.id] = struct{}{}
			blocked[target.state] = append(blocked[target.state], target.id)
		}
		target.state.mu.Unlock()
	}
	for _, target := range targets {
		select {
		case <-target.zero:
		case <-ctx.Done():
			release()
			return nil, ctx.Err()
		}
	}
	for _, target := range targets {
		if err := manager.closeConnection(ctx, target.capture, target.id); err != nil {
			release()
			return nil, errors.Join(ErrReconnectUnavailable, err)
		}
	}
	return &environmentTransitionLease{
		manager: manager, gate: gate, blocked: blocked,
		invalidations: leafInvalidations(transition.Active, transition.ActiveExists),
		finish:        finish,
	}, nil
}

func leafInvalidations(snapshot environment.EnvironmentSnapshot, exists bool) []LeafCacheInvalidation {
	if !exists {
		return nil
	}
	return LeafCacheInvalidations(snapshot)
}

func sameTransitionImpacts(expected, actual []environment.ImpactReference) bool {
	if len(expected) != len(actual) {
		return false
	}
	for index := range expected {
		if expected[index].Classification != actual[index].Classification ||
			expected[index].Capture.Capture != actual[index].Capture.Capture ||
			expected[index].Capture.LaunchAuthority != actual[index].Capture.LaunchAuthority ||
			!slices.Equal(expected[index].Capture.Bindings, actual[index].Capture.Bindings) {
			return false
		}
	}
	return true
}

func (manager *Manager) BeginShutdown() {
	if manager == nil {
		return
	}
	manager.lifecycle.closeAdmission()
	manager.statesMu.Lock()
	states := make([]*captureState, 0, len(manager.states))
	for _, state := range manager.states {
		states = append(states, state)
	}
	manager.statesMu.Unlock()
	for _, state := range states {
		state.mu.Lock()
		state.shutdown = true
		state.mu.Unlock()
	}
}

func (manager *Manager) Drain(ctx context.Context) error {
	if manager == nil {
		return nil
	}
	manager.BeginShutdown()
	if err := manager.closeAllConnections(ctx); err != nil {
		return err
	}
	return manager.lifecycle.drain(ctx)
}

func (manager *Manager) Shutdown(ctx context.Context) error { return manager.Drain(ctx) }

func (manager *Manager) closeAllConnections(ctx context.Context) error {
	manager.statesMu.Lock()
	type target struct {
		capture captureidentity.Reference
		id      string
	}
	var targets []target
	for _, state := range manager.states {
		state.mu.Lock()
		for id := range state.connections {
			targets = append(targets, target{capture: state.reference, id: id})
		}
		state.mu.Unlock()
	}
	manager.statesMu.Unlock()
	sort.Slice(targets, func(left, right int) bool {
		if targets[left].capture.Key() != targets[right].capture.Key() {
			return targets[left].capture.Key() < targets[right].capture.Key()
		}
		return targets[left].id < targets[right].id
	})
	for _, target := range targets {
		if err := manager.closeConnection(ctx, target.capture, target.id); err != nil {
			return errors.Join(ErrReconnectUnavailable, err)
		}
	}
	return nil
}
