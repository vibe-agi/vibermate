package productruntime

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

var errAcquireNotExpected = errors.New("egress acquisition is not expected in ProductRuntime lifecycle tests")

func TestProductionEnvironmentCompilerFreezesExactResponsesOperation(t *testing.T) {
	t.Parallel()

	compiler, err := productionEnvironmentCompiler(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	aggregate := passthroughEnvironment(
		t,
		"responses",
		"https://api.openai.com:443",
		environment.ClientProtocolOpenAIResponses,
	)
	snapshot, err := compiler.Compile(aggregate)
	if err != nil {
		t.Fatalf("compile Environment: %v", err)
	}
	origin, err := originidentity.ParseClientOrigin("https://api.openai.com:443")
	if err != nil {
		t.Fatal(err)
	}
	for operationID, path := range map[string]string{
		"openai-responses-create":       "/v1/responses",
		"openai-codex-responses-create": "/backend-api/codex/responses",
	} {
		plan, resolveErr := snapshot.ResolveRequest(origin, environment.RequestFacts{
			Target: protocolspec.RequestTarget{
				Method: "POST", Path: path,
				Transport: protocolspec.ClientOperationTransportHTTP,
			},
			DownstreamProtocol: wireprofile.ApplicationProtocolHTTP1,
		})
		if resolveErr != nil {
			t.Fatalf("resolve %s: %v", operationID, resolveErr)
		}
		if got := plan.Operation().ID().String(); got != operationID {
			t.Fatalf("operation ID = %q, want %q", got, operationID)
		}
		if plan.Operation().PathMatch() != protocolspec.ClientOperationPathExact ||
			plan.Route().CodecPlan().ClientDialect() != protocolspec.DialectOpenAIResponses ||
			plan.Route().CodecPlan().ProviderDialect() != protocolspec.DialectOpenAIResponses {
			t.Fatalf("compiled Responses plan = %+v", plan)
		}
	}
}

func TestProductRuntimeStartsWithSystemTransparentAndShutsDown(t *testing.T) {
	t.Parallel()

	coordinator := &coordinatorDouble{}
	runtime := startTestRuntime(t, testOptions(t, hostcontract.Desktop(), coordinator))

	status := runtime.Status()
	if status.State != RuntimeStateInitialized || status.Storage != StorageStateHealthy ||
		status.EnvironmentProjection.State != environment.ProjectionStateHealthy ||
		status.SchemaRevision <= 0 || status.InstanceID == "" ||
		coordinator.boundInstanceID() != status.InstanceID {
		t.Fatalf("initialized status = %+v", status)
	}
	if status.OfflineHold.State != offlinehold.StateOnline {
		t.Fatalf("offline-hold status = %+v", status.OfflineHold)
	}
	system, err := runtime.EnvironmentResolver().Resolve(environment.SystemTransparentID)
	if err != nil || !system.SystemOwned() || !system.BlindOnly() {
		t.Fatalf("system_transparent snapshot = %+v, %v", system, err)
	}
	if runtime.Environments() == nil || runtime.CaptureAssignments() == nil ||
		runtime.ProxyHandler() == nil || runtime.ExchangeExecutor() == nil ||
		runtime.LocalRootIdentity().Valid() == false ||
		runtime.LocalRootCertificate().Valid() == false {
		t.Fatal("production composition is incomplete")
	}
	wire, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if string(wire) == "" || !json.Valid(wire) {
		t.Fatalf("status JSON = %q", wire)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(wire, &fields); err != nil {
		t.Fatal(err)
	}
	if _, exists := fields["environmentProjection"]; !exists {
		t.Fatalf("status omits Environment projection: %s", wire)
	}
	if _, exists := fields["accessProjection"]; exists {
		t.Fatalf("status retained the removed projection field: %s", wire)
	}

	shutdownRuntime(t, runtime)
	stopped := runtime.Status()
	if stopped.State != RuntimeStateStopped || stopped.StoppedAt == nil ||
		stopped.StopReasonCode != "" {
		t.Fatalf("stopped status = %+v", stopped)
	}
}

func TestProductRuntimePublishesEnvironmentAndRestoresCaptureAssignment(t *testing.T) {
	t.Parallel()

	paths, err := NewRuntimePaths(filepath.Join(t.TempDir(), "runtime-data"))
	if err != nil {
		t.Fatal(err)
	}
	first := startTestRuntime(t, testOptionsWithPaths(
		t, paths, hostcontract.Desktop(), &coordinatorDouble{},
	))
	aggregate := passthroughEnvironment(
		t,
		"work",
		"https://api.anthropic.com:443",
		environment.ClientProtocolAnthropicMessages,
	)
	draft, err := first.Environments().SaveDraft(context.Background(), environment.DraftCommand{
		ExpectedBaseRevision: 0,
		Candidate:            aggregate,
	})
	if err != nil {
		t.Fatalf("save Environment draft: %v", err)
	}
	preview, err := first.Environments().Preview(context.Background(), aggregate.ID, draft.Revision)
	if err != nil {
		t.Fatalf("preview Environment: %v", err)
	}
	result, err := first.Environments().Publish(context.Background(), preview)
	if err != nil || result.Outcome != environment.CommitOutcomeCommitted {
		t.Fatalf("publish Environment result=%+v err=%v", result, err)
	}
	snapshot, err := first.EnvironmentResolver().Resolve(aggregate.ID)
	if err != nil || snapshot.Revision() != 1 || snapshot.Name() != "Work" {
		t.Fatalf("published Environment = %+v, %v", snapshot, err)
	}
	capture, err := captureidentity.New(captureidentity.KindManagedRun, "capture-work")
	if err != nil {
		t.Fatal(err)
	}
	assignment, err := first.CaptureAssignments().Create(context.Background(), captureassignment.CreateCommand{
		Capture: capture, EnvironmentID: aggregate.ID, Source: captureassignment.SourceLaunch,
	})
	if err != nil || assignment.Revision != 1 || assignment.EnvironmentID != aggregate.ID {
		t.Fatalf("create Capture assignment = %+v, %v", assignment, err)
	}
	shutdownRuntime(t, first)

	second := startTestRuntime(t, testOptionsWithPaths(
		t, paths, hostcontract.Desktop(), &coordinatorDouble{},
	))
	defer shutdownRuntime(t, second)
	recovered, err := second.EnvironmentResolver().Resolve(aggregate.ID)
	if err != nil || recovered.Digest() != snapshot.Digest() || recovered.Revision() != snapshot.Revision() {
		t.Fatalf("recovered Environment = %+v, %v", recovered, err)
	}
	recoveredAssignment, err := second.CaptureAssignments().Resolve(context.Background(), capture)
	if err != nil || recoveredAssignment != assignment {
		t.Fatalf("recovered Capture assignment = %+v, %v", recoveredAssignment, err)
	}
}

func TestProductRuntimeEnvironmentRecoveryFailureRollsBackSQLite(t *testing.T) {
	t.Parallel()

	startupCause := errors.New("Environment recovery failed")
	options := testOptions(t, hostcontract.Desktop(), &coordinatorDouble{})
	builders := productionBuilders()
	builders.environment = failingEnvironmentBuilder{err: startupCause}

	runtime, err := startWithBuilders(context.Background(), options, builders)
	if runtime != nil {
		t.Fatal("failed Environment recovery returned a runtime")
	}
	if !errors.Is(err, startupCause) {
		t.Fatalf("Environment recovery cause was not preserved: %v", err)
	}
	reopened, openErr := runtimepersistence.Open(context.Background(), runtimepersistence.Options{
		DatabasePath:           options.Paths.DatabasePath(),
		BusyTimeout:            runtimepersistence.DefaultBusyTimeout,
		CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout,
	})
	if openErr != nil {
		t.Fatalf("reopen SQLite after rollback: %v", openErr)
	}
	if closeErr := reopened.Shutdown(context.Background()); closeErr != nil {
		t.Fatalf("close reopened SQLite: %v", closeErr)
	}
}

func TestProductRuntimeStatusDegradesWithUnavailableEnvironmentProjection(t *testing.T) {
	t.Parallel()

	options := testOptions(t, hostcontract.Desktop(), &coordinatorDouble{})
	builders := productionBuilders()
	builders.environment = unhealthyEnvironmentBuilder{delegate: builders.environment}
	runtime, err := startWithBuilders(context.Background(), options, builders)
	if err != nil {
		t.Fatalf("start runtime: %v", err)
	}
	defer shutdownRuntime(t, runtime)

	health := runtime.EnvironmentProjectionHealth()
	if health.State != environment.ProjectionStateUnavailable ||
		len(health.UnavailableEnvironments) != 1 ||
		health.UnavailableEnvironments[0] != "unavailable-fixture" {
		t.Fatalf("Environment health = %+v", health)
	}
	status := runtime.Status()
	if status.State != RuntimeStateDegraded || status.EnvironmentProjection.State != health.State {
		t.Fatalf("runtime status = %+v", status)
	}
}

func TestProductRuntimeShutdownIsIdempotent(t *testing.T) {
	t.Parallel()

	coordinator := &coordinatorDouble{}
	runtime := startTestRuntime(t, testOptions(t, hostcontract.Desktop(), coordinator))
	const callers = 12
	results := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			results <- runtime.Shutdown(ctx)
		}()
	}
	wait.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("concurrent Shutdown() = %v", err)
		}
	}
	if coordinator.beginShutdownCount() != 1 || coordinator.drainCount() != 1 {
		t.Fatalf("offline cleanup counts = begin:%d drain:%d", coordinator.beginShutdownCount(), coordinator.drainCount())
	}
	if _, err := runtime.CaptureAssignments().Create(context.Background(), captureassignment.CreateCommand{}); !errors.Is(err, captureassignment.ErrRuntimeStopping) {
		t.Fatalf("stopped Capture assignment admission error = %v", err)
	}
}

