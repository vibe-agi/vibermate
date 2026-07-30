package productruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

var errAcquireNotExpected = errors.New("egress acquisition is not expected in M0 tests")

func TestProductRuntimeStartsAndShutsDownNormally(t *testing.T) {
	t.Parallel()

	coordinator := &coordinatorDouble{}
	options := testOptions(t, hostcontract.Desktop(), coordinator)

	runtime := startTestRuntime(t, options)
	status := runtime.Status()
	if status.State != RuntimeStateInitialized {
		t.Fatalf("runtime is not initialized: %+v", status)
	}
	if status.InstanceID == "" {
		t.Fatal("runtime instance ID is empty")
	}
	if status.Host != hostcontract.KindDesktop {
		t.Fatalf("runtime host = %q, want desktop", status.Host)
	}
	if status.SchemaRevision != 5 {
		t.Fatalf("schema revision = %d, want 5", status.SchemaRevision)
	}
	if status.AccessProjection.State != access.ProjectionStateHealthy ||
		status.AccessProjection.UnavailableAccessCount != 0 {
		t.Fatalf("initial Access projection health = %+v", status.AccessProjection)
	}
	if runtime.AccessProjectionHealth() != status.AccessProjection {
		t.Fatalf(
			"runtime Access projection health=%+v status=%+v",
			runtime.AccessProjectionHealth(),
			status.AccessProjection,
		)
	}
	if coordinator.boundInstanceID() != status.InstanceID {
		t.Fatalf(
			"coordinator instance ID = %q, runtime instance ID = %q",
			coordinator.boundInstanceID(),
			status.InstanceID,
		)
	}
	wire, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal runtime status: %v", err)
	}
	var wireStatus map[string]json.RawMessage
	if err := json.Unmarshal(wire, &wireStatus); err != nil {
		t.Fatalf("unmarshal runtime status: %v", err)
	}
	if _, exists := wireStatus["instanceId"]; !exists {
		t.Fatalf("runtime status does not use instanceId: %s", wire)
	}
	if _, exists := wireStatus["ready"]; exists {
		t.Fatalf("foundation status exposes product readiness: %s", wire)
	}
	if _, exists := wireStatus["accessProjection"]; !exists {
		t.Fatalf("runtime status omits Access projection health: %s", wire)
	}

	schemaState, err := runtime.SchemaStateReader().ReadSchemaState(context.Background())
	if err != nil {
		t.Fatalf("read runtime schema state: %v", err)
	}
	if schemaState.Revision != status.SchemaRevision {
		t.Fatalf(
			"schema state revision = %d, status revision = %d",
			schemaState.Revision,
			status.SchemaRevision,
		)
	}

	shutdownRuntime(t, runtime)
	stopped := runtime.Status()
	if stopped.State != RuntimeStateStopped {
		t.Fatalf("runtime did not stop: %+v", stopped)
	}
	if stopped.StoppedAt == nil {
		t.Fatal("runtime stopped timestamp is missing")
	}
	if stopped.StopReasonCode != "" {
		t.Fatalf("successful shutdown has a stop reason: %+v", stopped)
	}
	if _, err := runtime.SchemaStateReader().ReadSchemaState(context.Background()); !errors.Is(
		err,
		runtimepersistence.ErrStoreClosing,
	) {
		t.Fatalf("stopped runtime accepted a new schema read: %v", err)
	}
	if coordinator.beginShutdownCount() != 1 || coordinator.drainCount() != 1 {
		t.Fatalf(
			"offline shutdown counts = begin:%d drain:%d",
			coordinator.beginShutdownCount(),
			coordinator.drainCount(),
		)
	}
}

