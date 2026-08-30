package desktopcontrol

import (
	"testing"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

func TestAccountSelectorAuthorityIncludesOnlyReadyAccountsOnTheExactRoute(t *testing.T) {
	route := environment.UpstreamRoute{ProviderTarget: environment.ProviderTarget{
		ID: "endpoint.work", RealmID: "realm.work",
	}}
	view := provideraccount.View{
		Account: provideraccount.Account{
			ID: "account.work", UpstreamEndpointID: upstreamendpoint.ID("endpoint.work"),
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
}
