package provideraccount

import (
	"context"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

func TestFrozenEndpointLeaseSurvivesCapabilityUpgradeWithoutRetargeting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	endpoints := testEndpoints(t)
	manager, err := NewManager(ctx, &memoryRepository{accounts: make(map[ID]Account)}, newMemorySecrets(), endpoints, BuiltInRealms(), fixedClock{now: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Shutdown(ctx)
	secret, err := newTestCredentialValue(t, "synthetic-frozen-credential")
	if err != nil {
		t.Fatal(err)
	}
	defer secret.Destroy()
	view, err := manager.Create(ctx, CreateCommand{
		ID: "frozen-account", DisplayName: "Frozen account", UpstreamEndpointID: upstreamendpoint.ChatGPTOfficialID,
		Driver: providerauth.StaticHeaderDriverRef(), Secret: secret,
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := endpoints[upstreamendpoint.ChatGPTOfficialID]
	scope := accountLeaseScope{
		id: view.Account.ID, accountRevision: view.Account.Revision, realmID: endpoint.RealmID,
		upstreamEndpointID: endpoint.ID.String(), upstreamEndpointRevision: endpoint.Revision,
		upstreamEndpointOrigin: endpoint.Origin,
	}
	endpoint.Revision++
	endpoints[endpoint.ID] = endpoint
	lease, err := manager.acquire(ctx, scope)
	if err != nil {
		t.Fatalf("additive profile revision invalidated a frozen grant: %v", err)
	}
	ref, ok := lease.Account()
	lease.Release()
	if !ok || ref.ID != scope.id.String() || ref.Revision != scope.accountRevision || ref.CredentialEpoch != 1 {
		t.Fatal("frozen lease changed account identity or credential epoch")
	}
	for name, mutate := range map[string]func(*accountLeaseScope, *upstreamendpoint.Endpoint){
		"future revision": func(scope *accountLeaseScope, endpoint *upstreamendpoint.Endpoint) {
			scope.upstreamEndpointRevision = endpoint.Revision + 1
		},
		"other profile": func(scope *accountLeaseScope, _ *upstreamendpoint.Endpoint) {
			scope.upstreamEndpointID = upstreamendpoint.OpenAIPlatformID.String()
		},
		"other realm":            func(scope *accountLeaseScope, _ *upstreamendpoint.Endpoint) { scope.realmID = "openai.platform" },
		"wrong account revision": func(scope *accountLeaseScope, _ *upstreamendpoint.Endpoint) { scope.accountRevision++ },
		"different frozen destination": func(scope *accountLeaseScope, _ *upstreamendpoint.Endpoint) {
			scope.upstreamEndpointOrigin, _ = originidentity.ParseProviderOrigin("https://other.example")
		},
		"current endpoint retargeted": func(_ *accountLeaseScope, endpoint *upstreamendpoint.Endpoint) {
			endpoint.Origin, _ = originidentity.ParseProviderOrigin("https://other.example")
		},
		"disabled endpoint": func(_ *accountLeaseScope, endpoint *upstreamendpoint.Endpoint) {
			endpoint.State = upstreamendpoint.StateDisabled
		},
		"removed driver": func(_ *accountLeaseScope, endpoint *upstreamendpoint.Endpoint) {
			endpoint.Drivers = []providerauth.DriverRef{providerauth.CodexOAuthDriverRef()}
		},
	} {
		t.Run(name, func(t *testing.T) {
			badScope, current := scope, endpoint.Clone()
			mutate(&badScope, &current)
			endpoints[endpoint.ID] = current
			defer func() { endpoints[endpoint.ID] = endpoint }()
			got, err := manager.acquire(ctx, badScope)
			if got != nil {
				got.Release()
			}
			if err == nil || got != nil {
				t.Fatal("incompatible frozen grant acquired a credential")
			}
		})
	}
	account := manager.accounts[scope.id]
	account.Associations = EndpointAssociations{}
	manager.accounts[scope.id] = account
	if lease, err := manager.acquire(ctx, scope); err == nil || lease != nil {
		if lease != nil {
			lease.Release()
		}
		t.Fatal("unlinked account still acquired a credential")
	}
}