func TestProductRuntimeWiresAccessCASAndRestoresItAcrossRestart(t *testing.T) {
	t.Parallel()

	dataDirectory := filepath.Join(t.TempDir(), "runtime-data")
	paths, err := NewRuntimePaths(dataDirectory)
	if err != nil {
		t.Fatalf("create runtime paths: %v", err)
	}
	accessID, err := access.NewAccessID("access-runtime")
	if err != nil {
		t.Fatalf("create Access ID: %v", err)
	}

	first := startTestRuntime(t, testOptionsWithPaths(
		t,
		paths,
		hostcontract.Desktop(),
		&coordinatorDouble{},
	))
	if _, err := first.SnapshotResolver().ResolveAccess(accessID); !errors.Is(
		err,
		access.ErrAccessNotConfigured,
	) {
		t.Fatalf("empty runtime resolved an Access: %v", err)
	}
	write, err := first.AccessWriter().WriteAccess(
		context.Background(),
		access.WriteCommand{
			ExpectedRevision: 0,
			Aggregate: runtimeAccessAggregate(
				t,
				accessID,
				1,
				"Runtime Access",
			),
		},
	)
	if err != nil || write.Outcome != access.WriteOutcomeCommitted {
		t.Fatalf("write runtime Access result=%+v err=%v", write, err)
	}
	shutdownRuntime(t, first)
	if _, err := first.AccessWriter().WriteAccess(
		context.Background(),
		access.WriteCommand{
			ExpectedRevision: 1,
			Aggregate: runtimeAccessAggregate(
				t,
				accessID,
				2,
				"Rejected after shutdown",
			),
		},
	); !errors.Is(err, access.ErrAccessRuntimeStopping) {
		t.Fatalf("stopped runtime accepted an Access write: %v", err)
	}

	second := startTestRuntime(t, testOptionsWithPaths(
		t,
		paths,
		hostcontract.Desktop(),
		&coordinatorDouble{},
	))
	defer shutdownRuntime(t, second)
	recovered, err := second.SnapshotResolver().ResolveAccess(accessID)
	if err != nil {
		t.Fatalf("resolve recovered runtime Access: %v", err)
	}
	if recovered.Revision() != 1 ||
		recovered.Binding().Name != "Runtime Access" {
		t.Fatalf("recovered runtime Access: revision=%d binding=%+v",
			recovered.Revision(), recovered.Binding())
	}
	if second.Status().State != RuntimeStateInitialized {
		t.Fatalf("runtime with recovered Access is not initialized: %+v", second.Status())
	}
}

func TestProductRuntimeStatusDegradesWhenAccessProjectionIsUnavailable(t *testing.T) {
	t.Parallel()

	options := testOptions(
		t,
		hostcontract.Desktop(),
		&coordinatorDouble{},
	)
	builders := productionBuilders()
	builders.access = failingPublicationAccessBuilder{
		failRevision: 2,
	}

	runtime, err := startWithBuilders(context.Background(), options, builders)
	if err != nil {
		t.Fatalf("start runtime with publication failure fixture: %v", err)
	}
	defer shutdownRuntime(t, runtime)

	accessID, err := access.NewAccessID("access-runtime-health")
	if err != nil {
		t.Fatalf("construct Access ID: %v", err)
	}
	if _, err := runtime.AccessWriter().WriteAccess(
		context.Background(),
		access.WriteCommand{
			ExpectedRevision: 0,
			Aggregate: runtimeAccessAggregate(
				t,
				accessID,
				1,
				"Revision one",
			),
		},
	); err != nil {
		t.Fatalf("create Access before publication failure: %v", err)
	}
	result, err := runtime.AccessWriter().WriteAccess(
		context.Background(),
		access.WriteCommand{
			ExpectedRevision: 1,
			Aggregate: runtimeAccessAggregate(
				t,
				accessID,
				2,
				"Revision two",
			),
		},
	)
	if result.Outcome != access.WriteOutcomeCommitted ||
		!errors.Is(err, access.ErrProjectionUnavailable) {
		t.Fatalf("runtime publication failure result=%+v err=%v", result, err)
	}

	health := runtime.AccessProjectionHealth()
	if health.State != access.ProjectionStateUnavailable ||
		health.UnavailableAccessCount != 1 {
		t.Fatalf("runtime Access projection health = %+v", health)
	}
	status := runtime.Status()
	if status.State != RuntimeStateDegraded ||
		status.AccessProjection != health {
		t.Fatalf("runtime did not observe Access projection health: %+v", status)
	}
}

