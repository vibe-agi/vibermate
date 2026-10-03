package environment

import (
	"bytes"
	"testing"

	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

func clientToolFixture(t *testing.T) Environment {
	t.Helper()
	value := fixture(t, "native", mustOrigin(t, "https://chatgpt.com"))
	value.ClientToolAccess = &ClientToolAccessPolicy{Enabled: true, Route: &ClientToolRoute{
		ID: "native.mcp", Revision: 1, TargetOrigin: mustProviderOrigin(t, "https://chatgpt.com"), Path: "/backend-api/ps/mcp",
		CredentialOwner: ClientToolCredentialOriginal, EgressProfile: egressprofile.Direct(), Contract: ClientToolContractCodexLegacy,
	}}
	return value
}

func TestClientToolAccessOffCanonicalBytesUnchanged(t *testing.T) {
	value := fixture(t, "native", mustOrigin(t, "https://chatgpt.com"))
	before, err := CanonicalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	beforeDigest, _ := Digest(value)
	value.ClientToolAccess = &ClientToolAccessPolicy{}
	after, err := CanonicalJSON(value)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("OFF changed canonical bytes: %v", err)
	}
	afterDigest, _ := Digest(value)
	if beforeDigest != afterDigest {
		t.Fatal("OFF changed digest")
	}
	value.ClientToolAccess.Route = clientToolFixture(t).ClientToolAccess.Route
	if _, err := CanonicalJSON(value); err == nil {
		t.Fatal("OFF route accepted")
	}
}

func TestClientToolAccessExactResolverAndExclusivePlan(t *testing.T) {
	value := clientToolFixture(t)
	snapshot := mustCompile(t, value)
	binding, err := snapshot.BeginProviderConnection(value.ClientToolAccess.Route.TargetOrigin)
	if err != nil {
		t.Fatal(err)
	}
	facts := RequestFacts{Target: protocolspec.RequestTarget{Method: "POST", Path: "/backend-api/ps/mcp", Transport: protocolspec.ClientOperationTransportHTTP}}
	request, err := snapshot.ResolveClientToolRequest(binding, facts)
	if err != nil {
		t.Fatal(err)
	}
	native, ok := request.ClientToolPlan()
	if !ok || native.Validate() != nil || native.EnvironmentDigest() != snapshot.Digest() {
		t.Fatal("missing frozen native plan")
	}
	if request.PreservesOriginalDestination() || request.UsesUpstreamDestination() || request.UsesOriginalAccount() {
		t.Fatal("native acquired model/original arm")
	}
	if _, ok := request.UpstreamRoute(); ok {
		t.Fatal("native has model route")
	}
	if _, ok := request.OriginalOrigin(); ok {
		t.Fatal("native has original arm")
	}
	if request.Endpoint().ID() != "" || request.ProtocolPlan().ID() != "" || request.Operation().ID().String() != "" {
		t.Fatal("native acquired model endpoint/protocol/operation")
	}
	model, err := snapshot.ResolveConnectionRequest(binding, RequestFacts{Target: protocolspec.RequestTarget{Method: "POST", Path: "/v1/responses", Transport: protocolspec.ClientOperationTransportHTTP}, DownstreamProtocol: wireprofile.ApplicationProtocolHTTP1})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := model.ClientToolPlan(); ok {
		t.Fatal("model plan acquired native arm")
	}
	value.ClientToolAccess.Route.ID = "changed"
	exposed := snapshot.Aggregate()
	exposed.ClientToolAccess.Route.Path = "changed"
	route := native.Route()
	route.ID = "changed"
	if native.Route().ID != "native.mcp" || snapshot.Aggregate().ClientToolAccess.Route.Path != "/backend-api/ps/mcp" {
		t.Fatal("mutable route escaped")
	}
	for _, mutate := range []func(*ConnectionBinding, *RequestFacts){
		func(b *ConnectionBinding, f *RequestFacts) {
			b.ProviderOrigin = mustProviderOrigin(t, "https://other.example")
		},
		func(b *ConnectionBinding, f *RequestFacts) { f.Target.RawQuery = "x=1" },
		func(b *ConnectionBinding, f *RequestFacts) { f.Target.RawPath = f.Target.Path },
		func(b *ConnectionBinding, f *RequestFacts) { f.Target.Path += "/" },
		func(b *ConnectionBinding, f *RequestFacts) { f.Target.Method = "PUT" },
		func(b *ConnectionBinding, f *RequestFacts) {
			f.Target.Transport = protocolspec.ClientOperationTransportWebSocket
		},
	} {
		b, f := binding, facts
		mutate(&b, &f)
		if _, err := snapshot.ResolveClientToolRequest(b, f); err == nil {
			t.Fatal("nonexact native request accepted")
		}
	}
	off := snapshot.Aggregate()
	off.ClientToolAccess = nil
	if _, err := mustCompile(t, off).ResolveClientToolRequest(binding, facts); err == nil {
		t.Fatal("OFF admitted native")
	}
	disabled := snapshot.Aggregate()
	disabled.State = StateDisabled
	if _, err := mustCompile(t, disabled).ResolveClientToolRequest(binding, facts); err == nil {
		t.Fatal("disabled admitted native")
	}
}

func TestClientToolAccessRouteValidationAndTransitions(t *testing.T) {
	for _, mutate := range []func(*ClientToolRoute){
		func(r *ClientToolRoute) { r.CredentialOwner = "managed_account" }, func(r *ClientToolRoute) { r.Path = "/v1/responses" },
		func(r *ClientToolRoute) { r.Contract = "future" }, func(r *ClientToolRoute) { r.Revision = 0 }, func(r *ClientToolRoute) { r.ID = "" },
		func(r *ClientToolRoute) { r.TargetOrigin = mustProviderOrigin(t, "http://localhost:8080") }, func(r *ClientToolRoute) { r.EgressProfile = egressprofile.ProfileRevision{} },
	} {
		v := clientToolFixture(t)
		mutate(v.ClientToolAccess.Route)
		if v.Validate() == nil {
			t.Fatal("invalid native route accepted")
		}
	}
	previous := clientToolFixture(t)
	next := previous.Clone()
	next.Revision++
	next.ClientToolAccess.Route.EgressProfile.DisplayName = "Changed"
	if ValidateTransition(previous, next) == nil {
		t.Fatal("changed route without revision accepted")
	}
	next.ClientToolAccess.Route.Revision++
	if ValidateTransition(previous, next) != nil {
		t.Fatal("valid route revision rejected")
	}
	next = previous.Clone()
	next.Revision++
	next.ClientToolAccess.Route.TargetOrigin = mustProviderOrigin(t, "https://other.example")
	next.ClientToolAccess.Route.Revision++
	if ValidateTransition(previous, next) == nil {
		t.Fatal("route identity became origin alias")
	}
	removed := previous.Clone()
	removed.Revision++
	removed.ClientToolAccess = nil
	retired, err := materializeIdentityHistory(&previous, removed)
	if err != nil || len(retired.RetiredChildIdentities) != 1 {
		t.Fatal("native route retirement lost")
	}
	resurrected := retired.Clone()
	resurrected.Revision++
	resurrected.ClientToolAccess = previous.ClientToolAccess
	if _, err := materializeIdentityHistory(&retired, resurrected); err == nil {
		t.Fatal("retired native route reused")
	}
}
