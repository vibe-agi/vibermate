package productruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/accesscredential"
	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/anthropicchat"
	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/certidentity"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/localca"
	"github.com/vibe-agi/vibermate/internal/loopbackproxy"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/originaltransport"
	"github.com/vibe-agi/vibermate/internal/pathcapability"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/responseschat"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
	"github.com/vibe-agi/vibermate/internal/workspaceroute"
)

type storageBuildRequest struct {
	databasePath string
}

type storageBuildResult struct {
	store runtimepersistence.RuntimeStore
	state runtimepersistence.SchemaState
}

type storageBuilder interface {
	Build(context.Context, storageBuildRequest) (storageBuildResult, error)
}

type productionStorageBuilder struct{}

func (productionStorageBuilder) Build(
	ctx context.Context,
	request storageBuildRequest,
) (storageBuildResult, error) {
	store, err := runtimepersistence.Open(ctx, runtimepersistence.Options{
		DatabasePath:           request.databasePath,
		BusyTimeout:            runtimepersistence.DefaultBusyTimeout,
		CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout,
	})
	if err != nil {
		return storageBuildResult{}, err
	}
	state, err := store.SchemaStateReader().ReadSchemaState(ctx)
	if err != nil {
		closeErr := store.Shutdown(context.Background())
		return storageBuildResult{}, errors.Join(
			fmt.Errorf("read storage build state: %w", err),
			wrapOptionalError("close storage after build failure", closeErr),
		)
	}
	return storageBuildResult{store: store, state: state}, nil
}

type accessBuildRequest struct {
	repository   access.Repository
	rootRevision certidentity.RootRevision
	leafCache    access.LeafCacheInvalidator
}

type accessRuntime interface {
	access.AggregateCatalog
	access.Writer
	access.SnapshotResolver
	access.IngressResolver
	access.DownstreamProtocolResolver
	access.LeafIssuanceAdmitter
	access.IngressCatalogReader
	access.ProviderProbeCatalog
	access.ProjectionHealthReader
	Shutdown(context.Context) error
}

type accessBuilder interface {
	Build(context.Context, accessBuildRequest) (accessRuntime, error)
}

type productionAccessBuilder struct{}

func (productionAccessBuilder) Build(
	ctx context.Context,
	request accessBuildRequest,
) (accessRuntime, error) {
	compiler, err := productionAccessPlanCompiler()
	if err != nil {
		return nil, fmt.Errorf("build Access plan compiler: %w", err)
	}
	projection, err := access.NewSnapshotProjection(
		request.rootRevision,
		request.leafCache,
	)
	if err != nil {
		return nil, fmt.Errorf("build Access projection: %w", err)
	}
	return access.NewManager(ctx, request.repository, compiler, projection)
}

type credentialBuildRequest struct {
	resolver access.SnapshotResolver
	secrets  secretstore.Store
}

type credentialRuntime interface {
	accesscredential.Controller
}

type credentialBuilder interface {
	Build(credentialBuildRequest) (credentialRuntime, error)
}

type productionCredentialBuilder struct{}

func (productionCredentialBuilder) Build(
	request credentialBuildRequest,
) (credentialRuntime, error) {
	return accesscredential.New(request.resolver, request.secrets)
}

