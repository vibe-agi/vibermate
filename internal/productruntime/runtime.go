// Package productruntime owns the single production composition root.
package productruntime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/blindtunnel"
	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/localca"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originaltransport"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
	"github.com/vibe-agi/vibermate/internal/workspacedefault"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

var ErrInvalidBuildResult = errors.New("invalid runtime build result")

// Runtime owns every successfully constructed production component.
type Runtime struct {
	status            *statusTracker
	schemaReader      runtimepersistence.SchemaStateReader
	environments      environmentRuntime
	assignments       captureAssignmentRuntime
	workspaceIdentity *workspaceidentity.Manager
	workspaceDefaults *workspacedefault.Manager
	activities        activityRuntime
	connections       connectionEventRuntime
	egress            egressaudit.Reader
	egressCompletion  *runtimeEgressRepository
	accounts          *provideraccount.Manager
	approvals         approvalRuntime
	connectionRules   *connectionpolicy.Manager
	monitor           ownedComponent
	provider          providerRuntime
	original          originalRuntime
	exchanges         exchangeRuntime
	captureRuns       captureRuntime
	manualCaptures    manualCaptureRuntime
	localCA           localCARuntime
	proxy             proxyRuntime
	offlineHold       offlinehold.RuntimeCoordinator
	resumeProber      offlinehold.Prober
	cleanups          cleanupStack
	clock             Clock
	timeout           LifecycleOptions

	shutdownOnce sync.Once
	shutdownDone chan struct{}
	shutdownErr  error
}

// Start validates all dependencies and executes the typed production builder.
func Start(ctx context.Context, options Options) (*Runtime, error) {
	return startWithBuilders(ctx, options, productionBuilders())
}

