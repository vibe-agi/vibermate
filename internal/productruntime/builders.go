package productruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/anthropicchat"
	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/localca"
	"github.com/vibe-agi/vibermate/internal/loopbackproxy"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/originaltransport"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/responseschat"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
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

type environmentBuildRequest struct {
	repository           environment.Repository
	assignmentRepository captureassignment.Repository
	activity             captureassignment.CaptureActivity
	leafCache            captureassignment.LeafCacheInvalidator
	clock                captureassignment.Clock
	accounts             environment.AccountCatalog
}

type environmentRuntime interface {
	environment.Controller
	environment.AccountDeletionGuard
}

type captureAssignmentRuntime interface {
	captureassignment.Controller
	loopbackproxy.CaptureAssignmentAuthority
	environment.CaptureInspector
	environment.CaptureTransitionCoordinator
	BeginShutdown()
	Drain(context.Context) error
	Shutdown(context.Context) error
}

type environmentBuildResult struct {
	environments environmentRuntime
	assignments  captureAssignmentRuntime
}

type environmentBuilder interface {
	Build(context.Context, environmentBuildRequest) (environmentBuildResult, error)
}

type productionEnvironmentBuilder struct{}

func (productionEnvironmentBuilder) Build(
	ctx context.Context,
	request environmentBuildRequest,
) (environmentBuildResult, error) {
	projection := environment.NewAtomicProjection()
	assignments, err := captureassignment.NewManager(captureassignment.Options{
		Repository: request.assignmentRepository, Environments: projection,
		Activity: request.activity, LeafCacheInvalidator: request.leafCache,
		Clock: request.clock,
	})
	if err != nil {
		return environmentBuildResult{}, fmt.Errorf("build Capture assignment manager: %w", err)
	}
	compiler, err := productionEnvironmentCompiler(request.accounts)
	if err != nil {
		shutdownErr := assignments.Shutdown(context.WithoutCancel(ctx))
		return environmentBuildResult{}, errors.Join(
			fmt.Errorf("build Environment compiler: %w", err),
			wrapOptionalError("close Capture assignment manager", shutdownErr),
		)
	}
	environments, err := environment.NewManager(
		ctx, request.repository, compiler, projection, assignments,
	)
	if err != nil {
		shutdownErr := assignments.Shutdown(context.WithoutCancel(ctx))
		return environmentBuildResult{}, errors.Join(
			fmt.Errorf("recover Environment projection: %w", err),
			wrapOptionalError("close Capture assignment manager", shutdownErr),
		)
	}
	return environmentBuildResult{environments: environments, assignments: assignments}, nil
}

