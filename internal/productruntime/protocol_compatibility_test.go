package productruntime

import (
	"errors"
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/egressprofile/egressprofiletest"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

func TestProductionProtocolCompatibilityMatrixIsExact(t *testing.T) {
	t.Parallel()
	// generic is the provider every row but the ChatGPT ones uses.
	generic := compatibilityTarget{origin: "https://provider.example", realm: "realm.test"}
	chatGPT := compatibilityTarget{origin: "https://chatgpt.com/backend-api/codex", realm: "openai.chatgpt"}
	tests := []struct {
		name      string
		client    environment.ClientProtocol
		clientIR  protocolspec.Dialect
		provider  protocolspec.Dialect
		target    compatibilityTarget
		supported bool
		// refusal is the compile error of an unsupported row.
		refusal error
	}{
		{"Claude to Anthropic Messages", environment.ClientProtocolAnthropicMessages, protocolspec.DialectAnthropicMessages, protocolspec.DialectAnthropicMessages, generic, true, nil},
		{"Claude to OpenAI Responses", environment.ClientProtocolAnthropicMessages, protocolspec.DialectAnthropicMessages, protocolspec.DialectOpenAIResponses, generic, true, nil},
		{"Claude to OpenAI Chat", environment.ClientProtocolAnthropicMessages, protocolspec.DialectAnthropicMessages, protocolspec.DialectOpenAIChat, generic, true, nil},
		{"Codex to Anthropic Messages", environment.ClientProtocolOpenAIResponses, protocolspec.DialectOpenAIResponses, protocolspec.DialectAnthropicMessages, generic, false, protocolspec.ErrUnsupportedCodecPair},
		{"Codex to OpenAI Responses", environment.ClientProtocolOpenAIResponses, protocolspec.DialectOpenAIResponses, protocolspec.DialectOpenAIResponses, generic, true, nil},
		{"Codex to OpenAI Chat", environment.ClientProtocolOpenAIResponses, protocolspec.DialectOpenAIResponses, protocolspec.DialectOpenAIChat, generic, true, nil},
		// Stage 1 never sends a translated client to a ChatGPT plan (design
		// 13.1), while Codex keeps its own native route there.
		{"Claude to ChatGPT", environment.ClientProtocolAnthropicMessages, protocolspec.DialectAnthropicMessages, protocolspec.DialectOpenAIResponses, chatGPT, false, environment.ErrTranslatedChatGPTRoute},
		{"Codex to ChatGPT", environment.ClientProtocolOpenAIResponses, protocolspec.DialectOpenAIResponses, protocolspec.DialectOpenAIResponses, chatGPT, true, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			compiler, err := productionEnvironmentCompiler(
				compatibilityAccounts(test.provider, test.target), nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			aggregate, origin, path := compatibilityEnvironmentFor(t, test.client, test.provider, test.target)
			snapshot, err := compiler.Compile(aggregate)
			if !test.supported {
				if !errors.Is(err, test.refusal) {
					t.Fatalf("Compile() error = %v, want %v", err, test.refusal)
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
	_ string,
) (environment.AccountDescriptor, bool) {
	account, found := catalog[id]
	return account, found
}

// compatibilityTarget is the provider a matrix row routes to.
type compatibilityTarget struct {
	origin string
	realm  string
}

func compatibilityAccounts(provider protocolspec.Dialect, target compatibilityTarget) compatibilityAccountCatalog {
	return compatibilityAccountCatalog{"account.test": {
		ID: "account.test", Revision: 1, DisplayName: "Test Account",
		UpstreamEndpointID: "target.test", UpstreamEndpointRevision: 1,
		RealmID: target.realm, Active: true,
		BackendProtocols: []string{string(provider)},
	}}
}

func compatibilityEnvironment(
	t *testing.T,
	client environment.ClientProtocol,
	provider protocolspec.Dialect,
) (environment.Environment, originidentity.ClientOrigin, string) {
	t.Helper()
	return compatibilityEnvironmentFor(t, client, provider, compatibilityTarget{
		origin: "https://provider.example", realm: "realm.test",
	})
}

// compatibilityEnvironmentFor is compatibilityEnvironment routed to target.
func compatibilityEnvironmentFor(
	t *testing.T,
	client environment.ClientProtocol,
	provider protocolspec.Dialect,
	target compatibilityTarget,
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
	providerOrigin, err := originidentity.ParseProviderOrigin(target.origin)
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
								RealmID: target.realm,
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
				EgressProfile: egressprofiletest.Explicit(t, egressprofile.Direct()),
			}},
		}},
	}, clientOrigin, path
}
