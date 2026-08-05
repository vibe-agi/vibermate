package access_test

import (
	"errors"
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
)

func TestIngressResolverUsesTheSameAtomicPlanProjection(t *testing.T) {
	t.Parallel()

	compiler := testCompiler(t)
	accessID := newAccessID(t, "access-ingress")
	aggregate := testAggregate(t, accessID, 1, "Ingress")
	plan, err := compiler.Compile(aggregate)
	if err != nil {
		t.Fatalf("compile plan: %v", err)
	}
	projection := newProjection(t)
	if err := projection.Restore([]access.AccessPlanSnapshot{plan}); err != nil {
		t.Fatalf("restore projection: %v", err)
	}

	binding, err := projection.ResolveClientOrigin(
		aggregate.AgentEndpoint.ClientOrigin,
	)
	if err != nil {
		t.Fatalf("resolve ClientOrigin: %v", err)
	}
	if binding.AccessID() != accessID ||
		binding.AgentEndpointID() != aggregate.AgentEndpoint.ID ||
		binding.ClientDialect() != access.DialectAnthropicMessages ||
		binding.AccessRevision() != 1 ||
		binding.PlanHash() != plan.PlanHash() {
		t.Fatalf("ingress binding = %+v", binding)
	}

	unknown, err := access.NewClientOrigin("https://unknown.example.test:443")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projection.ResolveClientOrigin(unknown); !errors.Is(
		err,
		access.ErrAgentEndpointNotConfigured,
	) {
		t.Fatalf("unknown ClientOrigin resolution error = %v", err)
	}

	projection.MarkUnavailable(accessID)
	if _, err := projection.ResolveClientOrigin(
		aggregate.AgentEndpoint.ClientOrigin,
	); !errors.Is(err, access.ErrProjectionUnavailable) {
		t.Fatalf("unavailable Access ingress resolution error = %v", err)
	}
}

func TestProjectionRejectsDuplicateClientOriginsAcrossAccesses(t *testing.T) {
	t.Parallel()

	compiler := testCompiler(t)
	firstAggregate := testAggregate(
		t,
		newAccessID(t, "access-origin-first"),
		1,
		"First",
	)
	secondAggregate := testAggregate(
		t,
		newAccessID(t, "access-origin-second"),
		1,
		"Second",
	)
	secondAggregate.AgentEndpoint.ClientOrigin =
		firstAggregate.AgentEndpoint.ClientOrigin
	secondAggregate = refreshOriginalPassthrough(t, secondAggregate)
	first, err := compiler.Compile(firstAggregate)
	if err != nil {
		t.Fatal(err)
	}
	second, err := compiler.Compile(secondAggregate)
	if err != nil {
		t.Fatal(err)
	}
	projection := newProjection(t)
	if err := projection.Restore([]access.AccessPlanSnapshot{
		first,
		second,
	}); !errors.Is(err, access.ErrAgentEndpointConflict) {
		t.Fatalf("duplicate ClientOrigin restore error = %v", err)
	}
}

func TestIngressBindingAllowsProviderPlanAdvanceButRejectsEndpointChange(
	t *testing.T,
) {
	t.Parallel()

	compiler := testCompiler(t)
	accessID := newAccessID(t, "access-ingress-revalidation")
	revisionOne, err := compiler.Compile(
		testAggregate(t, accessID, 1, "Revision one"),
	)
	if err != nil {
		t.Fatal(err)
	}
	providerOnlyAggregate := testAggregate(
		t,
		accessID,
		2,
		"Provider-only revision",
	)
	providerOnlyAggregate.AgentEndpoint.Revision = 1
	providerOnlyRevision, err := compiler.Compile(providerOnlyAggregate)
	if err != nil {
		t.Fatal(err)
	}
	frozen := revisionOne.IngressBinding()
	if err := frozen.ValidateCurrent(
		providerOnlyRevision.IngressBinding(),
	); err != nil {
		t.Fatalf("provider-only binding advance was rejected: %v", err)
	}
	if err := frozen.ValidateSnapshot(providerOnlyRevision); err != nil {
		t.Fatalf("provider-only snapshot advance was rejected: %v", err)
	}

	changedAggregate := testAggregate(t, accessID, 3, "Endpoint changed")
	changedEndpointID, err := access.NewAgentEndpointID(
		"agent-endpoint-replacement",
	)
	if err != nil {
		t.Fatal(err)
	}
	changedAggregate.Binding.AgentEndpointID = changedEndpointID
	changedAggregate.AgentEndpoint.ID = changedEndpointID
	changedEndpoint, err := compiler.Compile(changedAggregate)
	if err != nil {
		t.Fatal(err)
	}
	if err := frozen.ValidateCurrent(
		changedEndpoint.IngressBinding(),
	); !errors.Is(err, access.ErrAgentEndpointNotConfigured) {
		t.Fatalf("changed endpoint binding validation error = %v", err)
	}
	if err := frozen.ValidateSnapshot(
		changedEndpoint,
	); !errors.Is(err, access.ErrAgentEndpointNotConfigured) {
		t.Fatalf("changed endpoint snapshot validation error = %v", err)
	}
}