func startWithBuilders(
	ctx context.Context,
	options Options,
	builders runtimeBuilders,
) (*Runtime, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: startup context is nil", ErrInvalidOptions)
	}
	if err := options.validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("start ProductRuntime: %w", err)
	}
	if builders.storage == nil ||
		builders.environment == nil ||
		builders.activity == nil ||
		builders.connection == nil ||
		builders.approval == nil ||
		builders.monitor == nil ||
		builders.provider == nil ||
		builders.original == nil ||
		builders.exchange == nil ||
		builders.capture == nil ||
		builders.manualCapture == nil ||
		builders.localCA == nil ||
		builders.proxy == nil {
		return nil, fmt.Errorf("%w: component builder is missing", ErrInvalidBuildResult)
	}

	instanceID, err := options.InstanceIDs.NewInstanceID(ctx)
	if err != nil {
		return nil, fmt.Errorf("create runtime incarnation: %w", err)
	}
	if instanceID == "" {
		return nil, fmt.Errorf("%w: instance ID is empty", ErrInvalidBuildResult)
	}

	tracker := newStatusTracker(instanceID, options.Host.Kind(), options.Clock.Now())
	var cleanups cleanupStack
	var pending cleanupStack
	fail := func(stage string, root error) (*Runtime, error) {
		startupErr := fmt.Errorf("start ProductRuntime stage %q: %w", stage, root)
		rollbackContext, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			options.Lifecycle.RollbackTimeout,
		)
		defer cancel()
		pendingErr := pending.shutdown(rollbackContext)
		rollbackErr := cleanups.shutdown(rollbackContext)
		if pendingErr != nil || rollbackErr != nil {
			return nil, errors.Join(
				startupErr,
				wrapOptionalError("pending startup rollback", pendingErr),
				wrapOptionalError("startup rollback", rollbackErr),
			)
		}
		return nil, startupErr
	}

	storageResult, err := builders.storage.Build(ctx, storageBuildRequest{
		databasePath: options.Paths.DatabasePath(),
	})
	if err != nil {
		return fail("sqlite", err)
	}
	if storageResult.store == nil || storageResult.state.Revision <= 0 {
		incompleteErr := fmt.Errorf("%w: storage component is incomplete", ErrInvalidBuildResult)
		if storageResult.store != nil {
			closeContext, cancel := context.WithTimeout(
				context.WithoutCancel(ctx),
				options.Lifecycle.RollbackTimeout,
			)
			closeErr := storageResult.store.Shutdown(closeContext)
			cancel()
			incompleteErr = errors.Join(
				incompleteErr,
				wrapOptionalError("close incomplete storage component", closeErr),
			)
		}
		return fail("sqlite", incompleteErr)
	}
	cleanups.register("sqlite", storageResult.store.Shutdown)
	egressRepository := storageResult.store.EgressAttemptRepository()
	if egressRepository == nil {
		return fail(
			"EgressAttempt recovery",
			fmt.Errorf(
				"%w: EgressAttempt repository is nil",
				ErrInvalidBuildResult,
			),
		)
	}
	if _, err := egressRepository.Recover(
		ctx,
		options.Clock.Now().UTC(),
	); err != nil {
		return fail("EgressAttempt recovery", err)
	}

	securityRandom := newSynchronizedReader(options.SecurityRandom)
	ownerContext, cancelOwner := context.WithCancelCause(context.WithoutCancel(ctx))
	cleanups.register("runtime owner context", func(context.Context) error {
		cancelOwner(errors.New("runtime owner context stopped"))
		return nil
	})
	runtimeEgress := newRuntimeEgressRepository(
		egressRepository,
		tracker,
		ownerContext,
		options.Lifecycle,
		cancelOwner,
	)
	workspaceIdentity, err := workspaceidentity.Open(
		ctx,
		options.Paths.DataDirectory(),
		securityRandom,
		options.Clock.Now(),
	)
	if err != nil {
		return fail("machine and workspace identity", err)
	}
	cleanups.register("machine and workspace identity", workspaceIdentity.Shutdown)

	certificateAuthority, err := builders.localCA.Build(ctx, localCABuildRequest{
		ownerContext: ownerContext,
		directory:    options.Paths.LocalCADirectory(),
		clock:        options.Clock,
		random:       securityRandom,
	})
	if err != nil {
		return fail("local Root CA", err)
	}
	if certificateAuthority == nil ||
		!certificateAuthority.Identity().Valid() ||
		!certificateAuthority.Certificate().Valid() {
		incompleteErr := fmt.Errorf(
			"%w: local CA component is incomplete",
			ErrInvalidBuildResult,
		)
		if certificateAuthority != nil {
			closeContext, cancel := context.WithTimeout(
				context.WithoutCancel(ctx),
				options.Lifecycle.RollbackTimeout,
			)
			closeErr := certificateAuthority.Shutdown(closeContext)
			cancel()
			incompleteErr = errors.Join(
				incompleteErr,
				wrapOptionalError(
					"close incomplete local CA component",
					closeErr,
				),
			)
		}
		return fail(
			"local Root CA",
			incompleteErr,
		)
	}
	cleanups.register("local Root CA", certificateAuthority.Shutdown)

	accounts, err := provideraccount.NewManager(
		ctx,
		storageResult.store.ProviderAccountRepository(),
		options.Secrets,
		provideraccount.BuiltInRealms(),
		options.Clock,
	)
	if err != nil {
		return fail("ProviderAccount recovery", err)
	}
	cleanups.register("ProviderAccount manager", accounts.Shutdown)

	environmentResult, err := builders.environment.Build(ctx, environmentBuildRequest{
		repository:           storageResult.store.EnvironmentRepository(),
		assignmentRepository: storageResult.store.CaptureAssignmentRepository(),
		leafCache:            certificateAuthority,
		clock:                options.Clock,
		accounts:             accounts,
	})
	if err != nil {
		return fail("Environment and Capture assignment recovery", err)
	}
	if environmentResult.environments == nil || environmentResult.assignments == nil {
		return fail(
			"Environment and Capture assignment recovery",
			fmt.Errorf(
				"%w: Environment or Capture assignment component is nil",
				ErrInvalidBuildResult,
			),
		)
	}
	environments := environmentResult.environments
	assignments := environmentResult.assignments
	pending.register("Capture assignment runtime", assignments.Shutdown)
	workspaceDefaults, err := workspacedefault.New(
		storageResult.store.WorkspaceDefaultRepository(),
		environments,
		options.Clock,
	)
	if err != nil {
		return fail("Workspace Environment defaults", err)
	}

	activities, err := builders.activity.Build(activityBuildRequest{
		repository: storageResult.store.ActivityRepository(),
		clock:      options.Clock,
		random:     securityRandom,
	})
	if err != nil || activities == nil {
		buildErr := err
		if buildErr == nil {
			buildErr = fmt.Errorf(
				"%w: Activity component is nil",
				ErrInvalidBuildResult,
			)
		}
		return fail("Activity recovery", buildErr)
	}
	cleanups.register("Activity component", activities.Shutdown)

	connections, err := builders.connection.Build(ctx, connectionEventBuildRequest{
		repository: storageResult.store.ConnectionEventRepository(),
		clock:      options.Clock,
		random:     securityRandom,
	})
	if err != nil || connections == nil {
		buildErr := err
		if buildErr == nil {
			buildErr = fmt.Errorf(
				"%w: ConnectionEvent component is nil",
				ErrInvalidBuildResult,
			)
		}
		return fail("ConnectionEvent recovery", buildErr)
	}
	cleanups.register("ConnectionEvent component", connections.Shutdown)

	// The rules are built before the ApprovalCenter, because a remembered
	// answer writes one and the answer has to be able to say so.
	connectionRules, err := connectionpolicy.NewManager(
		ownerContext,
		connectionpolicy.ManagerOptions{
			Repository: storageResult.store.ConnectionRuleRepository(),
			Clock:      options.Clock,
			Shipped:    connectionpolicy.ShippedSnapshot(1),
		},
	)
	if err != nil {
		return fail("connection policy", err)
	}

	approvals, err := builders.approval.Build(approvalBuildRequest{
		ctx:        ctx,
		repository: storageResult.store.ToolApprovalRepository(),
		clock:      options.Clock,
		random:     securityRandom,
		config:     options.Approvals,
		remembered: connectionRules,
	})
	if err != nil || approvals == nil {
		buildErr := err
		if buildErr == nil {
			buildErr = fmt.Errorf(
				"%w: tool approval component is nil",
				ErrInvalidBuildResult,
			)
		}
		return fail("tool approval recovery", buildErr)
	}
	cleanups.register("tool approval component", approvals.Shutdown)

	monitor, err := builders.monitor.Build(monitorBuildRequest{
		ownerContext: ownerContext,
		reader:       storageResult.store.SchemaStateReader(),
		interval:     options.Lifecycle.HealthPollInterval,
		observe: func(state runtimepersistence.SchemaState, observationErr error) {
			tracker.observeStorage(state.Revision, observationErr)
		},
	})
	if err != nil {
		return fail("storage health monitor", err)
	}
	if monitor == nil {
		return fail(
			"storage health monitor",
			fmt.Errorf("%w: monitor component is nil", ErrInvalidBuildResult),
		)
	}
	cleanups.register("storage health monitor", monitor.Shutdown)

	providerResumeProber, err := providertransport.NewProviderProber()
	if err != nil {
		return fail("offline-hold provider probe", err)
	}
	originalResumeProber, err := originaltransport.NewTLSProber()
	if err != nil {
		return fail("offline-hold original-origin probe", err)
	}
	blindResumeProber, err := blindtunnel.NewReachabilityProber()
	if err != nil {
		return fail("offline-hold blind tunnel probe", err)
	}
	resumeProber, err := newRuntimeResumeProber(
		providerResumeProber,
		originalResumeProber,
		blindResumeProber,
	)
	if err != nil {
		return fail("offline-hold resume probe", err)
	}

	provider, err := builders.provider.Build(providerBuildRequest{
		coordinator: options.OfflineHold,
		secrets:     options.Secrets,
		audit:       runtimeEgress,
	})
	if err != nil {
		return fail("provider transport", err)
	}
	if provider == nil {
		return fail(
			"provider transport",
			fmt.Errorf("%w: provider transport is nil", ErrInvalidBuildResult),
		)
	}
	pending.register("provider transport", provider.Shutdown)

	original, err := builders.original.Build(originalBuildRequest{
		coordinator: options.OfflineHold,
		audit:       runtimeEgress,
	})
	if err != nil {
		return fail("original-origin transport", err)
	}
	if original == nil {
		return fail(
			"original-origin transport",
			fmt.Errorf(
				"%w: original-origin transport is nil",
				ErrInvalidBuildResult,
			),
		)
	}
	pending.register("original-origin transport", original.Shutdown)

	exchanges, err := builders.exchange.Build(exchangeBuildRequest{
		ownerContext:  ownerContext,
		actions:       options.OfflineHold,
		accounts:      accounts,
		provider:      provider,
		toolDecisions: approvals,
		activities:    activities,
		hold:          options.ExchangeHold,
	})
	if err != nil || exchanges == nil {
		buildErr := err
		if buildErr == nil {
			buildErr = fmt.Errorf(
				"%w: Exchange pipeline is nil",
				ErrInvalidBuildResult,
			)
		}
		return fail("Exchange pipeline", buildErr)
	}
	pending.register("Exchange pipeline", exchanges.Shutdown)

	captureRuns, err := builders.capture.Build(ctx, captureBuildRequest{
		repository: storageResult.store.CaptureRunRepository(),
		clock:      options.Clock,
		random:     securityRandom,
	})
	if err != nil {
		return fail("CaptureRun recovery", err)
	}
	if captureRuns == nil {
		return fail(
			"CaptureRun recovery",
			fmt.Errorf("%w: CaptureRun component is nil", ErrInvalidBuildResult),
		)
	}
	pending.register("CaptureRun component", captureRuns.Shutdown)
	manualCaptures, err := builders.manualCapture.Build(ctx, manualCaptureBuildRequest{
		repository: storageResult.store.ManualCaptureRepository(),
		clock:      options.Clock,
		random:     securityRandom,
	})
	if err != nil {
		return fail("ManualCapture recovery", err)
	}
	if manualCaptures == nil {
		return fail(
			"ManualCapture recovery",
			fmt.Errorf("%w: ManualCapture component is nil", ErrInvalidBuildResult),
		)
	}
	pending.register("ManualCapture component", manualCaptures.Shutdown)
	captureAdmissions, err := captureadmission.NewAuthorizer(
		captureRuns,
		manualCaptures,
	)
	if err != nil {
		return fail("capture admission", err)
	}

	// A blind tunnel dials through the same egress admission as every other
	// outbound, so it cannot become the one path that ignores a planned hold.
	blindTunnels, err := newBlindTunnelDialer(options.OfflineHold)
	if err != nil {
		return fail("blind tunnel dialer", err)
	}
	proxy, err := builders.proxy.Build(proxyBuildRequest{
		ownerContext: ownerContext,
		admissions:   captureAdmissions,
		assignments:  assignments,
		exchanges:    exchanges,
		original:     original,
		certificates: certificateAuthority,
		connections:  connections,
		policy:       connectionRules.Source(),
		// A rule that asks blocks the connection on the same ApprovalCenter a
		// tool intent goes to, so a person answers both in one place.
		approvals:    approvals,
		blindTunnels: blindTunnels,
		egressAudit:  runtimeEgress,
		random:       securityRandom,
	})
	if err != nil {
		return fail("loopback proxy handler", err)
	}
	if proxy == nil {
		return fail(
			"loopback proxy handler",
			fmt.Errorf("%w: loopback proxy handler is nil", ErrInvalidBuildResult),
		)
	}
	pending.register("loopback proxy handler", proxy.Shutdown)

	if err := options.OfflineHold.Start(ctx, offlinehold.RuntimeBinding{
		InstanceID: instanceID,
	}); err != nil {
		return fail("offline-hold binding", err)
	}
	pending.register("offline-hold binding", func(shutdownContext context.Context) error {
		options.OfflineHold.BeginShutdown()
		return options.OfflineHold.Drain(shutdownContext)
	})

	cleanups.register("offline-hold drain", options.OfflineHold.Drain)
	cleanups.register("Exchange drain", exchanges.Drain)
	cleanups.register("Capture assignment drain", assignments.Drain)
	cleanups.register("loopback proxy drain", proxy.Drain)
	cleanups.register("provider transport", provider.Shutdown)
	cleanups.register("original-origin transport", original.Shutdown)
	cleanups.register("CaptureRun component", captureRuns.Shutdown)
	cleanups.register("ManualCapture component", manualCaptures.Shutdown)
	cleanups.register("Exchange admission", func(context.Context) error {
		exchanges.BeginShutdown()
		return nil
	})
	cleanups.register("offline-hold admission", func(context.Context) error {
		options.OfflineHold.BeginShutdown()
		return nil
	})
	cleanups.register("CaptureRun admission", func(context.Context) error {
		captureRuns.BeginShutdown()
		return nil
	})
	cleanups.register("ManualCapture admission", func(context.Context) error {
		manualCaptures.BeginShutdown()
		return nil
	})
	cleanups.register("Capture assignment admission", func(context.Context) error {
		assignments.BeginShutdown()
		return nil
	})
	cleanups.register("loopback proxy admission", func(context.Context) error {
		proxy.BeginShutdown()
		return nil
	})
	pending = cleanupStack{}

	finalState, err := storageResult.store.SchemaStateReader().ReadSchemaState(ctx)
	if err != nil {
		return fail("foundation state verification", err)
	}
	tracker.commitInitialized(finalState.Revision)
	return &Runtime{
		status:            tracker,
		schemaReader:      storageResult.store.SchemaStateReader(),
		environments:      environments,
		assignments:       assignments,
		workspaceIdentity: workspaceIdentity,
		workspaceDefaults: workspaceDefaults,
		activities:        activities,
		connections:       connections,
		egress:            runtimeEgress,
		egressCompletion:  runtimeEgress,
		accounts:          accounts,
		approvals:         approvals,
		connectionRules:   connectionRules,
		monitor:           monitor,
		provider:          provider,
		original:          original,
		exchanges:         exchanges,
		captureRuns:       captureRuns,
		manualCaptures:    manualCaptures,
		localCA:           certificateAuthority,
		proxy:             proxy,
		offlineHold:       options.OfflineHold,
		resumeProber:      resumeProber,
		cleanups:          cleanups,
		clock:             options.Clock,
		timeout:           options.Lifecycle,
		shutdownDone:      make(chan struct{}),
	}, nil
}