func TestProductRuntimeShutdownIsIdempotent(t *testing.T) {
	t.Parallel()

	coordinator := &coordinatorDouble{}
	runtime := startTestRuntime(
		t,
		testOptions(t, hostcontract.Desktop(), coordinator),
	)

	const callers = 12
	results := make(chan error, callers)
	var callersWaitGroup sync.WaitGroup
	for range callers {
		callersWaitGroup.Add(1)
		go func() {
			defer callersWaitGroup.Done()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			results <- runtime.Shutdown(ctx)
		}()
	}
	callersWaitGroup.Wait()
	close(results)
	for result := range results {
		if result != nil {
			t.Fatalf("concurrent shutdown returned an error: %v", result)
		}
	}
	if coordinator.beginShutdownCount() != 1 || coordinator.drainCount() != 1 {
		t.Fatalf(
			"offline cleanup ran more than once: begin=%d drain=%d",
			coordinator.beginShutdownCount(),
			coordinator.drainCount(),
		)
	}
}

func TestProductRuntimeAccessRecoveryFailureRollsBackSQLite(t *testing.T) {
	t.Parallel()

	startupCause := errors.New("Access recovery failed")
	var events eventLog
	options := testOptions(
		t,
		hostcontract.Desktop(),
		&coordinatorDouble{},
	)
	builders := productionBuilders()
	builders.storage = tracingStorageBuilder{
		delegate: builders.storage,
		events:   &events,
	}
	builders.access = failingAccessBuilder{err: startupCause}

	runtime, err := startWithBuilders(context.Background(), options, builders)
	if runtime != nil {
		t.Fatal("failed Access recovery returned a runtime")
	}
	if !errors.Is(err, startupCause) {
		t.Fatalf("Access recovery cause was not preserved: %v", err)
	}
	if got, want := events.snapshot(), []string{"sqlite.shutdown"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Access recovery rollback order = %v, want %v", got, want)
	}

	reopened, openErr := runtimepersistence.Open(
		context.Background(),
		runtimepersistence.Options{
			DatabasePath:           options.Paths.DatabasePath(),
			BusyTimeout:            runtimepersistence.DefaultBusyTimeout,
			CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout,
		},
	)
	if openErr != nil {
		t.Fatalf("reopen SQLite after Access recovery rollback: %v", openErr)
	}
	if closeErr := reopened.Shutdown(context.Background()); closeErr != nil {
		t.Fatalf("close reopened SQLite store: %v", closeErr)
	}
}

func TestProductRuntimeCreatesDistinctIncarnationsAcrossStarts(t *testing.T) {
	t.Parallel()

	dataDirectory := filepath.Join(t.TempDir(), "runtime-data")
	paths, err := NewRuntimePaths(dataDirectory)
	if err != nil {
		t.Fatalf("create runtime paths: %v", err)
	}

	firstOptions := testOptionsWithPaths(
		t,
		paths,
		hostcontract.Desktop(),
		&coordinatorDouble{},
	)
	first := startTestRuntime(t, firstOptions)
	firstStatus := first.Status()
	firstState, err := first.SchemaStateReader().ReadSchemaState(context.Background())
	if err != nil {
		t.Fatalf("read first schema state: %v", err)
	}
	shutdownRuntime(t, first)

	secondOptions := testOptionsWithPaths(
		t,
		paths,
		hostcontract.Desktop(),
		&coordinatorDouble{},
	)
	second := startTestRuntime(t, secondOptions)
	secondStatus := second.Status()
	secondState, err := second.SchemaStateReader().ReadSchemaState(context.Background())
	if err != nil {
		t.Fatalf("read second schema state: %v", err)
	}
	shutdownRuntime(t, second)

	if firstStatus.InstanceID == secondStatus.InstanceID {
		t.Fatalf("runtime incarnation was reused: %q", firstStatus.InstanceID)
	}
	if firstState != secondState {
		t.Fatalf(
			"schema state did not remain continuous: first=%+v second=%+v",
			firstState,
			secondState,
		)
	}
}

