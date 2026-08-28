package productruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/anthropicchat"
	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/clientannotation"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/evidencearchive"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/localca"
	"github.com/vibe-agi/vibermate/internal/loopbackproxy"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/openairesponses"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/originaltransport"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/responseschat"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

type storageBuildRequest struct {
	databasePath string
}

type storageBuildResult struct {
	store *runtimepersistence.Store
	state runtimepersistence.SchemaState
}

func buildStorage(
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
	clock                captureassignment.Clock
	accounts             environment.AccountCatalog
	endpoints            upstreamendpoint.Catalog
}

type environmentRuntime interface {
	environment.Controller
	environment.AccountDeletionGuard
}

type captureAssignmentRuntime interface {
	captureassignment.Controller
	loopbackproxy.CaptureAssignmentAuthority
	environment.CaptureInspector
	BeginShutdown()
	Drain(context.Context) error
	Shutdown(context.Context) error
}

type environmentBuildResult struct {
	environments environmentRuntime
	assignments  captureAssignmentRuntime
}

// historicalEnvironmentResolver keeps active reads lock-free through the
// projection while resolving a Capture's frozen revision from durable history.
// A published Environment therefore changes only Captures launched afterward.
type historicalEnvironmentResolver struct {
	environment.SnapshotProjection
	repository environment.Repository
	compiler   environment.Compiler
	system     environment.EnvironmentSnapshot
}

func (resolver historicalEnvironmentResolver) ResolveRevision(
	ctx context.Context,
	id environment.EnvironmentID,
	revision environment.Revision,
) (environment.EnvironmentSnapshot, error) {
	if ctx == nil || revision == 0 {
		return environment.EnvironmentSnapshot{}, environment.ErrInvalidEnvironment
	}
	if err := ctx.Err(); err != nil {
		return environment.EnvironmentSnapshot{}, err
	}
	if id == environment.SystemTransparentID {
		snapshot := resolver.system
		if snapshot.Revision() != revision {
			return environment.EnvironmentSnapshot{}, fmt.Errorf(
				"%w: environmentId=%q revision=%d",
				environment.ErrEnvironmentNotFound,
				id,
				revision,
			)
		}
		return snapshot, nil
	}
	aggregate, exists, err := resolver.repository.LoadRevision(ctx, id, revision)
	if err != nil {
		return environment.EnvironmentSnapshot{}, err
	}
	if !exists {
		return environment.EnvironmentSnapshot{}, fmt.Errorf(
			"%w: environmentId=%q revision=%d",
			environment.ErrEnvironmentNotFound,
			id,
			revision,
		)
	}
	return resolver.compiler.Compile(aggregate)
}