// Status returns an immutable copy of the current runtime state.
func (r *Runtime) Status() RuntimeStatus {
	status := r.status.snapshot()
	status.EnvironmentProjection = r.environments.Health()
	status.OfflineHold = r.offlineHold.Snapshot()
	if status.EnvironmentProjection.State == environment.ProjectionStateUnavailable &&
		status.State == RuntimeStateInitialized {
		status.State = RuntimeStateDegraded
	}
	return status
}

// ExchangeExecutor returns the runtime-owned data-plane core. It has no
// listener and executes one immutable Environment request plan per Exchange.
func (r *Runtime) ExchangeExecutor() exchange.Executor {
	return r.exchanges
}

// CaptureRuns returns the runtime-owned short-lived child attribution
// controller. It has no HTTP exposure until a Host composes authenticated
// control routes.
// CaptureRunReader is the read side of what is captured.
func (r *Runtime) CaptureRunReader() capturerun.Reader {
	return r.captureRuns
}

func (r *Runtime) CaptureRuns() capturerun.Controller {
	return r.captureRuns
}

// ManualCaptures returns the runtime-owned durable manual capture authority.
// It has no HTTP exposure until a Host composes an authenticated control
// adapter through the shared capture-grant issuer.
func (r *Runtime) ManualCaptures() manualcapture.Controller {
	return r.manualCaptures
}