func TestProductRuntimeCurrentSliceAcceptsBothHostContracts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		host hostcontract.Contract
		kind hostcontract.Kind
	}{
		{name: "Desktop", host: hostcontract.Desktop(), kind: hostcontract.KindDesktop},
		{name: "Server", host: hostcontract.Server(), kind: hostcontract.KindServer},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runtime := startTestRuntime(
				t,
				testOptions(t, test.host, &coordinatorDouble{}),
			)
			defer shutdownRuntime(t, runtime)
			if got := runtime.Status().Host; got != test.kind {
				t.Fatalf("runtime host = %q, want %q", got, test.kind)
			}
		})
	}
}

func TestProductRuntimeRequiresOfflineHoldCoordinatorBeforeOpeningResources(t *testing.T) {
	t.Parallel()

	options := testOptions(t, hostcontract.Desktop(), &coordinatorDouble{})
	options.OfflineHold = nil

	runtime, err := Start(context.Background(), options)
	if runtime != nil {
		t.Fatal("invalid options returned a runtime")
	}
	if !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("expected ErrInvalidOptions, got %v", err)
	}
	if _, statErr := os.Stat(options.Paths.DatabasePath()); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("invalid options created a database: %v", statErr)
	}
}

func TestProductRuntimeMiddleStageFailureRollsBackInReverseOrder(t *testing.T) {
	t.Parallel()

	startupCause := errors.New("offline binding failed")
	var events eventLog
	coordinator := &coordinatorDouble{startErr: startupCause}
	options := testOptions(t, hostcontract.Desktop(), coordinator)

	builders := productionBuilders()
	builders.storage = tracingStorageBuilder{
		delegate: builders.storage,
		events:   &events,
	}
	builders.access = tracingAccessBuilder{
		delegate: builders.access,
		events:   &events,
	}
	builders.monitor = fixedMonitorBuilder{
		component: &ownedComponentDouble{
			events: &events,
			event:  "monitor.shutdown",
		},
	}

	runtime, err := startWithBuilders(context.Background(), options, builders)
	if runtime != nil {
		t.Fatal("failed start returned a runtime")
	}
	if !errors.Is(err, startupCause) {
		t.Fatalf("startup cause was not preserved: %v", err)
	}
	if got, want := events.snapshot(), []string{
		"monitor.shutdown",
		"access.shutdown",
		"sqlite.shutdown",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rollback order = %v, want %v", got, want)
	}
	if coordinator.beginShutdownCount() != 0 || coordinator.drainCount() != 0 {
		t.Fatal("cleanup was registered for the failed offline-hold stage")
	}

	reopened, openErr := runtimepersistence.Open(context.Background(), runtimepersistence.Options{
		DatabasePath:           options.Paths.DatabasePath(),
		BusyTimeout:            runtimepersistence.DefaultBusyTimeout,
		CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout,
	})
	if openErr != nil {
		t.Fatalf("reopen rolled-back SQLite store: %v", openErr)
	}
	if closeErr := reopened.Shutdown(context.Background()); closeErr != nil {
		t.Fatalf("close reopened SQLite store: %v", closeErr)
	}
}