func productionAccessPlanCompiler() (*access.Compiler, error) {
	operations, err := operationcatalog.BuiltIn()
	if err != nil {
		return nil, fmt.Errorf("build client operation catalog: %w", err)
	}
	anthropicCodecPairID, err := access.NewCodecPairID(
		anthropicchat.CodecPairID,
	)
	if err != nil {
		return nil, err
	}
	responsesCodecPairID, err := access.NewCodecPairID(
		responseschat.CodecPairID,
	)
	if err != nil {
		return nil, err
	}
	messagesCodecPairID, err := access.NewCodecPairID(
		anthropicchat.MessagesCodecPairID,
	)
	if err != nil {
		return nil, err
	}
	catalog, err := access.NewCatalog(access.CatalogOptions{
		Capabilities: access.PlanCapabilities{
			// A RouteSet may name a second upstream so a dropped attempt has
			// somewhere to go. Whether one is used is the plan's fallback
			// policy, not this limit.
			MaxEndpointProfiles:          access.MaxEndpointProfiles,
			MaxAccountBindings:           access.MaxEndpointProfiles,
			MaxRouteSets:                 1,
			AllowMultipleRouteCandidates: true,
		},
		ClientOperations: operations.Definitions(),
		CodecPairs: []access.CodecPairDefinition{
			{
				ID:              anthropicCodecPairID,
				Revision:        anthropicchat.CodecRevision,
				ClientDialect:   access.DialectAnthropicMessages,
				ProviderDialect: access.DialectOpenAIChat,
				ClientOperationIDs: operations.SemanticOperationIDs(
					access.DialectAnthropicMessages,
				),
				RequiredCapabilities: []access.ProviderCapability{
					access.ProviderCapabilityMessages,
					access.ProviderCapabilityStreaming,
					access.ProviderCapabilityToolCalls,
				},
			},
			{
				ID:              messagesCodecPairID,
				Revision:        anthropicchat.MessagesCodecRevision,
				ClientDialect:   access.DialectAnthropicMessages,
				ProviderDialect: access.DialectAnthropicMessages,
				ClientOperationIDs: operations.SemanticOperationIDs(
					access.DialectAnthropicMessages,
				),
				RequiredCapabilities: []access.ProviderCapability{
					access.ProviderCapabilityMessages,
					access.ProviderCapabilityStreaming,
					access.ProviderCapabilityToolCalls,
				},
			},
			{
				ID:              responsesCodecPairID,
				Revision:        responseschat.CodecRevision,
				ClientDialect:   access.DialectOpenAIResponses,
				ProviderDialect: access.DialectOpenAIChat,
				ClientOperationIDs: operations.SemanticOperationIDs(
					access.DialectOpenAIResponses,
				),
				RequiredCapabilities: []access.ProviderCapability{
					access.ProviderCapabilityMessages,
					access.ProviderCapabilityStreaming,
					access.ProviderCapabilityToolCalls,
				},
			},
		},
		AuthDrivers: []access.AuthDriverDefinition{
			{
				Ref:      access.StaticHeaderAuthDriverRef(),
				Revision: 1,
			},
			{
				Ref:      access.AnthropicAPIKeyAuthDriverRef(),
				Revision: 1,
			},
		},
		EgressModes: []access.EgressModeDefinition{{
			Mode:     access.EgressModeDirect,
			Revision: 1,
		}},
		PluginPlanModes: []access.PluginPlanModeDefinition{{
			Mode:     access.PluginPlanModePassThrough,
			Revision: 1,
		}},
		ModelPolicyModes: []access.ModelPolicyModeDefinition{{
			Mode:     access.ModelPolicyModeFixed,
			Revision: 1,
		}},
		TransportProfiles:    access.BuiltInTransportFingerprintDefinitions(),
		UpstreamWireProfiles: access.BuiltInUpstreamWireProfileDefinitions(),
	})
	if err != nil {
		return nil, err
	}
	return access.NewCompiler(catalog)
}

type monitorBuildRequest struct {
	ownerContext context.Context
	reader       runtimepersistence.SchemaStateReader
	interval     time.Duration
	observe      func(runtimepersistence.SchemaState, error)
}

type activityBuildRequest struct {
	repository activity.Repository
	clock      activity.Clock
	random     io.Reader
}

type activityRuntime interface {
	activity.Runtime
}

type activityBuilder interface {
	Build(activityBuildRequest) (activityRuntime, error)
}

type productionActivityBuilder struct{}

func (productionActivityBuilder) Build(
	request activityBuildRequest,
) (activityRuntime, error) {
	return activity.New(activity.Options{
		Repository: request.repository,
		Clock:      request.clock,
		Random:     request.random,
	})
}

