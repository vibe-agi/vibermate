package accesscredential

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/secretstore"
)

func TestServiceUsesActivePlanReferenceAndReturnsOnlyRedactedMetadata(t *testing.T) {
	t.Parallel()

	snapshot := compiledSnapshot(t)
	store := &storeFixture{
		metadata: secretstore.Metadata{State: secretstore.StateMissing},
	}
	service, err := New(resolverFixture{snapshot: snapshot}, store)
	if err != nil {
		t.Fatal(err)
	}
	accessID, profileID, credentialID := identifiers(t)
	initial, err := service.GetCredential(
		context.Background(),
		accessID,
		profileID,
		credentialID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if initial.SecretState != secretstore.StateMissing ||
		initial.SecretRevision != 0 {
		t.Fatalf("initial view = %+v", initial)
	}

	value, err := secretstore.NewValue([]byte("provider-secret"))
	if err != nil {
		t.Fatal(err)
	}
	defer value.Destroy()
	configured, err := service.ReplaceSecret(
		context.Background(),
		ReplaceCommand{
			AccessID:     accessID,
			ProfileID:    profileID,
			CredentialID: credentialID,
			Value:        value,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if configured.SecretState != secretstore.StateConfigured ||
		configured.SecretRevision != 1 ||
		store.reference.String() != "secret://provider/work-account" {
		t.Fatalf("configured view=%+v reference=%q", configured, store.reference.String())
	}
	payload, err := json.Marshal(configured)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte("provider-secret")) ||
		bytes.Contains(payload, []byte("secret://")) {
		t.Fatalf("credential view exposed secret authority: %s", payload)
	}
}

func TestServiceRejectsCrossProfileCredentialBeforeSecretStore(t *testing.T) {
	t.Parallel()

	snapshot := compiledSnapshot(t)
	store := &storeFixture{}
	service, err := New(resolverFixture{snapshot: snapshot}, store)
	if err != nil {
		t.Fatal(err)
	}
	accessID, _, credentialID := identifiers(t)
	otherProfile, _ := access.NewEndpointProfileID("other-profile")
	_, err = service.GetCredential(
		context.Background(),
		accessID,
		otherProfile,
		credentialID,
	)
	if !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("GetCredential() error = %v", err)
	}
	if store.inspectCalls != 0 || store.replaceCalls != 0 {
		t.Fatal("cross-profile credential reached SecretStore")
	}
}

func TestServiceMapsSecretStoreAvailabilityToFailClosedView(t *testing.T) {
	t.Parallel()

	snapshot := compiledSnapshot(t)
	store := &storeFixture{inspectErr: secretstore.ErrUnavailable}
	service, err := New(resolverFixture{snapshot: snapshot}, store)
	if err != nil {
		t.Fatal(err)
	}
	accessID, profileID, credentialID := identifiers(t)
	view, err := service.GetCredential(
		context.Background(),
		accessID,
		profileID,
		credentialID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if view.SecretState != secretstore.StateUnavailable ||
		view.SecretRevision != 0 {
		t.Fatalf("unavailable view = %+v", view)
	}
}

func TestServiceReturnsConfiguredMetadataWithoutReadingSecret(t *testing.T) {
	t.Parallel()

	snapshot := compiledSnapshot(t)
	store := &storeFixture{
		metadata: secretstore.Metadata{
			State:    secretstore.StateConfigured,
			Revision: 7,
		},
		readErr: errors.New("secret bytes must not be read by metadata control"),
	}
	service, err := New(resolverFixture{snapshot: snapshot}, store)
	if err != nil {
		t.Fatal(err)
	}
	accessID, profileID, credentialID := identifiers(t)
	view, err := service.GetCredential(
		context.Background(),
		accessID,
		profileID,
		credentialID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if view.SecretState != secretstore.StateConfigured ||
		view.SecretRevision != 7 ||
		store.readCalls != 0 {
		t.Fatalf(
			"configured view = %+v readCalls=%d",
			view,
			store.readCalls,
		)
	}
}

func TestServiceMapsDeniedMetadataInspectionToUnavailable(t *testing.T) {
	t.Parallel()

	snapshot := compiledSnapshot(t)
	store := &storeFixture{
		inspectErr: secretstore.ErrDenied,
	}
	service, err := New(resolverFixture{snapshot: snapshot}, store)
	if err != nil {
		t.Fatal(err)
	}
	accessID, profileID, credentialID := identifiers(t)
	view, err := service.GetCredential(
		context.Background(),
		accessID,
		profileID,
		credentialID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if view.SecretState != secretstore.StateUnavailable ||
		view.SecretRevision != 0 {
		t.Fatalf("unavailable view = %+v", view)
	}
}

func TestServiceReturnsCommittedUnavailableReplacement(t *testing.T) {
	t.Parallel()

	snapshot := compiledSnapshot(t)
	store := &storeFixture{
		replaceResult: secretstore.Metadata{
			State:    secretstore.StateUnavailable,
			Revision: 2,
		},
	}
	service, err := New(resolverFixture{snapshot: snapshot}, store)
	if err != nil {
		t.Fatal(err)
	}
	accessID, profileID, credentialID := identifiers(t)
	value, err := secretstore.NewValue([]byte("provider-secret"))
	if err != nil {
		t.Fatal(err)
	}
	defer value.Destroy()
	view, err := service.ReplaceSecret(
		context.Background(),
		ReplaceCommand{
			AccessID:         accessID,
			ProfileID:        profileID,
			CredentialID:     credentialID,
			ExpectedRevision: 1,
			Value:            value,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if view.SecretState != secretstore.StateUnavailable ||
		view.SecretRevision != 2 {
		t.Fatalf("replacement view = %+v", view)
	}
}

type resolverFixture struct {
	snapshot access.AccessPlanSnapshot
	err      error
}

func (resolver resolverFixture) ResolveAccess(
	access.AccessID,
) (access.AccessPlanSnapshot, error) {
	return resolver.snapshot, resolver.err
}

type storeFixture struct {
	mu            sync.Mutex
	metadata      secretstore.Metadata
	inspectErr    error
	readValue     *secretstore.Value
	readErr       error
	replaceErr    error
	replaceResult secretstore.Metadata
	reference     secretstore.Reference
	inspectCalls  int
	readCalls     int
	replaceCalls  int
}

func (store *storeFixture) Read(
	_ context.Context,
	reference secretstore.Reference,
) (*secretstore.Value, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.readCalls++
	store.reference = reference
	if store.readErr != nil {
		return nil, store.readErr
	}
	if store.readValue == nil {
		return nil, secretstore.ErrNotFound
	}
	return store.readValue, nil
}

func (store *storeFixture) Inspect(
	_ context.Context,
	reference secretstore.Reference,
) (secretstore.Metadata, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.inspectCalls++
	store.reference = reference
	return store.metadata, store.inspectErr
}

func (store *storeFixture) Replace(
	_ context.Context,
	command secretstore.ReplaceCommand,
) (secretstore.Metadata, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.replaceCalls++
	store.reference = command.Reference
	if store.replaceErr != nil {
		return secretstore.Metadata{}, store.replaceErr
	}
	if store.replaceResult.State != "" {
		return store.replaceResult, nil
	}
	store.metadata = secretstore.Metadata{
		State:    secretstore.StateConfigured,
		Revision: command.ExpectedRevision + 1,
	}
	return store.metadata, nil
}

func (store *storeFixture) Delete(
	_ context.Context,
	reference secretstore.Reference,
) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.reference = reference
	return secretstore.ErrNotFound
}

func identifiers(
	t *testing.T,
) (access.AccessID, access.EndpointProfileID, access.AccountBindingID) {
	t.Helper()
	accessID, err := access.NewAccessID("work")
	if err != nil {
		t.Fatal(err)
	}
	profileID, err := access.NewEndpointProfileID("work-profile")
	if err != nil {
		t.Fatal(err)
	}
	credentialID, err := access.NewAccountBindingID("work-account")
	if err != nil {
		t.Fatal(err)
	}
	return accessID, profileID, credentialID
}

func compiledSnapshot(t *testing.T) access.AccessPlanSnapshot {
	t.Helper()
	accessID, profileID, credentialID := identifiers(t)
	endpointID, _ := access.NewAgentEndpointID("work-agent")
	targetID, _ := access.NewProviderTargetID("work-target")
	routeSetID, _ := access.NewRouteSetID("work-routes")
	egressID, _ := access.NewEgressPolicyID("work-egress")
	clientOrigin, _ := access.NewClientOrigin("https://api.anthropic.com")
	providerOrigin, _ := access.NewProviderOrigin("https://api.openai.com/v1")
	model, _ := access.NewModelName("gpt-4.1-mini")
	secretRef, _ := access.NewSecretRef("secret://provider/work-account")
	codecID, _ := access.NewCodecPairID("anthropic-messages-to-openai-chat")
	operations, err := operationcatalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := access.NewCatalog(access.CatalogOptions{
		Capabilities: access.PlanCapabilities{
			MaxEndpointProfiles: 2,
			MaxAccountBindings:  1,
			MaxRouteSets:        1,
		},
		ClientOperations: operations.Definitions(),
		CodecPairs: []access.CodecPairDefinition{{
			ID:              codecID,
			Revision:        1,
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
		TransportProfiles:    access.BuiltInTransportFingerprintDefinitions(),
		UpstreamWireProfiles: access.BuiltInUpstreamWireProfileDefinitions(),
	})
	if err != nil {
		t.Fatal(err)
	}
	compiler, err := access.NewCompiler(catalog)
	if err != nil {
		t.Fatal(err)
	}
	aggregate := access.Aggregate{
		Binding: access.AccessBinding{
			ID:                accessID,
			Revision:          1,
			Name:              "Work",
			Status:            access.AccessStatusEnabled,
			AgentEndpointID:   endpointID,
			DefaultRouteSetID: routeSetID,
			ProfileIDs:        []access.EndpointProfileID{profileID},
			EgressPolicyID:    egressID,
		},
		AgentEndpoint: access.AgentEndpoint{
			ID:            endpointID,
			Revision:      1,
			AccessID:      accessID,
			ClientOrigin:  clientOrigin,
			ClientDialect: access.DialectAnthropicMessages,
		},
		Profiles: []access.EndpointProfile{{
			ID:                     profileID,
			Revision:               1,
			AccessID:               accessID,
			Kind:                   access.EndpointProfileManaged,
			CredentialSource:       access.CredentialSourceManagedAccount,
			ProcessingMode:         access.ProfileProcessingManaged,
			Name:                   "Work",
			BackendDialect:         access.DialectOpenAIChat,
			TargetID:               targetID,
			UpstreamWireProfileRef: access.FollowClientUpstreamWireProfileRef(),
			DefaultModelPolicy: access.ModelPolicy{
				Revision:   1,
				Mode:       access.ModelPolicyModeFixed,
				FixedModel: model,
			},
			AccountBindingIDs:       []access.AccountBindingID{credentialID},
			DefaultAccountBindingID: credentialID,
		}},
		ProviderTargets: []access.ProviderTarget{{
			ID:        targetID,
			Revision:  1,
			AccessID:  accessID,
			ProfileID: profileID,
			Origin:    providerOrigin,
			Protocol:  access.DialectOpenAIChat,
			Capabilities: []access.ProviderCapability{
				access.ProviderCapabilityMessages,
				access.ProviderCapabilityStreaming,
				access.ProviderCapabilityToolCalls,
			},
		}},
		AccountBindings: []access.ProviderAccountBinding{{
			ID:            credentialID,
			Revision:      1,
			AccessID:      accessID,
			ProfileID:     profileID,
			Label:         "Work",
			SecretRef:     secretRef,
			AuthDriverRef: access.StaticHeaderAuthDriverRef(),
			Enabled:       true,
		}},
		RouteSets: []access.RouteSet{{
			ID:                  routeSetID,
			Revision:            1,
			AccessID:            accessID,
			CandidateProfileIDs: []access.EndpointProfileID{profileID},
		}},
		EgressPolicy: access.AccessEgressPolicy{
			ID:       egressID,
			Revision: 1,
			AccessID: accessID,
			Mode:     access.EgressModeDirect,
		},
		PluginPlan: access.PluginPlan{
			Revision: 1,
			AccessID: accessID,
			Mode:     access.PluginPlanModePassThrough,
		},
	}
	aggregate, err = access.AttachOriginalPassthrough(aggregate)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := compiler.Compile(aggregate)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
