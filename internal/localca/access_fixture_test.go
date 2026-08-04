package localca

import (
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/certidentity"
)

type accessFixture struct {
	accessID access.AccessID
	origin   access.ClientOrigin
	plan     access.AccessPlanSnapshot
}

func newAccessProjection(
	t *testing.T,
	authority *Authority,
	fixtures ...accessFixture,
) *access.AtomicSnapshotProjection {
	t.Helper()
	projection, err := access.NewSnapshotProjection(
		authority.Identity().Revision(),
		authority,
	)
	if err != nil {
		t.Fatalf("construct Access projection: %v", err)
	}
	plans := make([]access.AccessPlanSnapshot, len(fixtures))
	for index := range fixtures {
		plans[index] = fixtures[index].plan
	}
	if err := projection.Restore(plans); err != nil {
		t.Fatalf("restore Access projection: %v", err)
	}
	return projection
}

func newAccessFixture(
	t *testing.T,
	suffix string,
	revision access.Revision,
) accessFixture {
	t.Helper()
	accessID := mustAccessID(t, "access-"+suffix)
	endpointID := mustAgentEndpointID(t, "endpoint-"+suffix)
	profileID := mustEndpointProfileID(t, "profile-"+suffix)
	targetID := mustProviderTargetID(t, "target-"+suffix)
	accountID := mustAccountBindingID(t, "account-"+suffix)
	routeID := mustRouteSetID(t, "route-"+suffix)
	egressID := mustEgressPolicyID(t, "egress-"+suffix)
	origin, err := access.NewClientOrigin(
		"https://" + suffix + ".example.test:443",
	)
	if err != nil {
		t.Fatalf("construct ClientOrigin: %v", err)
	}
	providerOrigin, err := access.NewProviderOrigin(
		"https://provider.example.test:443/v1",
	)
	if err != nil {
		t.Fatalf("construct ProviderOrigin: %v", err)
	}
	model, err := access.NewModelName("test-model")
	if err != nil {
		t.Fatalf("construct model: %v", err)
	}
	secret, err := access.NewSecretRef("secret://provider/" + suffix)
	if err != nil {
		t.Fatalf("construct SecretRef: %v", err)
	}
	compiler := newAccessCompiler(t)
	plan, err := compiler.Compile(access.Aggregate{
		Binding: access.AccessBinding{
			ID:                accessID,
			Revision:          revision,
			Name:              "Local CA test Access",
			Status:            access.AccessStatusEnabled,
			AgentEndpointID:   endpointID,
			DefaultRouteSetID: routeID,
			ProfileIDs:        []access.EndpointProfileID{profileID},
			EgressPolicyID:    egressID,
		},
		AgentEndpoint: access.AgentEndpoint{
			ID:            endpointID,
			Revision:      revision,
			AccessID:      accessID,
			ClientOrigin:  origin,
			ClientDialect: access.DialectAnthropicMessages,
		},
		Profiles: []access.EndpointProfile{{
			ID:                      profileID,
			Revision:                revision,
			AccessID:                accessID,
			Name:                    "Provider profile",
			BackendDialect:          access.DialectOpenAIChat,
			TargetID:                targetID,
			UpstreamWireProfileRef:  access.FollowClientUpstreamWireProfileRef(),
			DefaultModelPolicy:      access.ModelPolicy{Revision: revision, Mode: access.ModelPolicyModeFixed, FixedModel: model},
			AccountBindingIDs:       []access.AccountBindingID{accountID},
			DefaultAccountBindingID: accountID,
		}},
		ProviderTargets: []access.ProviderTarget{{
			ID:           targetID,
			Revision:     revision,
			AccessID:     accessID,
			ProfileID:    profileID,
			Origin:       providerOrigin,
			Protocol:     access.DialectOpenAIChat,
			Capabilities: []access.ProviderCapability{access.ProviderCapabilityMessages},
		}},
		AccountBindings: []access.ProviderAccountBinding{{
			ID:            accountID,
			Revision:      revision,
			AccessID:      accessID,
			ProfileID:     profileID,
			Label:         "Provider account",
			SecretRef:     secret,
			AuthDriverRef: access.StaticHeaderAuthDriverRef(),
			Enabled:       true,
		}},
		RouteSets: []access.RouteSet{{
			ID:                  routeID,
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
	})
	if err != nil {
		t.Fatalf("compile Access fixture: %v", err)
	}
	return accessFixture{accessID: accessID, origin: origin, plan: plan}
}

func newAccessCompiler(t *testing.T) *access.Compiler {
	t.Helper()
	operationID, err := access.NewClientOperationID("messages-create")
	if err != nil {
		t.Fatal(err)
	}
	operation, err := access.NewClientOperationDefinition(
		access.ClientOperationOptions{
			ID:            operationID,
			Revision:      1,
			ClientDialect: access.DialectAnthropicMessages,
			Methods:       []string{http.MethodPost},
			PathPattern:   "/v1/messages",
			PathMatch:     access.ClientOperationPathExact,
			Kind:          access.ClientOperationSemantic,
			Transport:     access.ClientOperationTransportHTTP,
			BodyKind:      access.ClientOperationBodyJSON,
			ReplayClass:   access.ClientReplayGenerationCostOnly,
			CodecFeature:  "messages",
			MaxBodyBytes:  1 << 20,
			PayloadClass:  access.OperationPayloadClientSemantic,
			EgressBearing: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	codecID, err := access.NewCodecPairID(
		"anthropic-messages-to-openai-chat",
	)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := access.NewCatalog(access.CatalogOptions{
		Capabilities: access.PlanCapabilities{
			MaxEndpointProfiles: 1,
			MaxAccountBindings:  1,
			MaxRouteSets:        1,
		},
		ClientOperations: []access.ClientOperationDefinition{operation},
		CodecPairs: []access.CodecPairDefinition{{
			ID:                   codecID,
			Revision:             1,
			ClientDialect:        access.DialectAnthropicMessages,
			ProviderDialect:      access.DialectOpenAIChat,
			ClientOperationIDs:   []access.ClientOperationID{operationID},
			RequiredCapabilities: []access.ProviderCapability{access.ProviderCapabilityMessages},
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
			access.ClaudeCodeH1TransportFingerprintDefinition(),
		},
		UpstreamWireProfiles: access.BuiltInUpstreamWireProfileDefinitions(),
	})
	if err != nil {
		t.Fatalf("construct Access catalog: %v", err)
	}
	compiler, err := access.NewCompiler(catalog)
	if err != nil {
		t.Fatalf("construct Access compiler: %v", err)
	}
	return compiler
}

func leafAdmission(
	t *testing.T,
	projection *access.AtomicSnapshotProjection,
	authority *Authority,
	fixture accessFixture,
) access.LeafIssuanceAdmission {
	t.Helper()
	binding, err := projection.ResolveClientOrigin(fixture.origin)
	if err != nil {
		t.Fatalf("resolve ClientOrigin: %v", err)
	}
	san, err := certidentity.NewDNSName(fixture.origin.TLSServerName())
	if err != nil {
		t.Fatalf("construct leaf SAN: %v", err)
	}
	intent, err := access.NewLeafIssuanceIntent(
		authority.Identity().Revision(),
		binding,
		san,
		certidentity.LeafKeyAlgorithmECDSAP256,
	)
	if err != nil {
		t.Fatalf("construct leaf issuance intent: %v", err)
	}
	admission, err := projection.AdmitLeaf(intent)
	if err != nil {
		t.Fatalf("admit leaf issuance: %v", err)
	}
	return admission
}

func mustAccessID(t *testing.T, value string) access.AccessID {
	t.Helper()
	identifier, err := access.NewAccessID(value)
	if err != nil {
		t.Fatal(err)
	}
	return identifier
}

func mustAgentEndpointID(t *testing.T, value string) access.AgentEndpointID {
	t.Helper()
	identifier, err := access.NewAgentEndpointID(value)
	if err != nil {
		t.Fatal(err)
	}
	return identifier
}

func mustEndpointProfileID(t *testing.T, value string) access.EndpointProfileID {
	t.Helper()
	identifier, err := access.NewEndpointProfileID(value)
	if err != nil {
		t.Fatal(err)
	}
	return identifier
}

func mustProviderTargetID(t *testing.T, value string) access.ProviderTargetID {
	t.Helper()
	identifier, err := access.NewProviderTargetID(value)
	if err != nil {
		t.Fatal(err)
	}
	return identifier
}

func mustAccountBindingID(t *testing.T, value string) access.AccountBindingID {
	t.Helper()
	identifier, err := access.NewAccountBindingID(value)
	if err != nil {
		t.Fatal(err)
	}
	return identifier
}

func mustRouteSetID(t *testing.T, value string) access.RouteSetID {
	t.Helper()
	identifier, err := access.NewRouteSetID(value)
	if err != nil {
		t.Fatal(err)
	}
	return identifier
}

func mustEgressPolicyID(t *testing.T, value string) access.EgressPolicyID {
	t.Helper()
	identifier, err := access.NewEgressPolicyID(value)
	if err != nil {
		t.Fatal(err)
	}
	return identifier
}
