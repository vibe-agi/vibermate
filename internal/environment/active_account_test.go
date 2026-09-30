package environment

import "testing"

func TestOriginalAccountActivationPreservesScopeAndRejectsCrossOrigin(t *testing.T) {
	current := fixture(t, "work", mustOrigin(t, "https://api.example"))
	route := &current.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0]
	route.AllowAccountHistory = true
	fixed := route.AccountPolicy.Accounts[0]
	original, changed, err := ActivateRouteOriginalAccount(current, route.ID)
	if err != nil || !changed {
		t.Fatalf("original activation: %v, %v", changed, err)
	}
	next := original.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0]
	if next.AccountPolicy.Mode != AccountSelectionOriginal || next.AllowAccountHistory || len(next.AccountPolicy.Accounts) != 1 || next.AccountPolicy.FixedAccountID != "" {
		t.Fatalf("unexpected authority: %+v", next)
	}
	if err := ValidateTransition(current, original); err != nil {
		t.Fatal(err)
	}
	manual, changed, err := ActivateRouteAccount(original, route.ID, fixed)
	if err != nil || !changed || manual.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0].AccountPolicy.FixedAccountID != fixed.ID {
		t.Fatalf("manual activation: %v, %v", changed, err)
	}
	route.ProviderTarget.Origin = mustProviderOrigin(t, "https://other.example")
	if _, _, err := ActivateRouteOriginalAccount(current, route.ID); err == nil {
		t.Fatal("cross-origin passthrough accepted")
	}
	invalid := original.Clone()
	invalid.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0].ProviderTarget.Origin = route.ProviderTarget.Origin
	if _, err := testCompiler(t, nil).Compile(invalid); err == nil {
		t.Fatal("compiler admitted cross-origin passthrough")
	}
	// A verified client base-URL alias can differ from the canonical flow.
	// Its credentials must never be forwarded to the canonical provider.
	plan := RequestPlan{
		originalOrigin: mustProviderOrigin(t, "https://client-relay.example"),
		upstreamRoute:  &CompiledRoutePlan{target: next.ProviderTarget, accountPolicy: CompiledAccountPolicy{mode: AccountSelectionOriginal}},
	}
	if _, ok := plan.OriginalOrigin(); ok {
		t.Fatal("client target alias gained cross-origin credential authority")
	}
}

func TestActivateRouteAccountAdvancesOnlyTheOwningAuthorityChain(t *testing.T) {
	t.Parallel()
	current := fixture(t, "work", mustOrigin(t, "https://relay.example"))
	route := &current.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0]
	alternate := RouteAccountReference{
		ID: "account.alternate", Revision: 1, DisplayName: "Alternate",
	}
	route.AccountPolicy.Accounts = append(route.AccountPolicy.Accounts, alternate)

	candidate, changed, err := ActivateRouteAccount(current, route.ID, alternate)
	if err != nil || !changed {
		t.Fatalf("activate = changed %t, error %v", changed, err)
	}
	nextEndpoint := candidate.ClientEndpoints[0]
	nextPlan := nextEndpoint.ProtocolPlans[0]
	nextRoute := nextPlan.Destination.Upstream.Routes[0]
	if candidate.Revision != current.Revision+1 ||
		nextEndpoint.Revision != current.ClientEndpoints[0].Revision+1 ||
		nextPlan.Revision != current.ClientEndpoints[0].ProtocolPlans[0].Revision+1 ||
		nextRoute.Revision != route.Revision+1 ||
		nextRoute.AccountPolicy.Revision != route.AccountPolicy.Revision+1 ||
		nextRoute.AccountPolicy.FixedAccountID != "account.alternate" {
		t.Fatalf("candidate revisions = %+v", candidate)
	}
	if err := ValidateTransition(current, candidate); err != nil {
		t.Fatal(err)
	}
}

func TestActivateRouteAccountRejectsScriptedRoutes(t *testing.T) {
	t.Parallel()
	current := fixture(t, "work", mustOrigin(t, "https://relay.example"))
	route := &current.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0]
	route.AccountPolicy.Mode = AccountSelectionJavaScript
	route.AccountPolicy.FixedAccountID = ""
	if _, _, err := ActivateRouteAccount(current, route.ID, RouteAccountReference{
		ID: "account.alternate", Revision: 1, DisplayName: "Alternate",
	}); err == nil {
		t.Fatal("scripted Route accepted a manual Account activation")
	}
}