type connectionEventBuildRequest struct {
	repository connectionevent.Repository
	clock      connectionevent.Clock
	random     io.Reader
}

type connectionEventRuntime interface {
	connectionevent.Runtime
}

type connectionEventBuilder interface {
	Build(context.Context, connectionEventBuildRequest) (connectionEventRuntime, error)
}

type productionConnectionEventBuilder struct{}

func (productionConnectionEventBuilder) Build(
	ctx context.Context,
	request connectionEventBuildRequest,
) (connectionEventRuntime, error) {
	return connectionevent.New(ctx, connectionevent.Options{
		Repository: request.repository,
		Clock:      request.clock,
		Random:     request.random,
	})
}

type approvalBuildRequest struct {
	ctx        context.Context
	repository toolapproval.Repository
	clock      toolapproval.Clock
	random     io.Reader
	config     toolapproval.Config
	remembered toolapproval.RememberedListener
}

type approvalRuntime interface {
	exchange.ToolDecisionGate
	toolapproval.Controller
	loopbackproxy.NetworkApprovals
	ClientRootApprover
	Shutdown(context.Context) error
}

// ClientRootApprover decides whether a client recognized by its publisher,
// rather than by a catalogued build, may be handed the local Root. It is
// declared here rather than imported from the CaptureRun control package so
// that the runtime does not depend on one of its own hosts.
type ClientRootApprover interface {
	AskClientRoot(
		context.Context,
		toolapproval.ClientRootAskRequest,
	) (toolapproval.ClientRootAskOutcome, error)
}

type approvalBuilder interface {
	Build(approvalBuildRequest) (approvalRuntime, error)
}

type productionApprovalBuilder struct{}

func (productionApprovalBuilder) Build(
	request approvalBuildRequest,
) (approvalRuntime, error) {
	return toolapproval.New(request.ctx, toolapproval.Options{
		Repository: request.repository,
		Clock:      request.clock,
		Random:     request.random,
		Config:     request.config,
		Remembered: request.remembered,
	})
}

type ownedComponent interface {
	Shutdown(context.Context) error
}

type monitorBuilder interface {
	Build(monitorBuildRequest) (ownedComponent, error)
}

type productionMonitorBuilder struct{}

func (productionMonitorBuilder) Build(request monitorBuildRequest) (ownedComponent, error) {
	return newStorageHealthMonitor(request)
}

type providerBuildRequest struct {
	coordinator offlinehold.Coordinator
	secrets     secretstore.Reader
	audit       egressaudit.Writer
}

type providerRuntime interface {
	exchange.Provider
	Shutdown(context.Context) error
}

type providerBuilder interface {
	Build(providerBuildRequest) (providerRuntime, error)
}

type productionProviderBuilder struct{}

func (productionProviderBuilder) Build(
	request providerBuildRequest,
) (providerRuntime, error) {
	bearerAuthenticator, err := providertransport.NewStaticBearerAuthenticator(
		request.secrets,
	)
	if err != nil {
		return nil, fmt.Errorf("build static bearer AuthDriver: %w", err)
	}
	anthropicAuthenticator, err := providertransport.NewAnthropicAPIKeyAuthenticator(
		request.secrets,
	)
	if err != nil {
		return nil, fmt.Errorf("build Anthropic API-key AuthDriver: %w", err)
	}
	return providertransport.NewProductionClientWithAuthenticators(
		request.coordinator,
		[]providertransport.Authenticator{
			bearerAuthenticator,
			anthropicAuthenticator,
		},
		providertransport.DefaultTransportTimeouts(),
		request.audit,
	)
}

type exchangeBuildRequest struct {
	ownerContext    context.Context
	actions         offlinehold.ActionAdmission
	resolver        access.SnapshotResolver
	workspaceRoutes workspaceroute.Resolver
	provider        exchange.Provider
	toolDecisions   exchange.ToolDecisionGate
	activities      activity.Recorder
	hold            exchange.HoldPolicy
}

