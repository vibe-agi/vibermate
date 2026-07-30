package productruntime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/anthropicchat"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originaltransport"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"github.com/vibe-agi/vibermate/internal/secretstore"
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
	monitor  monitorBuilder
	provider providerBuilder
	original originalBuilder
}

func productionBuilders() runtimeBuilders {
	return runtimeBuilders{
		storage:  productionStorageBuilder{},
		access:   productionAccessBuilder{},
		monitor:  productionMonitorBuilder{},
		provider: productionProviderBuilder{},
		original: productionOriginalBuilder{},
	}
}

func wrapOptionalError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