// WorkspaceIdentity resolves local working directories into opaque,
// installation-scoped identities. It is not an authentication authority.
func (r *Runtime) WorkspaceIdentity() workspaceidentity.LocalResolver {
	return r.workspaceIdentity
}

// WorkspaceDefaults selects the initial Environment for future managed runs
// in one installation-scoped workspace. It never replaces Capture assignment
// or Environment request authority.
func (r *Runtime) WorkspaceDefaults() workspacedefault.Controller {
	return r.workspaceDefaults
}

// Activities returns the runtime-owned durable redacted timeline.
func (r *Runtime) Activities() activity.Runtime {
	return r.activities
}

// ConnectionEvents returns the durable body-free connection audit boundary.
func (r *Runtime) ConnectionEvents() connectionevent.Runtime {
	return r.connections
}

// EgressAttempts returns the durable per-egress audit boundary. It answers
// where each request actually went, which one connection record cannot.
func (r *Runtime) EgressAttempts() egressaudit.Reader {
	return r.egress
}

// ProviderAccounts returns the runtime-owned non-secret account authority.
// Credentials remain in the Host-selected SecretStore and are never exposed
// through this interface.
func (r *Runtime) ProviderAccounts() provideraccount.Controller {
	return r.accounts
}

// ToolApprovals returns the durable interactive tool decision authority used
// by the Exchange pipeline.
func (r *Runtime) ToolApprovals() toolapproval.Controller {
	return r.approvals
}

