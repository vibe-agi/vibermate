// Package productruntime owns the single production composition root.
package productruntime

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/accountoperation"
	"github.com/vibe-agi/vibermate/internal/acpobservation"
	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/blindtunnel"
	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/clientannotation"
	"github.com/vibe-agi/vibermate/internal/codelibrary"
	"github.com/vibe-agi/vibermate/internal/codexoauth"
	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/evidencearchive"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/launchsnapshot"
	"github.com/vibe-agi/vibermate/internal/localca"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/nativemcp"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originaltransport"
	"github.com/vibe-agi/vibermate/internal/productupdate"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/resourcedeletion"
	"github.com/vibe-agi/vibermate/internal/runtimedata"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
	"github.com/vibe-agi/vibermate/internal/socksproxy"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
	"github.com/vibe-agi/vibermate/internal/toolpolicy"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
	"github.com/vibe-agi/vibermate/internal/vlessprofile"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

var ErrInvalidBuildResult = errors.New("invalid runtime build result")

// Runtime owns every successfully constructed production component.
type Runtime struct {
	recordingStorage     *recordingStorage
	activityInspection   *recordingActivity
	contentInspection    *recordingContent
	connectionInspection *runtimepersistence.ConnectionInspectionReader
	nativeInspection     *runtimepersistence.NativeInspectionReader
	acpInspection        *runtimepersistence.ACPInspectionReader
	captureInspection    *runtimepersistence.CaptureInspectionReader
	usageInspection      *runtimepersistence.UsageInspectionReader
	recorder             *observationRecorder
	socks                *socksproxy.Server
	udp                  *runtimeUDP
	vlessProfiles        *vlessprofile.Manager
	network              *egressnetwork.Builder
	productUpdates       *productupdate.Reader
	bodyAdmission        *exchange.BodyAdmission
	launchSnapshots      launchsnapshot.Store
	paths                RuntimePaths
	storage              *runtimepersistence.ControlStore
	acpObservations      *acpobservation.Manager
	status               *statusTracker
	schemaReader         runtimepersistence.SchemaStateReader
	environments         environmentRuntime
	assignments          captureAssignmentRuntime
	workspaceIdentity    *workspaceidentity.Manager
	activities           activityRuntime
	conversationIDs      activity.ConversationIdentityRepository
	conversationWriter   activity.ConversationProjectionWriter
	contents             exchangeContentRuntime
	connections          connectionEventRuntime
	egress               egressaudit.Reader
	egressCompletion     *runtimeEgressRepository
	endpoints            *upstreamendpoint.Manager
	accounts             *provideraccount.Manager
	accountReads         *accountoperation.Reader
	codexOAuth           *codexoauth.Manager
	codexLogins          *codexoauth.LoginManager
	codeLibrary          *codelibrary.Manager
	egressProfiles       *egressprofile.Manager
	egressDefaults       *egressprofile.DefaultManager
	runtimeUsers         *runtimeuser.Manager
	approvals            approvalRuntime
	nativeTools          nativemcp.Executor
	nativeContentKey     [32]byte
	connectionRules      *connectionpolicy.Manager
	monitor              ownedComponent
	rawEvidence          rawEvidenceRuntime
	recordingBudget      *recording.Budget
	evidenceArchive      resourcedeletion.Archive
	archiveBarrier       *evidencearchive.Barrier
	provider             providerRuntime
	original             originalRuntime
	exchanges            exchangeRuntime
	captureRuns          captureRuntime
	manualCaptures       manualCaptureRuntime
	localCA              localCARuntime
	proxy                proxyRuntime
	offlineHold          offlinehold.RuntimeCoordinator
	resumeProber         offlinehold.Prober
	cleanups             cleanupStack
	clock                Clock
	timeout              LifecycleOptions

	shutdownOwnershipMu  sync.Mutex
	failedStartHostGuard *instanceguard.Guard
	failedStartGuardErr  error
	shutdownOnce         sync.Once
	shutdownDone         chan struct{}
	shutdownBudgetDone   chan struct{}
	shutdownErr          error
}

// Start validates all dependencies and executes the typed production builder.
func Start(ctx context.Context, options Options) (*Runtime, error) {
	return startWithBuilders(ctx, options, runtimeBuildOverrides{})
}

type runtimeBuildOverrides struct {
	publishObservations func(context.Context, string, string, *instanceguard.Guard) error
	egressObserver      func(*runtimeEgressRepository) egressaudit.Observer
	recorder            observationRecorderBuilder
	connectionEvent     func(context.Context, connectionEventBuildRequest) (connectionEventRuntime, error)
	originalProber      func() (*originaltransport.TLSProber, error)
	providerProber      func() (*providertransport.ProviderProber, error)
	blindProber         func() (*blindtunnel.ReachabilityProber, error)
	environment         func(context.Context, environmentBuildRequest) (environmentBuildResult, error)
	provider            func(providerBuildRequest) (providerRuntime, error)
	recoverEgress       func(context.Context, egressaudit.Repository, time.Time) error
	openObservations    func(context.Context, runtimepersistence.ObservationOptions) (*runtimepersistence.ObservationStore, error)
}

func recoverEgressAttempts(
	ctx context.Context,
	repository egressaudit.Repository,
	at time.Time,
) error {
	_, err := repository.Recover(ctx, at)
	return err
}