func TestProductRuntimeCreatesDistinctIncarnationsAcrossStarts(t *testing.T) {
	t.Parallel()

	paths, err := NewRuntimePaths(filepath.Join(t.TempDir(), "runtime-data"))
	if err != nil {
		t.Fatal(err)
	}
	first := startTestRuntime(t, testOptionsWithPaths(t, paths, hostcontract.Desktop(), &coordinatorDouble{}))
	firstStatus := first.Status()
	firstState, err := first.SchemaStateReader().ReadSchemaState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	shutdownRuntime(t, first)
	second := startTestRuntime(t, testOptionsWithPaths(t, paths, hostcontract.Desktop(), &coordinatorDouble{}))
	secondStatus := second.Status()
	secondState, err := second.SchemaStateReader().ReadSchemaState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	shutdownRuntime(t, second)
	if firstStatus.InstanceID == secondStatus.InstanceID {
		t.Fatalf("runtime incarnation was reused: %q", firstStatus.InstanceID)
	}
	if firstState != secondState {
		t.Fatalf("schema continuity = first:%+v second:%+v", firstState, secondState)
	}
}

func TestProductRuntimeCurrentSliceAcceptsBothHostContracts(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		host hostcontract.Contract
		kind hostcontract.Kind
	}{
		{name: "Desktop", host: hostcontract.Desktop(), kind: hostcontract.KindDesktop},
		{name: "Server", host: hostcontract.Server(), kind: hostcontract.KindServer},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runtime := startTestRuntime(t, testOptions(t, test.host, &coordinatorDouble{}))
			defer shutdownRuntime(t, runtime)
			if got := runtime.Status().Host; got != test.kind {
				t.Fatalf("host = %q, want %q", got, test.kind)
			}
		})
	}
}

