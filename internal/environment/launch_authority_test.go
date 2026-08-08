package environment

import (
	"errors"
	"testing"
)

func TestLaunchAuthorityTreatsMixedAccountModesAsClientCredentialPreserving(t *testing.T) {
	t.Parallel()

	base := fixture(t, "work", mustOrigin(t, "https://relay.example"))
	passthrough := mustCompile(t, base)
	mixedAggregate, catalog := withManagedRoute(base, 0)
	mixed, err := testCompiler(t, catalog).Compile(mixedAggregate)
	if err != nil {
		t.Fatal(err)
	}
	allManagedAggregate, allCatalog := withManagedRoute(mixedAggregate, 1)
	allManaged, err := testCompiler(t, mergeAccountCatalogs(catalog, allCatalog)).Compile(allManagedAggregate)
	if err != nil {
		t.Fatal(err)
	}

	passthroughBoundary, err := NewLaunchAuthorityBoundary(passthrough)
	if err != nil {
		t.Fatal(err)
	}
	mixedBoundary, err := NewLaunchAuthorityBoundary(mixed)
	if err != nil {
		t.Fatal(err)
	}
	managedBoundary, err := NewLaunchAuthorityBoundary(allManaged)
	if err != nil {
		t.Fatal(err)
	}
	if got := mixedBoundary.ManagedCredentialAuthorities(); len(got) != 0 {
		t.Fatalf("mixed endpoint enabled authority-wide rewrite: %v", got)
	}
	if got := managedBoundary.ManagedCredentialAuthorities(); len(got) != 1 || got[0] != "relay.example:443" {
		t.Fatalf("all-managed endpoint rewrite scope = %v", got)
	}
	if err := mixedBoundary.Covers(passthrough); err != nil {
		t.Fatalf("mixed -> passthrough should preserve client credentials: %v", err)
	}
	if err := passthroughBoundary.Covers(mixed); err != nil {
		t.Fatalf("passthrough -> mixed should preserve client credentials: %v", err)
	}
	for name, test := range map[string]struct {
		boundary  LaunchAuthorityBoundary
		candidate EnvironmentSnapshot
	}{
		"mixed to all managed":       {mixedBoundary, allManaged},
		"all managed to mixed":       {managedBoundary, mixed},
		"all managed to passthrough": {managedBoundary, passthrough},
	} {
		if err := test.boundary.Covers(test.candidate); !errors.Is(err, ErrLaunchAuthorityRestartRequired) {
			t.Fatalf("%s error = %v", name, err)
		}
	}
}

func withManagedRoute(value Environment, planIndex int) (Environment, accountCatalog) {
	value = value.Clone()
	route := &value.ClientEndpoints[0].ProtocolPlans[planIndex].UpstreamPlan.Routes[0]
	accountID := "account." + route.ProviderTarget.RealmID
	route.AccountPolicy = RouteAccountPolicy{
		Revision: 1, Mode: AccountModeManaged,
		AllowedRealmIDs:    []string{route.ProviderTarget.RealmID},
		PreferredAccountID: accountID, CandidateAccountIDs: []string{accountID},
		AccountRevisions: map[string]Revision{accountID: 1}, FailoverPolicy: FailoverOff,
	}
	return value, accountCatalog{accountID: {
		ID: accountID, Revision: 1, RealmID: route.ProviderTarget.RealmID, Active: true,
		BackendProtocols: []string{route.BackendProtocol},
	}}
}

func mergeAccountCatalogs(catalogs ...accountCatalog) accountCatalog {
	result := accountCatalog{}
	for _, catalog := range catalogs {
		for id, account := range catalog {
			result[id] = account
		}
	}
	return result
}
