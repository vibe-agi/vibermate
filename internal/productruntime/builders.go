package productruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/anthropicchat"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originaltransport"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
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
	repository access.Repository
}

type accessRuntime interface {
	access.Writer
	access.SnapshotResolver
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
	projection := access.NewSnapshotProjection()
	return access.NewManager(ctx, request.repository, compiler, projection)
}

func productionAccessPlanCompiler() (*access.Compiler, error) {
	codecPairID, err := access.NewCodecPairID(
		anthropicchat.CodecPairID,
	)
	if err != nil {
		return nil, err
	}
	catalog, err := access.NewCatalog(access.CatalogOptions{
		Capabilities: access.PlanCapabilities{
			MaxEndpointProfiles: 1,
			MaxAccountBindings:  1,
			MaxRouteSets:        1,
		},
		CodecPairs: []access.CodecPairDefinition{{
			ID:              codecPairID,
			Revision:        anthropicchat.CodecRevision,
			ClientDialect:   access.DialectAnthropicMessages,
			ProviderDialect: access.DialectOpenAIChat,
			RequiredCapabilities: []access.ProviderCapability{
				access.ProviderCapabilityMessages,
				access.ProviderCapabilityStreaming,
				access.ProviderCapabilityToolCalls,
			},
		}},
		AuthDrivers: []access.AuthDriverDefinition{{
			Ref:      access.StaticHeaderAuthDriverRef(),
			Revision: 1,
		}},
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
		TransportProfiles: []access.TransportFingerprintDefinition{
			access.ObservedClientH1TransportFingerprintDefinition(),
			access.StandardH1TransportFingerprintDefinition(),
		},
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

type approvalBuildRequest struct {
	ctx        context.Context
	repository toolapproval.Repository
	clock      toolapproval.Clock
	random     io.Reader
	config     toolapproval.Config
}

type approvalRuntime interface {
	exchange.ToolDecisionGate
	toolapproval.Controller
	Shutdown(context.Context) error
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
	authenticator, err := providertransport.NewStaticBearerAuthenticator(
		request.secrets,
	)
	if err != nil {
		return nil, fmt.Errorf("build static bearer AuthDriver: %w", err)
	}
	return providertransport.NewProductionClient(
		request.coordinator,
		authenticator,
		providertransport.DefaultTransportTimeouts(),
	)
}

type exchangeBuildRequest struct {
	ownerContext  context.Context
	actions       offlinehold.ActionAdmission
	resolver      access.SnapshotResolver
	provider      exchange.Provider
	toolDecisions exchange.ToolDecisionGate
	activities    activity.Recorder
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
	reasonCode := string(observation.ReasonCode)
	if observation.ProviderStatus != 0 {
		reasonCode += "_http_" + strconv.Itoa(observation.ProviderStatus)
	}
	if observation.ProviderField != exchange.ProviderFieldUnknown {
		reasonCode += "_field_" + string(observation.ProviderField)
	}
	if observation.ClientField != exchange.ClientFieldUnknown {
		reasonCode += "_client_field_" + string(observation.ClientField)
	}
	_, err := observer.recorder.Record(ctx, activity.Event{
		Kind:       activity.KindExchangeCompleted,
		AccessID:   observation.AccessID,
		SubjectID:  observation.ExchangeID,
		Status:     status,
		ReasonCode: reasonCode,
		Transport:  activityTransportEvidence(observation.Transport),
	})
	return err
}

func activityTransportEvidence(
	evidence transportprofile.Evidence,
) *activity.TransportEvidence {
	requested := evidence.Requested()
	if requested.Ref == "" || requested.Revision == 0 {
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
	converted := &activity.TransportEvidence{
		Requested:                profile(requested),
		FallbackReason:           string(evidence.FallbackReason()),
		ClientOfferedALPN:        evidence.ClientOfferedALPN(),
		DownstreamNegotiatedALPN: evidence.DownstreamNegotiatedALPN(),
		UpstreamOfferedALPN:      evidence.UpstreamOfferedALPN(),
		UpstreamNegotiatedALPN:   evidence.UpstreamNegotiatedALPN(),
		HTTPTransport:            string(evidence.HTTPTransport()),
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
	protocolPath, err := anthropicchat.NewProtocolPath(
		anthropicchat.DefaultOptions(),
	)
	if err != nil {
		return nil, fmt.Errorf("build Anthropic Chat protocol path: %w", err)
	}
	return exchange.New(exchange.Options{
		OwnerContext:  request.ownerContext,
		Actions:       request.actions,
		Resolver:      request.resolver,
		ProtocolPath:  protocolPath,
		Provider:      request.provider,
		ToolDecisions: request.toolDecisions,
		RetryWaiter:   exchange.TimerRetryWaiter{},
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
}

type originalRuntime interface {
	Shutdown(context.Context) error
}

type originalBuilder interface {
	Build(originalBuildRequest) (originalRuntime, error)
}

type productionOriginalBuilder struct{}

func (productionOriginalBuilder) Build(
	request originalBuildRequest,
) (originalRuntime, error) {
	return originaltransport.NewProduction(request.coordinator)
}

type runtimeBuilders struct {
	storage  storageBuilder
	access   accessBuilder
	activity activityBuilder
	approval approvalBuilder
	monitor  monitorBuilder
	provider providerBuilder
	original originalBuilder
	exchange exchangeBuilder
}

func productionBuilders() runtimeBuilders {
	return runtimeBuilders{
		storage:  productionStorageBuilder{},
		access:   productionAccessBuilder{},
		activity: productionActivityBuilder{},
		approval: productionApprovalBuilder{},
		monitor:  productionMonitorBuilder{},
		provider: productionProviderBuilder{},
		original: productionOriginalBuilder{},
		exchange: productionExchangeBuilder{},
	}
}

func wrapOptionalError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
