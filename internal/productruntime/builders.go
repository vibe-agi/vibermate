package productruntime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
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
		"anthropic-messages-to-openai-chat",
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
			Revision:        4,
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

type runtimeBuilders struct {
	storage storageBuilder
	access  accessBuilder
	monitor monitorBuilder
}

func productionBuilders() runtimeBuilders {
	return runtimeBuilders{
		storage: productionStorageBuilder{},
		access:  productionAccessBuilder{},
		monitor: productionMonitorBuilder{},
	}
}

func wrapOptionalError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