func TestProductRuntimeRollbackErrorDoesNotOverrideStartupCause(t *testing.T) {
	t.Parallel()

	startupCause := errors.New("foundation state verification failed")
	rollbackCause := errors.New("monitor drain failed")
	var events eventLog
	coordinator := &coordinatorDouble{events: &events}
	options := testOptions(t, hostcontract.Desktop(), coordinator)

	builders := productionBuilders()
	builders.storage = tracingStorageBuilder{
		delegate: builders.storage,
		events:   &events,
		readErr:  startupCause,
	}
	builders.monitor = fixedMonitorBuilder{
		component: &ownedComponentDouble{
			events: &events,
			event:  "monitor.shutdown",
			err:    rollbackCause,
		},
	}

	runtime, err := startWithBuilders(context.Background(), options, builders)
	if runtime != nil {
		t.Fatal("failed start returned a runtime")
	}
	if !errors.Is(err, startupCause) {
		t.Fatalf("startup cause was not preserved: %v", err)
	}
	if !errors.Is(err, rollbackCause) {
		t.Fatalf("rollback cause was not joined: %v", err)
	}
	wantEvents := []string{
		"offline.begin-shutdown",
		"offline.drain",
		"monitor.shutdown",
		"sqlite.shutdown",
	}
	if got := events.snapshot(); !reflect.DeepEqual(got, wantEvents) {
		t.Fatalf("rollback order = %v, want %v", got, wantEvents)
	}
}

func TestProductRuntimeRejectsCorruptSQLiteOnRestart(t *testing.T) {
	t.Parallel()

	paths, err := NewRuntimePaths(filepath.Join(t.TempDir(), "runtime-data"))
	if err != nil {
		t.Fatalf("create runtime paths: %v", err)
	}
	first := startTestRuntime(t, testOptionsWithPaths(
		t,
		paths,
		hostcontract.Desktop(),
		&coordinatorDouble{},
	))
	shutdownRuntime(t, first)
	for _, artifact := range []string{
		paths.DatabasePath() + "-wal",
		paths.DatabasePath() + "-shm",
	} {
		if err := os.Remove(artifact); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("remove SQLite corruption fixture artifact: %v", err)
		}
	}
	if err := os.WriteFile(
		paths.DatabasePath(),
		bytes.Repeat([]byte{0xa5}, 4096),
		0o600,
	); err != nil {
		t.Fatalf("create corrupt SQLite fixture: %v", err)
	}

	second, err := Start(context.Background(), testOptionsWithPaths(
		t,
		paths,
		hostcontract.Desktop(),
		&coordinatorDouble{},
	))
	if second != nil {
		t.Fatal("corrupt SQLite returned a runtime")
	}
	if err == nil {
		t.Fatal("corrupt SQLite startup returned no error")
	}
}

