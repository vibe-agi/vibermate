package captureassignment

import (
	"context"
	"errors"
	"sort"
	"sync"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/originidentity"
)

type Options struct {
	Repository   Repository
	Environments environment.SnapshotResolver
	Activity     CaptureActivity
	Clock        Clock
}

func DefaultOptions(
	repository Repository,
	environments environment.SnapshotResolver,
	activity CaptureActivity,
) Options {
	return Options{
		Repository: repository, Environments: environments,
		Activity: activity, Clock: SystemClock{},
	}
}

type connectionRecord struct {
	assignment     Assignment
	binding        environment.ConnectionBinding
	snapshot       environment.EnvironmentSnapshot
	finish         func()
	activeRequests int
	zero           chan struct{}
}

type captureState struct {
	reference captureidentity.Reference
	writer    sync.Mutex
	mu        sync.Mutex

	poisoned bool
	shutdown bool

	connections map[string]*connectionRecord
}

func newCaptureState(reference captureidentity.Reference) *captureState {
	return &captureState{
		reference:   reference,
		connections: make(map[string]*connectionRecord),
	}
}

type Manager struct {
	repository   Repository
	environments environment.SnapshotResolver
	activity     CaptureActivity
	clock        Clock
	lifecycle    *lifecycleGate

	statesMu sync.Mutex
	states   map[string]*captureState
}

var (
	_ Controller                   = (*Manager)(nil)
	_ environment.CaptureInspector = (*Manager)(nil)
)

func NewManager(options Options) (*Manager, error) {
	if options.Repository == nil || options.Environments == nil ||
		options.Activity == nil || options.Clock == nil {
		return nil, errors.New("Capture assignment dependencies are incomplete")
	}
	return &Manager{
		repository: options.Repository, environments: options.Environments,
		activity: options.Activity, clock: options.Clock,
		lifecycle: newLifecycleGate(), states: make(map[string]*captureState),
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

func (manager *Manager) resolveAssigned(
	ctx context.Context,
	assignment Assignment,
) (environment.EnvironmentSnapshot, error) {
	if assignment.Validate() != nil {
		return environment.EnvironmentSnapshot{}, ErrAssignmentUnavailable
	}
	revision := assignment.LaunchAuthority.InitialEnvironmentRevision()
	snapshot, err := manager.environments.ResolveRevision(
		ctx,
		assignment.EnvironmentID,
		revision,
	)
	if err != nil {
		return environment.EnvironmentSnapshot{}, err
	}
	if snapshot.ID() != assignment.EnvironmentID ||
		snapshot.Revision() != revision ||
		snapshot.Digest() != assignment.LaunchAuthority.InitialEnvironmentDigest() ||
		snapshot.State() != environment.StateActive {
		return environment.EnvironmentSnapshot{}, ErrAssignmentUnavailable
	}
	return snapshot, nil
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

// Environment returns the immutable Environment revision frozen when this
// Capture was launched. Publishing a later revision affects only new Captures.
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
) (*ConnectionLease, error) {
	finish, err := manager.lifecycle.begin(ctx)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*ConnectionLease, error) { finish(); return nil, err }
	if capture.Validate() != nil || !validConnectionID(id) || origin.Validate() != nil {
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
	snapshot, err := manager.resolveAssigned(ctx, assignment)
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
		assignment: assignment,
		binding:    binding, snapshot: snapshot,
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
	state := manager.state(capture)
	state.mu.Lock()
	connection, exists := state.connections[connectionID]
	if !exists {
		state.mu.Unlock()
		return fail(ErrConnectionNotFound)
	}
	if state.shutdown || state.poisoned {
		state.mu.Unlock()
		return fail(ErrOperationInProgress)
	}
	assignment := connection.assignment
	snapshot := connection.snapshot
	if assignment.Validate() != nil || assignment.Capture != capture ||
		snapshot.ID() != assignment.EnvironmentID ||
		snapshot.Revision() != assignment.LaunchAuthority.InitialEnvironmentRevision() ||
		snapshot.Digest() != assignment.LaunchAuthority.InitialEnvironmentDigest() {
		state.mu.Unlock()
		return fail(ErrAssignmentUnavailable)
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

func (lease *RequestLease) Release() {
	if lease == nil || lease.finish == nil {
		return
	}
	lease.once.Do(lease.finish)
}

func (manager *Manager) ActiveCaptures(ctx context.Context, environmentID environment.EnvironmentID, limit int) ([]environment.CaptureReference, error) {
	finish, err := manager.lifecycle.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	return manager.activeCaptures(ctx, environmentID, limit)
}

func (manager *Manager) activeCaptures(ctx context.Context, environmentID environment.EnvironmentID, limit int) ([]environment.CaptureReference, error) {
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
		active, err := manager.activity.Active(ctx, assignment.Capture)
		if err != nil {
			return nil, errors.Join(ErrAssignmentUnavailable, err)
		}
		if !active {
			continue
		}
		result = append(result, environment.CaptureReference{
			Capture: assignment.Capture,
		})
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].Capture.Key() < result[right].Capture.Key()
	})
	return result, nil
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
	return manager.lifecycle.drain(ctx)
}

func (manager *Manager) Shutdown(ctx context.Context) error { return manager.Drain(ctx) }
