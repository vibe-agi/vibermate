package provideraccount

import (
	"context"
	"errors"
	"testing"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

type settingsProfiles struct {
	egressprofile.Controller
	profile egressprofile.ProfileRevision
}

func (profiles settingsProfiles) GetRevision(_ context.Context, id egressprofile.ID, revision egressprofile.Revision) (egressprofile.ProfileRevision, error) {
	if id == egressprofile.DirectID && revision == 1 {
		return egressprofile.Direct(), nil
	}
	if id == profiles.profile.ID && revision == profiles.profile.Revision {
		return profiles.profile, nil
	}
	return egressprofile.ProfileRevision{}, egressprofile.ErrProfileNotFound
}

func TestAccountSettingsFreezeAtLeaseWithoutInvalidatingRoutes(t *testing.T) {
	ctx := context.Background()
	manager, err := NewManager(ctx, &memoryRepository{accounts: map[ID]Account{}}, newMemorySecrets(), testEndpoints(t), BuiltInRealms(), nil)
	if err != nil {
		t.Fatal(err)
	}
	value, err := newTestCredentialValue(t, "synthetic-settings-token")
	if err != nil {
		t.Fatal(err)
	}
	defer value.Destroy()
	view, err := manager.Create(ctx, CreateCommand{ID: "configured", DisplayName: "Configured", UpstreamEndpointID: upstreamendpoint.AnthropicOfficialID, Driver: providerauth.AnthropicAPIKeyDriverRef(), Secret: value})
	if err != nil {
		t.Fatal(err)
	}
	profile := egressprofile.Direct()
	profile.ID = "profile.us"
	profile.DisplayName = "US"
	profile.Policy.Proxy = egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: "127.0.0.1:1080"}
	if err := manager.BindEgressProfiles(settingsProfiles{profile: profile}); err != nil {
		t.Fatal(err)
	}
	view, err = manager.SetSettings(ctx, SettingsCommand{ID: view.Account.ID, ExpectedRevision: 1, EgressProfile: profile})
	if err != nil {
		t.Fatal(err)
	}
	if view.Account.Revision != 1 || view.Account.SettingsRevision != 2 || view.Account.AssociationRevision != 1 {
		t.Fatal("settings invalidated route identity or associations")
	}
	descriptor, ok := manager.LookupAccount(view.Account.ID.String(), upstreamendpoint.AnthropicOfficialID.String())
	if !ok || descriptor.Revision != 1 {
		t.Fatal("published route no longer authorized")
	}
	endpoint, _ := testEndpoints(t).LookupEndpoint(upstreamendpoint.AnthropicOfficialID.String())
	previous, err := manager.AcquireEndpointCredential(ctx, view.Account.ID, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer previous.Release()
	before, _ := previous.Account()
	if !before.EgressProfile.Equal(profile) || before.SettingsRevision != 2 {
		t.Fatalf("account override missing: %+v", before)
	}
	if _, err := manager.SetSettings(ctx, SettingsCommand{ID: view.Account.ID, ExpectedRevision: 1}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale write: %v", err)
	}
	if _, err := manager.SetSettings(ctx, SettingsCommand{ID: view.Account.ID, ExpectedRevision: 2, EgressProfile: egressprofile.Direct()}); err != nil {
		t.Fatal(err)
	}
	current, err := manager.AcquireEndpointCredential(ctx, view.Account.ID, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Release()
	after, _ := current.Account()
	retained, _ := previous.Account()
	if after.EgressProfile.ID != egressprofile.DirectID || retained != before {
		t.Fatal("setting did not affect the next lease or rewrote an in-flight lease")
	}
	if _, err := manager.SetSettings(ctx, SettingsCommand{ID: view.Account.ID, ExpectedRevision: 3, AutomaticRefresh: true}); !errors.Is(err, ErrInvalidAccount) {
		t.Fatalf("static credential enabled automatic refresh: %v", err)
	}
	forged := profile
	forged.Policy.Proxy.Endpoint = "127.0.0.1:9999"
	if _, err := manager.SetSettings(ctx, SettingsCommand{ID: view.Account.ID, ExpectedRevision: 3, EgressProfile: forged}); !errors.Is(err, ErrInvalidAccount) {
		t.Fatalf("unpublished policy accepted: %v", err)
	}
	missing := profile
	missing.Revision++
	if _, err := manager.SetSettings(ctx, SettingsCommand{ID: view.Account.ID, ExpectedRevision: 3, EgressProfile: missing}); !errors.Is(err, egressprofile.ErrProfileNotFound) {
		t.Fatalf("missing profile did not fail closed: %v", err)
	}
	if _, err := manager.SetSettings(ctx, SettingsCommand{ID: view.Account.ID, ExpectedRevision: 3}); err != nil {
		t.Fatal(err)
	}
	// A captured operation inherits its frozen profile only when no override exists.
	account := view.Account
	scope := accountLeaseScope{id: account.ID, accountRevision: account.Revision, realmID: account.RealmID, ownerOperation: true, upstreamEndpointOrigin: account.Origin, egressProfile: profile}
	inherited, err := manager.acquire(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	defer inherited.Release()
	got, _ := inherited.Account()
	if !got.EgressProfile.Equal(profile) {
		t.Fatal("request profile was not inherited")
	}
}

func TestImportedOAuthRefreshIsOptInAndManualRefreshRemainsAvailable(t *testing.T) {
	ctx := context.Background()
	secrets := newMemorySecrets()
	manager, err := NewManager(ctx, &memoryRepository{accounts: map[ID]Account{}}, secrets, testEndpoints(t), BuiltInRealms(), nil)
	if err != nil {
		t.Fatal(err)
	}
	value, err := newTestCredentialValue(t, "synthetic-oauth")
	if err != nil {
		t.Fatal(err)
	}
	defer value.Destroy()
	view, err := manager.Create(ctx, CreateCommand{ID: "imported", DisplayName: "Imported", UpstreamEndpointID: upstreamendpoint.ChatGPTOfficialID, Driver: providerauth.CodexOAuthDriverRef(), Secret: value})
	if err != nil {
		t.Fatal(err)
	}
	preparer := &rotatingCredentialPreparer{secrets: secrets, replacement: value}
	if err := manager.BindCredentialPreparer(preparer); err != nil {
		t.Fatal(err)
	}
	endpoint, _ := testEndpoints(t).LookupEndpoint(upstreamendpoint.ChatGPTOfficialID.String())
	acquire := func() {
		t.Helper()
		lease, err := manager.AcquireEndpointCredential(ctx, view.Account.ID, endpoint)
		if err != nil {
			t.Fatal(err)
		}
		lease.Release()
	}
	acquire()
	if view.Account.AutomaticRefresh || preparer.calls != 0 {
		t.Fatal("import silently refreshed")
	}
	if _, err := manager.SetSettings(ctx, SettingsCommand{ID: view.Account.ID, ExpectedRevision: 1, AutomaticRefresh: true}); err != nil {
		t.Fatal(err)
	}
	acquire()
	if preparer.calls != 1 {
		t.Fatal("opt-in did not enable preparation")
	}
	if _, err := manager.SetSettings(ctx, SettingsCommand{ID: view.Account.ID, ExpectedRevision: 2}); err != nil {
		t.Fatal(err)
	}
	acquire()
	if preparer.calls != 1 {
		t.Fatal("disabled account still automatically refreshes")
	}
	if _, err := manager.RefreshCredential(ctx, view.Account.ID, 2); err != nil {
		t.Fatal(err)
	}
	if preparer.calls != 2 {
		t.Fatal("explicit refresh was disabled")
	}
	replaced, err := manager.ReplaceSecret(ctx, ReplaceSecretCommand{ID: view.Account.ID, ExpectedCredentialEpoch: 3, Secret: value})
	if err != nil || replaced.Account.AutomaticRefresh || replaced.Account.SettingsRevision != 3 {
		t.Fatalf("reimport changed settings: %+v %v", replaced, err)
	}
}