func TestProductRuntimeRequiresOfflineHoldBeforeOpeningResources(t *testing.T) {
	t.Parallel()

	options := testOptions(t, hostcontract.Desktop(), &coordinatorDouble{})
	options.OfflineHold = nil
	runtime, err := Start(context.Background(), options)
	if runtime != nil || !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("Start() = %+v, %v", runtime, err)
	}
	if _, statErr := os.Stat(options.Paths.DatabasePath()); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("invalid options created database: %v", statErr)
	}
}

func TestProductRuntimeRejectsCorruptSQLiteOnRestart(t *testing.T) {
	t.Parallel()

	paths, err := NewRuntimePaths(filepath.Join(t.TempDir(), "runtime-data"))
	if err != nil {
		t.Fatal(err)
	}
	first := startTestRuntime(t, testOptionsWithPaths(t, paths, hostcontract.Desktop(), &coordinatorDouble{}))
	shutdownRuntime(t, first)
	for _, artifact := range []string{paths.DatabasePath() + "-wal", paths.DatabasePath() + "-shm"} {
		if err := os.Remove(artifact); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(paths.DatabasePath(), make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := Start(context.Background(), testOptionsWithPaths(t, paths, hostcontract.Desktop(), &coordinatorDouble{}))
	if second != nil || err == nil {
		t.Fatalf("corrupt SQLite Start() = %+v, %v", second, err)
	}
}

func TestProductRuntimeAppliesInternalShutdownDeadline(t *testing.T) {
	t.Parallel()

	coordinator := &coordinatorDouble{blockDrainUntilCancellation: true}
	options := testOptions(t, hostcontract.Desktop(), coordinator)
	options.Lifecycle.ShutdownTimeout = 40 * time.Millisecond
	runtime := startTestRuntime(t, options)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	err := runtime.Shutdown(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown() = %v", err)
	}
	if elapsed := time.Since(started); elapsed >= 500*time.Millisecond {
		t.Fatalf("bounded shutdown took %v", elapsed)
	}
	status := runtime.Status()
	if status.State != RuntimeStateStopFailed || status.StopReasonCode != StopReasonShutdownFailed || status.StoppedAt != nil {
		t.Fatalf("failed shutdown status = %+v", status)
	}
}

func testOptions(t *testing.T, host hostcontract.Contract, coordinator offlinehold.RuntimeCoordinator) Options {
	t.Helper()
	paths, err := NewRuntimePaths(filepath.Join(t.TempDir(), "runtime-data"))
	if err != nil {
		t.Fatal(err)
	}
	return testOptionsWithPaths(t, paths, host, coordinator)
}

func testOptionsWithPaths(t *testing.T, paths RuntimePaths, host hostcontract.Contract, coordinator offlinehold.RuntimeCoordinator) Options {
	t.Helper()
	return Options{
		Paths: paths, Host: host, OfflineHold: coordinator,
		Secrets: unavailableSecretStore{}, Approvals: toolapproval.DefaultConfig(),
		ExchangeHold: exchange.DefaultHoldPolicy(), Clock: SystemClock{},
		InstanceIDs: NewCryptographicInstanceIDSource(), SecurityRandom: rand.Reader,
		Lifecycle: LifecycleOptions{
			RollbackTimeout: time.Second, ShutdownTimeout: 5 * time.Second,
			HealthPollInterval: 10 * time.Millisecond,
		},
	}
}

func startTestRuntime(t *testing.T, options Options) *Runtime {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	runtime, err := Start(ctx, options)
	if err != nil {
		t.Fatalf("start ProductRuntime: %v", err)
	}
	return runtime
}

func shutdownRuntime(t *testing.T, runtime *Runtime) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runtime.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown ProductRuntime: %v", err)
	}
}

