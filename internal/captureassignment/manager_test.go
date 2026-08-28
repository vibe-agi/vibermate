package captureassignment

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

func TestCaptureKeepsFrozenEnvironmentRevisionAfterPublish(t *testing.T) {
	t.Parallel()
	repository := newMemoryRepository()
	revisionOne := environmentFixture(t, "work", "adapter.shared")
	revisionOne.LaunchEnvironment = environment.LaunchEnvironmentPolicy{
		SetEnv:    map[string]string{"TEAM_CONTEXT": "revision-one"},
		DeleteEnv: []string{"REMOVE_CONTEXT"},
	}
	resolver := newRevisionResolver(t, revisionOne)
	manager := newTestManager(t, repository, resolver)

	firstCapture := testCapture()
	firstAssignment, firstLaunchEnvironment, err := manager.CreateForLaunch(
		context.Background(),
		CreateCommand{
			Capture: firstCapture, EnvironmentID: "work", Source: SourceLaunch,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if firstAssignment.LaunchAuthority.InitialEnvironmentRevision() != 1 {
		t.Fatalf("first launch revision = %d", firstAssignment.LaunchAuthority.InitialEnvironmentRevision())
	}
	if firstLaunchEnvironment.SetEnv["TEAM_CONTEXT"] != "revision-one" ||
		len(firstLaunchEnvironment.DeleteEnv) != 1 ||
		firstLaunchEnvironment.DeleteEnv[0] != "REMOVE_CONTEXT" {
		t.Fatalf("first launch environment = %+v", firstLaunchEnvironment)
	}
	firstLaunchEnvironment.SetEnv["TEAM_CONTEXT"] = "mutated"
	firstConnection, err := manager.RegisterConnection(
		context.Background(), firstCapture, "connection.first", semanticOrigin(t),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer firstConnection.Close()
	assertRequestRoute(t, manager, firstCapture, "connection.first", 1, "route.default")

	revisionTwo := revisionOne.Clone()
	revisionTwo.Revision = 2
	revisionTwo.LaunchEnvironment.SetEnv["TEAM_CONTEXT"] = "revision-two"
	revisionTwo.ClientEndpoints[0].Revision = 2
	plan := &revisionTwo.ClientEndpoints[0].ProtocolPlans[0]
	plan.Revision = 2
	plan.Destination.Upstream.DefaultRouteID = "route.revision-two"
	plan.Destination.Upstream.RouteSet.Revision = 2
	plan.Destination.Upstream.RouteSet.CandidateRouteIDs = []environment.UpstreamRouteID{"route.revision-two"}
	plan.Destination.Upstream.Routes[0].ID = "route.revision-two"
	resolver.Publish(t, revisionTwo)

	// An already-open connection remains pinned to the launch revision.
	assertRequestRoute(t, manager, firstCapture, "connection.first", 1, "route.default")
	// Even a later connection belonging to the same Capture resolves durable r1.
	laterConnection, err := manager.RegisterConnection(
		context.Background(), firstCapture, "connection.later", semanticOrigin(t),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer laterConnection.Close()
	if laterConnection.Environment().Revision() != 1 {
		t.Fatalf("later connection in existing Capture revision = %d", laterConnection.Environment().Revision())
	}
	assertRequestRoute(t, manager, firstCapture, "connection.later", 1, "route.default")

	// A separately launched Capture observes the newly published revision.
	secondCapture, err := captureidentity.New(captureidentity.KindManagedRun, "run.two")
	if err != nil {
		t.Fatal(err)
	}
	secondAssignment, secondLaunchEnvironment, err := manager.CreateForLaunch(
		context.Background(),
		CreateCommand{
			Capture: secondCapture, EnvironmentID: "work", Source: SourceLaunch,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if secondAssignment.LaunchAuthority.InitialEnvironmentRevision() != 2 {
		t.Fatalf("second launch revision = %d", secondAssignment.LaunchAuthority.InitialEnvironmentRevision())
	}
	if secondLaunchEnvironment.SetEnv["TEAM_CONTEXT"] != "revision-two" {
		t.Fatalf("second launch environment = %+v", secondLaunchEnvironment)
	}
	secondConnection, err := manager.RegisterConnection(
		context.Background(), secondCapture, "connection.second", semanticOrigin(t),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer secondConnection.Close()
	assertRequestRoute(t, manager, secondCapture, "connection.second", 2, "route.revision-two")
}

func TestApplyLatestChangesOnlyRequestsBegunAfterApply(t *testing.T) {
	t.Parallel()
	repository := newMemoryRepository()
	revisionOne := environmentFixture(t, "work", "adapter.shared")
	resolver := newRevisionResolver(t, revisionOne)
	manager := newTestManager(t, repository, resolver)
	capture := testCapture()
	created, err := manager.Create(context.Background(), CreateCommand{
		Capture: capture, EnvironmentID: "work", Source: SourceLaunch,
	})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := manager.RegisterConnection(
		context.Background(), capture, "connection.semantic", semanticOrigin(t),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	inFlight, err := manager.BeginRequest(
		context.Background(), capture, "connection.semantic", semanticRequestFacts(),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer inFlight.Release()

	revisionTwo := revisionOne.Clone()
	revisionTwo.Revision = 2
	revisionTwo.ClientEndpoints[0].Revision = 2
	plan := &revisionTwo.ClientEndpoints[0].ProtocolPlans[0]
	plan.Revision = 2
	plan.Destination.Upstream.DefaultRouteID = "route.revision-two"
	plan.Destination.Upstream.RouteSet.Revision = 2
	plan.Destination.Upstream.RouteSet.CandidateRouteIDs = []environment.UpstreamRouteID{"route.revision-two"}
	plan.Destination.Upstream.Routes[0].ID = "route.revision-two"
	resolver.Publish(t, revisionTwo)

	applied, err := manager.ApplyLatest(context.Background(), capture, created.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Revision != 2 || applied.EnvironmentRevision != 2 ||
		applied.LaunchAuthority != created.LaunchAuthority ||
		applied.LaunchAuthority.InitialEnvironmentRevision() != 1 {
		t.Fatalf("applied assignment = %+v", applied)
	}
	oldRoute, oldRouteExists := inFlight.Plan().UpstreamRoute()
	if inFlight.Plan().EnvironmentRevision() != 1 || !oldRouteExists || oldRoute.ID() != "route.default" {
		t.Fatalf("in-flight request changed = revision %d route %q", inFlight.Plan().EnvironmentRevision(), oldRoute.ID())
	}
	assertRequestRoute(t, manager, capture, "connection.semantic", 2, "route.revision-two")
}

func TestApplyLatestRejectsAnEnvironmentThatCannotServeAnOpenConnection(t *testing.T) {
	t.Parallel()
	repository := newMemoryRepository()
	revisionOne := environmentFixture(t, "work", "adapter.shared")
	resolver := newRevisionResolver(t, revisionOne)
	manager := newTestManager(t, repository, resolver)
	capture := testCapture()
	created, err := manager.Create(context.Background(), CreateCommand{
		Capture: capture, EnvironmentID: "work", Source: SourceLaunch,
	})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := manager.RegisterConnection(
		context.Background(), capture, "connection.semantic", semanticOrigin(t),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()

	revisionTwo := revisionOne.Clone()
	revisionTwo.Revision = 2
	revisionTwo.ClientEndpoints = nil
	resolver.Publish(t, revisionTwo)

	if _, err := manager.ApplyLatest(
		context.Background(), capture, created.Revision,
	); !errors.Is(err, ErrAssignmentIncompatible) {
		t.Fatalf("incompatible apply = %v", err)
	}
	resolved, err := manager.Resolve(context.Background(), capture)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != created {
		t.Fatalf("rejected apply changed assignment = %+v", resolved)
	}
	assertRequestRoute(t, manager, capture, "connection.semantic", 1, "route.default")
}

func TestApplyLatestDoesNotOverflowTheAssignmentRevision(t *testing.T) {
	t.Parallel()
	repository := newMemoryRepository()
	revisionOne := environmentFixture(t, "work", "adapter.shared")
	resolver := newRevisionResolver(t, revisionOne)
	manager := newTestManager(t, repository, resolver)
	capture := testCapture()
	created, err := manager.Create(context.Background(), CreateCommand{
		Capture: capture, EnvironmentID: "work", Source: SourceLaunch,
	})
	if err != nil {
		t.Fatal(err)
	}
	saturated := created
	saturated.Revision = MaxRevision
	repository.assignments[capture] = saturated
	revisionTwo := revisionOne.Clone()
	revisionTwo.Revision = 2
	revisionTwo.ClientEndpoints[0].Revision = 2
	revisionTwo.ClientEndpoints[0].ProtocolPlans[0].Revision = 2
	resolver.Publish(t, revisionTwo)

	if _, err := manager.ApplyLatest(
		context.Background(), capture, MaxRevision,
	); !errors.Is(err, ErrAssignmentUnavailable) {
		t.Fatalf("apply at maximum assignment revision = %v", err)
	}
	resolved, err := manager.Resolve(context.Background(), capture)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != saturated {
		t.Fatalf("overflow attempt changed assignment = %+v", resolved)
	}
}

func TestRegisterConnectionFailsWhenFrozenRevisionIsUnavailable(t *testing.T) {
	t.Parallel()
	repository := newMemoryRepository()
	resolver := newRevisionResolver(t, environmentFixture(t, "work", "adapter.shared"))
	manager := newTestManager(t, repository, resolver)
	capture := testCapture()
	if _, err := manager.Create(context.Background(), CreateCommand{
		Capture: capture, EnvironmentID: "work", Source: SourceLaunch,
	}); err != nil {
		t.Fatal(err)
	}
	resolver.ForgetRevision("work", 1)
	if _, err := manager.RegisterConnection(
		context.Background(), capture, "connection.semantic", semanticOrigin(t),
	); !errors.Is(err, environment.ErrEnvironmentNotFound) {
		t.Fatalf("register without frozen revision = %v", err)
	}
}

func TestRequestAdmissionFailsClosedWithoutLeakingActiveLease(t *testing.T) {
	t.Parallel()
	repository := newMemoryRepository()
	resolver := newRevisionResolver(t, environmentFixture(t, "work", "adapter.shared"))
	manager := newTestManager(t, repository, resolver)
	capture := testCapture()
	if _, err := manager.Create(context.Background(), CreateCommand{
		Capture: capture, EnvironmentID: "work", Source: SourceLaunch,
	}); err != nil {
		t.Fatal(err)
	}
	connection, err := manager.RegisterConnection(
		context.Background(), capture, "connection.semantic", semanticOrigin(t),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	invalid := semanticRequestFacts()
	invalid.Target.Path = "/v1/not-catalogued"
	if _, err := manager.BeginRequest(
		context.Background(), capture, "connection.semantic", invalid,
	); !errors.Is(err, protocolspec.ErrOperationNotCatalogued) {
		t.Fatalf("unknown operation = %v", err)
	}
	state := manager.state(capture)
	state.mu.Lock()
	active := state.connections["connection.semantic"].activeRequests
	state.mu.Unlock()
	if active != 0 {
		t.Fatalf("failed admission retained %d active requests", active)
	}
	request, err := manager.BeginRequest(
		context.Background(), capture, "connection.semantic", semanticRequestFacts(),
	)
	if err != nil {
		t.Fatalf("valid request after rejection: %v", err)
	}
	request.Release()
}

func TestIndeterminateCreatePoisonsOnlyItsCapture(t *testing.T) {
	t.Parallel()
	repository := newMemoryRepository()
	repository.nextOutcome = CommitOutcomeIndeterminate
	resolver := newRevisionResolver(t, environmentFixture(t, "work", "adapter.shared"))
	manager := newTestManager(t, repository, resolver)
	first := testCapture()
	if _, err := manager.Create(context.Background(), CreateCommand{
		Capture: first, EnvironmentID: "work", Source: SourceLaunch,
	}); !errors.Is(err, ErrCommitOutcomeUnknown) {
		t.Fatalf("indeterminate create = %v", err)
	}
	if _, err := manager.Resolve(context.Background(), first); !errors.Is(err, ErrAssignmentUnavailable) {
		t.Fatalf("poisoned resolve = %v", err)
	}
	if _, err := manager.ApplyLatest(
		context.Background(), first, 1,
	); !errors.Is(err, ErrAssignmentUnavailable) {
		t.Fatalf("poisoned apply = %v", err)
	}
	second, _ := captureidentity.New(captureidentity.KindManualCapture, "manual.two")
	if _, err := manager.Create(context.Background(), CreateCommand{
		Capture: second, EnvironmentID: "work", Source: SourceManualCreate,
	}); err != nil {
		t.Fatalf("unrelated Capture = %v", err)
	}
}

func TestCreateIsInsertOnly(t *testing.T) {
	t.Parallel()
	repository := newMemoryRepository()
	resolver := newRevisionResolver(t, environmentFixture(t, "work", "adapter.shared"))
	manager := newTestManager(t, repository, resolver)
	capture := testCapture()
	first, err := manager.Create(context.Background(), CreateCommand{
		Capture: capture, EnvironmentID: "work", Source: SourceLaunch,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Create(context.Background(), CreateCommand{
		Capture: capture, EnvironmentID: "work", Source: SourceLaunch,
	})
	if !errors.Is(err, ErrAssignmentConflict) || second != first {
		t.Fatalf("second create = %+v, %v", second, err)
	}
	resolved, err := manager.Resolve(context.Background(), capture)
	if err != nil || resolved != first {
		t.Fatalf("insert-only assignment changed = %+v, %v", resolved, err)
	}
}

func TestActiveCapturesExcludesTerminalHistory(t *testing.T) {
	t.Parallel()
	repository := newMemoryRepository()
	resolver := newRevisionResolver(t, environmentFixture(t, "work", "adapter.shared"))
	activity := &fixedCaptureActivity{inactive: make(map[string]bool)}
	manager := newTestManagerWithActivity(t, repository, resolver, activity)
	active := testCapture()
	terminal, err := captureidentity.New(captureidentity.KindManagedRun, "finished.run")
	if err != nil {
		t.Fatal(err)
	}
	for _, capture := range []captureidentity.Reference{active, terminal} {
		if _, err := manager.Create(context.Background(), CreateCommand{
			Capture: capture, EnvironmentID: "work", Source: SourceLaunch,
		}); err != nil {
			t.Fatal(err)
		}
	}
	activity.inactive[terminal.Key()] = true
	references, err := manager.ActiveCaptures(context.Background(), "work", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(references) != 1 || references[0].Capture != active {
		t.Fatalf("active Captures = %+v", references)
	}
	if assignment, err := manager.Resolve(context.Background(), terminal); err != nil || assignment.Capture != terminal {
		t.Fatalf("terminal historical assignment was lost: %+v, %v", assignment, err)
	}
}

func assertRequestRoute(
	t *testing.T,
	manager *Manager,
	capture captureidentity.Reference,
	connectionID string,
	wantRevision environment.Revision,
	wantRoute environment.UpstreamRouteID,
) {
	t.Helper()
	request, err := manager.BeginRequest(
		context.Background(), capture, connectionID, semanticRequestFacts(),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer request.Release()
	route, exists := request.Plan().UpstreamRoute()
	if request.Plan().EnvironmentRevision() != wantRevision || !exists || route.ID() != wantRoute {
		t.Fatalf(
			"request authority = %q@%d route=%q exists=%v",
			request.Plan().EnvironmentID(),
			request.Plan().EnvironmentRevision(),
			route.ID(),
			exists,
		)
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
		ID: environment.EnvironmentID(id), Name: id, State: environment.StateActive, Revision: 1,
		ContentRecording: environment.DefaultContentRecordingPolicy(),
		ClientEndpoints: []environment.ClientEndpoint{{
			ID: "endpoint.api", Revision: 1, ClientOrigin: clientOrigin,
			ProtocolPlans: []environment.ClientProtocolPlan{{
				ID: "plan.messages", Revision: 1,
				ClientProtocol:      environment.ClientProtocolAnthropicMessages,
				ClientAdapterPolicy: environment.ClientAdapterPolicy{ID: adapterID, Revision: 1},
				EgressProfile:       egressprofile.Direct(),
				Destination: environment.DestinationPlan{
					Kind: environment.DestinationKindUpstream,
					Upstream: &environment.UpstreamPlan{
						DefaultRouteID: "route.default",
						RouteSet: environment.RouteSet{
							ID: "routes.default", Revision: 1,
							CandidateRouteIDs: []environment.UpstreamRouteID{"route.default"},
						},
						Routes: []environment.UpstreamRoute{{
							ID: "route.default", Revision: 1,
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
								Revision: 1, PreferredAccountID: "account.default",
								CandidateAccountIDs: []string{"account.default"},
								AccountRevisions:    map[string]environment.Revision{"account.default": 1},
								FailoverPolicy:      environment.FailoverOff,
							},
							ModelPolicy:    environment.ModelPolicy{Revision: 1, Mode: "passthrough"},
							WireProfileRef: wireprofile.UpstreamWireProfileFollowClientValue,
						}},
					},
				},
			}},
		}},
	}
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
	compiler, err := environment.NewCompiler(captureAssignmentAccountCatalog{}, nil, protocols, wires)
	if err != nil {
		t.Fatal(err)
	}
	return compiler
}

type captureAssignmentAccountCatalog struct{}

func (captureAssignmentAccountCatalog) LookupAccount(id string) (environment.AccountDescriptor, bool) {
	if id != "account.default" {
		return environment.AccountDescriptor{}, false
	}
	return environment.AccountDescriptor{
		ID: id, Revision: 1,
		UpstreamEndpointID: "target.default", UpstreamEndpointRevision: 1,
		RealmID: "realm.default", Active: true,
		BackendProtocols: []string{"anthropic_messages"},
	}, true
}

func mustOrigin(t *testing.T, value string) originidentity.ClientOrigin {
	t.Helper()
	origin, err := originidentity.ParseClientOrigin(value)
	if err != nil {
		t.Fatal(err)
	}
	return origin
}

type revisionResolver struct {
	mu       sync.Mutex
	compiler environment.Compiler
	active   map[environment.EnvironmentID]environment.EnvironmentSnapshot
	history  map[environment.EnvironmentID]map[environment.Revision]environment.EnvironmentSnapshot
}

func newRevisionResolver(t *testing.T, values ...environment.Environment) *revisionResolver {
	t.Helper()
	resolver := &revisionResolver{
		compiler: environmentCompiler(t),
		active:   make(map[environment.EnvironmentID]environment.EnvironmentSnapshot),
		history:  make(map[environment.EnvironmentID]map[environment.Revision]environment.EnvironmentSnapshot),
	}
	for _, value := range values {
		resolver.Publish(t, value)
	}
	return resolver
}

func (resolver *revisionResolver) Publish(t *testing.T, value environment.Environment) {
	t.Helper()
	snapshot, err := resolver.compiler.Compile(value)
	if err != nil {
		t.Fatal(err)
	}
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	if resolver.history[snapshot.ID()] == nil {
		resolver.history[snapshot.ID()] = make(map[environment.Revision]environment.EnvironmentSnapshot)
	}
	resolver.history[snapshot.ID()][snapshot.Revision()] = snapshot
	resolver.active[snapshot.ID()] = snapshot
}

func (resolver *revisionResolver) ForgetRevision(id environment.EnvironmentID, revision environment.Revision) {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	delete(resolver.history[id], revision)
}

func (resolver *revisionResolver) Resolve(id environment.EnvironmentID) (environment.EnvironmentSnapshot, error) {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	snapshot, exists := resolver.active[id]
	if !exists {
		return environment.EnvironmentSnapshot{}, environment.ErrEnvironmentNotFound
	}
	return snapshot, nil
}

func (resolver *revisionResolver) ResolveRevision(
	ctx context.Context,
	id environment.EnvironmentID,
	revision environment.Revision,
) (environment.EnvironmentSnapshot, error) {
	if ctx == nil {
		return environment.EnvironmentSnapshot{}, environment.ErrInvalidEnvironment
	}
	if err := ctx.Err(); err != nil {
		return environment.EnvironmentSnapshot{}, err
	}
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	snapshot, exists := resolver.history[id][revision]
	if !exists {
		return environment.EnvironmentSnapshot{}, environment.ErrEnvironmentNotFound
	}
	return snapshot, nil
}

func (resolver *revisionResolver) ResolveClientOrigin(
	id environment.EnvironmentID,
	origin originidentity.ClientOrigin,
) (environment.ClientEndpointSnapshot, error) {
	snapshot, err := resolver.Resolve(id)
	if err != nil {
		return environment.ClientEndpointSnapshot{}, err
	}
	endpoint, exists := snapshot.LookupClientOrigin(origin)
	if !exists {
		return environment.ClientEndpointSnapshot{}, environment.ErrEnvironmentNotFound
	}
	return endpoint, nil
}

type fixedClock struct{ value time.Time }

func (clock fixedClock) Now() time.Time { return clock.value }

func newTestManager(t *testing.T, repository Repository, resolver environment.SnapshotResolver) *Manager {
	t.Helper()
	return newTestManagerWithActivity(
		t,
		repository,
		resolver,
		&fixedCaptureActivity{inactive: make(map[string]bool)},
	)
}

func newTestManagerWithActivity(
	t *testing.T,
	repository Repository,
	resolver environment.SnapshotResolver,
	activity CaptureActivity,
) *Manager {
	t.Helper()
	manager, err := NewManager(Options{
		Repository:   repository,
		Environments: resolver,
		Activity:     activity,
		Clock:        fixedClock{value: time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

type fixedCaptureActivity struct {
	inactive map[string]bool
	err      error
}

func (activity *fixedCaptureActivity) Active(
	_ context.Context,
	reference captureidentity.Reference,
) (bool, error) {
	if activity.err != nil {
		return false, activity.err
	}
	return !activity.inactive[reference.Key()], nil
}

type memoryRepository struct {
	mu          sync.Mutex
	assignments map[captureidentity.Reference]Assignment
	nextOutcome CommitOutcome
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{assignments: make(map[captureidentity.Reference]Assignment)}
}

func (repository *memoryRepository) Load(
	_ context.Context,
	reference captureidentity.Reference,
) (Assignment, bool, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	assignment, exists := repository.assignments[reference]
	return assignment, exists, nil
}

func (repository *memoryRepository) ListByEnvironment(
	_ context.Context,
	id environment.EnvironmentID,
	limit int,
) ([]Assignment, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	result := make([]Assignment, 0)
	for _, assignment := range repository.assignments {
		if assignment.EnvironmentID == id {
			result = append(result, assignment)
		}
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].Capture.Key() < result[right].Capture.Key()
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (repository *memoryRepository) Write(
	_ context.Context,
	expected Revision,
	candidate Assignment,
) (CommitResult, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.nextOutcome != "" {
		outcome := repository.nextOutcome
		repository.nextOutcome = ""
		return CommitResult{
			Outcome:    outcome,
			Assignment: candidate,
			Actual:     candidate.Revision,
		}, errors.New("injected write outcome")
	}
	current, exists := repository.assignments[candidate.Capture]
	if expected == 0 && exists || expected != 0 && (!exists || current.Revision != expected) {
		actual := Revision(0)
		if exists {
			actual = current.Revision
		}
		return CommitResult{
			Outcome:    CommitOutcomeConflict,
			Assignment: current,
			Actual:     actual,
		}, nil
	}
	if expected != 0 && candidate.Revision != expected+1 {
		return CommitResult{Outcome: CommitOutcomeNotCommitted}, ErrInvalidAssignment
	}
	repository.assignments[candidate.Capture] = candidate
	return CommitResult{
		Outcome:    CommitOutcomeCommitted,
		Assignment: candidate,
		Actual:     candidate.Revision,
	}, nil
}
