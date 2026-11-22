package environment

import (
	"errors"
	"testing"

	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

// translatingTestCompiler holds, besides the two same-dialect pairs of
// testCompiler, the cross-dialect pairs a ChatGPT guard must cover: Anthropic
// to Responses, which production does not register yet, and Anthropic to Chat.
func translatingTestCompiler(t *testing.T, accounts AccountCatalog) Compiler {
	t.Helper()
	operations, err := operationcatalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	pair := func(id string, client, provider protocolspec.Dialect) protocolspec.CodecPairDefinition {
		pairID, pairErr := protocolspec.NewCodecPairID(id)
		if pairErr != nil {
			t.Fatal(pairErr)
		}
		return protocolspec.CodecPairDefinition{
			ID: pairID, Revision: 1, ClientDialect: client, ProviderDialect: provider,
			ClientOperationIDs: operations.SemanticOperationIDs(client),
			RequiredCapabilities: []protocolspec.ProviderCapability{
				protocolspec.ProviderCapabilityMessages,
				protocolspec.ProviderCapabilityStreaming,
				protocolspec.ProviderCapabilityToolCalls,
			},
		}
	}
	protocols, err := protocolspec.NewCatalog(operations.Definitions(), []protocolspec.CodecPairDefinition{
		pair("test.anthropic.passthrough", protocolspec.DialectAnthropicMessages, protocolspec.DialectAnthropicMessages),
		pair("test.responses.passthrough", protocolspec.DialectOpenAIResponses, protocolspec.DialectOpenAIResponses),
		pair("test.anthropic.responses", protocolspec.DialectAnthropicMessages, protocolspec.DialectOpenAIResponses),
		pair("test.anthropic.chat", protocolspec.DialectAnthropicMessages, protocolspec.DialectOpenAIChat),
	})
	if err != nil {
		t.Fatal(err)
	}
	wires, err := wireprofile.BuiltInCatalog()
	if err != nil {
		t.Fatal(err)
	}
	compiler, err := NewCompiler(accounts, nil, protocols, wires)
	if err != nil {
		t.Fatal(err)
	}
	return compiler
}

// routedFixture points the default route of the fixture's plan (0: the
// Anthropic client, 1: the Responses client) at one service.
func routedFixture(t *testing.T, plan int, backend, origin, realm string) Environment {
	t.Helper()
	value := fixture(t, "work", mustOrigin(t, "https://relay.example"))
	route := &value.ClientEndpoints[0].ProtocolPlans[plan].Destination.Upstream.Routes[0]
	route.BackendProtocol = backend
	route.ProviderTarget.Origin = mustProviderOrigin(t, origin)
	route.ProviderTarget.RealmID = realm
	return value
}

func compiledDefaultRoute(t *testing.T, snapshot EnvironmentSnapshot, plan int) CompiledRoutePlan {
	t.Helper()
	endpoint, ok := snapshot.LookupCompiledClientOrigin(mustOrigin(t, "https://relay.example"))
	if !ok {
		t.Fatal("compiled endpoint is missing")
	}
	routeSet, ok := endpoint.ProtocolPlans()[plan].UpstreamRouteSet()
	if !ok {
		t.Fatal("compiled RouteSet is missing")
	}
	route, err := routeSet.DefaultRoute()
	if err != nil {
		t.Fatal(err)
	}
	return route
}

func TestTranslatedRouteToChatGPTPlanIsRefused(t *testing.T) {
	t.Parallel()
	const responses, chat = "openai_responses", "openai_chat"
	for _, test := range []struct {
		name, backend, origin, realm string
	}{
		{"built-in service", responses, "https://chatgpt.com", chatGPTRealmID},
		{"built-in realm on another host", responses, "https://relay.example", chatGPTRealmID},
		{"custom service on chatgpt.com", responses, "https://chatgpt.com", "target.custom"},
		{"base path backend-api", responses, "https://chatgpt.com/backend-api", "target.custom"},
		{"base path backend-api/codex", responses, "https://chatgpt.com/backend-api/codex", "target.custom"},
		{"other base path", responses, "https://chatgpt.com/other", "target.custom"},
		{"other port", responses, "https://chatgpt.com:8443/backend-api/codex", "target.custom"},
		{"cleartext scheme", responses, "http://chatgpt.com", "target.custom"},
		{"spelled in capitals", responses, "https://ChatGPT.com/backend-api", "target.custom"},
		{"subdomain", responses, "https://api.chatgpt.com/v1", "target.custom"},
		{"nested subdomain", responses, "https://edge.eu.chatgpt.com", "target.custom"},
		{"chat backend", chat, "https://chatgpt.com", "target.custom"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			value := routedFixture(t, 0, test.backend, test.origin, test.realm)
			compiler := translatingTestCompiler(t, accountCatalogFor(value))
			_, err := compiler.Compile(value)
			if !errors.Is(err, ErrTranslatedChatGPTRoute) || !errors.Is(err, ErrInvalidEnvironment) {
				t.Fatalf("Compile() error = %v, want a translated ChatGPT route refusal", err)
			}
			// Published revisions are rebuilt by the same route compiler.
			if _, err := compiler.Restore(value); !errors.Is(err, ErrTranslatedChatGPTRoute) {
				t.Fatalf("Restore() error = %v, want a translated ChatGPT route refusal", err)
			}
		})
	}
}

