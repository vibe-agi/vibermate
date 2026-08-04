package access_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
)

func TestDownstreamProtocolsFollowTheActiveAccessTransport(t *testing.T) {
	t.Parallel()

	compiler := testCompiler(t)
	accessID := newAccessID(t, "access-downstream-protocol")
	aggregate := testAggregate(t, accessID, 1, "Protocol selection")
	strictTLS, err := compiler.Compile(aggregate)
	if err != nil {
		t.Fatal(err)
	}
	projection := newProjection(t)
	if err := projection.Restore([]access.AccessPlanSnapshot{strictTLS}); err != nil {
		t.Fatal(err)
	}
	binding, err := projection.ResolveClientOrigin(
		aggregate.AgentEndpoint.ClientOrigin,
	)
	if err != nil {
		t.Fatal(err)
	}
	protocols, err := projection.ResolveDownstreamProtocols(binding)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(protocols, []access.ApplicationProtocol{
		access.ApplicationProtocolHTTP2,
		access.ApplicationProtocolHTTP1,
	}) {
		t.Fatalf("strict TLS protocols = %v", protocols)
	}

	loopback := aggregate.Clone()
	loopback.Binding.Revision = 2
	loopback.Profiles[0].Revision = 2
	loopback.ProviderTargets[0].Revision = 2
	loopback.AccountBindings[0].Revision = 2
	loopback.RouteSets[0].Revision = 2
	loopback.EgressPolicy.Revision = 2
	loopback.PluginPlan.Revision = 2
	loopbackOrigin, err := access.NewProviderOrigin("http://127.0.0.1:23333/v1")
	if err != nil {
		t.Fatal(err)
	}
	loopback.ProviderTargets[0].Origin = loopbackOrigin
	loopbackPlan, err := compiler.Compile(loopback)
	if err != nil {
		t.Fatal(err)
	}
	if err := projection.Publish(loopbackPlan); err != nil {
		t.Fatal(err)
	}
	protocols, err = projection.ResolveDownstreamProtocols(binding)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(protocols, []access.ApplicationProtocol{
		access.ApplicationProtocolHTTP1,
	}) {
		t.Fatalf("loopback cleartext protocols = %v", protocols)
	}

	protocols[0] = access.ApplicationProtocolHTTP2
	again, err := projection.ResolveDownstreamProtocols(binding)
	if err != nil || !slices.Equal(again, []access.ApplicationProtocol{
		access.ApplicationProtocolHTTP1,
	}) {
		t.Fatalf("protocol result aliases projection state: %v, %v", again, err)
	}

	projection.MarkUnavailable(accessID)
	if _, err := projection.ResolveDownstreamProtocols(binding); !errors.Is(
		err,
		access.ErrProjectionUnavailable,
	) {
		t.Fatalf("unavailable projection error = %v", err)
	}
}

func TestProductPresentationCanNarrowTheDownstreamProtocol(t *testing.T) {
	t.Parallel()

	compiler := testCompiler(t)
	accessID := newAccessID(t, "access-product-protocol")
	aggregate := testAggregate(t, accessID, 1, "Product presentation")
	aggregate.Profiles[0].UpstreamWireProfileRef =
		access.ClaudeCodeUpstreamWireProfileRef()
	plan, err := compiler.Compile(aggregate)
	if err != nil {
		t.Fatal(err)
	}
	projection := newProjection(t)
	if err := projection.Restore([]access.AccessPlanSnapshot{plan}); err != nil {
		t.Fatal(err)
	}
	binding, err := projection.ResolveClientOrigin(
		aggregate.AgentEndpoint.ClientOrigin,
	)
	if err != nil {
		t.Fatal(err)
	}
	protocols, err := projection.ResolveDownstreamProtocols(binding)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(protocols, []access.ApplicationProtocol{
		access.ApplicationProtocolHTTP1,
	}) {
		t.Fatalf("product presentation protocols = %v", protocols)
	}
}