func buildEnvironment(
	ctx context.Context,
	request environmentBuildRequest,
) (environmentBuildResult, error) {
	compiler, err := productionEnvironmentCompiler(request.accounts, request.endpoints)
	if err != nil {
		return environmentBuildResult{}, fmt.Errorf("build Environment compiler: %w", err)
	}
	system, err := compiler.CompileSystemTransparent()
	if err != nil {
		return environmentBuildResult{}, fmt.Errorf("build system_transparent: %w", err)
	}
	projection := environment.NewAtomicProjection()
	resolver := historicalEnvironmentResolver{
		SnapshotProjection: projection,
		repository:         request.repository,
		compiler:           compiler,
		system:             system,
	}
	assignments, err := captureassignment.NewManager(captureassignment.Options{
		Repository:   request.assignmentRepository,
		Environments: resolver,
		Activity:     request.activity,
		Clock:        request.clock,
	})
	if err != nil {
		return environmentBuildResult{}, fmt.Errorf("build Capture assignment manager: %w", err)
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

func productionEnvironmentCompiler(
	accounts environment.AccountCatalog,
	endpoints upstreamendpoint.Catalog,
) (environment.Compiler, error) {
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
		responseschat.ResponsesPassthroughCodecPairID,
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
				Revision:        responseschat.ResponsesPassthroughCodecRevision,
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
	return environment.NewCompiler(accounts, endpoints, protocols, wires)
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

func buildActivity(
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

func buildExchangeContent(
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

func buildConnectionEvent(
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

func buildApproval(
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

func buildMonitor(request monitorBuildRequest) (ownedComponent, error) {
	return newStorageHealthMonitor(request)
}

type providerBuildRequest struct {
	coordinator offlinehold.Coordinator
	secrets     secretstore.Reader
	instanceIDs InstanceIDSource
	audit       egressaudit.Writer
	rawEvidence rawevidence.Observer
}

type providerRuntime interface {
	exchange.Provider
	FetchEndpointModels(
		context.Context,
		upstreamendpoint.Endpoint,
		providerauth.Lease,
	) (*http.Response, error)
	FetchModelsDev(context.Context) (*http.Response, error)
	Shutdown(context.Context) error
}

func buildProvider(
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
		request.instanceIDs,
		request.audit,
		request.rawEvidence,
	)
}

type rawEvidenceBuildRequest struct {
	ctx        context.Context
	repository rawevidence.Repository
	random     io.Reader
	clock      rawevidence.Clock
	config     rawevidence.Config
}

type rawEvidenceRuntime interface {
	rawevidence.RequestRecorder
	rawevidence.Reader
	Flush(context.Context, rawevidence.Watermark) error
	FlushScope(context.Context, rawevidence.ScopeKind, string) error
	Statistics() rawevidence.Statistics
	Shutdown(context.Context) error
}

type captureEvidenceBarrier struct {
	raw      rawEvidenceRuntime
	reporter interface{ ReportRawEvidenceFailure(error) }
}

func (barrier captureEvidenceBarrier) PrepareManagedRun(
	ctx context.Context,
	id string,
) (capturerun.TerminalEvidence, error) {
	terminal, err := barrier.raw.PrepareTerminalScope(
		ctx, rawevidence.ScopeManagedRun, id,
	)
	if err != nil {
		if barrier.reporter != nil {
			barrier.reporter.ReportRawEvidenceFailure(fmt.Errorf(
				"flush managed run %q Raw evidence: %w", id, err,
			))
		}
		return nil, nil
	}
	return terminal, nil
}

func (barrier captureEvidenceBarrier) PrepareManualCapture(
	ctx context.Context,
	id string,
) (manualcapture.TerminalEvidence, error) {
	terminal, err := barrier.raw.PrepareTerminalScope(
		ctx, rawevidence.ScopeManualCapture, id,
	)
	if err != nil {
		if barrier.reporter != nil {
			barrier.reporter.ReportRawEvidenceFailure(fmt.Errorf(
				"flush manual capture %q Raw evidence: %w", id, err,
			))
		}
		return nil, nil
	}
	return terminal, nil
}

func buildRawEvidence(
	request rawEvidenceBuildRequest,
) (rawEvidenceRuntime, error) {
	return rawevidence.Open(request.ctx, rawevidence.Options{
		Repository: request.repository,
		Random:     request.random,
		Clock:      request.clock,
		Config:     request.config,
	})
}

type exchangeBuildRequest struct {
	ownerContext             context.Context
	actions                  offlinehold.ActionAdmission
	accounts                 exchange.AccountLeaseAuthority
	provider                 exchange.Provider
	toolDecisions            exchange.ToolDecisionGate
	activities               activity.Recorder
	identities               activity.ConversationIdentityRepository
	contents                 exchangecontent.Recorder
	clock                    Clock
	hold                     exchange.HoldPolicy
	annotations              *clientannotation.Signer
	rawEvidence              rawevidence.Observer
	reportRawEvidenceFailure func(error)
}

type exchangeRuntime interface {
	exchange.Executor
	BeginShutdown()
	Drain(context.Context) error
	Shutdown(context.Context) error
}

type activityAttemptObserver struct {
	recorder   activity.Recorder
	identities activity.ConversationIdentityRepository
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

func (observer activityAttemptObserver) ObserveStart(
	ctx context.Context,
	observation exchange.StartObservation,
) error {
	if observer.recorder == nil {
		return errors.New("Exchange Activity recorder is nil")
	}
	source, err := exchangeActivitySource(
		observation.Admission,
		observation.HasAdmission,
	)
	if err != nil {
		return err
	}
	record, err := observer.recorder.Record(ctx, activity.Event{
		Kind:                   activity.KindExchangeStarted,
		EnvironmentID:          observation.EnvironmentID,
		EnvironmentRevision:    observation.EnvironmentRevision,
		EnvironmentDigest:      observation.EnvironmentDigest,
		ClientEndpointID:       observation.EndpointID,
		ClientEndpointRevision: observation.EndpointRevision,
		ProtocolPlanID:         observation.ProtocolPlanID,
		ProtocolPlanRevision:   observation.ProtocolPlanRevision,
		RouteID:                observation.RouteID,
		RouteRevision:          observation.RouteRevision,
		SubjectID:              observation.ExchangeID,
		Status:                 activity.StatusPending,
		SourceKind:             source.kind,
		SourceDisplayName:      source.displayName,
		SourceRecognition:      source.recognition,
		CaptureRunID:           source.captureRunID,
		ManualCaptureID:        source.manualCaptureID,
		ConnectionID:           observation.ConnectionID,
		Conversation:           observation.Conversation,
	})
	if err != nil {
		return err
	}
	return observer.persistProtocolIdentity(
		ctx,
		observation.ExchangeID,
		observation.ClientProtocolEvidence,
		"",
		record.OccurredAt,
	)
}

func (observer activityAttemptObserver) ObserveTerminal(
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
	source, err := exchangeActivitySource(
		observation.Admission,
		observation.HasAdmission,
	)
	if err != nil {
		return err
	}
	// The reason stays one stable code. The evidence beside it travels as its
	// own typed fields: a reason with facts glued onto its end cannot be
	// mapped to copy, matched by a rule, or told apart from a reason that
	// happens to contain the same words.
	record, err := observer.recorder.Record(ctx, activity.Event{
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
		SourceKind:             source.kind,
		SourceDisplayName:      source.displayName,
		SourceRecognition:      source.recognition,
		CaptureRunID:           source.captureRunID,
		ManualCaptureID:        source.manualCaptureID,
		ConnectionID:           observation.ConnectionID,
		Conversation:           observation.Conversation,
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
	if err != nil {
		return err
	}
	return observer.persistProtocolIdentity(
		ctx,
		observation.ExchangeID,
		observation.ClientProtocolEvidence,
		observation.ProviderResponseID,
		record.OccurredAt,
	)
}

func (observer activityAttemptObserver) persistProtocolIdentity(
	ctx context.Context,
	exchangeID string,
	evidence []protocolcore.ProtocolEvidenceValue,
	providerResponseID string,
	observedAt time.Time,
) error {
	if len(evidence) == 0 {
		return nil
	}
	if observer.identities == nil {
		return errors.New("Exchange Agent identity repository is nil")
	}
	identity, found := agentconversation.ClientIdentityFromProtocolEvidence(
		evidence,
		providerResponseID,
		observedAt,
	)
	if !found {
		return nil
	}
	return observer.identities.PutConversationIdentity(ctx, exchangeID, identity)
}

type exchangeActivitySourceEvidence struct {
	kind            activity.SourceKind
	displayName     string
	recognition     activity.SourceRecognition
	captureRunID    string
	manualCaptureID string
}

func exchangeActivitySource(
	admission captureadmission.Admission,
	hasAdmission bool,
) (exchangeActivitySourceEvidence, error) {
	evidence := exchangeActivitySourceEvidence{
		kind:        activity.SourceSystemProxy,
		displayName: "ViberMate runtime",
		recognition: activity.SourceRecognitionUnknown,
	}
	if !hasAdmission {
		return evidence, nil
	}
	if err := admission.Validate(); err != nil {
		return exchangeActivitySourceEvidence{}, errors.New(
			"Exchange capture admission is invalid",
		)
	}
	evidence.displayName = admission.SourceLabel()
	switch admission.AttributionConfidence() {
	case captureadmission.AttributionVerified:
		evidence.recognition = activity.SourceRecognitionVerified
	case captureadmission.AttributionConfigured:
		evidence.recognition = activity.SourceRecognitionConfigured
	default:
		return exchangeActivitySourceEvidence{}, errors.New(
			"Exchange source recognition is invalid",
		)
	}
	switch admission.Kind() {
	case captureadmission.KindManagedRun:
		evidence.kind = activity.SourceCaptureRun
		evidence.captureRunID, _ = admission.CaptureRunID()
	case captureadmission.KindManual:
		evidence.kind = activity.SourceManualProxy
		evidence.manualCaptureID, _ = admission.ManualCaptureID()
	default:
		return exchangeActivitySourceEvidence{}, errors.New(
			"Exchange source kind is invalid",
		)
	}
	return evidence, nil
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

func buildExchange(
	request exchangeBuildRequest,
) (exchangeRuntime, error) {
	if request.activities == nil || request.identities == nil ||
		request.contents == nil || request.clock == nil || request.annotations == nil {
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
	responsesPassthroughPath, err := responseschat.NewResponsesPassthroughProtocolPath(
		openairesponses.DefaultOptions(),
	)
	if err != nil {
		return nil, fmt.Errorf("build Responses passthrough protocol path: %w", err)
	}
	protocolPaths, err := protocolpath.NewSelector(
		anthropicPath,
		responsesPath,
		messagesPath,
		responsesPassthroughPath,
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
			recorder: request.activities, identities: request.identities,
		},
		ContentObserver: exchangeContentObserver{
			recorder: request.contents,
			clock:    request.clock,
		},
		ObservationTimeout:       2 * time.Second,
		Hold:                     request.hold,
		Stream:                   exchange.DefaultStreamBudgets(),
		ClientAnnotations:        request.annotations,
		Now:                      request.clock.Now,
		RawEvidence:              request.rawEvidence,
		ReportRawEvidenceFailure: request.reportRawEvidenceFailure,
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

func buildOriginal(
	request originalBuildRequest,
) (originalRuntime, error) {
	return originaltransport.NewProduction(request.coordinator, request.audit)
}

type captureBuildRequest struct {
	repository     capturerun.Repository
	clock          capturerun.Clock
	random         io.Reader
	barrier        capturerun.EvidenceBarrier
	archiveBarrier evidencearchive.CaptureCreationBarrier
}

type captureRuntime interface {
	capturerun.Controller
	capturerun.Reader
	capturerun.ProxyAuthorizer
	BeginShutdown()
	Drain(context.Context) error
	Shutdown(context.Context) error
}

func buildCapture(
	ctx context.Context,
	request captureBuildRequest,
) (captureRuntime, error) {
	options := capturerun.DefaultOptions(request.repository)
	options.Clock = request.clock
	options.Random = request.random
	options.EvidenceBarrier = request.barrier
	options.ArchiveBarrier = request.archiveBarrier
	return capturerun.NewManager(ctx, options)
}

type manualCaptureBuildRequest struct {
	repository     manualcapture.Repository
	clock          manualcapture.Clock
	random         io.Reader
	barrier        manualcapture.EvidenceBarrier
	archiveBarrier evidencearchive.CaptureCreationBarrier
}

type manualCaptureRuntime interface {
	manualcapture.Controller
	manualcapture.ProxyAuthorizer
	BeginShutdown()
	Drain(context.Context) error
	Shutdown(context.Context) error
}

func buildManualCapture(
	ctx context.Context,
	request manualCaptureBuildRequest,
) (manualCaptureRuntime, error) {
	options := manualcapture.DefaultOptions(request.repository)
	options.Clock = request.clock
	options.Random = request.random
	options.EvidenceBarrier = request.barrier
	options.ArchiveBarrier = request.archiveBarrier
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
	Certificate() localca.RootCertificate
	Shutdown(context.Context) error
}

func buildLocalCA(
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
	rawEvidence  rawevidence.RequestRecorder
	random       io.Reader
}

type proxyRuntime interface {
	http.Handler
	BeginShutdown()
	Drain(context.Context) error
	Shutdown(context.Context) error
}

func buildProxy(
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
		RawEvidence:  request.rawEvidence,
		ExchangeIDs: loopbackproxy.NewRandomExchangeIDSource(
			request.random,
		),
	})
}

func wrapOptionalError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
