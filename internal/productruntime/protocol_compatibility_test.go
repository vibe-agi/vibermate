package productruntime

import (
	"errors"
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

func TestProductionProtocolCompatibilityMatrixIsExact(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		client    environment.ClientProtocol
		clientIR  protocolspec.Dialect
		provider  protocolspec.Dialect
		supported bool
	}{
		{"Claude to Anthropic Messages", environment.ClientProtocolAnthropicMessages, protocolspec.DialectAnthropicMessages, protocolspec.DialectAnthropicMessages, true},
		{"Claude to OpenAI Responses", environment.ClientProtocolAnthropicMessages, protocolspec.DialectAnthropicMessages, protocolspec.DialectOpenAIResponses, false},
		{"Claude to OpenAI Chat", environment.ClientProtocolAnthropicMessages, protocolspec.DialectAnthropicMessages, protocolspec.DialectOpenAIChat, true},
		{"Codex to Anthropic Messages", environment.ClientProtocolOpenAIResponses, protocolspec.DialectOpenAIResponses, protocolspec.DialectAnthropicMessages, false},
		{"Codex to OpenAI Responses", environment.ClientProtocolOpenAIResponses, protocolspec.DialectOpenAIResponses, protocolspec.DialectOpenAIResponses, true},
		{"Codex to OpenAI Chat", environment.ClientProtocolOpenAIResponses, protocolspec.DialectOpenAIResponses, protocolspec.DialectOpenAIChat, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			compiler, err := productionEnvironmentCompiler(
				compatibilityAccounts(test.provider), nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			aggregate, origin, path := compatibilityEnvironment(t, test.client, test.provider)
			snapshot, err := compiler.Compile(aggregate)
			if !test.supported {
				if !errors.Is(err, protocolspec.ErrUnsupportedCodecPair) {
					t.Fatalf("Compile() error = %v, want unsupported codec pair", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Compile() error = %v", err)
			}
			plan, err := snapshot.ResolveRequest(origin, environment.RequestFacts{
				Target: protocolspec.RequestTarget{
					Method: http.MethodPost, Path: path,
					Transport: protocolspec.ClientOperationTransportHTTP,
				},
				DownstreamProtocol: wireprofile.ApplicationProtocolHTTP1,
			})
			if err != nil {
				t.Fatalf("ResolveRequest() error = %v", err)
			}
			if plan.CodecPlan().ClientDialect() != test.clientIR ||
				plan.CodecPlan().ProviderDialect() != test.provider {
				t.Fatalf("resolved codec = %s -> %s", plan.CodecPlan().ClientDialect(), plan.CodecPlan().ProviderDialect())
			}
		})
	}
}

type compatibilityAccountCatalog map[string]environment.AccountDescriptor

func (catalog compatibilityAccountCatalog) LookupAccount(
	id string,
) (environment.AccountDescriptor, bool) {
	account, found := catalog[id]
	return account, found
}

func compatibilityAccounts(provider protocolspec.Dialect) compatibilityAccountCatalog {
	return compatibilityAccountCatalog{"account.test": {
		ID: "account.test", Revision: 1, DisplayName: "Test Account",
		UpstreamEndpointID: "target.test", UpstreamEndpointRevision: 1,
		RealmID: "realm.test", Active: true,
		BackendProtocols: []string{string(provider)},
	}}
}

func compatibilityEnvironment(
	t *testing.T,
	client environment.ClientProtocol,
	provider protocolspec.Dialect,
) (environment.Environment, originidentity.ClientOrigin, string) {
	t.Helper()
	clientOriginValue, path := "https://api.anthropic.com", "/v1/messages"
	if client == environment.ClientProtocolOpenAIResponses {
		clientOriginValue, path = "https://api.openai.com", "/v1/responses"
	}
	clientOrigin, err := originidentity.ParseClientOrigin(clientOriginValue)
	if err != nil {
		t.Fatal(err)
	}
	providerOrigin, err := originidentity.ParseProviderOrigin("https://provider.example")
	if err != nil {
		t.Fatal(err)
	}
	return environment.Environment{
		ID: "compatibility", Name: "Compatibility", State: environment.StateActive, Revision: 1,
		ContentRecording: environment.DefaultContentRecordingPolicy(),
		ClientEndpoints: []environment.ClientEndpoint{{
			ID: "endpoint.test", Revision: 1, ClientOrigin: clientOrigin,
			ProtocolPlans: []environment.ClientProtocolPlan{{
				ID: "plan.test", Revision: 1, ClientProtocol: client,
				ClientAdapterPolicy: environment.ClientAdapterPolicy{ID: "adapter.test", Revision: 1},
				Destination: environment.DestinationPlan{
					Kind: environment.DestinationKindUpstream,
					Upstream: &environment.UpstreamPlan{
						DefaultRouteID: "route.test",
						RouteSet: environment.RouteSet{
							ID: "routes.test", Revision: 1,
							CandidateRouteIDs: []environment.UpstreamRouteID{"route.test"},
						},
						Routes: []environment.UpstreamRoute{{
							ID: "route.test", Revision: 1,
							ProviderTarget: environment.ProviderTarget{
								ID: "target.test", Revision: 1, Origin: providerOrigin,
								RealmID: "realm.test",
								Capabilities: []protocolspec.ProviderCapability{
									protocolspec.ProviderCapabilityMessages,
									protocolspec.ProviderCapabilityStreaming,
									protocolspec.ProviderCapabilityToolCalls,
								},
							},
							BackendProtocol: string(provider),
							AccountPolicy: environment.RouteAccountPolicy{
								Revision: 1, Mode: environment.AccountSelectionFixed,
								FixedAccountID: "account.test",
								Accounts: []environment.RouteAccountReference{{
									ID: "account.test", Revision: 1, DisplayName: "Test Account",
								}},
							},
							ModelPolicy:    environment.ModelPolicy{Revision: 1, Mode: environment.ModelModePassthrough},
							WireProfileRef: wireprofile.UpstreamWireProfileFollowClientValue,
						}},
					},
				},
				EgressProfile: egressprofile.Direct(),
			}},
		}},
	}, clientOrigin, path
}
