package accountoperation

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/modelcatalog"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/upstreamservice"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

func TestCapturedModelCatalogAliasesUseTargetCapabilities(t *testing.T) {
	route := modelCatalogRoute(t, []environment.ModelMapping{
		{RequestedModel: "alias", UpstreamModel: "target"},
		{RequestedModel: "new-z", UpstreamModel: "target"},
		{RequestedModel: "new-a", UpstreamModel: "target"},
	})
	body := []byte(`{"catalog_metadata":{"future":[1,2]},"models":[{"slug":"alias","display_name":"Old model","context_window":100,"service_tiers":[]},{"slug":"target","id":"target","display_name":"Target","context_window":400000,"default_service_tier":"priority","service_tiers":[{"id":"priority","future":true}],"reasoning":{"future":["max",7]},"unknown":{"nested":"preserved"}},{"slug":"untouched","future":"exact"}]}`)
	original := bytes.Clone(body)
	got, err := transformCapturedModelCatalog(body, route)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Metadata json.RawMessage              `json:"catalog_metadata"`
		Models   []map[string]json.RawMessage `json:"models"`
	}
	if json.Unmarshal(got, &result) != nil || len(result.Models) != 5 || string(result.Metadata) != `{"future":[1,2]}` {
		t.Fatalf("transformed catalog = %s", got)
	}
	for index, want := range []string{"alias", "target", "untouched", "new-a", "new-z"} {
		var slug string
		if json.Unmarshal(result.Models[index]["slug"], &slug) != nil || slug != want {
			t.Fatalf("catalog order/identity = %s", got)
		}
	}
	for _, index := range []int{0, 3, 4} {
		model := result.Models[index]
		for _, key := range []string{"context_window", "default_service_tier", "service_tiers", "reasoning", "unknown"} {
			if !bytes.Equal(model[key], result.Models[1][key]) {
				t.Fatalf("alias inherited unrelated %s: %s", key, got)
			}
		}
		if !bytes.Equal(model["slug"], model["id"]) || !bytes.Equal(model["slug"], model["display_name"]) {
			t.Fatalf("alias identity disagrees: %s", got)
		}
	}
	if string(result.Models[2]["future"]) != `"exact"` || !bytes.Equal(body, original) {
		t.Fatal("unmapped entry or upstream body changed")
	}
	aliases := route.ModelAliases()
	aliases[0].UpstreamModel = "missing"
	again, err := transformCapturedModelCatalog(body, route)
	if err != nil || !bytes.Equal(got, again) {
		t.Fatal("mutating returned aliases changed the frozen catalog mapping")
	}
}

func TestCapturedModelCatalogOmitsUnavailableAliasesWithoutChaining(t *testing.T) {
	route := modelCatalogRoute(t, []environment.ModelMapping{
		{RequestedModel: "alias", UpstreamModel: "missing"},
		{RequestedModel: "absent", UpstreamModel: "missing"},
		{RequestedModel: "first", UpstreamModel: "second"},
		{RequestedModel: "second", UpstreamModel: "third"},
	})
	got, err := transformCapturedModelCatalog([]byte(`{"models":[{"slug":"alias","capability":"unrelated"},{"slug":"second","capability":"second"},{"slug":"third","capability":"third"}]}`), route)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Models []struct{ Slug, Capability string } `json:"models"`
	}
	if json.Unmarshal(got, &result) != nil || len(result.Models) != 3 {
		t.Fatalf("unavailable alias survived: %s", got)
	}
	if result.Models[0].Slug != "second" || result.Models[0].Capability != "third" || result.Models[2].Slug != "first" || result.Models[2].Capability != "second" {
		t.Fatalf("exact mappings were chained or target metadata changed: %s", got)
	}
}

func TestCapturedModelCatalogPassthroughIsByteExact(t *testing.T) {
	body := []byte("{ \"models\": [ {\"slug\":\"model\",\"future\":1.0} ], \"metadata\": null }\n")
	got, err := transformCapturedModelCatalog(body, environment.CompiledRoutePlan{})
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("passthrough changed bytes: %s, %v", got, err)
	}
}

