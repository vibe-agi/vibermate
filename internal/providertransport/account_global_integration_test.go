package providertransport_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/accountoperation"
	"github.com/vibe-agi/vibermate/internal/codexoauth"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

func TestUnselectedOwnedAccountUsesGlobalSOCKS(t *testing.T) {
	providertransport.ExerciseOwnedAccountGlobalSOCKS(t, func(t *testing.T, proxy egressprofile.ProfileRevision, encoded []byte, secrets secretstore.Store) providerauth.Lease {
		ctx := context.Background()
		store, err := runtimepersistence.Open(ctx, runtimepersistence.Options{DatabasePath: t.TempDir() + "/runtime.db", BusyTimeout: runtimepersistence.DefaultBusyTimeout, CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Shutdown(ctx) })
		profiles, err := egressprofile.NewManager(store.EgressProfileRepository(), nil)
		if err != nil {
			t.Fatal(err)
		}
		profile, err := profiles.Publish(ctx, egressprofile.PublishCommand{ID: "global.fixture", DisplayName: "Synthetic global SOCKS", Policy: proxy.Policy})
		if err != nil {
			t.Fatal(err)
		}
		defaults, err := egressprofile.NewDefaultManager(store.EgressDefaultRepository(), profiles)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = defaults.SetDefault(ctx, egressprofile.SetDefaultCommand{ExpectedRevision: 1, Profile: &egressprofile.ProfileRef{ID: profile.ID, Revision: profile.Revision}}); err != nil {
			t.Fatal(err)
		}
		commands, err := upstreamendpoint.BuiltInCommands()
		if err != nil {
			t.Fatal(err)
		}
		endpoints, err := upstreamendpoint.NewManager(ctx, store.UpstreamEndpointRepository(), commands, nil)
		if err != nil {
			t.Fatal(err)
		}
		accounts, err := provideraccount.NewManager(ctx, store.ProviderAccountRepository(), secrets, endpoints, provideraccount.BuiltInRealms(), nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = accounts.Shutdown(ctx) })
		if err = accounts.BindEgressDefaults(defaults); err != nil {
			t.Fatal(err)
		}
		material, err := providerauth.NewMaterial(string(encoded), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer material.Destroy()
		wrapped, err := material.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		defer clear(wrapped)
		value, err := secretstore.NewValue(wrapped)
		if err != nil {
			t.Fatal(err)
		}
		defer value.Destroy()
		view, err := accounts.Create(ctx, provideraccount.CreateCommand{ID: "global-owned", DisplayName: "Synthetic account", UpstreamEndpointID: upstreamendpoint.ChatGPTOfficialID, Driver: providerauth.CodexOAuthDriverRef(), Secret: value})
		if err != nil {
			t.Fatal(err)
		}
		lease, _, err := accounts.AcquireOwnedCredential(ctx, view.Account.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = defaults.SetDefault(ctx, egressprofile.SetDefaultCommand{ExpectedRevision: 2}); err != nil {
			t.Fatal(err)
		}
		next, _, err := accounts.AcquireOwnedCredential(ctx, view.Account.ID)
		if err != nil {
			t.Fatal(err)
		}
		nextScope, _ := next.Account()
		next.Release()
		if nextScope.ResolvedEgress.Source() != egressprofile.SelectionBuiltin || nextScope.ResolvedEgress.DefaultRevision() != 3 {
			t.Fatal("next owned admission did not see updated default")
		}
		prior, _ := lease.Account()
		if prior.ResolvedEgress.Source() != egressprofile.SelectionGlobal || prior.ResolvedEgress.DefaultRevision() != 2 || !prior.EgressProfile.Equal(profile) {
			t.Fatal("default edit rewrote existing actual lease")
		}
		return lease
	})
}

func TestOwnedResetDetailsAndConsumeKeepOneGlobalSelection(t *testing.T) {
	testOwnedGlobalWorkflow(t, false, "")
}

func TestManualRefreshUsesFrozenGlobalSOCKS(t *testing.T) {
	testOwnedGlobalWorkflow(t, true, "")
}

type actualRefreshTransport struct{ client *providertransport.Client }

type actualRefreshClock struct{}

func (actualRefreshClock) Now() time.Time { return time.Now().UTC() }

func (a actualRefreshTransport) Do(request *http.Request, scope providerauth.AccountRef) (*http.Response, error) {
	return a.client.DoCodexOAuthTokenRequest(request, scope)
}

func testOwnedGlobalWorkflow(t *testing.T, refresh bool, modelChoice string) {
	workflow := &providertransport.AccountEgressWorkflow{Refresh: refresh, AutomaticModel: modelChoice != "", ExpectDirectRefusal: modelChoice == "account_direct" || modelChoice == "flow_direct"}
	workflow.Run = func(t *testing.T, proxy egressprofile.ProfileRevision, encoded []byte, secrets secretstore.Store, client *providertransport.Client) {
		ctx := context.Background()
		store, err := runtimepersistence.Open(ctx, runtimepersistence.Options{DatabasePath: t.TempDir() + "/runtime.db", BusyTimeout: runtimepersistence.DefaultBusyTimeout, CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Shutdown(ctx) })
		profiles, err := egressprofile.NewManager(store.EgressProfileRepository(), nil)
		if err != nil {
			t.Fatal(err)
		}
		profile, err := profiles.Publish(ctx, egressprofile.PublishCommand{ID: "reset.global", DisplayName: "Synthetic reset", Policy: proxy.Policy})
		if err != nil {
			t.Fatal(err)
		}
		defaults, err := egressprofile.NewDefaultManager(store.EgressDefaultRepository(), profiles)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = defaults.SetDefault(ctx, egressprofile.SetDefaultCommand{ExpectedRevision: 1, Profile: &egressprofile.ProfileRef{ID: profile.ID, Revision: profile.Revision}}); err != nil {
			t.Fatal(err)
		}
		commands, err := upstreamendpoint.BuiltInCommands()
		if err != nil {
			t.Fatal(err)
		}
		endpoints, err := upstreamendpoint.NewManager(ctx, store.UpstreamEndpointRepository(), commands, nil)
		if err != nil {
			t.Fatal(err)
		}
		accounts, err := provideraccount.NewManager(ctx, store.ProviderAccountRepository(), secrets, endpoints, provideraccount.BuiltInRealms(), nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = accounts.Shutdown(ctx) })
		if err = accounts.BindEgressDefaults(defaults); err != nil {
			t.Fatal(err)
		}
		material, err := providerauth.NewMaterial(string(encoded), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer material.Destroy()
		wrapped, err := material.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		defer clear(wrapped)
		value, err := secretstore.NewValue(wrapped)
		if err != nil {
			t.Fatal(err)
		}
		defer value.Destroy()
		view, err := accounts.Create(ctx, provideraccount.CreateCommand{ID: "reset-owned", DisplayName: "Synthetic reset account", UpstreamEndpointID: upstreamendpoint.ChatGPTOfficialID, Driver: providerauth.CodexOAuthDriverRef(), Secret: value, AutomaticRefresh: modelChoice != ""})
		if err != nil {
			t.Fatal(err)
		}
		if refresh {
			oauth, err := codexoauth.NewManager(codexoauth.Options{Secrets: secrets, Client: actualRefreshTransport{client}, Clock: actualRefreshClock{}})
			if err != nil {
				t.Fatal(err)
			}
			if err = accounts.BindCredentialPreparer(oauth); err != nil {
				t.Fatal(err)
			}
			if modelChoice != "" {
				if err = accounts.BindEgressProfiles(profiles); err != nil {
					t.Fatal(err)
				}
				runAutomaticModelWorkflow(t, workflow, modelChoice, accounts, endpoints, defaults, profile, view, client)
				return
			}
			changed := make(chan error, 1)
			workflow.AfterToken = func() {
				_, err := defaults.SetDefault(ctx, egressprofile.SetDefaultCommand{ExpectedRevision: 2})
				changed <- err
			}
			if _, err = accounts.RefreshCredential(ctx, view.Account.ID, 1); err != nil {
				t.Fatal("actual global refresh failed", err)
			}
			if err = <-changed; err != nil {
				t.Fatal(err)
			}
			next, _, err := accounts.AcquireOwnedCredential(ctx, view.Account.ID)
			if err != nil {
				t.Fatal(err)
			}
			defer next.Release()
			ref, _ := next.Account()
			if ref.CredentialEpoch != 2 || ref.ResolvedEgress.Source() != egressprofile.SelectionBuiltin || ref.ResolvedEgress.DefaultRevision() != 3 {
				t.Fatal("refresh rotation/default change did not reach next admission")
			}
			return
		}
		reader, err := accountoperation.New(accountoperation.Options{Accounts: accounts, Transport: client})
		if err != nil {
			t.Fatal(err)
		}
		changed := make(chan error, 1)
		workflow.AfterDetails = func() {
			_, err := defaults.SetDefault(ctx, egressprofile.SetDefaultCommand{ExpectedRevision: 2})
			changed <- err
		}
		result, err := reader.RedeemOwned(ctx, view.Account.ID, view.Account.Revision, "credit-fixture")
		if err != nil || result.Outcome != "reset" || result.CreditID != "credit-fixture" || result.WindowsReset != 1 {
			t.Fatal("real reset reader did not complete synthetic two-step workflow", err)
		}
		if err := <-changed; err != nil {
			t.Fatal(err)
		}
		next, _, err := accounts.AcquireOwnedCredential(ctx, view.Account.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer next.Release()
		ref, _ := next.Account()
		if ref.ResolvedEgress.Source() != egressprofile.SelectionBuiltin || ref.ResolvedEgress.DefaultRevision() != 3 {
			t.Fatal("next operation did not see post-details default")
		}
	}
	providertransport.ExerciseOwnedGlobalWorkflow(t, workflow)
}

func TestFirstLoginUsesGlobalSOCKSWithoutAccount(t *testing.T) {
	providertransport.ExerciseFirstLoginGlobalSOCKS(t, globalAccountFreeFixture(t))
}

func TestRuntimeMetadataUsesGlobalSOCKSWithoutAccount(t *testing.T) {
	providertransport.ExerciseRuntimeMetadataGlobalSOCKS(t, globalAccountFreeFixture(t))
}

func TestGlobalMetadataFrozenAcrossHoldCancelAndDoHFailure(t *testing.T) {
	for _, mode := range []string{"hold_resume", "hold_cancel", "doh_failure"} {
		t.Run(mode, func(t *testing.T) {
			providertransport.ExerciseGlobalMetadataFault(t, globalAccountFreeFixture(t), mode)
		})
	}
}

func globalAccountFreeFixture(t *testing.T) func(egressprofile.ProfileRevision) egressprofile.Resolver {
	return func(proxy egressprofile.ProfileRevision) egressprofile.Resolver {
		ctx := context.Background()
		store, err := runtimepersistence.Open(ctx, runtimepersistence.Options{DatabasePath: t.TempDir() + "/runtime.db", BusyTimeout: runtimepersistence.DefaultBusyTimeout, CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Shutdown(ctx) })
		profiles, err := egressprofile.NewManager(store.EgressProfileRepository(), nil)
		if err != nil {
			t.Fatal(err)
		}
		profile, err := profiles.Publish(ctx, egressprofile.PublishCommand{ID: "login.global", DisplayName: "Synthetic login", Policy: proxy.Policy})
		if err != nil {
			t.Fatal(err)
		}
		defaults, err := egressprofile.NewDefaultManager(store.EgressDefaultRepository(), profiles)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = defaults.SetDefault(ctx, egressprofile.SetDefaultCommand{ExpectedRevision: 1, Profile: &egressprofile.ProfileRef{ID: profile.ID, Revision: profile.Revision}}); err != nil {
			t.Fatal(err)
		}
		return defaults
	}
}
