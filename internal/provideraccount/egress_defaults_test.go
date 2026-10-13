package provideraccount

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

type accountDefaultFixture struct {
	mu      sync.Mutex
	setting egressprofile.DefaultSetting
	err     error
	reads   int
}

// Old account-unit fixtures explicitly compose a valid, persisted-unset-shaped
// default instead of relying on production to invent a default when unbound.
func newTestAccountManager(ctx context.Context, repository Repository, secrets secretstore.Store, endpoints upstreamendpoint.Catalog, realms []Realm, clock Clock) (*Manager, error) {
	m, err := NewManager(ctx, repository, secrets, endpoints, realms, clock)
	if err != nil {
		return nil, err
	}
	d, err := egressprofile.NewDefaultManager(&accountDefaultFixture{setting: egressprofile.DefaultSetting{Revision: 1}}, settingsProfiles{})
	if err != nil {
		return nil, err
	}
	if err = m.BindEgressDefaults(d); err != nil {
		return nil, err
	}
	return m, nil
}

func (f *accountDefaultFixture) LoadDefault(context.Context) (egressprofile.DefaultSetting, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	return f.setting, f.err
}
func (*accountDefaultFixture) WriteDefault(context.Context, uint64, egressprofile.DefaultSetting) (egressprofile.DefaultCommitResult, error) {
	return egressprofile.DefaultCommitResult{}, errors.New("unexpected default write")
}

func bindTestAccountDefaults(t *testing.T, manager *Manager) *accountDefaultFixture {
	t.Helper()
	repo := &accountDefaultFixture{setting: egressprofile.DefaultSetting{Revision: 1}}
	defaults, err := egressprofile.NewDefaultManager(repo, settingsProfiles{})
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.BindEgressDefaults(defaults); err != nil {
		t.Fatal(err)
	}
	return repo
}

type scopedRefreshObserver struct {
	*rotatingCredentialPreparer
	scope providerauth.AccountRef
}

func (p *scopedRefreshObserver) Refresh(ctx context.Context, driver providerauth.DriverRef, ref secretstore.Reference, scope providerauth.AccountRef) (secretstore.Revision, error) {
	p.scope = scope
	return p.rotatingCredentialPreparer.Refresh(ctx, driver, ref, scope)
}

