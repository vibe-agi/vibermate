package protocolspec

import (
	"errors"
	"testing"
)

func TestOperationAndCodecPlanAreImmutable(t *testing.T) {
	t.Parallel()
	operationID, err := NewClientOperationID("anthropic-messages-create")
	if err != nil {
		t.Fatal(err)
	}
	definition, err := NewClientOperationDefinition(ClientOperationOptions{
		ID: operationID, Revision: 1, ClientDialect: DialectAnthropicMessages,
		Methods: []string{"POST"}, PathPattern: "/v1/messages",
		PathMatch: ClientOperationPathExact, Kind: ClientOperationSemantic,
		Transport: ClientOperationTransportHTTP, BodyKind: ClientOperationBodyJSON,
		ReplayClass: ClientReplayGenerationCostOnly, CodecFeature: "messages",
		MaxBodyBytes: 1024, AllowedQueries: []string{"beta=true"},
		AllowedQueryKeys: []string{"client_version", "scope"},
		PayloadClass:     OperationPayloadClientSemantic, EgressBearing: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	codecID, err := NewCodecPairID("anthropic-to-openai")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := NewCodecPlan(
		codecID, 3, DialectAnthropicMessages, DialectOpenAIChat,
		[]ClientOperationDefinition{definition}, []ProviderCapability{ProviderCapabilityMessages},
	)
	if err != nil {
		t.Fatal(err)
	}
	methods := plan.ClientOperations()[0].Methods()
	methods[0] = "DELETE"
	queries := plan.ClientOperations()[0].AllowedQueries()
	queries[0] = "changed=true"
	queryKeys := plan.ClientOperations()[0].AllowedQueryKeys()
	queryKeys[0] = "changed"
	capabilities := plan.RequiredCapabilities()
	capabilities[0] = ProviderCapabilityToolCalls
	operation := plan.ClientOperations()[0]
	if operation.Methods()[0] != "POST" || operation.AllowedQueries()[0] != "beta=true" ||
		operation.AllowedQueryKeys()[0] != "client_version" ||
		plan.RequiredCapabilities()[0] != ProviderCapabilityMessages {
		t.Fatal("caller mutated immutable protocol plan")
	}
}

func TestOperationMatchSeparatesUnknownPathFromKnownContractMismatch(t *testing.T) {
	t.Parallel()
	operationID, err := NewClientOperationID("messages.create")
	if err != nil {
		t.Fatal(err)
	}
	definition, err := NewClientOperationDefinition(ClientOperationOptions{
		ID: operationID, Revision: 1, ClientDialect: DialectAnthropicMessages,
		Methods: []string{"POST"}, PathPattern: "/v1/messages",
		PathMatch: ClientOperationPathExact, Kind: ClientOperationSemantic,
		Transport: ClientOperationTransportHTTP, BodyKind: ClientOperationBodyJSON,
		ReplayClass: ClientReplayGenerationCostOnly, CodecFeature: "messages",
		MaxBodyBytes: 1024, AllowedQueries: []string{"beta=true"},
		AllowedQueryKeys: []string{"client_version", "scope"},
		PayloadClass:     OperationPayloadClientSemantic, EgressBearing: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := definition
	for _, test := range []struct {
		name      string
		target    RequestTarget
		matched   bool
		pathKnown bool
		wantErr   bool
	}{
		{"match", RequestTarget{Method: "POST", Path: "/v1/messages", RawQuery: "beta=true", Transport: ClientOperationTransportHTTP}, true, true, false},
		{"dynamic control query", RequestTarget{Method: "POST", Path: "/v1/messages", RawQuery: "client_version=0.147.0", Transport: ClientOperationTransportHTTP}, true, true, false},
		{"dynamic query key order is not semantic", RequestTarget{Method: "POST", Path: "/v1/messages", RawQuery: "scope=USER&client_version=0.147.0", Transport: ClientOperationTransportHTTP}, true, true, false},
		{"dynamic query duplicate key", RequestTarget{Method: "POST", Path: "/v1/messages", RawQuery: "scope=USER&scope=GLOBAL", Transport: ClientOperationTransportHTTP}, false, true, false},
		{"unknown query key", RequestTarget{Method: "POST", Path: "/v1/messages", RawQuery: "other=value", Transport: ClientOperationTransportHTTP}, false, true, false},
		{"wrong method", RequestTarget{Method: "GET", Path: "/v1/messages", Transport: ClientOperationTransportHTTP}, false, true, false},
		{"unknown path", RequestTarget{Method: "POST", Path: "/v1/other", Transport: ClientOperationTransportHTTP}, false, false, false},
		{"noncanonical", RequestTarget{Method: "POST", Path: "/v1/../messages", Transport: ClientOperationTransportHTTP}, false, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			matched, pathKnown, matchErr := plan.Match(test.target)
			if matched != test.matched || pathKnown != test.pathKnown || (matchErr != nil) != test.wantErr {
				t.Fatalf("Match() = %t, %t, %v", matched, pathKnown, matchErr)
			}
		})
	}
}

func TestSelectOperationUsesExactBeforePrefixAndFailsClosed(t *testing.T) {
	t.Parallel()
	exact := operationForSelection(t, "responses.create", ClientOperationPathExact, "POST", ClientOperationTransportHTTP)
	prefix := operationForSelection(t, "responses.manage", ClientOperationPathPrefix, "DELETE", ClientOperationTransportHTTP)
	websocket := operationForSelection(t, "responses.websocket", ClientOperationPathExact, "GET", ClientOperationTransportWebSocket)
	operations := []ClientOperationDefinition{prefix, websocket, exact}
	selected, err := SelectOperation(operations, RequestTarget{
		Method: "POST", Path: "/v1/responses", Transport: ClientOperationTransportHTTP,
	})
	if err != nil || selected.ID().String() != "responses.create" {
		t.Fatalf("selected = %q, %v", selected.ID().String(), err)
	}
	if _, err := SelectOperation(operations, RequestTarget{
		Method: "DELETE", Path: "/v1/responses", Transport: ClientOperationTransportHTTP,
	}); !errors.Is(err, ErrOperationContractMismatch) {
		t.Fatalf("exact path fell through to prefix: %v", err)
	}
	selected, err = SelectOperation(operations, RequestTarget{
		Method: "DELETE", Path: "/v1/responses/abc", Transport: ClientOperationTransportHTTP,
	})
	if err != nil || selected.ID().String() != "responses.manage" {
		t.Fatalf("prefix selected = %q, %v", selected.ID().String(), err)
	}
}

func operationForSelection(
	t *testing.T,
	id string,
	pathMatch ClientOperationPathMatch,
	method string,
	transport ClientOperationTransport,
) ClientOperationDefinition {
	t.Helper()
	operationID, err := NewClientOperationID(id)
	if err != nil {
		t.Fatal(err)
	}
	kind := ClientOperationUnsupported
	body := ClientOperationBodyNone
	payload := OperationPayloadNone
	maxBody := int64(0)
	egress := false
	if id == "responses.create" {
		kind = ClientOperationSemantic
		body = ClientOperationBodyJSON
		payload = OperationPayloadClientSemantic
		maxBody = 1024
		egress = true
	}
	definition, err := NewClientOperationDefinition(ClientOperationOptions{
		ID: operationID, Revision: 1, ClientDialect: DialectOpenAIResponses,
		Methods: []string{method}, PathPattern: "/v1/responses", PathMatch: pathMatch,
		Kind: kind, Transport: transport, BodyKind: body,
		ReplayClass: ClientReplayNonReplayable, CodecFeature: func() CodecFeature {
			if kind == ClientOperationSemantic {
				return "responses"
			}
			return ""
		}(), MaxBodyBytes: maxBody, PayloadClass: payload, EgressBearing: egress,
	})
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func TestOperationRejectsPayloadBearingOriginalOriginClaims(t *testing.T) {
	t.Parallel()
	if OperationPayloadClientSemantic.AllowsOriginalOrigin() ||
		!OperationPayloadUnknown.CarriesClientPayload() ||
		!OperationPayloadControl.AllowsOriginalOrigin() {
		t.Fatal("payload classification failed closed incorrectly")
	}
}

func TestCodecRejectsMixedClientDialects(t *testing.T) {
	t.Parallel()
	operationID, _ := NewClientOperationID("responses-create")
	definition, err := NewClientOperationDefinition(ClientOperationOptions{
		ID: operationID, Revision: 1, ClientDialect: DialectOpenAIResponses,
		Methods: []string{"POST"}, PathPattern: "/v1/responses",
		PathMatch: ClientOperationPathExact, Kind: ClientOperationSemantic,
		Transport: ClientOperationTransportHTTP, BodyKind: ClientOperationBodyJSON,
		ReplayClass: ClientReplayGenerationCostOnly, CodecFeature: "responses",
		MaxBodyBytes: 1024, PayloadClass: OperationPayloadClientSemantic, EgressBearing: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	codecID, _ := NewCodecPairID("wrong-client-dialect")
	if _, err := NewCodecPlan(
		codecID, 1, DialectAnthropicMessages, DialectOpenAIChat,
		[]ClientOperationDefinition{definition}, nil,
	); err == nil {
		t.Fatal("mixed client dialect was accepted")
	}
}