func productionEnvironmentCompiler(accounts environment.AccountCatalog) (environment.Compiler, error) {
	operations, err := operationcatalog.BuiltIn()
	if err != nil {
		return environment.Compiler{}, fmt.Errorf("build client operation catalog: %w", err)
	}
	anthropicCodecPairID, err := protocolspec.NewCodecPairID(anthropicchat.CodecPairID)
	if err != nil {
		return environment.Compiler{}, err
	}
	responsesCodecPairID, err := protocolspec.NewCodecPairID(responseschat.CodecPairID)
	if err != nil {
		return environment.Compiler{}, err
	}
	messagesCodecPairID, err := protocolspec.NewCodecPairID(anthropicchat.MessagesCodecPairID)
	if err != nil {
		return environment.Compiler{}, err
	}
	responsesPassthroughCodecPairID, err := protocolspec.NewCodecPairID(
		"openai-responses-original-passthrough",
	)
	if err != nil {
		return environment.Compiler{}, err
	}
	protocols, err := protocolspec.NewCatalog(
		operations.Definitions(),
		[]protocolspec.CodecPairDefinition{
			{
				ID:              anthropicCodecPairID,
				Revision:        anthropicchat.CodecRevision,
				ClientDialect:   protocolspec.DialectAnthropicMessages,
				ProviderDialect: protocolspec.DialectOpenAIChat,
				ClientOperationIDs: operations.SemanticOperationIDs(
					protocolspec.DialectAnthropicMessages,
				),
				RequiredCapabilities: []protocolspec.ProviderCapability{
					protocolspec.ProviderCapabilityMessages,
					protocolspec.ProviderCapabilityStreaming,
					protocolspec.ProviderCapabilityToolCalls,
				},
			},
			{
				ID:              messagesCodecPairID,
				Revision:        anthropicchat.MessagesCodecRevision,
				ClientDialect:   protocolspec.DialectAnthropicMessages,
				ProviderDialect: protocolspec.DialectAnthropicMessages,
				ClientOperationIDs: operations.SemanticOperationIDs(
					protocolspec.DialectAnthropicMessages,
				),
				RequiredCapabilities: []protocolspec.ProviderCapability{
					protocolspec.ProviderCapabilityMessages,
					protocolspec.ProviderCapabilityStreaming,
					protocolspec.ProviderCapabilityToolCalls,
				},
			},
			{
				ID:              responsesCodecPairID,
				Revision:        responseschat.CodecRevision,
				ClientDialect:   protocolspec.DialectOpenAIResponses,
				ProviderDialect: protocolspec.DialectOpenAIChat,
				ClientOperationIDs: operations.SemanticOperationIDs(
					protocolspec.DialectOpenAIResponses,
				),
				RequiredCapabilities: []protocolspec.ProviderCapability{
					protocolspec.ProviderCapabilityMessages,
					protocolspec.ProviderCapabilityStreaming,
					protocolspec.ProviderCapabilityToolCalls,
				},
			},
			{
				ID:              responsesPassthroughCodecPairID,
				Revision:        1,
				ClientDialect:   protocolspec.DialectOpenAIResponses,
				ProviderDialect: protocolspec.DialectOpenAIResponses,
				ClientOperationIDs: operations.SemanticOperationIDs(
					protocolspec.DialectOpenAIResponses,
				),
				RequiredCapabilities: []protocolspec.ProviderCapability{
					protocolspec.ProviderCapabilityMessages,
					protocolspec.ProviderCapabilityStreaming,
					protocolspec.ProviderCapabilityToolCalls,
				},
			},
		},
	)
	if err != nil {
		return environment.Compiler{}, err
	}
	wires, err := wireprofile.BuiltInCatalog()
	if err != nil {
		return environment.Compiler{}, err
	}
	return environment.NewCompiler(accounts, protocols, wires)
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

type exchangeContentBuildRequest struct {
	ctx        context.Context
	repository exchangecontent.Repository
	clock      exchangecontent.Clock
}

type exchangeContentRuntime interface {
	exchangecontent.Runtime
}

type exchangeContentBuilder interface {
	Build(exchangeContentBuildRequest) (exchangeContentRuntime, error)
}

type productionExchangeContentBuilder struct{}

func (productionExchangeContentBuilder) Build(
	request exchangeContentBuildRequest,
) (exchangeContentRuntime, error) {
	return exchangecontent.New(request.ctx, exchangecontent.Options{
		Repository: request.repository,
		Clock:      request.clock,
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
	ownerContext  context.Context
	actions       offlinehold.ActionAdmission
	accounts      exchange.AccountLeaseAuthority
	provider      exchange.Provider
	toolDecisions exchange.ToolDecisionGate
	activities    activity.Recorder
	contents      exchangecontent.Recorder
	clock         Clock
	hold          exchange.HoldPolicy
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

type exchangeContentObserver struct {
	recorder exchangecontent.Recorder
	clock    Clock
}

func (observer exchangeContentObserver) ObserveContent(
	ctx context.Context,
	observation exchange.ContentObservation,
) error {
	if observer.recorder == nil || observer.clock == nil {
		return errors.New("Exchange content recorder is nil")
	}
	record, err := exchangecontent.NewRecord(
		observation.ExchangeID,
		exchangecontent.FrozenRef{
			EnvironmentID:          observation.EnvironmentID.String(),
			EnvironmentRevision:    uint64(observation.EnvironmentRevision),
			EnvironmentDigest:      observation.EnvironmentDigest,
			ClientEndpointID:       observation.EndpointID.String(),
			ClientEndpointRevision: uint64(observation.EndpointRevision),
			ProtocolPlanID:         observation.ProtocolPlanID.String(),
			ProtocolPlanRevision:   uint64(observation.ProtocolPlanRevision),
			RouteID:                observation.RouteID.String(),
			RouteRevision:          uint64(observation.RouteRevision),
		},
		observation.Recording,
		observer.clock.Now(),
		observation.Request,
		observation.Response,
		exchangecontent.WithParentRef(exchangecontent.ParentRef{
			CaptureRunID:    observation.CaptureRunID,
			ManualCaptureID: observation.ManualCaptureID,
		}),
	)
	if err != nil {
		return err
	}
	return observer.recorder.Record(ctx, record)
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
	sourceKind := activity.SourceSystemProxy
	sourceDisplayName := "ViberMate runtime"
	sourceRecognition := activity.SourceRecognitionUnknown
	captureRunID := ""
	manualCaptureID := ""
	if observation.HasAdmission {
		if err := observation.Admission.Validate(); err != nil {
			return errors.New("Exchange capture admission is invalid")
		}
		sourceDisplayName = observation.Admission.SourceLabel()
		switch observation.Admission.AttributionConfidence() {
		case captureadmission.AttributionVerified:
			sourceRecognition = activity.SourceRecognitionVerified
		case captureadmission.AttributionConfigured:
			sourceRecognition = activity.SourceRecognitionConfigured
		default:
			return errors.New("Exchange source recognition is invalid")
		}
		switch observation.Admission.Kind() {
		case captureadmission.KindManagedRun:
			sourceKind = activity.SourceCaptureRun
			captureRunID, _ = observation.Admission.CaptureRunID()
		case captureadmission.KindManual:
			sourceKind = activity.SourceManualProxy
			manualCaptureID, _ = observation.Admission.ManualCaptureID()
		default:
			return errors.New("Exchange source kind is invalid")
		}
	}
	// The reason stays one stable code. The evidence beside it travels as its
	// own typed fields: a reason with facts glued onto its end cannot be
	// mapped to copy, matched by a rule, or told apart from a reason that
	// happens to contain the same words.
	_, err := observer.recorder.Record(ctx, activity.Event{
		Kind:                   activity.KindExchangeCompleted,
		EnvironmentID:          observation.EnvironmentID,
		EnvironmentRevision:    observation.EnvironmentRevision,
		EnvironmentDigest:      observation.EnvironmentDigest,
		ClientEndpointID:       observation.EndpointID,
		ClientEndpointRevision: observation.EndpointRevision,
		ProtocolPlanID:         observation.ProtocolPlanID,
		ProtocolPlanRevision:   observation.ProtocolPlanRevision,
		RouteID:                observation.RouteID,
		RouteRevision:          observation.RouteRevision,
		AccountID:              observation.AccountID,
		AccountRevision:        observation.AccountRevision,
		CredentialEpoch:        observation.CredentialEpoch,
		SubjectID:              observation.ExchangeID,
		Status:                 status,
		ReasonCode:             string(observation.ReasonCode),
		SourceKind:             sourceKind,
		SourceDisplayName:      sourceDisplayName,
		SourceRecognition:      sourceRecognition,
		CaptureRunID:           captureRunID,
		ManualCaptureID:        manualCaptureID,
		ConnectionID:           observation.ConnectionID,
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
	if request.activities == nil || request.contents == nil || request.clock == nil {
		return nil, errors.New("Exchange evidence dependencies are incomplete")
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
		OwnerContext:  request.ownerContext,
		Actions:       request.actions,
		Accounts:      request.accounts,
		ProtocolPaths: protocolPaths,
		Provider:      request.provider,
		ToolDecisions: request.toolDecisions,
		RetryWaiter:   exchange.TimerRetryWaiter{},
		Observer: activityAttemptObserver{
			recorder: request.activities,
		},
		ContentObserver: exchangeContentObserver{
			recorder: request.contents,
			clock:    request.clock,
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
	captureassignment.LeafCacheInvalidator
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
	assignments  loopbackproxy.CaptureAssignmentAuthority
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
	return loopbackproxy.New(loopbackproxy.Options{
		OwnerContext: request.ownerContext,
		Admissions:   request.admissions,
		Assignments:  request.assignments,
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
	environment   environmentBuilder
	activity      activityBuilder
	content       exchangeContentBuilder
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
		environment:   productionEnvironmentBuilder{},
		activity:      productionActivityBuilder{},
		content:       productionExchangeContentBuilder{},
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
