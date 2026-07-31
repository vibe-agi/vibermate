package operationcatalog_test

import (
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
)

func TestM0CatalogHasOneExactResponsesCreateOperation(t *testing.T) {
	t.Parallel()

	catalog, err := operationcatalog.M0()
	if err != nil {
		t.Fatal(err)
	}
	identifiers := catalog.SemanticOperationIDs(
		access.DialectOpenAIResponses,
	)
	if len(identifiers) != 1 ||
		identifiers[0].String() != operationcatalog.OpenAIResponsesCreateID {
		t.Fatalf("Responses semantic operation IDs = %v", identifiers)
	}
	var matched access.ClientOperationDefinition
	for _, definition := range catalog.Definitions() {
		if definition.ID() == identifiers[0] {
			matched = definition
		}
	}
	if matched.PathPattern() != "/v1/responses" ||
		matched.PathMatch() != access.ClientOperationPathExact ||
		matched.Kind() != access.ClientOperationSemantic ||
		matched.Transport() != access.ClientOperationTransportHTTP ||
		matched.CodecFeature() != "responses" ||
		matched.ReplayClass() != access.ClientReplayGenerationCostOnly ||
		!matched.EgressBearing() {
		t.Fatalf("Responses operation = %+v", matched)
	}
	if methods := matched.Methods(); len(methods) != 1 ||
		methods[0] != "POST" {
		t.Fatalf("Responses operation methods = %v", methods)
	}
}

func TestM0CatalogDeclaresResponsesWebSocketAsUnsupported(t *testing.T) {
	t.Parallel()

	catalog, err := operationcatalog.M0()
	if err != nil {
		t.Fatal(err)
	}
	var matched access.ClientOperationDefinition
	for _, definition := range catalog.Definitions() {
		if definition.ID().String() ==
			operationcatalog.OpenAIResponsesWebSocketUnsupportedID {
			matched = definition
			break
		}
	}
	methods := matched.Methods()
	if matched.PathPattern() != "/v1/responses" ||
		matched.PathMatch() != access.ClientOperationPathExact ||
		matched.Kind() != access.ClientOperationUnsupported ||
		matched.Transport() != access.ClientOperationTransportWebSocket ||
		matched.BodyKind() != access.ClientOperationBodyNone ||
		matched.EgressBearing() ||
		len(methods) != 1 ||
		methods[0] != "GET" {
		t.Fatalf("Responses WebSocket operation = %+v", matched)
	}
}

func TestM0CatalogDefinitionsAreImmutableValues(t *testing.T) {
	t.Parallel()

	catalog, err := operationcatalog.M0()
	if err != nil {
		t.Fatal(err)
	}
	first := catalog.Definitions()
	first[0] = access.ClientOperationDefinition{}
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