type exchangeRuntime interface {
	exchange.Executor
	BeginShutdown()
	Drain(context.Context) error
	Shutdown(context.Context) error
}

type exchangeBuilder interface {
	Build(exchangeBuildRequest) (exchangeRuntime, error)
}

type productionExchangeBuilder struct{}

type activityAttemptObserver struct {
	recorder activity.Recorder
}

func (observer activityAttemptObserver) Observe(
	ctx context.Context,
	observation exchange.AttemptObservation,
) error {
	if observer.recorder == nil {
		return errors.New("Exchange Activity recorder is nil")
	}
	status := activity.StatusFailed
	switch observation.Outcome {
	case exchange.AttemptSucceeded:
		status = activity.StatusSucceeded
	case exchange.AttemptCanceled:
		status = activity.StatusCanceled
	case exchange.AttemptFailed, exchange.AttemptAborted:
	default:
		return errors.New("Exchange observation outcome is invalid")
	}
	// The reason stays one stable code. The evidence beside it travels as its
	// own typed fields: a reason with facts glued onto its end cannot be
	// mapped to copy, matched by a rule, or told apart from a reason that
	// happens to contain the same words.
	_, err := observer.recorder.Record(ctx, activity.Event{
		Kind:       activity.KindExchangeCompleted,
		AccessID:   observation.AccessID,
		SubjectID:  observation.ExchangeID,
		Status:     status,
		ReasonCode: string(observation.ReasonCode),
		Diagnosis: activity.Diagnosis{
			ProviderStatus: observation.ProviderStatus,
			ProviderField:  string(observation.ProviderField),
			ClientField:    string(observation.ClientField),
			ClientPath:     observation.ClientPath,
		},
		Transport: activityTransportEvidence(
			observation.Presentation,
			observation.Transport,
		),
	})
	return err
}

func activityTransportEvidence(
	presentation providertransport.WirePresentationEvidence,
	evidence transportprofile.Evidence,
) *activity.TransportEvidence {
	requested := evidence.Requested()
	if (requested.Ref == "" || requested.Revision == 0) &&
		presentation.RequestedRef == "" {
		return nil
	}
	profile := func(
		value transportprofile.ProfileEvidence,
	) activity.TransportProfileEvidence {
		return activity.TransportProfileEvidence{
			Ref:      value.Ref,
			Revision: uint64(value.Revision),
			Source:   string(value.Source),
		}
	}
	converted := &activity.TransportEvidence{}
	if requested.Ref != "" && requested.Revision != 0 {
		value := profile(requested)
		converted.Requested = &value
		converted.FallbackReason = string(evidence.FallbackReason())
		converted.ClientOfferedALPN = evidence.ClientOfferedALPN()
		converted.DownstreamNegotiatedALPN = evidence.DownstreamNegotiatedALPN()
		converted.UpstreamOfferedALPN = evidence.UpstreamOfferedALPN()
		converted.UpstreamNegotiatedALPN = evidence.UpstreamNegotiatedALPN()
		converted.HTTPTransport = string(evidence.HTTPTransport())
	}
	if presentation.RequestedRef != "" {
		converted.Presentation = &activity.WirePresentationEvidence{
			RequestedRef:     presentation.RequestedRef,
			EffectiveRef:     presentation.EffectiveRef,
			Revision:         uint64(presentation.Revision),
			Mode:             string(presentation.Mode),
			Product:          string(presentation.Product),
			ClientProtocol:   string(presentation.ClientProtocol),
			UpstreamProtocol: string(presentation.UpstreamProtocol),
			EvidenceDigest:   presentation.EvidenceDigest,
		}
	}
	for _, attempted := range evidence.FallbackChain() {
		converted.FallbackChain = append(
			converted.FallbackChain,
			profile(attempted),
		)
	}
	effective := evidence.Effective()
	if effective.Ref != "" && effective.Revision != 0 {
		value := profile(effective)
		converted.Effective = &value
	}
	return converted
}