func startWithBuilders(
	ctx context.Context,
	options Options,
	overrides runtimeBuildOverrides,
) (*Runtime, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: startup context is nil", ErrInvalidOptions)
	}
	if options.Resources == nil {
		policy, err := exchange.DefaultResourcePolicy()
		if err != nil {
			return nil, err
		}
		options.Resources = &policy
	}
	if err := options.validate(); err != nil {
		return nil, err
	}
	var bodyAdmission *exchange.BodyAdmission
	var contentLimits *exchangecontent.SourceLimits
	if options.Resources != nil {
		policy := *options.Resources
		options.Resources = &policy
		var err error
		bodyAdmission, err = exchange.NewBodyAdmission(policy)
		if err != nil {
			return nil, err
		}
		contentLimits = &policy.Content
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("start ProductRuntime: %w", err)
	}
	environmentBuilder := buildEnvironment
	if overrides.environment != nil {
		environmentBuilder = overrides.environment
	}

	instanceID, err := options.InstanceIDs.NewInstanceID(ctx)
	if err != nil {
		return nil, fmt.Errorf("create runtime incarnation: %w", err)
	}
	if instanceID == "" {
		return nil, fmt.Errorf("%w: instance ID is empty", ErrInvalidBuildResult)
	}

	tracker := newStatusTracker(instanceID, options.Host.Kind(), options.Clock.Now())
	tracker.clock = options.Clock
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

	dataGuard, err := runtimedata.Acquire(options.Paths.DataDirectory())
	if err != nil {
		return fail("data directory ownership", err)
	}
	cleanups.register("data directory ownership", func(context.Context) error { return dataGuard.Release() })
	hostGuard := options.SchemaUpgradeHostGuard
	if hostGuard == nil {
		hostGuard, err = instanceguard.Acquire(filepath.Join(options.Paths.DataDirectory(), "server.lock"))
		if err != nil {
			return fail("host ownership", err)
		}
		// Retain through Open, all startup and failed-start cleanup. Desktop's
		// existing serverhost.Attach acquires it only after Start returns.
		defer hostGuard.Release()
	} else if hostGuard.Path() != filepath.Join(options.Paths.DataDirectory(), "server.lock") || !hostGuard.OwnsDirectory(options.Paths.DataDirectory()) {
		return fail("host ownership", ErrInvalidOptions)
	}
	ownerContext, cancelOwner := context.WithCancelCause(context.WithoutCancel(ctx))
	cleanups.register("runtime owner context", func(context.Context) error { cancelOwner(errors.New("runtime stopped")); return nil })
	recordingBudget, err := recording.NewBudget(recording.Limits{Records: 2048, Bytes: 64 << 20})
	if err != nil {
		return fail("recording budget", err)
	}
	recorderBuilder := overrides.recorder
	if recorderBuilder == nil {
		recorderBuilder = newObservationRecorder
	}
	recorder := recorderBuilder(ownerContext, recordingBudget, nil, nil)
	recorder.originalScopesOnly = true
	recorder.reportFailure = tracker.failRecording
	cleanups.register("observation recorder", recorder.Shutdown)
	storageResult, err := buildStorage(ctx, storageBuildRequest{
		databasePath: options.Paths.DatabasePath(),
		directory:    options.Paths.DataDirectory(), dataGuard: dataGuard, hostGuard: hostGuard, now: options.Clock.Now().UTC(), upgradeTimeout: options.SchemaUpgradeTimeout, progress: options.SchemaUpgradeProgress,
		recorder: recorder,
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
	cleanups.register("sqlite", func(ctx context.Context) error {
		err := storageResult.store.Shutdown(ctx)
		if err != nil {
			return errors.Join(err, storageResult.store.Shutdown(context.WithoutCancel(ctx)))
		}
		return nil
	})
	visibility, err := storageResult.store.ReadInspectionSnapshot(ctx, nil)
	if err != nil {
		return fail("recording epoch", err)
	}
	recorder.epoch.Advance(visibility.Stamp.ArchiveEpoch)
	if err := storageResult.store.NativeToolRepository().Recover(ctx, options.Clock.Now().UTC()); err != nil {
		return fail("native tool recovery", err)
	}

	if options.ConsumePendingRootReset {
		if err := localca.ApplyPendingRootReset(ctx, options.Paths.DataDirectory()); err != nil {
			return fail("local Root reset", err)
		}
	}
	securityRandom := newSynchronizedReader(options.SecurityRandom)
	var nativeContentKey [32]byte
	if _, err := io.ReadFull(securityRandom, nativeContentKey[:]); err != nil {
		return fail("native content cursor key", err)
	}
	clientAnnotations, err := clientannotation.Open(ctx, options.Secrets, securityRandom)
	if err != nil {
		return fail("client annotation signing", err)
	}
	cleanups.register("client annotation signer", func(context.Context) error {
		clientAnnotations.Destroy()
		return nil
	})
	codeLibrary, err := codelibrary.NewManager(
		storageResult.store.CodeLibraryRepository(),
		options.Clock,
	)
	if err != nil {
		return fail("Code Library", err)
	}
	egressProfiles, err := egressprofile.NewManager(
		storageResult.store.EgressProfileRepository(),
		options.Clock,
	)
	if err != nil {
		return fail("egress profiles", err)
	}
	egressDefaults, err := egressprofile.NewDefaultManager(storageResult.store.EgressDefaultRepository(), egressProfiles)
	if err != nil {
		return fail("egress defaults", err)
	}
	nativeRevoker := &nativeRevocation{}
	runtimeUsers, err := runtimeuser.New(runtimeuser.Options{
		AuthorityChanges: nativeRevoker,
		Repository:       storageResult.store.RuntimeUserRepository(),
		Clock:            options.Clock,
		Random:           securityRandom,
		SessionLifetime:  30 * 24 * time.Hour,
	})
	if err != nil {
		return fail("Runtime User authority", err)
	}
	optional, err := newRecordingStorage(ownerContext, options.Paths.DataDirectory(), storageResult.layout, dataGuard, storageResult.store, recorder, tracker, contentLimits, securityRandom, options.Clock, overrides)
	if err != nil {
		return fail("recording descriptors", err)
	}
	cleanups.register("optional recording ownership", func(ctx context.Context) error {
		err := optional.Shutdown(ctx)
		if err != nil {
			<-optional.done
		}
		return err
	})
	activityReader, err := runtimepersistence.NewActivityInspectionReader(optional.catalog, storageResult.store, *contentLimits)
	if err != nil {
		return fail("Activity inspection configuration", err)
	}
	contentReader, err := runtimepersistence.NewContentInspectionReader(optional.catalog, storageResult.store, *contentLimits)
	if err != nil {
		return fail("content inspection configuration", err)
	}
	activities := &recordingActivity{InspectionReader: activityReader, owner: optional}
	contents := &recordingContent{InspectionReader: contentReader, owner: optional}
	connectionReader := runtimepersistence.NewConnectionInspectionReader(optional.catalog, storageResult.store)
	egressReader := runtimepersistence.NewEgressInspectionReader(optional.catalog, storageResult.store)
	acpReader := runtimepersistence.NewACPInspectionReader(optional.catalog, storageResult.store)
	rawEvidence := &recordingRaw{runtimeRawReader: runtimepersistence.NewRawInspectionReader(optional.catalog, storageResult.store, optional, optional), owner: optional}
	usagePolicy, err := storageResult.store.UsagePolicyRepository().UsagePolicy(ctx)
	if err != nil {
		return fail("usage collection policy", err)
	}
	if err = usagePolicy.Validate(); err != nil {
		return fail("usage collection policy", err)
	}
	recorder.publishUsagePolicy(usagePolicy)

	runtimeEgress := newRuntimeEgressRepository(
		egressReader,
		tracker,
		ownerContext,
		options.Lifecycle,
		cancelOwner,
	)
	runtimeEgress.recorder = recorder
	var ordinaryEgress egressaudit.Observer = runtimeEgress
	if overrides.egressObserver != nil {
		ordinaryEgress = overrides.egressObserver(runtimeEgress)
	}
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

	certificateAuthority, err := buildLocalCA(ctx, localCABuildRequest{
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

	builtInEndpoints, err := upstreamendpoint.BuiltInCommands()
	if err != nil {
		return fail("UpstreamEndpoint definitions", err)
	}
	endpoints, err := upstreamendpoint.NewManager(
		ctx,
		storageResult.store.UpstreamEndpointRepository(),
		builtInEndpoints,
		options.Clock,
	)
	if err != nil {
		return fail("UpstreamEndpoint recovery", err)
	}
	cleanups.register("UpstreamEndpoint manager", endpoints.Shutdown)

	accounts, err := provideraccount.NewManager(
		ctx,
		storageResult.store.ProviderAccountRepository(),
		options.Secrets,
		endpoints,
		provideraccount.BuiltInRealms(),
		options.Clock,
	)
	if err != nil {
		return fail("ProviderAccount recovery", err)
	}
	cleanups.register("ProviderAccount manager", accounts.Shutdown)
	if err := accounts.BindEgressProfiles(egressProfiles); err != nil {
		return fail("ProviderAccount egress profiles", err)
	}
	if err := accounts.BindEgressDefaults(egressDefaults); err != nil {
		return fail("ProviderAccount egress defaults", err)
	}
	captureRunRepository := storageResult.store.CaptureRunRepository()
	manualCaptureRepository := storageResult.store.ManualCaptureRepository()
	captureActivity, err := newCaptureActivityResolver(
		captureRunRepository,
		manualCaptureRepository,
		options.Clock,
	)
	if err != nil {
		return fail("Capture activity projection", err)
	}

	environmentResult, err := environmentBuilder(ctx, environmentBuildRequest{
		egressDefaults:       egressDefaults,
		nativeRevocation:     nativeRevoker,
		repository:           storageResult.store.EnvironmentRepository(),
		assignmentRepository: storageResult.store.CaptureAssignmentRepository(),
		activity:             captureActivity,
		clock:                options.Clock,
		accounts:             accounts,
		endpoints:            endpoints,
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
	if err := accounts.BindDeletionGuard(environments); err != nil {
		return fail("ProviderAccount deletion authority", err)
	}
	pending.register("Capture assignment runtime", assignments.Shutdown)

	connectionBuilder := buildConnectionEvent
	if overrides.connectionEvent != nil {
		connectionBuilder = overrides.connectionEvent
	}
	connections, err := connectionBuilder(ctx, connectionEventBuildRequest{
		observer: recorder, scopes: recorder,
		repository: connectionReader,
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

	approvals, err := buildApproval(approvalBuildRequest{
		ctx:              ctx,
		repository:       storageResult.store.ToolApprovalRepository(),
		clock:            options.Clock,
		random:           securityRandom,
		config:           options.Approvals,
		remembered:       connectionRules,
		nativeRepository: storageResult.store.NativeToolRepository(),
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
	if owner, ok := approvals.(toolapproval.ArchiveClearAuthority); ok {
		optional.clearAuthority = owner
	}
	optional.legacyAuthority, _ = approvals.(toolapproval.LegacyApprovalRemover)
	close(optional.approvalReady)
	if owner, ok := approvals.(toolapproval.CaptureCanceler); ok {
		nativeRevoker.installApprovals(owner)
	}
	cleanups.register("tool approval component", approvals.Shutdown)
	toolDecisions, err := toolpolicy.New(approvals)
	if err != nil {
		return fail("tool policy", err)
	}

	monitor, err := buildMonitor(monitorBuildRequest{
		ownerContext: ownerContext,
		reader:       storageResult.store.SchemaStateReader(),
		interval:     options.Lifecycle.HealthPollInterval,
		observe: func(state runtimepersistence.SchemaState, observationErr error) {
			tracker.observeStorage(state.Revision, observationErr)
		},
		cleanup: func(ctx context.Context) error {
			_, err := storageResult.store.RetireNativeMetadata(ctx, options.Clock.Now().UTC(), 100)
			return err
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

	vlessProfiles, err := vlessprofile.New(vlessprofile.Options{Store: options.Secrets, Profiles: egressProfiles, Owner: ownerContext, Executable: options.VLESSExecutable, SystemResolver: options.NetworkOptions.SystemResolver})
	if err != nil {
		return fail("VLESS profile authority", err)
	}
	cleanups.register("VLESS sessions", vlessProfiles.Shutdown)
	networkOptions := options.NetworkOptions
	networkOptions.VLESS = vlessProfiles
	network, err := egressnetwork.NewBuilder(networkOptions)
	if err != nil {
		return fail("shared egress network", err)
	}
	buildProviderProber := func() (*providertransport.ProviderProber, error) {
		return providertransport.NewProviderProberWithBuilder(network, nil)
	}
	if overrides.providerProber != nil {
		buildProviderProber = overrides.providerProber
	}
	providerResumeProber, err := buildProviderProber()
	if err != nil {
		return fail("offline-hold provider probe", err)
	}
	if providerResumeProber == nil {
		return fail("offline-hold provider probe", fmt.Errorf("%w: provider prober is nil", ErrInvalidBuildResult))
	}
	buildOriginalProber := func() (*originaltransport.TLSProber, error) {
		return originaltransport.NewTLSProberWithBuilder(network, nil)
	}
	if overrides.originalProber != nil {
		buildOriginalProber = overrides.originalProber
	}
	originalResumeProber, err := buildOriginalProber()
	if err != nil {
		return fail("offline-hold original-origin probe", err)
	}
	if originalResumeProber == nil {
		return fail("offline-hold original-origin probe", fmt.Errorf("%w: original prober is nil", ErrInvalidBuildResult))
	}
	buildBlindProber := func() (*blindtunnel.ReachabilityProber, error) {
		return blindtunnel.NewReachabilityProberWithBuilder(network)
	}
	if overrides.blindProber != nil {
		buildBlindProber = overrides.blindProber
	}
	blindResumeProber, err := buildBlindProber()
	if err != nil {
		return fail("offline-hold blind tunnel probe", err)
	}
	if blindResumeProber == nil {
		return fail("offline-hold blind tunnel probe", fmt.Errorf("%w: blind prober is nil", ErrInvalidBuildResult))
	}
	resumeProber, err := newRuntimeResumeProber(
		providerResumeProber,
		originalResumeProber,
		blindResumeProber,
	)
	if err != nil {
		return fail("offline-hold resume probe", err)
	}
	resumeProber.udp = udpSetupProber{network: network}

	providerBuilder := buildProvider
	if overrides.provider != nil {
		providerBuilder = overrides.provider
	}
	provider, err := providerBuilder(providerBuildRequest{
		network:        network,
		egressDefaults: egressDefaults,
		coordinator:    options.OfflineHold,
		secrets:        options.Secrets,
		instanceIDs:    options.InstanceIDs,
		audit:          ordinaryEgress,
		rawEvidence:    rawEvidence,
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
	productUpdates, err := productupdate.New(egressDefaults, provider, options.Clock.Now)
	if err != nil {
		return fail("product update reader", err)
	}
	codexOAuth, err := codexoauth.NewManager(codexoauth.Options{
		Secrets: options.Secrets,
		Client:  codexOAuthHTTPClient{provider: provider},
		Clock:   options.Clock,
	})
	if err != nil {
		return fail("Codex OAuth credential manager", err)
	}
	pending.register("Codex OAuth credential manager", codexOAuth.Shutdown)
	if err := accounts.BindCredentialPreparer(codexOAuth); err != nil {
		return fail("ProviderAccount credential preparation authority", err)
	}
	codexLogins, err := codexoauth.NewLoginManager(codexoauth.LoginOptions{
		TokenClient: provider,
		Client:      codexOAuthHTTPClient{provider: provider}, Clock: options.Clock,
		Persist: persistCodexLogin(accounts, options.Secrets),
	})
	if err != nil {
		return fail("Codex OAuth login manager", err)
	}
	pending.register("Codex OAuth login manager", codexLogins.Shutdown)

	nativeTransport, err := selectOriginalTransport(options, network)
	if err != nil {
		return fail("native strict transport", err)
	}
	original, err := buildOriginal(originalBuildRequest{
		transport:   nativeTransport,
		coordinator: options.OfflineHold,
		audit:       ordinaryEgress,
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
	nativeOperationIDs := nativeIDs{random: securityRandom}
	nativeTools, err := nativemcp.New(nativemcp.Dependencies{EgressDefaults: egressDefaults, OwnerContext: ownerContext, Transport: nativeTransport, Store: storageResult.store.NativeToolRepository(), ToolDecisions: toolDecisions, Offline: options.OfflineHold, Clock: options.Clock.Now, IDs: nativeOperationIDs, ContentProfiles: options.ContentProfiles, ContentRecorder: recorder, RecordingScopes: recorder}, nativemcp.DefaultLimits())
	if err != nil {
		return fail("native MCP recovery", err)
	}
	nativeRevoker.install(nativeTools)
	pending.register("native MCP module", nativeTools.Close)

	exchanges, err := buildExchange(exchangeBuildRequest{
		bodyAdmission:            bodyAdmission,
		ownerContext:             ownerContext,
		actions:                  options.OfflineHold,
		accounts:                 accounts,
		provider:                 provider,
		toolDecisions:            toolDecisions,
		activities:               activities,
		identities:               optional,
		contents:                 contents,
		usage:                    optional,
		recorder:                 recorder,
		clock:                    options.Clock,
		hold:                     options.ExchangeHold,
		annotations:              clientAnnotations,
		rawEvidence:              rawEvidence,
		reportRawEvidenceFailure: runtimeEgress.ReportRawEvidenceFailure,
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

	archiveBarrier := evidencearchive.NewBarrier()
	captureRuns, err := buildCapture(ctx, captureBuildRequest{
		nativeRevocation: nativeRevoker,
		repository:       captureRunRepository,
		clock:            options.Clock,
		random:           securityRandom,
		archiveBarrier:   archiveBarrier,
		barrier: captureEvidenceBarrier{
			raw: rawEvidence, reporter: runtimeEgress, recorder: recorder,
		},
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
	manualCaptures, err := buildManualCapture(ctx, manualCaptureBuildRequest{
		nativeRevocation: nativeRevoker,
		repository:       manualCaptureRepository,
		clock:            options.Clock,
		random:           securityRandom,
		archiveBarrier:   archiveBarrier,
		barrier: captureEvidenceBarrier{
			raw: rawEvidence, reporter: runtimeEgress, recorder: recorder,
		},
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

	currentAuthority, ok := captureAdmissions.(captureadmission.CurrentAuthority)
	if !ok {
		return fail("retained capture authority", errors.New("current Capture authority unavailable"))
	}
	retained := captureadmission.NewRetained(ownerContext, currentAuthority, options.Clock.Now)
	captureAdmissions = retained
	nativeRevoker.installRetained(retained)
	shutdownRetained := func(ctx context.Context) error {
		err := retained.Shutdown(ctx)
		if err != nil {
			return errors.Join(err, retained.Shutdown(context.WithoutCancel(ctx)))
		}
		return nil
	}
	pending.register("retained capture authority", shutdownRetained)

	// A blind tunnel dials through the same egress admission as every other
	// outbound, so it cannot become the one path that ignores a planned hold.
	blindTunnels, err := selectBlindTunnels(options, network)
	if err != nil {
		return fail("blind tunnel dialer", err)
	}
	accountReads, err := accountoperation.New(accountoperation.Options{Accounts: accounts, Transport: provider, Clock: options.Clock.Now})
	if err != nil {
		return fail("account operation reader", err)
	}
	proxy, err := buildProxy(proxyBuildRequest{
		ownerContext: ownerContext,
		admissions:   captureAdmissions,
		assignments:  assignments,
		nativeTools:  nativeTools, nativeIDs: nativeOperationIDs,
		exchanges:     exchanges,
		original:      original,
		accountReads:  accountReads,
		certificates:  certificateAuthority,
		connections:   connections,
		policy:        connectionRules.Source(),
		bodyAdmission: bodyAdmission,
		// A rule that asks blocks the connection on the same ApprovalCenter a
		// tool intent goes to, so a person answers both in one place.
		approvals:      approvals,
		blindTunnels:   blindTunnels,
		egressDefaults: egressDefaults,
		egressAudit:    ordinaryEgress,
		rawEvidence:    rawEvidence,
		random:         securityRandom,
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
	udp := &runtimeUDP{owner: ownerContext, activity: captureActivity,
		policy: connectionRules.Source(), approvals: approvals,
		defaults: egressDefaults, gate: options.OfflineHold, network: network,
		connections: connections, audit: runtimeEgress, clock: options.Clock, changed: make(chan struct{})}
	pending.register("UDP finalization", udp.Shutdown)
	socks, err := socksproxy.New(socksproxy.Options{
		Authorizer: captureAdmissions, TCPHandler: proxy,
		UDP:          udp,
		TargetAccess: egressnetwork.TargetAccessPublicOnly,
	})
	if err != nil {
		return fail("SOCKS proxy", err)
	}
	pending.register("SOCKS proxy", socks.Shutdown)
	nativeRevoker.installSOCKS(socks)

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
	if bodyAdmission != nil {
		// This flag belongs only to the existing serial cleanup goroutine.
		// Once the caller's budget expires, dependencies must still close after
		// real body owners drain. WithoutCancel adds no new deadline and cannot
		// interrupt their outstanding synchronous borrows or skip final cleanup.
		lateDrain := false
		for index := range cleanups.entries {
			run := cleanups.entries[index].run
			cleanups.entries[index].run = func(ctx context.Context) error {
				if lateDrain || ctx.Err() != nil {
					ctx = context.WithoutCancel(ctx)
				}
				return run(ctx)
			}
		}
		cleanups.register("body admission drain", func(ctx context.Context) error {
			err := bodyAdmission.Drain(ctx)
			if err == nil {
				err = ctx.Err()
			}
			if err != nil {
				lateDrain = true
				return errors.Join(err, bodyAdmission.Drain(context.WithoutCancel(ctx)))
			}
			return nil
		})
	}
	cleanups.register("Exchange drain", exchanges.Drain)
	cleanups.register("Capture assignment drain", assignments.Drain)
	cleanups.register("retained capture authority drain", shutdownRetained)
	cleanups.register("loopback proxy drain", proxy.Drain)
	cleanups.register("UDP finalization drain", udp.Drain)
	cleanups.register("SOCKS proxy drain", socks.Drain)
	cleanups.register("native MCP module", nativeTools.Close)
	cleanups.register("provider transport", provider.Shutdown)
	// LIFO: settle admitted rotations and their pending CAS before closing the
	// provider transport; login shutdown first prevents further credential work.
	cleanups.register("Codex OAuth credential manager", codexOAuth.Shutdown)
	cleanups.register("Codex OAuth login manager", codexLogins.Shutdown)
	cleanups.register("original-origin transport", original.Shutdown)
	cleanups.register("CaptureRun component", captureRuns.Shutdown)
	cleanups.register("ManualCapture component", manualCaptures.Shutdown)
	cleanups.register("Exchange admission", func(context.Context) error {
		if bodyAdmission != nil {
			bodyAdmission.BeginShutdown()
		}
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
	cleanups.register("SOCKS proxy admission", func(context.Context) error {
		socks.BeginShutdown()
		return nil
	})
	cleanups.register("UDP admission", func(context.Context) error {
		udp.BeginShutdown()
		return nil
	})
	cleanups.register("retained capture authority admission", func(context.Context) error { retained.BeginShutdown(); return nil })
	acpObservations, err := acpobservation.NewManager(storageResult.store.ACPRegistrations(), captureRuns, assignments, environments, options.Clock, acpobservation.RecordingOptions{Recorder: recorder, Scopes: recorder, Reader: acpReader})
	if err != nil {
		return fail("ACP observations", err)
	}
	pending = cleanupStack{}

	finalState, err := storageResult.store.SchemaStateReader().ReadSchemaState(ctx)
	if err != nil {
		return fail("foundation state verification", err)
	}
	tracker.commitInitialized(finalState.Revision)
	if err := ctx.Err(); err != nil {
		return fail("startup cancellation", err)
	}
	if !dataGuard.OwnsDirectory(options.Paths.DataDirectory()) || !hostGuard.OwnsDirectory(options.Paths.DataDirectory()) {
		return fail("startup ownership", runtimedata.ErrSchemaUpgradeIncomplete)
	}
	var shutdownBudgetDone chan struct{}
	if bodyAdmission != nil {
		shutdownBudgetDone = make(chan struct{})
	}
	return &Runtime{
		recordingStorage: optional, activityInspection: activities, contentInspection: contents, connectionInspection: connectionReader, nativeInspection: runtimepersistence.NewNativeInspectionReader(optional.catalog, storageResult.store), acpInspection: acpReader, captureInspection: runtimepersistence.NewCaptureInspectionReader(optional.catalog, storageResult.store), usageInspection: runtimepersistence.NewUsageInspectionReader(optional.catalog, storageResult.store),
		socks:              socks,
		udp:                udp,
		bodyAdmission:      bodyAdmission,
		paths:              options.Paths,
		storage:            storageResult.store,
		acpObservations:    acpObservations,
		status:             tracker,
		schemaReader:       storageResult.store.SchemaStateReader(),
		environments:       environments,
		assignments:        assignments,
		workspaceIdentity:  workspaceIdentity,
		activities:         activities,
		conversationIDs:    optional,
		conversationWriter: nil,
		contents:           contents,
		connections:        connections,
		egress:             egressReader,
		egressCompletion:   runtimeEgress,
		recorder:           recorder,
		endpoints:          endpoints,
		accounts:           accounts,
		accountReads:       accountReads,
		codexOAuth:         codexOAuth,
		codexLogins:        codexLogins,
		codeLibrary:        codeLibrary,
		egressProfiles:     egressProfiles,
		vlessProfiles:      vlessProfiles,
		network:            network,
		egressDefaults:     egressDefaults,
		productUpdates:     productUpdates,
		runtimeUsers:       runtimeUsers,
		approvals:          approvals,
		nativeTools:        nativeTools,
		nativeContentKey:   nativeContentKey,
		connectionRules:    connectionRules,
		monitor:            monitor,
		rawEvidence:        rawEvidence,
		recordingBudget:    recordingBudget,
		evidenceArchive:    recordingArchive{delegate: optional, recorder: recorder, raw: rawEvidence, revoker: nativeRevoker},
		archiveBarrier:     archiveBarrier,
		provider:           provider,
		original:           original,
		exchanges:          exchanges,
		captureRuns:        captureRuns,
		manualCaptures:     manualCaptures,
		localCA:            certificateAuthority,
		proxy:              proxy,
		offlineHold:        options.OfflineHold,
		resumeProber:       resumeProber,
		cleanups:           cleanups,
		clock:              options.Clock,
		timeout:            options.Lifecycle,
		shutdownDone:       make(chan struct{}),
		shutdownBudgetDone: shutdownBudgetDone,
	}, nil
}

// Status returns an immutable copy of the current runtime state.
func (r *Runtime) Status() RuntimeStatus {
	status := r.status.snapshot()
	if r.recordingBudget != nil {
		snapshot := r.recordingBudget.Snapshot()
		status.Recording.ReservedRecords = snapshot.ReservedRecords
		status.Recording.ReservedBytes = snapshot.ReservedBytes
		for _, kind := range snapshot.ByKind {
			for result, count := range kind.Offers {
				if recording.OfferResult(result) != recording.Accepted {
					status.Recording.Dropped += count.Records
				}
			}
			status.Recording.Unconfirmed += kind.Settlements[recording.Unconfirmed].Records
		}
		if status.Recording.State == RecordingAvailable && (status.Recording.Dropped > 0 || status.Recording.Unconfirmed > 0) {
			status.Recording.State = RecordingDegraded
		}
	}
	if r.recordingStorage != nil {
		status.Recording.CleanupPending = status.Recording.CleanupPending || r.recordingStorage.cleanupPending.Load()
		select {
		case <-r.recordingStorage.done:
			status.Recording.CleanupPending = r.recordingStorage.closeErr != nil || r.recordingStorage.cleanupPending.Load()
		default:
		}
	}
	if status.Recording.State == RecordingAvailable && (status.RecordingFailure != nil || r.recordingStorage != nil && !r.recordingStorage.available()) {
		status.Recording.State = RecordingDegraded
	}
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

// DryRun evaluates a frozen Exchange without acquiring credentials, opening a
// provider connection, or writing evidence.
func (r *Runtime) DryRun(ctx context.Context, request exchange.ClientRequest) (exchange.DryRunResult, error) {
	if r == nil || r.exchanges == nil {
		return exchange.DryRunResult{}, errors.New("Runtime dry run is unavailable")
	}
	return r.exchanges.DryRun(ctx, request)
}

// CaptureRuns returns the runtime-owned short-lived child attribution
// controller. It has no HTTP exposure until a Host composes authenticated
// control routes.
// CaptureRunReader is the read side of what is captured.
func (r *Runtime) CaptureRunReader() capturerun.Reader {
	return r.captureRuns
}

func (r *Runtime) ACPObservations() *acpobservation.Manager { return r.acpObservations }

func (r *Runtime) CaptureRuns() capturerun.Controller {
	return r.captureRuns
}

func (r *Runtime) LaunchSnapshots() *launchsnapshot.Store {
	return &r.launchSnapshots
}

// CaptureRunActivity exposes only an installation-wide active count for
// lifecycle operations that affect all managed Captures.
func (r *Runtime) CaptureRunActivity() capturerun.GlobalActivityReader {
	return r.captureRuns
}

// ManualCaptures returns the runtime-owned durable manual capture authority.
// It has no HTTP exposure until a Host composes an authenticated control
// adapter through the shared capture-grant issuer.
func (r *Runtime) ManualCaptures() manualcapture.Controller {
	return r.manualCaptures
}

// ManualCaptureActivity exposes only an installation-wide active count for
// lifecycle operations that affect all local and remote Capture owners.
func (r *Runtime) ManualCaptureActivity() manualcapture.GlobalActivityReader {
	return r.manualCaptures
}

// BeginCaptureMaintenance excludes every Capture creation until release.
// Existing Captures remain visible so the caller can check them while holding
// the same admission boundary.
func (r *Runtime) BeginCaptureMaintenance(
	ctx context.Context,
) (func(), error) {
	if r == nil || r.archiveBarrier == nil || ctx == nil {
		return nil, evidencearchive.ErrBarrierUnavailable
	}
	return r.archiveBarrier.BeginClear(ctx)
}

// WorkspaceIdentity resolves local working directories into opaque,
// installation-scoped identities. It is not an authentication authority.
func (r *Runtime) WorkspaceIdentity() workspaceidentity.LocalResolver {
	return r.workspaceIdentity
}

// Activities returns the runtime-owned durable redacted timeline.
func (r *Runtime) Activities() *recordingActivity {
	return r.activityInspection
}

// ConversationIdentities retains exact, client-owned session and actor IDs
// independently from retention-bound semantic content.
func (r *Runtime) ConversationIdentities() activity.ConversationIdentityRepository {
	return r.conversationIDs
}

// ConversationProjectionWriter updates only the rebuildable terminal index.
func (r *Runtime) ConversationProjectionWriter() activity.ConversationProjectionWriter {
	return r.conversationWriter
}

// ExchangeContents returns the separate, retention-bound semantic evidence
// reader. Body-free Activity and egress journals never expose this content.
func (r *Runtime) UsageRepository() runtimeusage.PolicyRepository {
	return recordingUsageRepository{PolicyRepository: r.storage.UsagePolicyRepository(), recorder: r.recorder}
}
func (r *Runtime) UsageInspection() runtimeusage.InspectionReader { return r.usageInspection }

func (r *Runtime) ExchangeContents() exchangecontent.InspectionReader {
	return r.contentInspection
}

func (r *Runtime) ExchangeContentLimits() (exchangecontent.SourceLimits, bool) {
	if r == nil || r.bodyAdmission == nil {
		return exchangecontent.SourceLimits{}, false
	}
	return r.bodyAdmission.Policy().Content, true
}

// RawEvidence returns retained HTTP evidence. Search and timeline reads expose
// metadata; an explicit reveal returns the original retained payload.
func (r *Runtime) RawEvidence() rawevidence.Reader {
	return r.rawEvidence
}

// RawEvidenceStatistics exposes writer health without reading payload bytes.
// It is useful for diagnostics and for proving that an
// apparently empty read is not an unflushed or degraded writer.
func (r *Runtime) RawEvidenceStatistics() rawevidence.Statistics {
	if r == nil || r.rawEvidence == nil {
		return rawevidence.Statistics{}
	}
	return r.rawEvidence.Statistics()
}

// EvidenceArchive returns the one transactional boundary that can remove a
// Capture evidence graph or clear all retained evidence. Hosts expose it only
// behind an authenticated, confirmed control action.
func (r *Runtime) EvidenceArchive() resourcedeletion.Archive {
	return r.evidenceArchive
}

// EvidenceArchiveBarrier is shared by both Capture creation authorities and
// the whole-archive clear. A Host must expose the destructive action with this
// exact runtime-owned instance so holder discovery cannot race a new Capture.
func (r *Runtime) EvidenceArchiveBarrier() evidencearchive.ClearBarrier {
	return r.archiveBarrier
}

// ConnectionEvents returns the durable body-free connection audit boundary.
func (r *Runtime) ConnectionEvents() *runtimepersistence.ConnectionInspectionReader {
	return r.connectionInspection
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

func (r *Runtime) AccountReads() *accountoperation.Reader { return r.accountReads }

// CodexOAuthAccounts exposes only safe managed-account inspection. Token bytes
// remain in the Host-selected SecretStore.
func (r *Runtime) CodexOAuthAccounts() codexoauth.Inspector {
	return r.codexOAuth
}

func (r *Runtime) CodexOAuthLogins() codexoauth.LoginController {
	return r.codexLogins
}

// CodeLibrary returns the published immutable Transform revision authority.
func (r *Runtime) CodeLibrary() codelibrary.Controller {
	return r.codeLibrary
}

// EgressProfiles returns the reusable immutable network-exit authority.
func (r *Runtime) EgressProfiles() egressprofile.Controller {
	return r.egressProfiles
}

// VLESSProfiles accepts private imports at the existing owner-control boundary.
func (r *Runtime) VLESSProfiles() vlessprofile.Importer { return r.vlessProfiles }

// EgressDefaults is the instance-owned global configuration, not send authority.
func (r *Runtime) EgressDefaults() egressprofile.Defaults { return r.egressDefaults }

func (r *Runtime) ProductUpdates() *productupdate.Reader { return r.productUpdates }

// UpstreamEndpoints returns the reusable upstream authority. Every managed
// ProviderAccount belongs to exactly one Endpoint exposed by this controller.
func (r *Runtime) UpstreamEndpoints() upstreamendpoint.Controller {
	return r.endpoints
}

// FetchEndpointModels discovers models through the same Hold, audit, and
// transport authority as every other ProductRuntime egress.
func (r *Runtime) FetchEndpointModels(
	ctx context.Context,
	endpoint upstreamendpoint.Endpoint,
	credential providerauth.Lease,
) (*http.Response, error) {
	if r == nil || r.provider == nil {
		return nil, errors.New("provider transport is unavailable")
	}
	return r.provider.FetchEndpointModels(ctx, endpoint, credential)
}

// FetchModelsDev reads the fixed metadata directory through ProductRuntime.
func (r *Runtime) FetchModelsDev(ctx context.Context) (*http.Response, error) {
	if r == nil || r.provider == nil {
		return nil, errors.New("provider transport is unavailable")
	}
	return r.provider.FetchModelsDev(ctx)
}

func (r *Runtime) FetchModelsDevPrices(ctx context.Context) (*http.Response, error) {
	if r == nil || r.provider == nil {
		return nil, errors.New("provider transport is unavailable")
	}
	return r.provider.FetchModelsDevPrices(ctx)
}

// NativeToolRepository and NativeToolPreviews expose the same Store and live
// executor used by captured native traffic to authenticated host composition.
func (r *Runtime) NativeToolRepository() nativetool.AuthorityRepository {
	return r.storage.NativeToolRepository()
}
func (r *Runtime) NativeToolPreviews() nativemcp.Executor { return r.nativeTools }
func (r *Runtime) NativeToolContentKey() [32]byte         { return r.nativeContentKey }

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

// SignServerCertificate initializes an attached host's TLS identity at startup.
// It is not exposed as a management API. The shared Root key remains in localca.
func (r *Runtime) SignServerCertificate(ctx context.Context, publicKey *ecdsa.PublicKey, hosts []string) ([]byte, error) {
	return r.localCA.SignServerCertificate(ctx, publicKey, hosts)
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

// RuntimeUsers owns server-created human identities and their revocable Login
// Sessions. It is deliberately separate from upstream Provider Accounts.
func (r *Runtime) RuntimeUsers() *runtimeuser.Manager {
	if r == nil {
		return nil
	}
	return r.runtimeUsers
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
	r.BeginShutdown()

	select {
	case <-r.shutdownDone:
		return r.shutdownErr
	default:
	}
	select {
	case <-r.shutdownDone:
		return r.shutdownErr
	case <-r.shutdownBudgetDone:
		return fmt.Errorf("ProductRuntime shutdown budget: %w", context.DeadlineExceeded)
	case <-ctx.Done():
		return fmt.Errorf("wait for ProductRuntime shutdown: %w", ctx.Err())
	}
}

// BeginShutdown starts the existing cleanup owner without joining it. Host
// rollback uses this before joining handlers that may need Runtime cancellation.
func (r *Runtime) BeginShutdown() {
	r.shutdownOnce.Do(func() {
		r.status.beginStopping()
		go r.executeShutdown()
	})
}

func (r *Runtime) executeShutdown() {
	shutdownContext, cancel := context.WithTimeout(
		context.Background(),
		r.timeout.ShutdownTimeout,
	)
	var stopBudget func() bool
	var budgetCallbackDone chan struct{}
	if r.shutdownBudgetDone != nil {
		budgetCallbackDone = make(chan struct{})
		stopBudget = context.AfterFunc(shutdownContext, func() {
			defer close(budgetCallbackDone)
			if r.recordingStorage != nil {
				select {
				case <-r.recordingStorage.done:
				default:
					r.status.recordingCleanupPending()
				}
			}
			r.status.finishStopping(r.clock.Now(), context.DeadlineExceeded)
			close(r.shutdownBudgetDone)
		})
	}
	if r.egressCompletion != nil {
		r.egressCompletion.beginShutdown(shutdownContext)
	}
	cleanupErr := r.cleanups.shutdown(shutdownContext)
	// Stop/join the budget callback before our normal cancellation and final
	// publication. shutdownErr is written only here, before shutdownDone closes.
	if stopBudget != nil && !stopBudget() {
		<-budgetCallbackDone
		cleanupErr = errors.Join(cleanupErr, shutdownContext.Err())
	}
	cancel()
	var durabilityErr error
	if r.egressCompletion != nil {
		r.egressCompletion.finishShutdown()
		durabilityErr = r.egressCompletion.failure()
	}
	r.shutdownOwnershipMu.Lock()
	if r.failedStartHostGuard != nil {
		r.failedStartGuardErr = r.failedStartHostGuard.Release()
	}
	r.shutdownErr = errors.Join(cleanupErr, durabilityErr, r.failedStartGuardErr)
	r.status.finishStopping(r.clock.Now(), r.shutdownErr)
	close(r.shutdownDone)
	r.shutdownOwnershipMu.Unlock()
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

func (r *Runtime) ActivityRecorder() activity.Recorder { return r.activities }
func (r *Runtime) IdentityInspection() activity.IdentityInspectionReader {
	return r.activityInspection.InspectionReader.(*runtimepersistence.ActivityInspectionReader)
}
func (r *Runtime) NativeInspection() nativetool.InspectionReader  { return r.nativeInspection }
func (r *Runtime) ACPInspection() acpobservation.InspectionReader { return r.acpInspection }
func (r *Runtime) ACPMarkers() acpobservation.MarkerReader        { return r.storage.ACPRegistrations() }
func (r *Runtime) CaptureObservations() recording.CaptureObservationReader {
	return r.captureInspection
}

func (r *Runtime) ConversationRepairWriter() activity.ConversationRepairWriter {
	return r.recordingStorage
}

// ShutdownDone closes only after actual owned cleanup, independently of the
// caller/internal budget and historical shutdown error. It conveys no readiness.
func (r *Runtime) ShutdownDone() <-chan struct{} { return r.shutdownDone }
func (r *Runtime) ShutdownCompleted() bool {
	select {
	case <-r.shutdownDone:
		return true
	default:
		return false
	}
}

// HandoffFailedStartHostGuard transfers the one failed Server startup guard to
// this existing shutdown owner. Register before bounded Shutdown. Successful
// hosts continue owning their own guards. Duplicate handoff is idempotent.
func (r *Runtime) HandoffFailedStartHostGuard(guard *instanceguard.Guard) error {
	if r == nil || guard == nil {
		return ErrInvalidOptions
	}
	r.shutdownOwnershipMu.Lock()
	defer r.shutdownOwnershipMu.Unlock()
	if r.failedStartHostGuard != nil {
		if r.failedStartHostGuard != guard {
			return ErrInvalidOptions
		}
		return r.failedStartGuardErr
	}
	if guard.Path() != filepath.Join(r.paths.DataDirectory(), "server.lock") || !guard.OwnsDirectory(r.paths.DataDirectory()) {
		return ErrInvalidOptions
	}
	r.failedStartHostGuard = guard
	select {
	case <-r.shutdownDone:
		r.failedStartGuardErr = guard.Release()
	default:
	}
	return r.failedStartGuardErr
}
