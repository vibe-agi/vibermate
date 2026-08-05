package access_test

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/certidentity"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

const accessIntegrationStartupTimeout = 60 * time.Second

type discardLeafCacheInvalidator struct{}

func (discardLeafCacheInvalidator) InvalidateLeafCache(
	access.LeafCacheInvalidation,
) {
}

func newProjection(t *testing.T) *access.AtomicSnapshotProjection {
	t.Helper()
	projection, err := access.NewSnapshotProjection(
		certidentity.InitialRootRevision,
		discardLeafCacheInvalidator{},
	)
	if err != nil {
		t.Fatalf("construct Access projection: %v", err)
	}
	return projection
}

func testCompiler(t *testing.T) *access.Compiler {
	t.Helper()
	catalog, err := access.NewCatalog(testCatalogOptions(t))
	if err != nil {
		t.Fatalf("construct Access plan catalog: %v", err)
	}
	compiler, err := access.NewCompiler(catalog)
	if err != nil {
		t.Fatalf("construct Access plan compiler: %v", err)
	}
	return compiler
}

func testCatalogOptions(t *testing.T) access.CatalogOptions {
	t.Helper()
	codecID, err := access.NewCodecPairID("anthropic-messages-to-openai-chat")
	if err != nil {
		t.Fatalf("construct codec pair ID: %v", err)
	}
	operation := mustClientOperationDefinition(
		t,
		"anthropic-messages-create",
		access.DialectAnthropicMessages,
		http.MethodPost,
		"/v1/messages",
		"messages",
	)
	responsesCodecID, err := access.NewCodecPairID(
		"openai-responses-to-openai-chat",
	)
	if err != nil {
		t.Fatalf("construct Responses codec pair ID: %v", err)
	}
	responsesOperation := mustClientOperationDefinition(
		t,
		"openai-responses-create",
		access.DialectOpenAIResponses,
		http.MethodPost,
		"/v1/responses",
		"responses",
	)
	anthropicPassthroughID, err := access.NewCodecPairID(
		"anthropic-messages-original-passthrough",
	)
	if err != nil {
		t.Fatalf("construct Anthropic passthrough codec pair ID: %v", err)
	}
	responsesPassthroughID, err := access.NewCodecPairID(
		"openai-responses-original-passthrough",
	)
	if err != nil {
		t.Fatalf("construct Responses passthrough codec pair ID: %v", err)
	}
	return access.CatalogOptions{
		Capabilities: access.PlanCapabilities{
			MaxEndpointProfiles:          2,
			MaxAccountBindings:           1,
			MaxRouteSets:                 1,
			AllowMultipleRouteCandidates: true,
		},
		ClientOperations: []access.ClientOperationDefinition{
			operation,
			responsesOperation,
		},
		CodecPairs: []access.CodecPairDefinition{
			{
				ID:              codecID,
				Revision:        1,
				ClientDialect:   access.DialectAnthropicMessages,
				ProviderDialect: access.DialectOpenAIChat,
				ClientOperationIDs: []access.ClientOperationID{
					operation.ID(),
				},
				RequiredCapabilities: []access.ProviderCapability{
					access.ProviderCapabilityMessages,
					access.ProviderCapabilityStreaming,
					access.ProviderCapabilityToolCalls,
				},
			},
			{
				ID:              responsesCodecID,
				Revision:        1,
				ClientDialect:   access.DialectOpenAIResponses,
				ProviderDialect: access.DialectOpenAIChat,
				ClientOperationIDs: []access.ClientOperationID{
					responsesOperation.ID(),
				},
				RequiredCapabilities: []access.ProviderCapability{
					access.ProviderCapabilityMessages,
					access.ProviderCapabilityStreaming,
					access.ProviderCapabilityToolCalls,
				},
			},
			{
				ID:              anthropicPassthroughID,
				Revision:        1,
				ClientDialect:   access.DialectAnthropicMessages,
				ProviderDialect: access.DialectAnthropicMessages,
				ClientOperationIDs: []access.ClientOperationID{
					operation.ID(),
				},
				RequiredCapabilities: []access.ProviderCapability{
					access.ProviderCapabilityMessages,
					access.ProviderCapabilityStreaming,
					access.ProviderCapabilityToolCalls,
				},
			},
			{
				ID:              responsesPassthroughID,
				Revision:        1,
				ClientDialect:   access.DialectOpenAIResponses,
				ProviderDialect: access.DialectOpenAIResponses,
				ClientOperationIDs: []access.ClientOperationID{
					responsesOperation.ID(),
				},
				RequiredCapabilities: []access.ProviderCapability{
					access.ProviderCapabilityMessages,
					access.ProviderCapabilityStreaming,
					access.ProviderCapabilityToolCalls,
				},
			},
		},
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
		ModelPolicyModes: []access.ModelPolicyModeDefinition{
			{Mode: access.ModelPolicyModePassthrough, Revision: 1},
			{Mode: access.ModelPolicyModeFixed, Revision: 1},
		},
		TransportProfiles:    access.BuiltInTransportFingerprintDefinitions(),
		UpstreamWireProfiles: access.BuiltInUpstreamWireProfileDefinitions(),
	}
}