func (productionExchangeBuilder) Build(
	request exchangeBuildRequest,
) (exchangeRuntime, error) {
	if request.activities == nil {
		return nil, errors.New("Exchange Activity recorder is nil")
	}
	anthropicPath, err := anthropicchat.NewProtocolPath(
		anthropicchat.DefaultOptions(),
	)
	if err != nil {
		return nil, fmt.Errorf("build Anthropic Chat protocol path: %w", err)
	}
	responsesPath, err := responseschat.NewProtocolPath(
		responseschat.DefaultOptions(),
	)
	if err != nil {
		return nil, fmt.Errorf("build Responses Chat protocol path: %w", err)
	}
	messagesPath, err := anthropicchat.NewMessagesProtocolPath(
		anthropicchat.DefaultOptions(),
	)
	if err != nil {
		return nil, fmt.Errorf("build Anthropic Messages protocol path: %w", err)
	}
	protocolPaths, err := protocolpath.NewSelector(
		anthropicPath,
		responsesPath,
		messagesPath,
	)
	if err != nil {
		return nil, fmt.Errorf("build protocol path selector: %w", err)
	}
	return exchange.New(exchange.Options{
		OwnerContext:    request.ownerContext,
		Actions:         request.actions,
		Resolver:        request.resolver,
		WorkspaceRoutes: request.workspaceRoutes,
		ProtocolPaths:   protocolPaths,
		Provider:        request.provider,
		ToolDecisions:   request.toolDecisions,
		RetryWaiter:     exchange.TimerRetryWaiter{},
		Observer: activityAttemptObserver{
			recorder: request.activities,
		},
		ObservationTimeout: 2 * time.Second,
		Hold:               request.hold,
		Stream:             exchange.DefaultStreamBudgets(),
	})
}

type originalBuildRequest struct {
	coordinator offlinehold.Coordinator
	audit       egressaudit.Writer
}

type originalRuntime interface {
	loopbackproxy.OriginalClient
	Shutdown(context.Context) error
}

type originalBuilder interface {
	Build(originalBuildRequest) (originalRuntime, error)
}

type productionOriginalBuilder struct{}

func (productionOriginalBuilder) Build(
	request originalBuildRequest,
) (originalRuntime, error) {
	return originaltransport.NewProduction(request.coordinator, request.audit)
}

type captureBuildRequest struct {
	repository capturerun.Repository
	clock      capturerun.Clock
	random     io.Reader
}

type captureRuntime interface {
	capturerun.Controller
	capturerun.Reader
	capturerun.ProxyAuthorizer
	BeginShutdown()
	Drain(context.Context) error
	Shutdown(context.Context) error
}

type captureBuilder interface {
	Build(context.Context, captureBuildRequest) (captureRuntime, error)
}

type productionCaptureBuilder struct{}

func (productionCaptureBuilder) Build(
	ctx context.Context,
	request captureBuildRequest,
) (captureRuntime, error) {
	options := capturerun.DefaultOptions(request.repository)
	options.Clock = request.clock
	options.Random = request.random
	return capturerun.NewManager(ctx, options)
}

type manualCaptureBuildRequest struct {
	repository manualcapture.Repository
	clock      manualcapture.Clock
	random     io.Reader
}

type manualCaptureRuntime interface {
	manualcapture.Controller
	manualcapture.ProxyAuthorizer
	BeginShutdown()
	Drain(context.Context) error
	Shutdown(context.Context) error
}

type manualCaptureBuilder interface {
	Build(context.Context, manualCaptureBuildRequest) (manualCaptureRuntime, error)
}

type productionManualCaptureBuilder struct{}

func (productionManualCaptureBuilder) Build(
	ctx context.Context,
	request manualCaptureBuildRequest,
) (manualCaptureRuntime, error) {
	options := manualcapture.DefaultOptions(request.repository)
	options.Clock = request.clock
	options.Random = request.random
	return manualcapture.NewManager(ctx, options)
}

