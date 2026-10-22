package provideraccount

import (
	"context"
	"errors"
	"testing"

	"github.com/vibe-agi/vibermate/internal/cyberaccess"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

func TestCyberAccessSettingsFreezeAtLeaseAndPreserveRouteIdentity(t *testing.T) {
	for _, scope := range []struct {
		driver   providerauth.DriverRef
		endpoint upstreamendpoint.ID
	}{
		{providerauth.CodexOAuthDriverRef(), upstreamendpoint.ChatGPTOfficialID},
		{providerauth.StaticHeaderDriverRef(), upstreamendpoint.ChatGPTOfficialID},
		{providerauth.StaticHeaderDriverRef(), upstreamendpoint.OpenAIPlatformID},
	} {
		driver := scope.driver
		for _, preference := range []cyberaccess.Preference{cyberaccess.Blue, cyberaccess.Red} {
			t.Run(scope.endpoint.String()+"/"+driver.String()+"/"+preference.WireValue(), func(t *testing.T) {
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
				initial, err := manager.Create(ctx, CreateCommand{ID: "daybreak", DisplayName: "Daybreak", UpstreamEndpointID: scope.endpoint, Driver: driver, Secret: value})
				if err != nil || initial.Account.CyberAccessProgram != cyberaccess.Standard {
					t.Fatalf("account did not default to standard: %+v %v", initial.Account, err)
				}
				endpoint, _ := testEndpoints(t).LookupEndpoint(scope.endpoint.String())
				previous, err := manager.AcquireEndpointCredential(ctx, initial.Account.ID, endpoint)
				if err != nil {
					t.Fatal(err)
				}
				defer previous.Release()
				before, _ := previous.Account()
				updated, err := manager.SetSettings(ctx, SettingsCommand{ID: initial.Account.ID, ExpectedRevision: 1, CyberAccessProgram: preference})
				if err != nil || updated.Account.CyberAccessProgram != preference || updated.Account.SettingsRevision != 2 || updated.Account.Revision != 1 || updated.Account.AssociationRevision != 1 {
					t.Fatalf("selection changed identity or was not stored: %+v %v", updated.Account, err)
				}
				descriptor, ok := manager.LookupAccount(initial.Account.ID.String(), scope.endpoint.String())
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
				if before.CyberAccessProgram != cyberaccess.Standard || retained != before || after.CyberAccessProgram != preference || after.SettingsRevision != 2 {
					t.Fatal("new setting was not frozen separately from the in-flight lease")
				}
				fallback := updated.Account.CredentialScope(updated.Account.RealmID, 1, egressprofile.Direct())
				if fallback.CyberAccessProgram != preference || fallback.SettingsRevision != 2 {
					t.Fatal("unresolved account scope lost the preference")
				}
				noop, err := manager.SetSettings(ctx, SettingsCommand{ID: initial.Account.ID, ExpectedRevision: 2, CyberAccessProgram: preference})
				if err != nil || noop.Account != updated.Account {
					t.Fatalf("no-op changed settings: %+v %v", noop.Account, err)
				}
				if _, err := manager.SetSettings(ctx, SettingsCommand{ID: initial.Account.ID, ExpectedRevision: 1, CyberAccessProgram: preference}); !errors.Is(err, ErrRevisionConflict) {
					t.Fatalf("stale settings accepted: %v", err)
				}
				standard, err := manager.SetSettings(ctx, SettingsCommand{ID: initial.Account.ID, ExpectedRevision: 2})
				if err != nil || standard.Account.CyberAccessProgram != cyberaccess.Standard || standard.Account.SettingsRevision != 3 || standard.Account.Revision != 1 {
					t.Fatalf("standard failed: %+v %v", standard.Account, err)
				}
				retained, _ = current.Account()
				if retained != after {
					t.Fatal("setting reset rewrote an in-flight lease")
				}
			})
		}
	}
}

func TestNonstandardCyberAccessOnlyAcceptsOfficialCodexOriginsAndDrivers(t *testing.T) {
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
		{"https://api.openai.com", providerauth.StaticHeaderDriverRef(), true},
		{"https://api.openai.com/v1", providerauth.StaticHeaderDriverRef(), true},
		{"https://api.openai.com:8443", providerauth.StaticHeaderDriverRef(), false},
		{"https://api.openai.com/custom", providerauth.StaticHeaderDriverRef(), false},
		{"https://api.openai.com.example", providerauth.StaticHeaderDriverRef(), false},
		{"https://api.openai.com", providerauth.CodexOAuthDriverRef(), false},
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
			for _, preference := range []cyberaccess.Preference{cyberaccess.Blue, cyberaccess.Red} {
				account.CyberAccessProgram = preference
				if err := account.Validate(); (err == nil) != test.want {
					t.Fatalf("%s validation = %v, want supported %v", preference.WireValue(), err, test.want)
				}
			}
			account.CyberAccessProgram = cyberaccess.Standard
			if err := account.Validate(); err != nil {
				t.Fatalf("standard rejected: %v", err)
			}
			account.CyberAccessProgram = cyberaccess.Preference("unknown")
			if err := account.Validate(); !errors.Is(err, ErrInvalidAccount) {
				t.Fatalf("invalid preference accepted: %v", err)
			}
		})
	}
}
