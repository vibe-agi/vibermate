package captureassignment

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

func TestHotSwitchPinsOldRequestAndKeepsConnection(t *testing.T) {
	t.Parallel()
	repository := newMemoryRepository()
	work := environmentFixture(t, "work", "adapter.shared")
	personal := environmentFixture(t, "personal", "adapter.shared")
	personal.ClientEndpoints[0].ProtocolPlans[0].UpstreamPlan.DefaultRouteID = "route.personal"
	personal.ClientEndpoints[0].ProtocolPlans[0].UpstreamPlan.RouteSet.CandidateRouteIDs =
		[]environment.UpstreamRouteID{"route.personal"}
	personal.ClientEndpoints[0].ProtocolPlans[0].UpstreamPlan.Routes[0].ID = "route.personal"
	resolver := environmentResolver(t,
		work,
		personal,
	)
	closer := &recordingCloser{}
	manager := newTestManager(t, repository, resolver, closer)
	capture := testCapture()
	created, err := manager.Create(context.Background(), CreateCommand{
		Capture: capture, EnvironmentID: "work", Source: SourceLaunch,
	})
	if err != nil || created.Revision != 1 {
		t.Fatalf("Create = %+v, %v", created, err)
	}
	connection, err := manager.RegisterConnection(
		context.Background(), capture, "connection.semantic", semanticOrigin(t), closer.Handle("connection.semantic"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if connection.Assignment().EnvironmentID != "work" ||
		connection.Assignment().Revision != 1 ||
		connection.Environment().ID() != "work" ||
		connection.Environment().Revision() != 1 {
		t.Fatalf(
			"connection authority = %+v / %q@%d",
			connection.Assignment(),
			connection.Environment().ID(),
			connection.Environment().Revision(),
		)
	}
	oldRequest, err := manager.BeginRequest(context.Background(), capture, "connection.semantic", semanticRequestFacts())
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Switch(context.Background(), SwitchCommand{
		Capture: capture, ExpectedRevision: 1, TargetEnvironmentID: "personal", Source: SourceOperatorSwitch,
	})
	if err != nil || result.Boundary != BoundaryHotSwitch || len(result.ClosedConnections) != 0 {
		t.Fatalf("Switch = %+v, %v", result, err)
	}
	if oldRequest.Assignment().EnvironmentID != "work" || oldRequest.Plan().EnvironmentID() != "work" ||
		oldRequest.Plan().Route().ID() != "route.default" {
		t.Fatalf("old request moved = %+v / %q", oldRequest.Assignment(), oldRequest.Plan().EnvironmentID())
	}
	newRequest, err := manager.BeginRequest(context.Background(), capture, "connection.semantic", semanticRequestFacts())
	if err != nil {
		t.Fatal(err)
	}
	if newRequest.Assignment().EnvironmentID != "personal" || newRequest.Assignment().Revision != 2 ||
		newRequest.Plan().EnvironmentID() != "personal" || newRequest.Plan().Route().ID() != "route.personal" {
		t.Fatalf("new request = %+v", newRequest.Assignment())
	}
	newRequest.Release()
	oldRequest.Release()
	if len(closer.IDs()) != 0 {
		t.Fatalf("hot switch closed connections: %v", closer.IDs())
	}
}

func TestSwitchRejectsEnvironmentOutsideFrozenLaunchAuthority(t *testing.T) {
	t.Parallel()

	repository := newMemoryRepository()
	work := environmentFixture(t, "work", "adapter.shared")
	other := environmentFixture(t, "other", "adapter.shared")
	otherOrigin := mustOrigin(t, "https://other.example")
	otherProvider, err := originidentity.ParseProviderOrigin(otherOrigin.String())
	if err != nil {
		t.Fatal(err)
	}
	other.ClientEndpoints[0].ClientOrigin = otherOrigin
	other.ClientEndpoints[0].ProtocolPlans[0].UpstreamPlan.Routes[0].ProviderTarget.Origin = otherProvider
	manager := newTestManager(t, repository, environmentResolver(t, work, other), nil)
	capture := testCapture()
	created, err := manager.Create(context.Background(), CreateCommand{
		Capture: capture, EnvironmentID: "work", Source: SourceLaunch,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Switch(context.Background(), SwitchCommand{
		Capture: capture, ExpectedRevision: created.Revision,
		TargetEnvironmentID: "other", Source: SourceOperatorSwitch,
	})
	if !errors.Is(err, ErrLaunchRestartRequired) ||
		result.Boundary != BoundaryRestartRequired || result.Assignment != created {
		t.Fatalf("Switch = %+v, %v", result, err)
	}
	current, err := manager.Resolve(context.Background(), capture)
	if err != nil || current != created {
		t.Fatalf("assignment changed after restart-required switch: %+v, %v", current, err)
	}
}

func TestRequestAdmissionFailsClosedWithoutLeakingAnActiveLease(t *testing.T) {
	t.Parallel()
	repository := newMemoryRepository()
	resolver := environmentResolver(t, environmentFixture(t, "work", "adapter.shared"))
	manager := newTestManager(t, repository, resolver, nil)
	capture := testCapture()
	if _, err := manager.Create(context.Background(), CreateCommand{
		Capture: capture, EnvironmentID: "work", Source: SourceLaunch,
	}); err != nil {
		t.Fatal(err)
	}
	closer := &recordingCloser{}
	connection, err := manager.RegisterConnection(
		context.Background(), capture, "connection.semantic", semanticOrigin(t), closer.Handle("connection.semantic"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	invalid := semanticRequestFacts()
	invalid.Target.Path = "/v1/not-catalogued"
	if _, err := manager.BeginRequest(context.Background(), capture, "connection.semantic", invalid); !errors.Is(err, protocolspec.ErrOperationNotCatalogued) {
		t.Fatalf("unknown operation = %v", err)
	}
	state := manager.state(capture)
	state.mu.Lock()
	active := state.connections["connection.semantic"].activeRequests
	state.mu.Unlock()
	if active != 0 {
		t.Fatalf("failed admission retained %d active requests", active)
	}
	request, err := manager.BeginRequest(context.Background(), capture, "connection.semantic", semanticRequestFacts())
	if err != nil {
		t.Fatalf("valid request after rejection: %v", err)
	}
	request.Release()
}

func TestReconnectDrainsOnlyIncompatibleConnection(t *testing.T) {
	t.Parallel()
	repository := newMemoryRepository()
	resolver := environmentResolver(t,
		environmentFixture(t, "work", "adapter.work"),
		environmentFixture(t, "personal", "adapter.personal"),
	)
	closer := &recordingCloser{}
	manager := newTestManager(t, repository, resolver, closer)
	capture := testCapture()
	if _, err := manager.Create(context.Background(), CreateCommand{Capture: capture, EnvironmentID: "work", Source: SourceLaunch}); err != nil {
		t.Fatal(err)
	}
	semanticConnection, err := manager.RegisterConnection(
		context.Background(), capture, "connection.semantic", semanticOrigin(t), closer.Handle("connection.semantic"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer semanticConnection.Close()
	blindConnection, err := manager.RegisterConnection(
		context.Background(), capture, "connection.blind", mustOrigin(t, "https://files.example"), closer.Handle("connection.blind"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer blindConnection.Close()
	semanticRequest, err := manager.BeginRequest(context.Background(), capture, "connection.semantic", semanticRequestFacts())
	if err != nil {
		t.Fatal(err)
	}
	if semanticConnection.Binding().Mode != environment.ConnectionModeSemantic ||
		blindConnection.Binding().Mode != environment.ConnectionModeBlind {
		t.Fatalf("connection modes = %q / %q", semanticConnection.Binding().Mode, blindConnection.Binding().Mode)
	}

	type switchAnswer struct {
		result SwitchResult
		err    error
	}
	answer := make(chan switchAnswer, 1)
	go func() {
		result, err := manager.Switch(context.Background(), SwitchCommand{
			Capture: capture, ExpectedRevision: 1, TargetEnvironmentID: "personal", Source: SourceOperatorSwitch,
		})
		answer <- switchAnswer{result: result, err: err}
	}()
	waitForBlockedConnection(t, manager, capture, "connection.semantic")
	if _, err := manager.BeginRequest(context.Background(), capture, "connection.semantic", semanticRequestFacts()); !errors.Is(err, ErrOperationInProgress) {
		t.Fatalf("affected request admission = %v", err)
	}
	assertConnectionAvailable(t, manager, capture, "connection.blind")
	select {
	case value := <-answer:
		t.Fatalf("switch completed before affected drain: %+v", value)
	default:
	}
	semanticRequest.Release()
	select {
	case value := <-answer:
		if value.err != nil || value.result.Boundary != BoundaryReconnectRequired ||
			len(value.result.ClosedConnections) != 1 || value.result.ClosedConnections[0] != "connection.semantic" {
			t.Fatalf("switch = %+v, %v", value.result, value.err)
		}
	case <-time.After(time.Second):
		t.Fatal("switch did not finish after affected request drained")
	}
	if ids := closer.IDs(); len(ids) != 1 || ids[0] != "connection.semantic" {
		t.Fatalf("closed = %v", ids)
	}
	assertConnectionAvailable(t, manager, capture, "connection.blind")
}

func TestReconnectFailureDoesNotPublishAssignment(t *testing.T) {
	t.Parallel()
	repository := newMemoryRepository()
	resolver := environmentResolver(t,
		environmentFixture(t, "work", "adapter.work"),
		environmentFixture(t, "personal", "adapter.personal"),
	)
	closer := &recordingCloser{err: errors.New("close failed")}
	manager := newTestManager(t, repository, resolver, closer)
	capture := testCapture()
	if _, err := manager.Create(context.Background(), CreateCommand{Capture: capture, EnvironmentID: "work", Source: SourceLaunch}); err != nil {
		t.Fatal(err)
	}
	connection, err := manager.RegisterConnection(
		context.Background(), capture, "connection.semantic", semanticOrigin(t), closer.Handle("connection.semantic"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := manager.Switch(context.Background(), SwitchCommand{
		Capture: capture, ExpectedRevision: 1, TargetEnvironmentID: "personal", Source: SourceOperatorSwitch,
	}); !errors.Is(err, ErrReconnectUnavailable) {
		t.Fatalf("Switch error = %v", err)
	}
	assignment, err := manager.Resolve(context.Background(), capture)
	if err != nil || assignment.EnvironmentID != "work" || assignment.Revision != 1 {
		t.Fatalf("assignment changed after close failure = %+v, %v", assignment, err)
	}
	request, err := manager.BeginRequest(context.Background(), capture, "connection.semantic", semanticRequestFacts())
	if err != nil {
		t.Fatalf("old connection stayed blocked: %v", err)
	}
	request.Release()
}

func TestIndeterminateWritePoisonsOnlyItsCapture(t *testing.T) {
	t.Parallel()
	repository := newMemoryRepository()
	repository.nextOutcome = CommitOutcomeIndeterminate
	resolver := environmentResolver(t, environmentFixture(t, "work", "adapter.shared"))
	manager := newTestManager(t, repository, resolver, nil)
	first := testCapture()
	if _, err := manager.Create(context.Background(), CreateCommand{Capture: first, EnvironmentID: "work", Source: SourceLaunch}); !errors.Is(err, ErrCommitOutcomeUnknown) {
		t.Fatalf("indeterminate create = %v", err)
	}
	if _, err := manager.Resolve(context.Background(), first); !errors.Is(err, ErrAssignmentUnavailable) {
		t.Fatalf("poisoned resolve = %v", err)
	}
	second, _ := captureidentity.New(captureidentity.KindManualCapture, "manual.two")
	if _, err := manager.Create(context.Background(), CreateCommand{Capture: second, EnvironmentID: "work", Source: SourceManualCreate}); err != nil {
		t.Fatalf("unrelated capture = %v", err)
	}
}

func TestAffectedCapturesKeepsAllActiveBindings(t *testing.T) {
	t.Parallel()
	repository := newMemoryRepository()
	resolver := environmentResolver(t, environmentFixture(t, "work", "adapter.shared"))
	closer := &recordingCloser{}
	manager := newTestManager(t, repository, resolver, closer)
	capture := testCapture()
	if _, err := manager.Create(context.Background(), CreateCommand{Capture: capture, EnvironmentID: "work", Source: SourceLaunch}); err != nil {
		t.Fatal(err)
	}
	first, err := manager.RegisterConnection(
		context.Background(), capture, "connection.one", semanticOrigin(t), closer.Handle("connection.one"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := manager.RegisterConnection(
		context.Background(), capture, "connection.two", semanticOrigin(t), closer.Handle("connection.two"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	refs, err := manager.AffectedCaptures(context.Background(), "work", 10)
	if err != nil || len(refs) != 1 || len(refs[0].Bindings) != 1 {
		t.Fatalf("affected = %+v, %v", refs, err)
	}
}

func TestEnvironmentTransitionDrainsOnlyIncompatibleConnectionsAndHoldsFence(t *testing.T) {
	t.Parallel()
	repository := newMemoryRepository()
	work := environmentFixture(t, "work", "adapter.work")
	resolver := environmentResolver(t, work)
	closer := &recordingCloser{}
	manager := newTestManager(t, repository, resolver, closer)
	capture := testCapture()
	if _, err := manager.Create(context.Background(), CreateCommand{Capture: capture, EnvironmentID: "work", Source: SourceLaunch}); err != nil {
		t.Fatal(err)
	}
	semanticConnection, err := manager.RegisterConnection(
		context.Background(), capture, "connection.semantic", semanticOrigin(t), closer.Handle("connection.semantic"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer semanticConnection.Close()
	blindConnection, err := manager.RegisterConnection(
		context.Background(), capture, "connection.blind", mustOrigin(t, "https://files.example"), closer.Handle("connection.blind"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer blindConnection.Close()
	semanticRequest, err := manager.BeginRequest(context.Background(), capture, "connection.semantic", semanticRequestFacts())
	if err != nil {
		t.Fatal(err)
	}

	active, err := resolver.Resolve("work")
	if err != nil {
		t.Fatal(err)
	}
	candidateAggregate := work.Clone()
	candidateAggregate.Revision = 2
	candidateAggregate.ClientEndpoints[0].Revision = 2
	candidateAggregate.ClientEndpoints[0].ProtocolPlans[0].Revision = 2
	candidateAggregate.ClientEndpoints[0].ProtocolPlans[0].ClientAdapterPolicy = environment.ClientAdapterPolicy{
		ID: "adapter.replacement", Revision: 2,
	}
	candidate, err := environmentCompiler(t).Compile(candidateAggregate)
	if err != nil {
		t.Fatal(err)
	}
	references, err := manager.AffectedCaptures(context.Background(), "work", 10)
	if err != nil || len(references) != 1 {
		t.Fatalf("affected = %+v, %v", references, err)
	}
	transition := environment.EnvironmentTransition{
		ActiveExists: true,
		Active:       active,
		Candidate:    candidate,
		Expected: environment.ImpactPreview{
			EnvironmentID: "work", BaseRevision: 1, DraftRevision: 1,
			CandidateDigest: candidate.Digest(), Classification: environment.CompatibilityReconnectRequired,
			ReconnectRequiredCount: 1,
			Affected:               []environment.ImpactReference{{Capture: references[0], Classification: environment.CompatibilityReconnectRequired}},
		},
	}
	type prepareAnswer struct {
		lease environment.EnvironmentTransitionLease
		err   error
	}
	answer := make(chan prepareAnswer, 1)
	go func() {
		lease, err := manager.PrepareEnvironmentTransition(context.Background(), transition)
		answer <- prepareAnswer{lease: lease, err: err}
	}()
	waitForBlockedConnection(t, manager, capture, "connection.semantic")
	select {
	case value := <-answer:
		t.Fatalf("transition completed before semantic drain: %+v", value)
	default:
	}
	semanticRequest.Release()
	var lease environment.EnvironmentTransitionLease
	select {
	case value := <-answer:
		if value.err != nil || value.lease == nil {
			t.Fatalf("prepare = %+v", value)
		}
		lease = value.lease
	case <-time.After(time.Second):
		t.Fatal("transition did not finish after incompatible request drained")
	}
	if ids := closer.IDs(); len(ids) != 1 || ids[0] != "connection.semantic" {
		t.Fatalf("closed = %v", ids)
	}
	// The compatible blind connection did not participate in the drain.
	lease.Release()
	assertConnectionAvailable(t, manager, capture, "connection.blind")
}

func waitForBlockedConnection(t *testing.T, manager *Manager, capture captureidentity.Reference, connectionID string) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		state := manager.state(capture)
		state.mu.Lock()
		_, blocked := state.blocked[connectionID]
		state.mu.Unlock()
		if blocked {
			return
		}
		select {
		case <-deadline:
			t.Fatal("connection was not blocked for drain")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func testCapture() captureidentity.Reference {
	reference, _ := captureidentity.New(captureidentity.KindManagedRun, "run.one")
	return reference
}

func semanticOrigin(t *testing.T) originidentity.ClientOrigin {
	t.Helper()
	return mustOrigin(t, "https://api.example")
}

func semanticRequestFacts() environment.RequestFacts {
	return environment.RequestFacts{
		Target: protocolspec.RequestTarget{
			Method: "POST", Path: "/v1/messages",
			Transport: protocolspec.ClientOperationTransportHTTP,
		},
		DownstreamProtocol: wireprofile.ApplicationProtocolHTTP1,
	}
}

func environmentFixture(t *testing.T, id string, adapterID string) environment.Environment {
	t.Helper()
	clientOrigin := mustOrigin(t, "https://api.example")
	providerOrigin, err := originidentity.ParseProviderOrigin(clientOrigin.String())
	if err != nil {
		t.Fatal(err)
	}
	return environment.Environment{
		ID:               environment.EnvironmentID(id),
		Name:             id,
		State:            environment.StateActive,
		Revision:         1,
		ContentRecording: environment.DefaultContentRecordingPolicy(),
		ClientEndpoints: []environment.ClientEndpoint{
			{
				ID:           "endpoint.api",
				Revision:     1,
				ClientOrigin: clientOrigin,
				ProtocolPlans: []environment.ClientProtocolPlan{
					{
						ID:                  "plan.messages",
						Revision:            1,
						ClientProtocol:      environment.ClientProtocolAnthropicMessages,
						ClientAdapterPolicy: environment.ClientAdapterPolicy{ID: adapterID, Revision: 1},
						Mode:                environment.PlanModeManaged,
						UpstreamPlan: environment.UpstreamPlan{
							DefaultRouteID: "route.default",
							RouteSet: environment.RouteSet{
								ID: "routes.default", Revision: 1,
								CandidateRouteIDs: []environment.UpstreamRouteID{"route.default"},
							},
							Routes: []environment.UpstreamRoute{
								{
									ID:       "route.default",
									Revision: 1,
									ProviderTarget: environment.ProviderTarget{
										ID: "target.default", Revision: 1, Origin: providerOrigin, RealmID: "realm.default",
										Capabilities: []protocolspec.ProviderCapability{
											protocolspec.ProviderCapabilityMessages,
											protocolspec.ProviderCapabilityStreaming,
											protocolspec.ProviderCapabilityToolCalls,
										},
									},
									BackendProtocol: "anthropic_messages",
									AccountPolicy: environment.RouteAccountPolicy{
										Revision: 1, Mode: environment.AccountModeClientPassthrough,
										AllowedRealmIDs: []string{"realm.default"}, FailoverPolicy: environment.FailoverOff,
									},
									ModelPolicy:    environment.ModelPolicy{Revision: 1, Mode: "passthrough"},
									WireProfileRef: wireprofile.UpstreamWireProfileFollowClientValue,
								},
							},
						},
					},
				},
			},
		},
	}
}

func environmentResolver(t *testing.T, values ...environment.Environment) *environment.AtomicProjection {
	t.Helper()
	projection := environment.NewAtomicProjection()
	snapshots := make([]environment.EnvironmentSnapshot, 0, len(values))
	for _, value := range values {
		snapshot, err := environmentCompiler(t).Compile(value)
		if err != nil {
			t.Fatal(err)
		}
		snapshots = append(snapshots, snapshot)
	}
	if err := projection.Restore(snapshots); err != nil {
		t.Fatal(err)
	}
	return projection
}

func environmentCompiler(t *testing.T) environment.Compiler {
	t.Helper()
	operations, err := operationcatalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	pairID, err := protocolspec.NewCodecPairID("test.anthropic.passthrough")
	if err != nil {
		t.Fatal(err)
	}
	protocols, err := protocolspec.NewCatalog(
		operations.Definitions(),
		[]protocolspec.CodecPairDefinition{{
			ID: pairID, Revision: 1,
			ClientDialect:      protocolspec.DialectAnthropicMessages,
			ProviderDialect:    protocolspec.DialectAnthropicMessages,
			ClientOperationIDs: operations.SemanticOperationIDs(protocolspec.DialectAnthropicMessages),
			RequiredCapabilities: []protocolspec.ProviderCapability{
				protocolspec.ProviderCapabilityMessages,
				protocolspec.ProviderCapabilityStreaming,
				protocolspec.ProviderCapabilityToolCalls,
			},
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	wires, err := wireprofile.BuiltInCatalog()
	if err != nil {
		t.Fatal(err)
	}
	compiler, err := environment.NewCompiler(nil, protocols, wires)
	if err != nil {
		t.Fatal(err)
	}
	return compiler
}

func assertConnectionAvailable(
	t *testing.T,
	manager *Manager,
	capture captureidentity.Reference,
	connectionID string,
) {
	t.Helper()
	state := manager.state(capture)
	state.mu.Lock()
	defer state.mu.Unlock()
	connection, exists := state.connections[connectionID]
	_, blocked := state.blocked[connectionID]
	if !exists || connection.closing || blocked {
		t.Fatalf("connection %q is unavailable", connectionID)
	}
}

func mustOrigin(t *testing.T, value string) originidentity.ClientOrigin {
	t.Helper()
	origin, err := originidentity.ParseClientOrigin(value)
	if err != nil {
		t.Fatal(err)
	}
	return origin
}

type fixedClock struct{ value time.Time }

func (clock fixedClock) Now() time.Time { return clock.value }

func newTestManager(t *testing.T, repository Repository, resolver environment.SnapshotResolver, _ *recordingCloser) *Manager {
	t.Helper()
	manager, err := NewManager(Options{
		Repository: repository, Environments: resolver,
		Clock: fixedClock{value: time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

type recordingCloser struct {
	mu  sync.Mutex
	ids []string
	err error
}

type recordingCloseHandle struct {
	closer *recordingCloser
	id     string
}

func (closer *recordingCloser) Handle(id string) ConnectionCloseHandle {
	return &recordingCloseHandle{closer: closer, id: id}
}

func (handle *recordingCloseHandle) Close(_ context.Context) error {
	closer := handle.closer
	closer.mu.Lock()
	defer closer.mu.Unlock()
	if closer.err != nil {
		return closer.err
	}
	closer.ids = append(closer.ids, handle.id)
	return nil
}

func (closer *recordingCloser) IDs() []string {
	closer.mu.Lock()
	defer closer.mu.Unlock()
	return append([]string(nil), closer.ids...)
}

type memoryRepository struct {
	mu          sync.Mutex
	assignments map[captureidentity.Reference]Assignment
	nextOutcome CommitOutcome
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{assignments: make(map[captureidentity.Reference]Assignment)}
}

func (repository *memoryRepository) Load(_ context.Context, reference captureidentity.Reference) (Assignment, bool, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	assignment, exists := repository.assignments[reference]
	return assignment, exists, nil
}

func (repository *memoryRepository) ListByEnvironment(_ context.Context, id environment.EnvironmentID, limit int) ([]Assignment, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	result := make([]Assignment, 0)
	for _, assignment := range repository.assignments {
		if assignment.EnvironmentID == id {
			result = append(result, assignment)
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Capture.Key() < result[right].Capture.Key() })
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (repository *memoryRepository) Write(_ context.Context, expected Revision, candidate Assignment) (CommitResult, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.nextOutcome != "" {
		outcome := repository.nextOutcome
		repository.nextOutcome = ""
		return CommitResult{Outcome: outcome, Assignment: candidate, Actual: candidate.Revision}, errors.New("injected write outcome")
	}
	current, exists := repository.assignments[candidate.Capture]
	if (!exists && expected != 0) || (exists && current.Revision != expected) {
		actual := Revision(0)
		if exists {
			actual = current.Revision
		}
		return CommitResult{Outcome: CommitOutcomeConflict, Assignment: current, Actual: actual}, nil
	}
	repository.assignments[candidate.Capture] = candidate
	return CommitResult{Outcome: CommitOutcomeCommitted, Assignment: candidate, Actual: candidate.Revision}, nil
}