type localCABuildRequest struct {
	ownerContext context.Context
	directory    string
	clock        localca.Clock
	random       io.Reader
}

type localCARuntime interface {
	loopbackproxy.CertificateAuthority
	access.LeafCacheInvalidator
	Certificate() localca.RootCertificate
	Shutdown(context.Context) error
}

type localCABuilder interface {
	Build(context.Context, localCABuildRequest) (localCARuntime, error)
}

type productionLocalCABuilder struct{}

func (productionLocalCABuilder) Build(
	ctx context.Context,
	request localCABuildRequest,
) (localCARuntime, error) {
	options := localca.DefaultOptions(request.directory, request.ownerContext)
	options.Clock = request.clock
	options.Random = request.random
	return localca.Open(ctx, options)
}

type proxyBuildRequest struct {
	ownerContext context.Context
	admissions   captureadmission.Authorizer
	ingress      loopbackproxy.IngressAuthority
	exchanges    exchange.Executor
	original     loopbackproxy.OriginalClient
	certificates loopbackproxy.CertificateAuthority
	connections  connectionevent.Runtime
	policy       connectionpolicy.Source
	approvals    loopbackproxy.NetworkApprovals
	blindTunnels loopbackproxy.BlindTunnelDialer
	egressAudit  egressaudit.Writer
	random       io.Reader
}

type proxyRuntime interface {
	http.Handler
	BeginShutdown()
	Drain(context.Context) error
	Shutdown(context.Context) error
}

type proxyBuilder interface {
	Build(proxyBuildRequest) (proxyRuntime, error)
}

type productionProxyBuilder struct{}

func (productionProxyBuilder) Build(
	request proxyBuildRequest,
) (proxyRuntime, error) {
	operations, err := operationcatalog.BuiltIn()
	if err != nil {
		return nil, fmt.Errorf("build client operation catalog: %w", err)
	}
	paths, err := pathcapability.NewCatalog(operations.Definitions())
	if err != nil {
		return nil, fmt.Errorf("build PathCapability catalog: %w", err)
	}
	return loopbackproxy.New(loopbackproxy.Options{
		OwnerContext: request.ownerContext,
		Admissions:   request.admissions,
		Ingress:      request.ingress,
		Paths:        paths,
		Exchanges:    request.exchanges,
		Original:     request.original,
		Certificates: request.certificates,
		Connections:  request.connections,
		Policy:       request.policy,
		Approvals:    request.approvals,
		BlindTunnels: request.blindTunnels,
		EgressAudit:  request.egressAudit,
		ExchangeIDs: loopbackproxy.NewRandomExchangeIDSource(
			request.random,
		),
	})
}

type runtimeBuilders struct {
	storage       storageBuilder
	access        accessBuilder
	credential    credentialBuilder
	activity      activityBuilder
	connection    connectionEventBuilder
	approval      approvalBuilder
	monitor       monitorBuilder
	provider      providerBuilder
	original      originalBuilder
	exchange      exchangeBuilder
	capture       captureBuilder
	manualCapture manualCaptureBuilder
	localCA       localCABuilder
	proxy         proxyBuilder
}

func productionBuilders() runtimeBuilders {
	return runtimeBuilders{
		storage:       productionStorageBuilder{},
		access:        productionAccessBuilder{},
		credential:    productionCredentialBuilder{},
		activity:      productionActivityBuilder{},
		connection:    productionConnectionEventBuilder{},
		approval:      productionApprovalBuilder{},
		monitor:       productionMonitorBuilder{},
		provider:      productionProviderBuilder{},
		original:      productionOriginalBuilder{},
		exchange:      productionExchangeBuilder{},
		capture:       productionCaptureBuilder{},
		manualCapture: productionManualCaptureBuilder{},
		localCA:       productionLocalCABuilder{},
		proxy:         productionProxyBuilder{},
	}
}

func wrapOptionalError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