func TestProductRuntimeStartupRollbackUsesInternalDeadline(t *testing.T) {
	t.Parallel()

	startupCause := errors.New("foundation state verification failed")
	coordinator := &coordinatorDouble{blockDrainUntilCancellation: true}
	options := testOptions(t, hostcontract.Desktop(), coordinator)
	options.Lifecycle.RollbackTimeout = 40 * time.Millisecond
	builders := productionBuilders()
	builders.storage = tracingStorageBuilder{
		delegate: builders.storage,
		readErr:  startupCause,
	}

	started := time.Now()
	runtime, err := startWithBuilders(context.Background(), options, builders)
	if runtime != nil {
		t.Fatal("failed start returned a runtime")
	}
	if !errors.Is(err, startupCause) {
		t.Fatalf("startup cause was not preserved: %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("rollback deadline was not reported: %v", err)
	}
	if elapsed := time.Since(started); elapsed >= 500*time.Millisecond {
		t.Fatalf("bounded startup rollback took too long: %v", elapsed)
	}
}

func TestProductRuntimeShutdownDrainsOwnedGoroutineWithinDeadline(t *testing.T) {
	t.Parallel()

	options := testOptions(
		t,
		hostcontract.Desktop(),
		&coordinatorDouble{},
	)
	options.Lifecycle.HealthPollInterval = time.Millisecond
	runtime := startTestRuntime(t, options)
	monitor, ok := runtime.monitor.(*storageHealthMonitor)
	if !ok {
		t.Fatalf("runtime monitor has unexpected type %T", runtime.monitor)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	if err := runtime.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown runtime: %v", err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("shutdown exceeded caller deadline: %v", elapsed)
	}
	select {
	case <-monitor.done:
	default:
		t.Fatal("shutdown returned before the owned monitor goroutine drained")
	}
}

func TestProductRuntimeAppliesInternalShutdownDeadline(t *testing.T) {
	t.Parallel()

	coordinator := &coordinatorDouble{blockDrainUntilCancellation: true}
	options := testOptions(
		t,
		hostcontract.Desktop(),
		coordinator,
	)
	options.Lifecycle.ShutdownTimeout = 40 * time.Millisecond
	runtime := startTestRuntime(t, options)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	err := runtime.Shutdown(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected internal shutdown deadline, got %v", err)
	}
	if elapsed := time.Since(started); elapsed >= 500*time.Millisecond {
		t.Fatalf("bounded shutdown took too long: %v", elapsed)
	}
	if runtime.Status().State != RuntimeStateStopFailed {
		t.Fatalf("runtime status after bounded shutdown: %+v", runtime.Status())
	}
	if runtime.Status().StopReasonCode != StopReasonShutdownFailed {
		t.Fatalf("runtime stop reason after bounded shutdown: %+v", runtime.Status())
	}
	if runtime.Status().StoppedAt != nil {
		t.Fatalf("failed shutdown reported a stopped timestamp: %+v", runtime.Status())
	}
}

func testOptions(
	t *testing.T,
	host hostcontract.Contract,
	coordinator offlinehold.Coordinator,
) Options {
	t.Helper()
	paths, err := NewRuntimePaths(filepath.Join(t.TempDir(), "runtime-data"))
	if err != nil {
		t.Fatalf("create runtime paths: %v", err)
	}
	return testOptionsWithPaths(t, paths, host, coordinator)
}

func testOptionsWithPaths(
	t *testing.T,
	paths RuntimePaths,
	host hostcontract.Contract,
	coordinator offlinehold.Coordinator,
) Options {
	t.Helper()
	return Options{
		Paths:       paths,
		Host:        host,
		OfflineHold: coordinator,
		Clock:       SystemClock{},
		InstanceIDs: NewCryptographicInstanceIDSource(),
		Lifecycle: LifecycleOptions{
			RollbackTimeout:    time.Second,
			ShutdownTimeout:    time.Second,
			HealthPollInterval: 10 * time.Millisecond,
		},
	}
}

func startTestRuntime(t *testing.T, options Options) *Runtime {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runtime, err := Start(ctx, options)
	if err != nil {
		t.Fatalf("start ProductRuntime: %v", err)
	}
	return runtime
}

func shutdownRuntime(t *testing.T, runtime *Runtime) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runtime.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown ProductRuntime: %v", err)
	}
}

type coordinatorDouble struct {
	mu                          sync.Mutex
	instanceID                  string
	startErr                    error
	drainErr                    error
	beginCount                  int
	drains                      int
	events                      *eventLog
	blockDrainUntilCancellation bool
}

func (c *coordinatorDouble) Start(_ context.Context, binding offlinehold.RuntimeBinding) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.instanceID = binding.InstanceID
	return c.startErr
}

func (c *coordinatorDouble) Acquire(
	context.Context,
	offlinehold.AcquireRequest,
) (offlinehold.Lease, error) {
	return nil, errAcquireNotExpected
}

func (c *coordinatorDouble) BeginShutdown() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.beginCount++
	if c.events != nil {
		c.events.add("offline.begin-shutdown")
	}
}

func (c *coordinatorDouble) Drain(ctx context.Context) error {
	c.mu.Lock()
	c.drains++
	block := c.blockDrainUntilCancellation
	drainErr := c.drainErr
	if c.events != nil {
		c.events.add("offline.drain")
	}
	c.mu.Unlock()
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	return drainErr
}

func (c *coordinatorDouble) boundInstanceID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.instanceID
}

func (c *coordinatorDouble) beginShutdownCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.beginCount
}

func (c *coordinatorDouble) drainCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.drains
}