func mustClientOperationDefinition(
	t *testing.T,
	id string,
	dialect access.Dialect,
	method string,
	path string,
	feature access.CodecFeature,
) access.ClientOperationDefinition {
	t.Helper()
	identifier, err := access.NewClientOperationID(id)
	if err != nil {
		t.Fatalf("construct client operation ID: %v", err)
	}
	definition, err := access.NewClientOperationDefinition(
		access.ClientOperationOptions{
			ID:             identifier,
			Revision:       1,
			ClientDialect:  dialect,
			Methods:        []string{method},
			PathPattern:    path,
			PathMatch:      access.ClientOperationPathExact,
			Kind:           access.ClientOperationSemantic,
			Transport:      access.ClientOperationTransportHTTP,
			BodyKind:       access.ClientOperationBodyJSON,
			ReplayClass:    access.ClientReplayGenerationCostOnly,
			CodecFeature:   feature,
			MaxBodyBytes:   16 << 20,
			PayloadClass:   access.OperationPayloadClientSemantic,
			EgressBearing:  true,
			AllowedQueries: nil,
		},
	)
	if err != nil {
		t.Fatalf("construct client operation definition: %v", err)
	}
	return definition
}

func testAggregate(
	t *testing.T,
	accessID access.AccessID,
	revision access.Revision,
	name string,
) access.Aggregate {
	t.Helper()
	suffix := accessID.String()
	endpointID := mustAgentEndpointID(t, suffix+"-endpoint")
	profileID := mustEndpointProfileID(t, suffix+"-profile")
	targetID := mustProviderTargetID(t, suffix+"-target")
	accountID := mustAccountBindingID(t, suffix+"-account")
	routeSetID := mustRouteSetID(t, suffix+"-routes")
	egressID := mustEgressPolicyID(t, suffix+"-egress")
	clientOrigin, err := access.NewClientOrigin(
		"https://" + suffix + ".example.test:443",
	)
	if err != nil {
		t.Fatalf("construct ClientOrigin: %v", err)
	}
	providerOrigin, err := access.NewProviderOrigin("https://api.openai.com:443/v1")
	if err != nil {
		t.Fatalf("construct ProviderOrigin: %v", err)
	}
	model, err := access.NewModelName("gpt-4.1-mini")
	if err != nil {
		t.Fatalf("construct model name: %v", err)
	}
	secretRef, err := access.NewSecretRef("secret://provider/" + suffix)
	if err != nil {
		t.Fatalf("construct SecretRef: %v", err)
	}
	aggregate := access.Aggregate{
		Binding: access.AccessBinding{
			ID:                accessID,
			Revision:          revision,
			Name:              name,
			Description:       "Executable test Access",
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
			ID:                     profileID,
			Revision:               revision,
			AccessID:               accessID,
			Kind:                   access.EndpointProfileManaged,
			CredentialSource:       access.CredentialSourceManagedAccount,
			ProcessingMode:         access.ProfileProcessingManaged,
			Name:                   "OpenAI Chat",
			Description:            "Fixed M0 profile",
			BackendDialect:         access.DialectOpenAIChat,
			TargetID:               targetID,
			UpstreamWireProfileRef: access.FollowClientUpstreamWireProfileRef(),
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
				access.ProviderCapabilityToolCalls,
				access.ProviderCapabilityMessages,
				access.ProviderCapabilityStreaming,
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
	withOriginal, err := access.AttachOriginalPassthrough(aggregate)
	if err != nil {
		t.Fatalf("attach original passthrough profile: %v", err)
	}
	return withOriginal
}

func refreshOriginalPassthrough(
	t *testing.T,
	aggregate access.Aggregate,
) access.Aggregate {
	t.Helper()
	refreshed, err := access.RefreshOriginalPassthrough(aggregate)
	if err != nil {
		t.Fatalf("refresh original passthrough profile: %v", err)
	}
	return refreshed
}

func newAccessID(t *testing.T, value string) access.AccessID {
	t.Helper()
	accessID, err := access.NewAccessID(value)
	if err != nil {
		t.Fatalf("construct Access ID: %v", err)
	}
	return accessID
}

func mustAgentEndpointID(t *testing.T, value string) access.AgentEndpointID {
	t.Helper()
	id, err := access.NewAgentEndpointID(value)
	if err != nil {
		t.Fatalf("construct AgentEndpoint ID: %v", err)
	}
	return id
}

func mustEndpointProfileID(t *testing.T, value string) access.EndpointProfileID {
	t.Helper()
	id, err := access.NewEndpointProfileID(value)
	if err != nil {
		t.Fatalf("construct EndpointProfile ID: %v", err)
	}
	return id
}

func mustProviderTargetID(t *testing.T, value string) access.ProviderTargetID {
	t.Helper()
	id, err := access.NewProviderTargetID(value)
	if err != nil {
		t.Fatalf("construct ProviderTarget ID: %v", err)
	}
	return id
}

func mustAccountBindingID(t *testing.T, value string) access.AccountBindingID {
	t.Helper()
	id, err := access.NewAccountBindingID(value)
	if err != nil {
		t.Fatalf("construct account binding ID: %v", err)
	}
	return id
}

func mustRouteSetID(t *testing.T, value string) access.RouteSetID {
	t.Helper()
	id, err := access.NewRouteSetID(value)
	if err != nil {
		t.Fatalf("construct RouteSet ID: %v", err)
	}
	return id
}

func mustEgressPolicyID(t *testing.T, value string) access.EgressPolicyID {
	t.Helper()
	id, err := access.NewEgressPolicyID(value)
	if err != nil {
		t.Fatalf("construct egress policy ID: %v", err)
	}
	return id
}

func openStore(t *testing.T, databasePath string) *runtimepersistence.Store {
	t.Helper()
	// The full repository race job starts several independently migrated SQLite
	// fixtures in parallel. Keep a hard bound, but do not turn race-detector CPU
	// contention into a false migration failure.
	ctx, cancel := context.WithTimeout(
		context.Background(),
		accessIntegrationStartupTimeout,
	)
	defer cancel()
	store, err := runtimepersistence.Open(ctx, runtimepersistence.Options{
		DatabasePath:           databasePath,
		BusyTimeout:            runtimepersistence.DefaultBusyTimeout,
		CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout,
	})
	if err != nil {
		t.Fatalf("open runtime store: %v", err)
	}
	return store
}

func newTemporaryStore(t *testing.T) *runtimepersistence.Store {
	t.Helper()
	return openStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
}

func shutdownStore(t *testing.T, store *runtimepersistence.Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := store.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown runtime store: %v", err)
	}
}

func newManager(
	t *testing.T,
	store *runtimepersistence.Store,
	projection access.SnapshotProjection,
) *access.Manager {
	t.Helper()
	ctx, cancel := context.WithTimeout(
		context.Background(),
		accessIntegrationStartupTimeout,
	)
	defer cancel()
	manager, err := access.NewManager(
		ctx,
		store.AccessRepository(),
		testCompiler(t),
		projection,
		discardSecretRetirer{},
	)
	if err != nil {
		t.Fatalf("construct Access manager: %v", err)
	}
	return manager
}

type discardSecretRetirer struct{}

func (discardSecretRetirer) RetireSecret(context.Context, access.SecretRef) error {
	return nil
}

func shutdownManager(t *testing.T, manager *access.Manager) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown Access manager: %v", err)
	}
}
