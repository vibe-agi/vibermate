package environment

import (
	"testing"

	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

func TestOriginalOperationOriginFreezesAliasBesideManagedRoutes(t *testing.T) {
	canonical := mustOrigin(t, "https://api.openai.com")
	snapshot := mustCompile(t, fixture(t, "alias", canonical))
	for _, actual := range []string{"https://alias.example:8443", "http://127.0.0.1:8080"} {
		binding, err := snapshot.BeginClientTargetConnection(mustProviderOrigin(t, actual), canonical)
		if err != nil {
			t.Fatal(err)
		}
		facts := RequestFacts{Target: protocolspec.RequestTarget{Method: "GET", Path: "/backend-api/plugins/featured", RawQuery: "platform=synthetic", Transport: protocolspec.ClientOperationTransportHTTP}, DownstreamProtocol: wireprofile.ApplicationProtocolHTTP1}
		plan, err := snapshot.ResolveConnectionRequest(binding, facts)
		if err != nil {
			t.Fatal(err)
		}
		binding.ProviderOrigin = mustProviderOrigin(t, "https://mutated.example")
		returned := plan.Endpoint()
		returned.origin = mustOrigin(t, "https://mutated.example")
		original, ok := plan.OriginalOperationOrigin()
		if !ok || original.String() != actual || plan.Endpoint().ClientOrigin() != canonical {
			t.Fatalf("frozen original=%s valid=%v", original, ok)
		}
		if _, ok := plan.OriginalOrigin(); ok {
			t.Fatal("managed model route acquired original-account authority")
		}
		facts.Target = protocolspec.RequestTarget{Method: "GET", Path: "/backend-api/codex/models", RawQuery: "client_version=0.163.0", Transport: protocolspec.ClientOperationTransportHTTP}
		models, err := snapshot.ResolveConnectionRequest(binding, facts)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := models.OriginalOperationOrigin(); ok {
			t.Fatal("managed model directory acquired original-account authority")
		}
		facts.Target = protocolspec.RequestTarget{Method: "POST", Path: "/v1/responses", Transport: protocolspec.ClientOperationTransportHTTP}
		semantic, err := snapshot.ResolveConnectionRequest(binding, facts)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := semantic.OriginalOperationOrigin(); ok {
			t.Fatal("model payload acquired original-operation authority")
		}
	}
	if _, ok := (RequestPlan{}).OriginalOperationOrigin(); ok {
		t.Fatal("zero plan acquired authority")
	}
	if _, err := snapshot.BeginClientTargetConnection(mustProviderOrigin(t, "https://alias.example/base"), canonical); err == nil {
		t.Fatal("base path was accepted as a connection authority")
	}
}