// ClientRootApprovals returns the authority asked before a client recognized
// by its publisher is handed the Root. It is the same authority that answers a
// connection ask, so both questions reach a person the same way.
func (r *Runtime) ClientRootApprovals() ClientRootApprover {
	return r.approvals
}

// ConnectionRules is the outbound firewall a person reads and edits.
func (r *Runtime) ConnectionRules() *connectionpolicy.Manager {
	return r.connectionRules
}

// LocalRootIdentity returns immutable public signing-authority identity.
// ProductRuntime does not install it into an operating-system trust store.
func (r *Runtime) LocalRootIdentity() localca.RootIdentity {
	return r.localCA.Identity()
}

// LocalRootCertificate returns defensive-copy public delivery material. Its
// path is not part of Root identity or signing authorization.
func (r *Runtime) LocalRootCertificate() localca.RootCertificate {
	return r.localCA.Certificate()
}

// ProxyHandler returns the fully composed CONNECT/MITM handler. A Host must
// bind a literal loopback listener and is solely responsible for publishing
// that address after every route is ready.
func (r *Runtime) ProxyHandler() http.Handler {
	return r.proxy
}

// SchemaStateReader returns the SQLite-backed initialization-state reader.
// It is distinct from the Environment aggregate SnapshotResolver.
func (r *Runtime) SchemaStateReader() runtimepersistence.SchemaStateReader {
	return r.schemaReader
}

