package desktopcontrol

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

func TestAccountRelationshipFailuresHaveDistinctSafeProblems(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status int
		reason ReasonCode
	}{
		{"unlinked", provideraccount.ErrEndpointMismatch, 422, "provider_account_scope_mismatch"},
		{"missing", provideraccount.ErrAccountNotFound, 404, ReasonProviderAccountNotFound},
		{"disabled", provideraccount.ErrAccountDisabled, 409, "provider_account_disabled"},
		{"credential unavailable", provideraccount.ErrCredentialMissing, 422, "provider_account_credential_unavailable"},
		{"invalid input", provideraccount.ErrInvalidAccount, 422, ReasonInvalidRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			problem := classifyEnvironmentAccountPolicyError(fmt.Errorf("private context: %w", test.err))
			if problem.status != test.status || problem.reason != test.reason || problem.detail != "" {
				t.Fatalf("problem = %+v", problem)
			}
		})
	}
	for _, test := range []struct {
		err    error
		reason ReasonCode
	}{
		{provideraccount.ErrAccountInUse, ReasonProviderAccountInUse},
		{provideraccount.ErrRevisionConflict, ReasonProviderAccountConflict},
		{provideraccount.ErrOperationInProgress, "provider_account_busy"},
	} {
		problem := classifyProviderAccountError(test.err)
		if problem.status != 409 || problem.reason != test.reason || problem.detail != "" {
			t.Fatalf("problem = %+v", problem)
		}
	}
}

func TestStaleEnvironmentUpstreamHasSpecificSafeControlProblem(t *testing.T) {
	spec := classifyEnvironmentError(fmt.Errorf("private diagnostic context: %w", environment.ErrUpstreamEndpointStale))
	if spec.status != http.StatusUnprocessableEntity || spec.reason != ReasonEnvironmentUpstreamStale || spec.detail != "" {
		t.Fatalf("upstream revision problem = %+v", spec)
	}
}

func TestAccountSelectorAuthorityIncludesOnlyReadyAccountsOnTheExactRoute(t *testing.T) {
	route := environment.UpstreamRoute{ProviderTarget: environment.ProviderTarget{
		ID: "endpoint.work", RealmID: "realm.work",
	}}
	associations, err := provideraccount.NewEndpointAssociations([]upstreamendpoint.ID{"endpoint.work"})
	if err != nil {
		t.Fatal(err)
	}
	view := provideraccount.View{
		Account: provideraccount.Account{
			ID: "account.work", Associations: associations,
			RealmID: "realm.work", State: provideraccount.StateActive,
		},
		Health: provideraccount.Health{State: provideraccount.HealthReady, CredentialEpoch: 1},
	}
	if !accountBelongsToRoute(view, route) {
		t.Fatal("ready Account on the exact Endpoint was rejected")
	}
	view.Health = provideraccount.Health{State: provideraccount.HealthMissing}
	if accountBelongsToRoute(view, route) {
		t.Fatal("Account without a ready credential entered selector authority")
	}
	if !errors.Is(routeAccountError(view, route), provideraccount.ErrCredentialMissing) {
		t.Fatal("missing credential was not distinguished from a broken association")
	}
	view.Health = provideraccount.Health{State: provideraccount.HealthReady, CredentialEpoch: 1}
	route.ProviderTarget.ID = "endpoint.unlinked"
	if !errors.Is(routeAccountError(view, route), provideraccount.ErrEndpointMismatch) {
		t.Fatal("unlinked account was not diagnosed as an association mismatch")
	}
	route.ProviderTarget.ID = "endpoint.work"
	view.Account.State = provideraccount.StateDisabled
	if !errors.Is(routeAccountError(view, route), provideraccount.ErrAccountDisabled) {
		t.Fatal("disabled account was not diagnosed")
	}
}