func passthroughEnvironment(t *testing.T, id, rawOrigin string, protocol environment.ClientProtocol) environment.Environment {
	t.Helper()
	clientOrigin, err := originidentity.ParseClientOrigin(rawOrigin)
	if err != nil {
		t.Fatal(err)
	}
	providerOrigin, err := originidentity.ParseProviderOrigin(rawOrigin)
	if err != nil {
		t.Fatal(err)
	}
	capabilities := []protocolspec.ProviderCapability{
		protocolspec.ProviderCapabilityMessages,
		protocolspec.ProviderCapabilityStreaming,
		protocolspec.ProviderCapabilityToolCalls,
	}
	return environment.Environment{
		ID: environment.EnvironmentID(id), Name: "Work", State: environment.StateActive, Revision: 1,
		ContentRecording: environment.DefaultContentRecordingPolicy(),
		ClientEndpoints: []environment.ClientEndpoint{{
			ID: environment.ClientEndpointID("endpoint." + id), Revision: 1, ClientOrigin: clientOrigin,
			ProtocolPlans: []environment.ClientProtocolPlan{{
				ID: environment.ClientProtocolPlanID("plan." + id), Revision: 1, ClientProtocol: protocol,
				ClientAdapterPolicy: environment.ClientAdapterPolicy{ID: "adapter." + id, Revision: 1},
				Mode:                environment.PlanModeOriginalPassthrough,
				UpstreamPlan: environment.UpstreamPlan{
					DefaultRouteID: environment.UpstreamRouteID("route." + id),
					RouteSet:       environment.RouteSet{ID: "routes." + id, Revision: 1, CandidateRouteIDs: []environment.UpstreamRouteID{"route." + environment.UpstreamRouteID(id)}},
					Routes: []environment.UpstreamRoute{{
						ID: "route." + environment.UpstreamRouteID(id), Revision: 1,
						ProviderTarget: environment.ProviderTarget{
							ID: "target." + id, Revision: 1, Origin: providerOrigin,
							RealmID: "realm." + id, Capabilities: capabilities,
						},
						BackendProtocol: string(protocol),
						AccountPolicy:   environment.RouteAccountPolicy{Revision: 1, Mode: environment.AccountModeClientPassthrough, FailoverPolicy: environment.FailoverOff},
						ModelPolicy:     environment.ModelPolicy{Revision: 1, Mode: "preserve"},
						WireProfileRef:  wireprofile.UpstreamWireProfileFollowClientValue,
					}},
				},
			}},
		}},
	}
}