func TestCapturedModelCatalogRejectsAmbiguityAndExpansionBeyondBounds(t *testing.T) {
	route := modelCatalogRoute(t, []environment.ModelMapping{{RequestedModel: "alias", UpstreamModel: "target"}})
	for _, body := range []string{
		`null`, `{"models":null}`, `{"models":[null]}`, `{"models":[{"slug":"target"},{"slug":"target"}]}`,
		`{"models":[{"slug":"target","Slug":"other"}]}`, `{"models":[{"slug":"target"}],"models":[]}`,
		`{"models":[{"slug":"target","future":"` + strings.Repeat("x", upstreamservice.MaxCodexModelsBodyBytes/2) + `"}]}`,
	} {
		if _, err := transformCapturedModelCatalog([]byte(body), route); !errors.Is(err, ErrInvalidResponse) {
			t.Fatalf("ambiguous or expanded catalog accepted: %v", err)
		}
	}
	models := make([]map[string]string, modelcatalog.MaxCatalogModels)
	// Count overflow is tested with legal small identifiers so identifier limits
	// cannot be the reason the added alias is rejected.
	for index := range models {
		models[index] = map[string]string{"slug": "model-" + strconv.Itoa(index)}
	}
	models[0]["slug"] = "target"
	body, err := json.Marshal(map[string]any{"models": models})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transformCapturedModelCatalog(body, route); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("model count expansion accepted: %v", err)
	}
}

func modelCatalogRoute(t *testing.T, mappings []environment.ModelMapping) environment.CompiledRoutePlan {
	t.Helper()
	operations, err := operationcatalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	pair, err := protocolspec.NewCodecPairID("test.responses.passthrough")
	if err != nil {
		t.Fatal(err)
	}
	capabilities := []protocolspec.ProviderCapability{protocolspec.ProviderCapabilityMessages, protocolspec.ProviderCapabilityStreaming, protocolspec.ProviderCapabilityToolCalls}
	protocols, err := protocolspec.NewCatalog(operations.Definitions(), []protocolspec.CodecPairDefinition{{ID: pair, Revision: 1, ClientDialect: protocolspec.DialectOpenAIResponses, ProviderDialect: protocolspec.DialectOpenAIResponses, ClientOperationIDs: operations.SemanticOperationIDs(protocolspec.DialectOpenAIResponses), RequiredCapabilities: capabilities}})
	if err != nil {
		t.Fatal(err)
	}
	wires, err := wireprofile.BuiltInCatalog()
	if err != nil {
		t.Fatal(err)
	}
	compiler, err := environment.NewCompiler(nil, nil, protocols, wires)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := originidentity.ParseClientOrigin("https://chatgpt.com")
	if err != nil {
		t.Fatal(err)
	}
	providerOrigin, err := originidentity.ParseProviderOrigin(origin.String())
	if err != nil {
		t.Fatal(err)
	}
	route := environment.UpstreamRoute{ID: "route.models", Revision: 1, ProviderTarget: environment.ProviderTarget{ID: "target.models", Revision: 1, Origin: providerOrigin, RealmID: "realm.models", Capabilities: capabilities}, BackendProtocol: "openai_responses", AccountPolicy: environment.RouteAccountPolicy{Revision: 1, Mode: environment.AccountSelectionOriginal}, ModelPolicy: environment.ModelPolicy{Revision: 1, Mode: environment.ModelModeMap, Mappings: mappings}, WireProfileRef: wireprofile.UpstreamWireProfileFollowClientValue}
	aggregate := environment.Environment{ID: "environment.models", Name: "Model directory", State: environment.StateActive, Revision: 1, ContentRecording: environment.DefaultContentRecordingPolicy(), ClientEndpoints: []environment.ClientEndpoint{{ID: "endpoint.models", Revision: 1, ClientOrigin: origin, ProtocolPlans: []environment.ClientProtocolPlan{{ID: "protocol.models", Revision: 1, ClientProtocol: environment.ClientProtocolOpenAIResponses, ClientAdapterPolicy: environment.ClientAdapterPolicy{ID: "adapter.models", Revision: 1}, EgressProfile: egressprofile.Inherit(), Destination: environment.DestinationPlan{Kind: environment.DestinationKindUpstream, Upstream: &environment.UpstreamPlan{DefaultRouteID: route.ID, RouteSet: environment.RouteSet{ID: "routes.models", Revision: 1, CandidateRouteIDs: []environment.UpstreamRouteID{route.ID}}, Routes: []environment.UpstreamRoute{route}}}}}}}}
	snapshot, err := compiler.Compile(aggregate)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, ok := snapshot.LookupCompiledClientOrigin(origin)
	if !ok {
		t.Fatal("compiled endpoint missing")
	}
	set, ok := endpoint.ProtocolPlans()[0].UpstreamRouteSet()
	if !ok {
		t.Fatal("compiled routes missing")
	}
	compiled, err := set.DefaultRoute()
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}
