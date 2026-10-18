package provideraccount

import (
	"context"
	"errors"
	"testing"

	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

func TestDaybreakBlueSettingsFreezeAtLeaseAndPreserveRouteIdentity(t *testing.T) {
	for _, driver := range []providerauth.DriverRef{providerauth.CodexOAuthDriverRef(), providerauth.StaticHeaderDriverRef()} {
		t.Run(driver.String(), func(t *testing.T) {
			ctx := context.Background()
			manager, err := newTestAccountManager(ctx, &memoryRepository{accounts: map[ID]Account{}}, newMemorySecrets(), testEndpoints(t), BuiltInRealms(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Shutdown(ctx)
			value, err := newTestCredentialValue(t, "synthetic-daybreak-credential")
			if err != nil {
				t.Fatal(err)
			}
			defer value.Destroy()
			initial, err := manager.Create(ctx, CreateCommand{ID: "daybreak", DisplayName: "Daybreak", UpstreamEndpointID: upstreamendpoint.ChatGPTOfficialID, Driver: driver, Secret: value})
			if err != nil || initial.Account.DaybreakBlueEnabled {
				t.Fatalf("account silently opted in: %+v %v", initial.Account, err)
			}
			endpoint, _ := testEndpoints(t).LookupEndpoint(upstreamendpoint.ChatGPTOfficialID.String())
			previous, err := manager.AcquireEndpointCredential(ctx, initial.Account.ID, endpoint)
			if err != nil {
				t.Fatal(err)
			}
			defer previous.Release()
			before, _ := previous.Account()
			updated, err := manager.SetSettings(ctx, SettingsCommand{ID: initial.Account.ID, ExpectedRevision: 1, DaybreakBlueEnabled: true})
			if err != nil || !updated.Account.DaybreakBlueEnabled || updated.Account.SettingsRevision != 2 || updated.Account.Revision != 1 || updated.Account.AssociationRevision != 1 {
				t.Fatalf("opt-in changed identity or was not stored: %+v %v", updated.Account, err)
			}
			descriptor, ok := manager.LookupAccount(initial.Account.ID.String(), upstreamendpoint.ChatGPTOfficialID.String())
			if !ok || descriptor.Revision != 1 {
				t.Fatal("settings invalidated the published route")
			}
			current, err := manager.AcquireEndpointCredential(ctx, initial.Account.ID, endpoint)
			if err != nil {
				t.Fatal(err)
			}
			defer current.Release()
			after, _ := current.Account()
			retained, _ := previous.Account()
			if before.DaybreakBlueEnabled || retained != before || !after.DaybreakBlueEnabled || after.SettingsRevision != 2 {
				t.Fatal("new setting was not frozen separately from the in-flight lease")
			}
			fallback := updated.Account.CredentialScope(updated.Account.RealmID, 1, egressprofile.Direct())
			if !fallback.DaybreakBlueEnabled || fallback.SettingsRevision != 2 {
				t.Fatal("unresolved account scope lost the opt-in")
			}
			noop, err := manager.SetSettings(ctx, SettingsCommand{ID: initial.Account.ID, ExpectedRevision: 2, DaybreakBlueEnabled: true})
			if err != nil || noop.Account != updated.Account {
				t.Fatalf("no-op changed settings: %+v %v", noop.Account, err)
			}
			if _, err := manager.SetSettings(ctx, SettingsCommand{ID: initial.Account.ID, ExpectedRevision: 1, DaybreakBlueEnabled: true}); !errors.Is(err, ErrRevisionConflict) {
				t.Fatalf("stale settings accepted: %v", err)
			}
			disabled, err := manager.SetSettings(ctx, SettingsCommand{ID: initial.Account.ID, ExpectedRevision: 2})
			if err != nil || disabled.Account.DaybreakBlueEnabled || disabled.Account.SettingsRevision != 3 || disabled.Account.Revision != 1 {
				t.Fatalf("opt-out failed: %+v %v", disabled.Account, err)
			}
			retained, _ = current.Account()
			if retained != after {
				t.Fatal("opt-out rewrote an in-flight lease")
			}
		})
	}
}

func TestDaybreakBlueSettingOnlyAcceptsOfficialCodexOriginsAndDrivers(t *testing.T) {
	for _, test := range []struct {
		origin string
		driver providerauth.DriverRef
		want   bool
	}{
		{"https://chatgpt.com", providerauth.CodexOAuthDriverRef(), true},
		{"https://chatgpt.com/backend-api", providerauth.StaticHeaderDriverRef(), true},
		{"https://chatgpt.com/backend-api/codex", providerauth.StaticHeaderDriverRef(), true},
		{"https://chatgpt.com/other", providerauth.CodexOAuthDriverRef(), false},
		{"https://chatgpt.com:8443", providerauth.CodexOAuthDriverRef(), false},
		{"https://chatgpt.com.example", providerauth.CodexOAuthDriverRef(), false},
		{"https://api.openai.com", providerauth.StaticHeaderDriverRef(), false},
		{"https://chatgpt.com", providerauth.AnthropicAPIKeyDriverRef(), false},
	} {
		t.Run(test.origin+"/"+test.driver.String(), func(t *testing.T) {
			ctx := context.Background()
			manager, err := newTestAccountManager(ctx, &memoryRepository{accounts: map[ID]Account{}}, newMemorySecrets(), testEndpoints(t), BuiltInRealms(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Shutdown(ctx)
			value, err := newTestCredentialValue(t, "synthetic-daybreak-scope")
			if err != nil {
				t.Fatal(err)
			}
			defer value.Destroy()
			view, err := manager.Create(ctx, CreateCommand{ID: "daybreak", DisplayName: "Daybreak", UpstreamEndpointID: upstreamendpoint.ChatGPTOfficialID, Driver: providerauth.StaticHeaderDriverRef(), Secret: value})
			if err != nil {
				t.Fatal(err)
			}
			account := view.Account
			account.Origin, err = originidentity.ParseProviderOrigin(test.origin)
			if err != nil {
				t.Fatal(err)
			}
			account.Driver = test.driver
			account.DaybreakBlueEnabled = true
			if err := account.Validate(); (err == nil) != test.want {
				t.Fatalf("opt-in validation = %v, want supported %v", err, test.want)
			}
			account.DaybreakBlueEnabled = false
			if err := account.Validate(); err != nil {
				t.Fatalf("opt-out rejected: %v", err)
			}
		})
	}
}
