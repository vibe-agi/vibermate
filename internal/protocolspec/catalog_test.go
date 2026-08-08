package protocolspec

import "testing"

func TestCatalogSeparatesRequestAdmissionFromCodecPairs(t *testing.T) {
	t.Parallel()
	semantic := mustOperation(t, "messages.create", DialectAnthropicMessages, ClientOperationSemantic)
	probe := mustOperation(t, "messages.probe", DialectAnthropicMessages, ClientOperationOpaque)
	pairID, err := NewCodecPairID("messages.passthrough")
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewCatalog(
		[]ClientOperationDefinition{probe, semantic},
		[]CodecPairDefinition{{
			ID: pairID, Revision: 1,
			ClientDialect: DialectAnthropicMessages, ProviderDialect: DialectAnthropicMessages,
			ClientOperationIDs:   []ClientOperationID{semantic.ID()},
			RequiredCapabilities: []ProviderCapability{ProviderCapabilityMessages},
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	operations, err := catalog.OperationsForDialect(DialectAnthropicMessages)
	if err != nil || len(operations) != 2 {
		t.Fatalf("operations = %+v, %v", operations, err)
	}
	plan, err := catalog.Resolve(DialectAnthropicMessages, DialectAnthropicMessages)
	if err != nil || len(plan.ClientOperations()) != 1 || plan.ClientOperations()[0].ID() != semantic.ID() {
		t.Fatalf("plan = %+v, %v", plan, err)
	}
	operations[0].definition.methods[0] = "DELETE"
	if catalogOperations, _ := catalog.OperationsForDialect(DialectAnthropicMessages); catalogOperations[0].Methods()[0] == "DELETE" {
		t.Fatal("catalog operation was aliased")
	}
}

func TestCatalogRejectsDuplicateAndCrossDialectReferences(t *testing.T) {
	t.Parallel()
	semantic := mustOperation(t, "messages.create", DialectAnthropicMessages, ClientOperationSemantic)
	pairID, err := NewCodecPairID("bad.pair")
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewCatalog(
		[]ClientOperationDefinition{semantic},
		[]CodecPairDefinition{{
			ID: pairID, Revision: 1,
			ClientDialect: DialectOpenAIResponses, ProviderDialect: DialectAnthropicMessages,
			ClientOperationIDs: []ClientOperationID{semantic.ID()},
		}},
	)
	if err == nil {
		t.Fatal("cross-dialect operation reference was accepted")
	}
}

func mustOperation(
	t *testing.T,
	id string,
	dialect Dialect,
	kind ClientOperationKind,
) ClientOperationDefinition {
	t.Helper()
	operationID, err := NewClientOperationID(id)
	if err != nil {
		t.Fatal(err)
	}
	feature := CodecFeature("")
	payload := OperationPayloadNone
	egress := true
	if kind == ClientOperationSemantic {
		feature = "messages"
		payload = OperationPayloadClientSemantic
	}
	definition, err := NewClientOperationDefinition(ClientOperationOptions{
		ID: operationID, Revision: 1, ClientDialect: dialect,
		Methods: []string{"POST"}, PathPattern: "/v1/messages",
		PathMatch: ClientOperationPathExact, Kind: kind,
		Transport: ClientOperationTransportHTTP, BodyKind: ClientOperationBodyJSON,
		ReplayClass: ClientReplayGenerationCostOnly, CodecFeature: feature,
		MaxBodyBytes: 1024, PayloadClass: payload, EgressBearing: egress,
	})
	if err != nil {
		t.Fatal(err)
	}
	return definition
}
