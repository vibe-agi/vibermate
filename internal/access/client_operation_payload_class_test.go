package access_test

import (
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
)

func payloadClassBaseOptions(
	t *testing.T,
) access.ClientOperationOptions {
	t.Helper()

	identifier, err := access.NewClientOperationID("payload-class-operation")
	if err != nil {
		t.Fatal(err)
	}
	return access.ClientOperationOptions{
		ID:            identifier,
		Revision:      1,
		ClientDialect: access.DialectAnthropicMessages,
		Methods:       []string{http.MethodPost},
		PathPattern:   "/v1/messages/count_tokens",
		PathMatch:     access.ClientOperationPathExact,
		Kind:          access.ClientOperationAuxiliary,
		Transport:     access.ClientOperationTransportHTTP,
		BodyKind:      access.ClientOperationBodyJSON,
		ReplayClass:   access.ClientReplaySafe,
		CodecFeature:  "token_count",
		MaxBodyBytes:  1 << 20,
		PayloadClass:  access.OperationPayloadClientSemantic,
		EgressBearing: true,
	}
}

func TestClientOperationFreezesPayloadClass(t *testing.T) {
	t.Parallel()

	definition, err := access.NewClientOperationDefinition(
		payloadClassBaseOptions(t),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := definition.PayloadClass(); got !=
		access.OperationPayloadClientSemantic {
		t.Fatalf("payload class = %q", got)
	}
}

func TestClientOperationRequiresADeclaredPayloadClass(t *testing.T) {
	t.Parallel()

	options := payloadClassBaseOptions(t)
	options.PayloadClass = ""
	if _, err := access.NewClientOperationDefinition(options); err == nil {
		t.Fatal("empty payload class was accepted")
	}
}

func TestClientOperationRejectsAnInvalidPayloadClass(t *testing.T) {
	t.Parallel()

	options := payloadClassBaseOptions(t)
	options.PayloadClass = access.OperationPayloadClass("prompt")
	if _, err := access.NewClientOperationDefinition(options); err == nil {
		t.Fatal("invalid payload class was accepted")
	}
}

// The unknown class exists only for requests that no catalog entry claims. A
// catalogued operation that declares it would let an uncatalogued-path policy
// be attached to a known operation.
func TestClientOperationRejectsTheUnknownPayloadClass(t *testing.T) {
	t.Parallel()

	options := payloadClassBaseOptions(t)
	options.PayloadClass = access.OperationPayloadUnknown
	if _, err := access.NewClientOperationDefinition(options); err == nil {
		t.Fatal("catalogued operation declared the unknown payload class")
	}
}

func TestClientOperationRejectsClientPayloadWithoutABody(t *testing.T) {
	t.Parallel()

	for _, class := range []access.OperationPayloadClass{
		access.OperationPayloadClientSemantic,
		access.OperationPayloadClientData,
	} {
		options := payloadClassBaseOptions(t)
		options.Methods = []string{http.MethodGet}
		options.BodyKind = access.ClientOperationBodyNone
		options.MaxBodyBytes = 0
		options.PayloadClass = class
		if _, err := access.NewClientOperationDefinition(options); err == nil {
			t.Fatalf("bodyless operation declared payload class %q", class)
		}
	}
}

func TestSemanticClientOperationMustCarryClientSemanticPayload(t *testing.T) {
	t.Parallel()

	for _, class := range []access.OperationPayloadClass{
		access.OperationPayloadNone,
		access.OperationPayloadControl,
		access.OperationPayloadClientData,
	} {
		options := payloadClassBaseOptions(t)
		options.Kind = access.ClientOperationSemantic
		options.CodecFeature = "messages"
		options.PathPattern = "/v1/messages"
		options.PayloadClass = class
		if _, err := access.NewClientOperationDefinition(options); err == nil {
			t.Fatalf("semantic operation declared payload class %q", class)
		}
	}
}

func TestOperationPayloadClassReportsClientPayload(t *testing.T) {
	t.Parallel()

	for class, carries := range map[access.OperationPayloadClass]bool{
		access.OperationPayloadNone:           false,
		access.OperationPayloadControl:        false,
		access.OperationPayloadClientData:     true,
		access.OperationPayloadClientSemantic: true,
		access.OperationPayloadUnknown:        true,
	} {
		if got := class.CarriesClientPayload(); got != carries {
			t.Fatalf("%q carries client payload = %v", class, got)
		}
	}
}

// Original-origin forwarding is only ever admissible for a class that is
// proven not to carry client payload.
func TestOnlyProvenNoPayloadClassesAllowOriginalOrigin(t *testing.T) {
	t.Parallel()

	for class, allowed := range map[access.OperationPayloadClass]bool{
		access.OperationPayloadNone:           true,
		access.OperationPayloadControl:        true,
		access.OperationPayloadClientData:     false,
		access.OperationPayloadClientSemantic: false,
		access.OperationPayloadUnknown:        false,
		access.OperationPayloadClass("other"): false,
	} {
		if got := class.AllowsOriginalOrigin(); got != allowed {
			t.Fatalf("%q allows original origin = %v", class, got)
		}
	}
}
