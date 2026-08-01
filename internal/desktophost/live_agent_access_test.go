package desktophost_test

import (
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
)

// liveAgentAccess is the plan a real agent client runs against: its own
// origin as the AgentEndpoint, and the live backend behind it.
func liveAgentAccess(
	t *testing.T,
	accessID access.AccessID,
	providerOriginValue string,
	modelName string,
) access.Aggregate {
	revision := access.Revision(1)
	name := "Live Agent"
	t.Helper()
	endpointID, err := access.NewAgentEndpointID(accessID.String() + "-endpoint")
	if err != nil {
		t.Fatalf("construct AgentEndpoint ID: %v", err)
	}
	profileID, err := access.NewEndpointProfileID(accessID.String() + "-profile")
	if err != nil {
		t.Fatalf("construct EndpointProfile ID: %v", err)
	}
	targetID, err := access.NewProviderTargetID(accessID.String() + "-target")
	if err != nil {
		t.Fatalf("construct ProviderTarget ID: %v", err)
	}
	accountID, err := access.NewAccountBindingID(accessID.String() + "-account")
	if err != nil {
		t.Fatalf("construct account binding ID: %v", err)
	}
	routeSetID, err := access.NewRouteSetID(accessID.String() + "-routes")
	if err != nil {
		t.Fatalf("construct RouteSet ID: %v", err)
	}
	egressID, err := access.NewEgressPolicyID(accessID.String() + "-egress")
	if err != nil {
		t.Fatalf("construct egress policy ID: %v", err)
	}
	clientOrigin, err := access.NewClientOrigin("https://api.anthropic.com:443")
	if err != nil {
		t.Fatalf("construct ClientOrigin: %v", err)
	}
	providerOrigin, err := access.NewProviderOrigin(providerOriginValue)
	if err != nil {
		t.Fatalf("construct ProviderOrigin: %v", err)
	}
	model, err := access.NewModelName(modelName)
	if err != nil {
		t.Fatalf("construct model: %v", err)
	}
	secretRef, err := access.NewSecretRef("secret://provider/" + accessID.String())
	if err != nil {
		t.Fatalf("construct SecretRef: %v", err)
	}
	return access.Aggregate{
		Binding: access.AccessBinding{
			ID:                accessID,
			Revision:          revision,
			Name:              name,
			Description:       "ProductRuntime executable Access",
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
			ID:                  profileID,
			Revision:            revision,
			AccessID:            accessID,
			Name:                "OpenAI Chat",
			Description:         "Fixed M0 profile",
			BackendDialect:      access.DialectOpenAIChat,
			TargetID:            targetID,
			TransportProfileRef: access.ObservedClientH1TransportProfileRef(),
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
				access.ProviderCapabilityMessages,
				access.ProviderCapabilityStreaming,
				access.ProviderCapabilityToolCalls,
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
}