func TestTranslatedRouteToOtherServicesCompiles(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, origin, realm string
	}{
		{"OpenAI API", "https://api.openai.com", "openai.platform"},
		{"OpenAI API with a version path", "https://api.openai.com/v1", "target.custom"},
		{"host ending in chatgpt.com", "https://notchatgpt.com", "target.custom"},
		{"chatgpt.com as a label", "https://chatgpt.com.example", "target.custom"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			value := routedFixture(t, 0, "openai_responses", test.origin, test.realm)
			snapshot, err := translatingTestCompiler(t, accountCatalogFor(value)).Compile(value)
			if err != nil {
				t.Fatalf("Compile() error = %v", err)
			}
			codec := compiledDefaultRoute(t, snapshot, 0).CodecPlan()
			if codec.ClientDialect() != protocolspec.DialectAnthropicMessages ||
				codec.ProviderDialect() != protocolspec.DialectOpenAIResponses {
				t.Fatalf("codec = %s -> %s", codec.ClientDialect(), codec.ProviderDialect())
			}
		})
	}
}

func TestSameDialectRouteToChatGPTPlanCompiles(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, origin, realm string
	}{
		{"built-in service", "https://chatgpt.com/backend-api/codex", chatGPTRealmID},
		{"custom service", "https://chatgpt.com", "target.custom"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			value := routedFixture(t, 1, "openai_responses", test.origin, test.realm)
			for name, compiler := range map[string]Compiler{
				"test catalog":        testCompiler(t, accountCatalogFor(value)),
				"translating catalog": translatingTestCompiler(t, accountCatalogFor(value)),
			} {
				snapshot, err := compiler.Compile(value)
				if err != nil {
					t.Fatalf("%s: Compile() error = %v", name, err)
				}
				codec := compiledDefaultRoute(t, snapshot, 1).CodecPlan()
				if codec.ClientDialect() != protocolspec.DialectOpenAIResponses ||
					codec.ProviderDialect() != protocolspec.DialectOpenAIResponses {
					t.Fatalf("%s: codec = %s -> %s", name, codec.ClientDialect(), codec.ProviderDialect())
				}
			}
		})
	}
}

// The guard runs before the codec pair resolves, so a build without the
// translating pair refuses a ChatGPT route for the same reason a build with it
// does, and every other missing pair stays an unsupported pair.
func TestChatGPTRouteGuardPrecedesCodecResolution(t *testing.T) {
	t.Parallel()
	chatGPT := routedFixture(t, 0, "openai_responses", "https://chatgpt.com/backend-api/codex", chatGPTRealmID)
	_, err := testCompiler(t, accountCatalogFor(chatGPT)).Compile(chatGPT)
	if !errors.Is(err, ErrTranslatedChatGPTRoute) || errors.Is(err, protocolspec.ErrUnsupportedCodecPair) {
		t.Fatalf("ChatGPT route error = %v, want only the ChatGPT refusal", err)
	}
	platform := routedFixture(t, 0, "openai_responses", "https://api.openai.com", "openai.platform")
	_, err = testCompiler(t, accountCatalogFor(platform)).Compile(platform)
	if !errors.Is(err, protocolspec.ErrUnsupportedCodecPair) || errors.Is(err, ErrTranslatedChatGPTRoute) {
		t.Fatalf("OpenAI API route error = %v, want an unsupported codec pair", err)
	}
}