type failingEnvironmentBuilder struct{ err error }

func (builder failingEnvironmentBuilder) Build(context.Context, environmentBuildRequest) (environmentBuildResult, error) {
	return environmentBuildResult{}, builder.err
}

type unhealthyEnvironmentBuilder struct{ delegate environmentBuilder }

func (builder unhealthyEnvironmentBuilder) Build(ctx context.Context, request environmentBuildRequest) (environmentBuildResult, error) {
	result, err := builder.delegate.Build(ctx, request)
	if err != nil {
		return environmentBuildResult{}, err
	}
	result.environments = unhealthyEnvironmentRuntime{environmentRuntime: result.environments}
	return result, nil
}

type unhealthyEnvironmentRuntime struct{ environmentRuntime }

func (unhealthyEnvironmentRuntime) Health() environment.ProjectionHealth {
	return environment.ProjectionHealth{
		State:                   environment.ProjectionStateUnavailable,
		UnavailableEnvironments: []environment.EnvironmentID{"unavailable-fixture"},
	}
}

type coordinatorDouble struct {
	mu                          sync.Mutex
	instanceID                  string
	startErr                    error
	drainErr                    error
	beginCount                  int
	drains                      int
	blockDrainUntilCancellation bool
	state                       offlinehold.State
	revision                    uint64
}

func (coordinator *coordinatorDouble) Start(_ context.Context, binding offlinehold.RuntimeBinding) error {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	coordinator.instanceID = binding.InstanceID
	coordinator.state = offlinehold.StateOnline
	coordinator.revision++
	return coordinator.startErr
}

func (*coordinatorDouble) Acquire(context.Context, offlinehold.AcquireRequest) (offlinehold.Lease, error) {
	return nil, errAcquireNotExpected
}

func (*coordinatorDouble) BeginAction(context.Context, offlinehold.ActionRequest) (*offlinehold.ActionLease, error) {
	return &offlinehold.ActionLease{}, nil
}

func (coordinator *coordinatorDouble) BeginShutdown() {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	coordinator.beginCount++
	coordinator.state = offlinehold.StateStopping
	coordinator.revision++
}

func (coordinator *coordinatorDouble) Snapshot() offlinehold.Snapshot {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return offlinehold.Snapshot{
		State: coordinator.state, Revision: coordinator.revision,
		ActiveByKind: map[offlinehold.EgressKind]int{}, QueuedByKind: map[offlinehold.EgressKind]int{},
	}
}

func (*coordinatorDouble) PendingProbeTargets() []offlinehold.ProbeTarget { return nil }
func (coordinator *coordinatorDouble) Enter(context.Context, uint64) (offlinehold.Snapshot, error) {
	return coordinator.Snapshot(), errors.New("offline control is not expected")
}
func (coordinator *coordinatorDouble) Resume(context.Context, uint64, offlinehold.ResumeRequest, offlinehold.Prober) (offlinehold.Snapshot, error) {
	return coordinator.Snapshot(), errors.New("offline control is not expected")
}
func (coordinator *coordinatorDouble) Drain(ctx context.Context) error {
	coordinator.mu.Lock()
	coordinator.drains++
	block := coordinator.blockDrainUntilCancellation
	err := coordinator.drainErr
	coordinator.mu.Unlock()
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	return err
}
func (coordinator *coordinatorDouble) boundInstanceID() string {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return coordinator.instanceID
}
func (coordinator *coordinatorDouble) beginShutdownCount() int {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return coordinator.beginCount
}
func (coordinator *coordinatorDouble) drainCount() int {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return coordinator.drains
}

type unavailableSecretStore struct{}

func (unavailableSecretStore) Read(context.Context, secretstore.Reference) (*secretstore.Value, error) {
	return nil, secretstore.ErrNotFound
}
func (unavailableSecretStore) ReadAtRevision(
	context.Context,
	secretstore.Reference,
	secretstore.Revision,
) (*secretstore.Value, error) {
	return nil, secretstore.ErrNotFound
}
func (unavailableSecretStore) Inspect(context.Context, secretstore.Reference) (secretstore.Metadata, error) {
	return secretstore.Metadata{State: secretstore.StateMissing}, nil
}
func (unavailableSecretStore) Replace(context.Context, secretstore.ReplaceCommand) (secretstore.Metadata, error) {
	return secretstore.Metadata{}, secretstore.ErrReadOnly
}
func (unavailableSecretStore) Delete(context.Context, secretstore.Reference) error {
	return secretstore.ErrNotFound
}