// Environments is the single editable configuration authority. Publishing a
// revision updates the immutable projection only after Capture transition
// coordination has reached its declared boundary.
func (r *Runtime) Environments() environment.Controller {
	return r.environments
}

// EnvironmentResolver returns the process-local immutable Environment
// projection consumed by Capture assignment and request planning.
func (r *Runtime) EnvironmentResolver() environment.SnapshotResolver {
	return r.environments
}

// CaptureAssignments owns the mutable Capture-to-Environment choice. It is
// intentionally separate from Environment configuration and request routing.
func (r *Runtime) CaptureAssignments() captureassignment.Controller {
	return r.assignments
}

// EnvironmentProjectionHealth reports whether published Environment
// snapshots can be trusted. It is an internal health signal, not readiness.
func (r *Runtime) EnvironmentProjectionHealth() environment.ProjectionHealth {
	return r.environments.Health()
}

// Shutdown starts one bounded LIFO shutdown and waits for either that result or
// the caller deadline. Repeated calls never run component cleanup twice.
func (r *Runtime) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return errors.New("ProductRuntime shutdown context is nil")
	}
	r.shutdownOnce.Do(func() {
		r.status.beginStopping()
		go r.executeShutdown()
	})

	select {
	case <-r.shutdownDone:
		return r.shutdownErr
	default:
	}
	select {
	case <-r.shutdownDone:
		return r.shutdownErr
	case <-ctx.Done():
		return fmt.Errorf("wait for ProductRuntime shutdown: %w", ctx.Err())
	}
}

func (r *Runtime) executeShutdown() {
	shutdownContext, cancel := context.WithTimeout(
		context.Background(),
		r.timeout.ShutdownTimeout,
	)
	if r.egressCompletion != nil {
		r.egressCompletion.beginShutdown(shutdownContext)
	}
	cleanupErr := r.cleanups.shutdown(shutdownContext)
	cancel()
	var durabilityErr error
	if r.egressCompletion != nil {
		r.egressCompletion.finishShutdown()
		durabilityErr = r.egressCompletion.failure()
	}
	r.shutdownErr = errors.Join(cleanupErr, durabilityErr)
	r.status.finishStopping(r.clock.Now(), r.shutdownErr)
	close(r.shutdownDone)
}

// blindTunnelDialer pairs the gated dialer with the action admission the
// tunnel needs before it may dial.
type blindTunnelDialer struct {
	coordinator offlinehold.Coordinator
	dialer      *blindtunnel.Dialer
}

func newBlindTunnelDialer(
	coordinator offlinehold.Coordinator,
) (*blindTunnelDialer, error) {
	dialer, err := blindtunnel.NewDialer(coordinator)
	if err != nil {
		return nil, err
	}
	return &blindTunnelDialer{coordinator: coordinator, dialer: dialer}, nil
}

func (tunnels *blindTunnelDialer) BeginAction(
	ctx context.Context,
	request offlinehold.ActionRequest,
) (*offlinehold.ActionLease, error) {
	return tunnels.coordinator.BeginAction(ctx, request)
}

func (tunnels *blindTunnelDialer) Dial(
	ctx context.Context,
	request blindtunnel.DialRequest,
) (net.Conn, offlinehold.Lease, error) {
	return tunnels.dialer.Dial(ctx, request)
}