type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) add(event string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

func (l *eventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

type fixedMonitorBuilder struct {
	component ownedComponent
	err       error
}

func (b fixedMonitorBuilder) Build(monitorBuildRequest) (ownedComponent, error) {
	return b.component, b.err
}

type failingAccessBuilder struct {
	err error
}

func (b failingAccessBuilder) Build(
	context.Context,
	accessBuildRequest,
) (accessRuntime, error) {
	return nil, b.err
}

type tracingAccessBuilder struct {
	delegate accessBuilder
	events   *eventLog
}

func (b tracingAccessBuilder) Build(
	ctx context.Context,
	request accessBuildRequest,
) (accessRuntime, error) {
	component, err := b.delegate.Build(ctx, request)
	if err != nil {
		return nil, err
	}
	return &tracingAccessRuntime{
		accessRuntime: component,
		events:        b.events,
	}, nil
}

type tracingAccessRuntime struct {
	accessRuntime
	events *eventLog
}

func (r *tracingAccessRuntime) Shutdown(ctx context.Context) error {
	if r.events != nil {
		r.events.add("access.shutdown")
	}
	return r.accessRuntime.Shutdown(ctx)
}

type failingPublicationAccessBuilder struct {
	failRevision access.Revision
}

func (b failingPublicationAccessBuilder) Build(
	ctx context.Context,
	request accessBuildRequest,
) (accessRuntime, error) {
	compiler, err := productionAccessPlanCompiler()
	if err != nil {
		return nil, err
	}
	projection := &failingPublicationProjection{
		SnapshotProjection: access.NewSnapshotProjection(),
		failRevision:       b.failRevision,
	}
	return access.NewManager(ctx, request.repository, compiler, projection)
}

type failingPublicationProjection struct {
	access.SnapshotProjection
	failRevision access.Revision
}

func (p *failingPublicationProjection) Publish(
	snapshot access.AccessPlanSnapshot,
) error {
	if snapshot.Revision() == p.failRevision {
		return errors.New("injected ProductRuntime Access publication failure")
	}
	return p.SnapshotProjection.Publish(snapshot)
}

func runtimeAccessAggregate(
	t *testing.T,
	accessID access.AccessID,
	revision access.Revision,
	name string,
) access.Aggregate {
	t.Helper()
	endpointID, err := access.NewAgentEndpointID(accessID.String() + "-endpoint")
	if err != nil {
		t.Fatalf("construct AgentEndpoint ID: %v", err)
	}
	profileID, err := access.NewEndpointProfileID(accessID.String() + "-profile")
	if err != nil {
		t.Fatalf("construct EndpointProfile ID: %v", err)
	}
	targetID, err := access.NewProviderTargetID(accessID.String() + "-target")
	if err != nil {
		t.Fatalf("construct ProviderTarget ID: %v", err)
	}
	accountID, err := access.NewAccountBindingID(accessID.String() + "-account")
	if err != nil {
		t.Fatalf("construct account binding ID: %v", err)
	}
	routeSetID, err := access.NewRouteSetID(accessID.String() + "-routes")
	if err != nil {
		t.Fatalf("construct RouteSet ID: %v", err)
	}
	egressID, err := access.NewEgressPolicyID(accessID.String() + "-egress")
	if err != nil {
		t.Fatalf("construct egress policy ID: %v", err)
	}
	clientOrigin, err := access.NewClientOrigin("https://api.anthropic.com:443")
	if err != nil {
		t.Fatalf("construct ClientOrigin: %v", err)
	}
	providerOrigin, err := access.NewProviderOrigin("https://api.openai.com:443/v1")
	if err != nil {
		t.Fatalf("construct ProviderOrigin: %v", err)
	}
	model, err := access.NewModelName("gpt-4.1-mini")
	if err != nil {
		t.Fatalf("construct model: %v", err)
	}
	secretRef, err := access.NewSecretRef("secret://provider/" + accessID.String())
	if err != nil {
		t.Fatalf("construct SecretRef: %v", err)
	}
	return access.Aggregate{
		Binding: access.AccessBinding{
			ID:                accessID,
			Revision:          revision,
			Name:              name,
			Description:       "ProductRuntime executable Access",
			Status:            access.AccessStatusEnabled,
			AgentEndpointID:   endpointID,
			DefaultRouteSetID: routeSetID,
			ProfileIDs:        []access.EndpointProfileID{profileID},
			EgressPolicyID:    egressID,
		},
		AgentEndpoint: access.AgentEndpoint{
			ID:            endpointID,
			Revision:      revision,
			AccessID:      accessID,
			ClientOrigin:  clientOrigin,
			ClientDialect: access.DialectAnthropicMessages,
		},
		Profiles: []access.EndpointProfile{{
			ID:                  profileID,
			Revision:            revision,
			AccessID:            accessID,
			Name:                "OpenAI Chat",
			Description:         "Fixed M0 profile",
			BackendDialect:      access.DialectOpenAIChat,
			TargetID:            targetID,
			TransportProfileRef: access.ObservedClientH1TransportProfileRef(),
			AccountBindingIDs: []access.AccountBindingID{
				accountID,
			},
			DefaultAccountBindingID: accountID,
			DefaultModelPolicy: access.ModelPolicy{
				Revision:   revision,
				Mode:       access.ModelPolicyModeFixed,
				FixedModel: model,
			},
		}},
		ProviderTargets: []access.ProviderTarget{{
			ID:        targetID,
			Revision:  revision,
			AccessID:  accessID,
			ProfileID: profileID,
			Origin:    providerOrigin,
			Protocol:  access.DialectOpenAIChat,
			Capabilities: []access.ProviderCapability{
				access.ProviderCapabilityMessages,
				access.ProviderCapabilityStreaming,
				access.ProviderCapabilityToolCalls,
			},
		}},
		AccountBindings: []access.ProviderAccountBinding{{
			ID:            accountID,
			Revision:      revision,
			AccessID:      accessID,
			ProfileID:     profileID,
			Label:         "Primary",
			SecretRef:     secretRef,
			AuthDriverRef: access.StaticHeaderAuthDriverRef(),
			Enabled:       true,
		}},
		RouteSets: []access.RouteSet{{
			ID:                  routeSetID,
			Revision:            revision,
			AccessID:            accessID,
			CandidateProfileIDs: []access.EndpointProfileID{profileID},
		}},
		EgressPolicy: access.AccessEgressPolicy{
			ID:       egressID,
			Revision: revision,
			AccessID: accessID,
			Mode:     access.EgressModeDirect,
		},
		PluginPlan: access.PluginPlan{
			Revision: revision,
			AccessID: accessID,
			Mode:     access.PluginPlanModePassThrough,
		},
	}
}

type ownedComponentDouble struct {
	events *eventLog
	event  string
	err    error
}

func (c *ownedComponentDouble) Shutdown(context.Context) error {
	if c.events != nil {
		c.events.add(c.event)
	}
	return c.err
}

type tracingStorageBuilder struct {
	delegate storageBuilder
	events   *eventLog
	readErr  error
}

func (b tracingStorageBuilder) Build(
	ctx context.Context,
	request storageBuildRequest,
) (storageBuildResult, error) {
	result, err := b.delegate.Build(ctx, request)
	if err != nil {
		return storageBuildResult{}, err
	}
	result.store = &tracingStore{
		RuntimeStore: result.store,
		events:       b.events,
		readErr:      b.readErr,
	}
	return result, nil
}

type tracingStore struct {
	runtimepersistence.RuntimeStore
	events  *eventLog
	readErr error
}

func (s *tracingStore) SchemaStateReader() runtimepersistence.SchemaStateReader {
	if s.readErr == nil {
		return s.RuntimeStore.SchemaStateReader()
	}
	return failingSchemaStateReader{err: s.readErr}
}

func (s *tracingStore) Shutdown(ctx context.Context) error {
	if s.events != nil {
		s.events.add("sqlite.shutdown")
	}
	return s.RuntimeStore.Shutdown(ctx)
}

type failingSchemaStateReader struct {
	err error
}

func (r failingSchemaStateReader) ReadSchemaState(
	context.Context,
) (runtimepersistence.SchemaState, error) {
	return runtimepersistence.SchemaState{}, r.err
}
