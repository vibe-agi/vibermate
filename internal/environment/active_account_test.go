package environment

import "testing"

func TestActivateRouteAccountAdvancesOnlyTheOwningAuthorityChain(t *testing.T) {
	t.Parallel()
	current := fixture(t, "work", mustOrigin(t, "https://relay.example"))
	route := &current.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0]
	alternate := RouteAccountReference{
		ID: "account.alternate", Revision: 1, DisplayName: "Alternate",
	}

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
