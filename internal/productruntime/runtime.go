// Package productruntime owns the single production composition root.
package productruntime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/accesscredential"
	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/blindtunnel"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/localca"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originaltransport"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

var ErrInvalidBuildResult = errors.New("invalid runtime build result")

// Runtime owns every successfully constructed production component.
type Runtime struct {
	status       *statusTracker
	schemaReader runtimepersistence.SchemaStateReader
	accesses     accessRuntime
	probeCatalog access.ProviderProbeCatalog
	credentials  credentialRuntime
	activities   activityRuntime
	connections  connectionEventRuntime
	egress       egressaudit.Reader
	approvals    approvalRuntime
	monitor      ownedComponent
	provider     providerRuntime
	original     originalRuntime
	exchanges    exchangeRuntime
	captureRuns  captureRuntime
	localCA      localCARuntime
	proxy        proxyRuntime
	offlineHold  offlinehold.RuntimeCoordinator
	resumeProber offlinehold.Prober
	cleanups     cleanupStack
	clock        Clock
	timeout      LifecycleOptions

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
		builders.access == nil ||
		builders.credential == nil ||
		builders.activity == nil ||
		builders.connection == nil ||
		builders.approval == nil ||
		builders.monitor == nil ||
		builders.provider == nil ||
		builders.original == nil ||
		builders.exchange == nil ||
		builders.capture == nil ||
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

	securityRandom := newSynchronizedReader(options.SecurityRandom)
	ownerContext, cancelOwner := context.WithCancelCause(context.WithoutCancel(ctx))
	cleanups.register("runtime owner context", func(context.Context) error {
		cancelOwner(errors.New("runtime owner context stopped"))
		return nil
	})

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

	accesses, err := builders.access.Build(ctx, accessBuildRequest{
		repository:   storageResult.store.AccessRepository(),
		rootRevision: certificateAuthority.Identity().Revision(),
		leafCache:    certificateAuthority,
	})
	if err != nil {
		return fail("Access recovery", err)
	}
	if accesses == nil {
		return fail(
			"Access recovery",
			fmt.Errorf("%w: Access component is nil", ErrInvalidBuildResult),
		)
	}
	cleanups.register("Access runtime", accesses.Shutdown)

	credentials, err := builders.credential.Build(credentialBuildRequest{
		resolver: accesses,
		secrets:  options.Secrets,
	})
	if err != nil || credentials == nil {
		buildErr := err
		if buildErr == nil {
			buildErr = fmt.Errorf(
				"%w: credential component is nil",
				ErrInvalidBuildResult,
			)
		}
		return fail("credential control", buildErr)
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

	approvals, err := builders.approval.Build(approvalBuildRequest{
		ctx:        ctx,
		repository: storageResult.store.ToolApprovalRepository(),
		clock:      options.Clock,
		random:     securityRandom,
		config:     options.Approvals,
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
	resumeProber, err := newRuntimeResumeProber(
		providerResumeProber,
		originalResumeProber,
	)
	if err != nil {
		return fail("offline-hold resume probe", err)
	}

	provider, err := builders.provider.Build(providerBuildRequest{
		coordinator: options.OfflineHold,
		secrets:     options.Secrets,
		audit:       storageResult.store.EgressAttemptRepository(),
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
		audit:       storageResult.store.EgressAttemptRepository(),
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
		resolver:      accesses,
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

	// A blind tunnel dials through the same egress admission as every other
	// outbound, so it cannot become the one path that ignores a planned hold.
	blindTunnels, err := newBlindTunnelDialer(options.OfflineHold)
	if err != nil {
		return fail("blind tunnel dialer", err)
	}
	// Every proxied connection is decided before any dial. The rule set this
	// stage ships is an explicit named placeholder for the ask path, not a
	// permissive default; see connectionpolicy.InterimAllowUnmatchedRuleID.
	connectionRules, err := connectionpolicy.InterimRuleSet(1)
	if err != nil {
		return fail("connection policy", err)
	}
	proxy, err := builders.proxy.Build(proxyBuildRequest{
		ownerContext: ownerContext,
		runs:         captureRuns,
		ingress:      accesses,
		exchanges:    exchanges,
		original:     original,
		certificates: certificateAuthority,
		connections:  connections,
		policy:       connectionRules,
		blindTunnels: blindTunnels,
		egressAudit:  storageResult.store.EgressAttemptRepository(),
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
	cleanups.register("loopback proxy drain", proxy.Drain)
	cleanups.register("provider transport", provider.Shutdown)
	cleanups.register("original-origin transport", original.Shutdown)
	cleanups.register("CaptureRun component", captureRuns.Shutdown)
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
		status:       tracker,
		schemaReader: storageResult.store.SchemaStateReader(),
		accesses:     accesses,
		probeCatalog: accesses,
		credentials:  credentials,
		activities:   activities,
		connections:  connections,
		egress:       storageResult.store.EgressAttemptRepository(),
		approvals:    approvals,
		monitor:      monitor,
		provider:     provider,
		original:     original,
		exchanges:    exchanges,
		captureRuns:  captureRuns,
		localCA:      certificateAuthority,
		proxy:        proxy,
		offlineHold:  options.OfflineHold,
		resumeProber: resumeProber,
		cleanups:     cleanups,
		clock:        options.Clock,
		timeout:      options.Lifecycle,
		shutdownDone: make(chan struct{}),
	}, nil
}

// Status returns an immutable copy of the current runtime state.
func (r *Runtime) Status() RuntimeStatus {
	status := r.status.snapshot()
	status.AccessProjection = r.accesses.ProjectionHealth()
	status.OfflineHold = r.offlineHold.Snapshot()
	if status.AccessProjection.State == access.ProjectionStateUnavailable &&
		status.State == RuntimeStateInitialized {
		status.State = RuntimeStateDegraded
	}
	return status
}

// ExchangeExecutor returns the runtime-owned data-plane core. It has no
// listener and resolves one Access plan for each submitted Exchange.
func (r *Runtime) ExchangeExecutor() exchange.Executor {
	return r.exchanges
}

// CaptureRuns returns the runtime-owned short-lived child attribution
// controller. It has no HTTP exposure until a Host composes authenticated
// control routes.
func (r *Runtime) CaptureRuns() capturerun.Controller {
	return r.captureRuns
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

// ToolApprovals returns the durable interactive tool decision authority used
// by the Exchange pipeline.
func (r *Runtime) ToolApprovals() toolapproval.Controller {
	return r.approvals
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
// It is distinct from the Access aggregate SnapshotResolver.
func (r *Runtime) SchemaStateReader() runtimepersistence.SchemaStateReader {
	return r.schemaReader
}

// AccessWriter returns the serialized Access aggregate mutation boundary.
func (r *Runtime) AccessWriter() access.Writer {
	return r.accesses
}

// Credentials returns the write-only credential control boundary bound to the
// active Access plan and host SecretStore.
func (r *Runtime) Credentials() accesscredential.Controller {
	return r.credentials
}

// SnapshotResolver returns the current process-local Access projection.
func (r *Runtime) SnapshotResolver() access.SnapshotResolver {
	return r.accesses
}

// ActiveClientAuthorities returns the enabled exact AgentEndpoint authorities
// used to remove dangerous NO_PROXY bypasses from captured child environments.
func (r *Runtime) ActiveClientAuthorities() ([]string, error) {
	return r.accesses.ActiveClientAuthorities()
}

// AccessProjectionHealth reports whether process-local Access snapshots can be
// trusted. It is an internal health signal, not product readiness.
func (r *Runtime) AccessProjectionHealth() access.ProjectionHealth {
	return r.accesses.ProjectionHealth()
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
	r.shutdownErr = r.cleanups.shutdown(shutdownContext)
	cancel()
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