func TestManualRefreshRequiresOneFrozenGlobalEgress(t *testing.T) {
	ctx := context.Background()
	secrets := newMemorySecrets()
	manager, err := NewManager(ctx, &memoryRepository{accounts: map[ID]Account{}}, secrets, testEndpoints(t), BuiltInRealms(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Shutdown(ctx)
	defaults := bindTestAccountDefaults(t, manager)
	value, err := newTestCredentialValue(t, "synthetic-refresh")
	if err != nil {
		t.Fatal(err)
	}
	defer value.Destroy()
	view, err := manager.Create(ctx, CreateCommand{ID: "refresh-global", DisplayName: "Synthetic", UpstreamEndpointID: upstreamendpoint.ChatGPTOfficialID, Driver: providerauth.CodexOAuthDriverRef(), Secret: value})
	if err != nil {
		t.Fatal(err)
	}
	preparer := &scopedRefreshObserver{rotatingCredentialPreparer: &rotatingCredentialPreparer{secrets: secrets, replacement: value}}
	if err = manager.BindCredentialPreparer(preparer); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.RefreshCredential(ctx, view.Account.ID, 1); err != nil {
		t.Fatal(err)
	}
	if defaults.reads != 1 || preparer.scope.ResolvedEgress.Validate() != nil || preparer.scope.ResolvedEgress.Source() != egressprofile.SelectionBuiltin || preparer.scope.ResolvedEgress.DefaultRevision() != 1 || !preparer.scope.EgressProfile.Equal(preparer.scope.ResolvedEgress.Profile()) {
		t.Fatal("manual refresh bypassed global resolution or changed its frozen account scope")
	}
}

func TestAccountOverrideDoesNotMakeMissingAuthoredChoiceValid(t *testing.T) {
	ctx := t.Context()
	manager, err := newTestAccountManager(ctx, &memoryRepository{accounts: map[ID]Account{}}, newMemorySecrets(), testEndpoints(t), BuiltInRealms(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Shutdown(context.Background())
	value, err := newTestCredentialValue(t, "synthetic-choice-validation")
	if err != nil {
		t.Fatal(err)
	}
	defer value.Destroy()
	view, err := manager.Create(ctx, CreateCommand{ID: "choice-invalid", DisplayName: "Synthetic", UpstreamEndpointID: upstreamendpoint.ChatGPTOfficialID, Driver: providerauth.StaticHeaderDriverRef(), Secret: value})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.BindEgressProfiles(settingsProfiles{}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SetSettings(ctx, SettingsCommand{ID: view.Account.ID, ExpectedRevision: 1, EgressProfile: egressprofile.Direct()}); err != nil {
		t.Fatal(err)
	}
	account := view.Account
	// Missing authored configuration is not the explicitly typed absence
	// owned/endpoint operations will supply. No alternative account is invented.
	lease, err := manager.acquire(ctx, accountLeaseScope{id: account.ID, accountRevision: account.Revision, realmID: account.RealmID, ownerOperation: true, upstreamEndpointOrigin: account.Origin})
	if lease != nil {
		lease.Release()
	}
	if err == nil {
		t.Fatal("explicit account override masked missing authored choice")
	}
}

func TestAccountAdmissionRejectsUnavailableInheritedDefaultButNotExplicitExit(t *testing.T) {
	for _, bound := range []bool{false, true} {
		for _, choice := range []string{"inherit", "account", "flow"} {
			t.Run(fmt.Sprintf("bound=%t/%s", bound, choice), func(t *testing.T) {
				ctx := t.Context()
				endpoints := testEndpoints(t)
				manager, err := NewManager(ctx, &memoryRepository{accounts: map[ID]Account{}}, newMemorySecrets(), endpoints, BuiltInRealms(), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer manager.Shutdown(context.Background())
				var defaults *accountDefaultFixture
				if bound {
					defaults = bindTestAccountDefaults(t, manager)
					defaults.err = errors.New("synthetic unavailable default")
				}
				value, err := newTestCredentialValue(t, "synthetic")
				if err != nil {
					t.Fatal(err)
				}
				defer value.Destroy()
				view, err := manager.Create(ctx, CreateCommand{ID: "explicit-check", DisplayName: "Synthetic", UpstreamEndpointID: upstreamendpoint.ChatGPTOfficialID, Driver: providerauth.StaticHeaderDriverRef(), Secret: value})
				if err != nil {
					t.Fatal(err)
				}
				if choice == "account" {
					if err = manager.BindEgressProfiles(settingsProfiles{}); err != nil {
						t.Fatal(err)
					}
					if _, err = manager.SetSettings(ctx, SettingsCommand{ID: view.Account.ID, ExpectedRevision: 1, EgressProfile: egressprofile.Direct()}); err != nil {
						t.Fatal(err)
					}
				}
				account := view.Account
				scope := accountLeaseScope{egressProfile: egressprofile.Inherit(), id: account.ID, accountRevision: account.Revision, realmID: account.RealmID, ownerOperation: true, upstreamEndpointOrigin: account.Origin}
				if choice == "flow" {
					scope.egressProfile = authoredEgress(t, egressprofile.Direct())
				}
				lease, err := manager.acquire(ctx, scope)
				if choice == "inherit" {
					if err == nil || lease != nil {
						t.Fatal("missing inherited default silently became Direct")
					}
				} else {
					if err != nil || lease == nil {
						t.Fatal("explicit exit consulted unavailable default", err)
					}
					ref, _ := lease.Account()
					lease.Release()
					if ref.ResolvedEgress.Source() != egressprofile.SelectionSource(choice) || ref.ResolvedEgress.DefaultRevision() != 0 || !ref.EgressProfile.Equal(egressprofile.Direct()) {
						t.Fatal("explicit admission selection changed")
					}
				}
				if defaults != nil {
					want := 0
					if choice == "inherit" {
						want = 1
					}
					if defaults.reads != want {
						t.Fatal("incorrect default read count")
					}
				}
				manager.mu.RLock()
				active, inflight := len(manager.active), manager.inFlight
				manager.mu.RUnlock()
				if active != 0 || inflight != 0 {
					t.Fatal("resolution/lease release leaked operation ownership")
				}
			})
		}
	}
}
