package operationcatalog_test

import (
	"testing"

	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

func TestBuiltInCatalogHasExactResponsesCreateOperations(t *testing.T) {
	t.Parallel()

	catalog, err := operationcatalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	identifiers := catalog.SemanticOperationIDs(
		protocolspec.DialectOpenAIResponses,
	)
	// Two exact create operations: the API-key entrypoint and the observed
	// ChatGPT-login entrypoint. Both are exact paths; neither is a prefix.
	expected := map[string]string{
		operationcatalog.OpenAIResponsesCreateID:      "/v1/responses",
		operationcatalog.OpenAICodexResponsesCreateID: "/backend-api/codex/responses",
	}
	if len(identifiers) != len(expected) {
		t.Fatalf("Responses semantic operation IDs = %v", identifiers)
	}
	for _, identifier := range identifiers {
		path, known := expected[identifier.String()]
		if !known {
			t.Fatalf("unexpected Responses semantic operation %q", identifier)
		}
		for _, definition := range catalog.Definitions() {
			if definition.ID() != identifier {
				continue
			}
			if definition.PathPattern() != path ||
				definition.PathMatch() != protocolspec.ClientOperationPathExact {
				t.Fatalf("Responses operation = %+v", definition)
			}
		}
	}
	var matched protocolspec.ClientOperationDefinition
	for _, definition := range catalog.Definitions() {
		if definition.ID().String() ==
			operationcatalog.OpenAIResponsesCreateID {
			matched = definition
		}
	}
	if matched.PathPattern() != "/v1/responses" ||
		matched.PathMatch() != protocolspec.ClientOperationPathExact ||
		matched.Kind() != protocolspec.ClientOperationSemantic ||
		matched.Transport() != protocolspec.ClientOperationTransportHTTP ||
		matched.CodecFeature() != "responses" ||
		matched.ReplayClass() != protocolspec.ClientReplayGenerationCostOnly ||
		!matched.EgressBearing() {
		t.Fatalf("Responses operation = %+v", matched)
	}
	if methods := matched.Methods(); len(methods) != 1 ||
		methods[0] != "POST" {
		t.Fatalf("Responses operation methods = %v", methods)
	}
}

func TestBuiltInCatalogDeclaresResponsesWebSocketsAsUnsupported(t *testing.T) {
	t.Parallel()

	catalog, err := operationcatalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	for identifier, path := range map[string]string{
		operationcatalog.OpenAIResponsesWebSocketUnsupportedID:      "/v1/responses",
		operationcatalog.OpenAICodexResponsesWebSocketUnsupportedID: "/backend-api/codex/responses",
	} {
		var matched protocolspec.ClientOperationDefinition
		for _, definition := range catalog.Definitions() {
			if definition.ID().String() == identifier {
				matched = definition
				break
			}
		}
		methods := matched.Methods()
		if matched.PathPattern() != path ||
			matched.PathMatch() != protocolspec.ClientOperationPathExact ||
			matched.Kind() != protocolspec.ClientOperationUnsupported ||
			matched.Transport() != protocolspec.ClientOperationTransportWebSocket ||
			matched.BodyKind() != protocolspec.ClientOperationBodyNone ||
			matched.EgressBearing() ||
			len(methods) != 1 ||
			methods[0] != "GET" {
			t.Fatalf("Responses WebSocket operation %q = %+v", identifier, matched)
		}
	}
}

func TestBuiltInCatalogDefinitionsAreImmutableValues(t *testing.T) {
	t.Parallel()

	catalog, err := operationcatalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	first := catalog.Definitions()
	first[0] = protocolspec.ClientOperationDefinition{}
	second := catalog.Definitions()
	if len(second) == 0 || second[0].ID().String() == "" {
		t.Fatal("Definitions() exposed catalog slice ownership")
	}
	methods := second[0].Methods()
	methods[0] = "DELETE"
	if catalog.Definitions()[0].Methods()[0] != "POST" {
		t.Fatal("operation definition exposed method ownership")
	}
}

// An operation that admits query values carries client-chosen data, so it cannot
// be classified as carrying none. The class is written into EgressAttempt as
// durable evidence, and "none" there is a claim that the outbound request held
// nothing from the client.
//
// This is stated as an invariant over the whole catalogue rather than a fix to
// four rows, because the four were added in the same change that classified a
// sibling probe correctly: the rule is what stops the next one repeating it.
func TestQueryBearingOperationsAreNotClassifiedAsCarryingNoPayload(t *testing.T) {
	t.Parallel()

	catalog, err := operationcatalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range catalog.Definitions() {
		if len(definition.AllowedQueryKeys()) == 0 {
			continue
		}
		if definition.PayloadClass() == protocolspec.OperationPayloadNone {
			t.Errorf(
				"%s admits query keys %v yet records its egress as carrying no client payload",
				definition.ID(), definition.AllowedQueryKeys(),
			)
		}
	}
}
