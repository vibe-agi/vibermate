package providertransport_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/codexoauth"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

type countingLocalDefaults struct {
	egressprofile.DefaultRepository
	reads       atomic.Int64
	unavailable atomic.Bool
}

func (r *countingLocalDefaults) LoadDefault(ctx context.Context) (egressprofile.DefaultSetting, error) {
	r.reads.Add(1)
	if r.unavailable.Load() {
		return egressprofile.DefaultSetting{}, egressprofile.ErrDefaultUnavailable
	}
	return r.DefaultRepository.LoadDefault(ctx)
}

type localAccountSetup struct {
	store     *runtimepersistence.Store
	accounts  *provideraccount.Manager
	endpoint  upstreamendpoint.Endpoint
	endpoints *upstreamendpoint.Manager
	view      provideraccount.View
	repo      *countingLocalDefaults
	defaults  *egressprofile.DefaultManager
	profiles  *egressprofile.Manager
	profile   egressprofile.ProfileRevision
}

func newLocalAccountSetup(t *testing.T, origin originidentity.ProviderOrigin, proxy egressprofile.ProfileRevision, secrets secretstore.Store, driver providerauth.DriverRef, credential string, unbound ...bool) localAccountSetup {
	ctx := t.Context()
	store, err := runtimepersistence.Open(ctx, runtimepersistence.Options{DatabasePath: t.TempDir() + "/runtime.db", BusyTimeout: runtimepersistence.DefaultBusyTimeout, CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	profiles, err := egressprofile.NewManager(store.EgressProfileRepository(), nil)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := profiles.Publish(ctx, egressprofile.PublishCommand{ID: "global.external", DisplayName: "Synthetic proxy", Policy: proxy.Policy})
	if err != nil {
		t.Fatal(err)
	}
	repo := &countingLocalDefaults{DefaultRepository: store.EgressDefaultRepository()}
	defaults, err := egressprofile.NewDefaultManager(repo, profiles)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = defaults.SetDefault(ctx, egressprofile.SetDefaultCommand{ExpectedRevision: 1, Profile: &egressprofile.ProfileRef{ID: profile.ID, Revision: profile.Revision}}); err != nil {
		t.Fatal(err)
	}
	endpoints, err := upstreamendpoint.NewManager(ctx, store.UpstreamEndpointRepository(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := endpoints.Create(ctx, upstreamendpoint.CreateCommand{ID: "local.fixture", DisplayName: "Synthetic local", Origin: origin, RealmID: "local.fixture", BackendProtocols: []string{"openai_responses"}, Drivers: []providerauth.DriverRef{driver}, Capabilities: []protocolspec.ProviderCapability{protocolspec.ProviderCapabilityMessages, protocolspec.ProviderCapabilityStreaming, protocolspec.ProviderCapabilityToolCalls}})
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := provideraccount.NewManager(ctx, store.ProviderAccountRepository(), secrets, endpoints, provideraccount.BuiltInRealms(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := accounts.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if len(unbound) == 0 || !unbound[0] {
		if err = accounts.BindEgressDefaults(defaults); err != nil {
			t.Fatal(err)
		}
	}
	material, err := providerauth.NewMaterial(credential, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer material.Destroy()
	encoded, err := material.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(encoded)
	value, err := secretstore.NewValue(encoded)
	if err != nil {
		t.Fatal(err)
	}
	defer value.Destroy()
	view, err := accounts.Create(ctx, provideraccount.CreateCommand{ID: "local-account", DisplayName: "Synthetic", UpstreamEndpointID: endpoint.ID, Driver: driver, Secret: value, AutomaticRefresh: driver == providerauth.CodexOAuthDriverRef()})
	if err != nil {
		t.Fatal(err)
	}
	repo.reads.Store(0)
	return localAccountSetup{store: store, accounts: accounts, endpoint: endpoint, endpoints: endpoints, view: view, repo: repo, defaults: defaults, profiles: profiles, profile: profile}
}

func TestLocalDiscoveryDoesNotInheritExternalGlobalProxy(t *testing.T) {
	providertransport.ExerciseLocalDefaultDiscovery(t, func(t *testing.T, origin originidentity.ProviderOrigin, proxy egressprofile.ProfileRevision, secrets secretstore.Store) providertransport.LocalDiscoveryLease {
		f := newLocalAccountSetup(t, origin, proxy, secrets, providerauth.StaticHeaderDriverRef(), "synthetic-local-token")
		lease, err := f.accounts.AcquireEndpointCredential(t.Context(), f.view.Account.ID, f.endpoint)
		if err != nil {
			t.Fatal(err)
		}
		return providertransport.LocalDiscoveryLease{Endpoint: f.endpoint, Lease: lease, DefaultReads: f.repo.reads.Load}
	})
}

func TestPrivateDNSDiscoveryUsesOnlyExactPrivatePeer(t *testing.T) {
	providertransport.ExerciseLocalDefaultDiscovery(t, func(t *testing.T, origin originidentity.ProviderOrigin, proxy egressprofile.ProfileRevision, secrets secretstore.Store) providertransport.LocalDiscoveryLease {
		if origin.Transport() != originidentity.ProviderTransportPrivateCleartext {
			t.Fatal("fixture did not exercise DNS private-cleartext classification")
		}
		f := newLocalAccountSetup(t, origin, proxy, secrets, providerauth.StaticHeaderDriverRef(), "synthetic-local-token")
		lease, err := f.accounts.AcquireEndpointCredential(t.Context(), f.view.Account.ID, f.endpoint)
		if err != nil {
			t.Fatal(err)
		}
		return providertransport.LocalDiscoveryLease{Endpoint: f.endpoint, Lease: lease, DefaultReads: f.repo.reads.Load}
	}, true)
}

func TestLocalOwnedAndDiscoveryRemainDirectAcrossDefaultState(t *testing.T) {
	for _, mode := range []string{"owned", "unavailable", "unbound", "held_change", "account_direct", "account_proxy"} {
		t.Run(mode, func(t *testing.T) {
			providertransport.ExerciseLocalDefaultDiscovery(t, func(t *testing.T, origin originidentity.ProviderOrigin, proxy egressprofile.ProfileRevision, secrets secretstore.Store) providertransport.LocalDiscoveryLease {
				counted := &countedLocalSecrets{Store: secrets}
				f := newLocalAccountSetup(t, origin, proxy, counted, providerauth.StaticHeaderDriverRef(), "synthetic-local-token", mode == "unbound")
				selected := providertransport.LocalDiscoveryLease{Endpoint: f.endpoint, DefaultReads: f.repo.reads.Load}
				if mode == "unavailable" {
					f.repo.unavailable.Store(true)
				}
				if mode == "account_direct" || mode == "account_proxy" {
					if err := f.accounts.BindEgressProfiles(f.profiles); err != nil {
						t.Fatal(err)
					}
					profile := egressprofile.Direct()
					if mode == "account_proxy" {
						profile = f.profile
					}
					if _, err := f.accounts.SetSettings(t.Context(), provideraccount.SettingsCommand{ID: f.view.Account.ID, ExpectedRevision: 1, EgressProfile: profile}); err != nil {
						t.Fatal(err)
					}
					selected.WantSource = egressprofile.SelectionAccount
				}
				counted.inspects.Store(0)
				counted.reads.Store(0)
				var lease providerauth.Lease
				var err error
				if mode == "owned" {
					lease, _, err = f.accounts.AcquireOwnedCredential(t.Context(), f.view.Account.ID)
				} else {
					lease, err = f.accounts.AcquireEndpointCredential(t.Context(), f.view.Account.ID, f.endpoint)
				}
				if mode == "account_proxy" {
					if lease != nil {
						lease.Release()
					}
					if !errors.Is(err, provideraccount.ErrInvalidAccount) || lease != nil || counted.inspects.Load() != 0 || counted.reads.Load() != 0 || f.repo.reads.Load() != 0 {
						t.Fatal("winning explicit proxy was not rejected before preparation", err)
					}
					selected.Rejected = true
					return selected
				}
				if err != nil {
					t.Fatal(err)
				}
				selected.Lease = lease
				if mode == "held_change" {
					selected.ExpectedDefaultReads = 1
					selected.WhileHeld = func() error {
						_, err := f.defaults.SetDefault(t.Context(), egressprofile.SetDefaultCommand{ExpectedRevision: 2})
						return err
					}
				}
				return selected
			})
		})
	}
}

type countedLocalSecrets struct {
	secretstore.Store
	inspects, reads atomic.Int64
}

func (s *countedLocalSecrets) Inspect(ctx context.Context, ref secretstore.Reference) (secretstore.Metadata, error) {
	s.inspects.Add(1)
	return s.Store.Inspect(ctx, ref)
}
func (s *countedLocalSecrets) ReadAtRevision(ctx context.Context, ref secretstore.Reference, revision secretstore.Revision) (*secretstore.Value, error) {
	s.reads.Add(1)
	return s.Store.ReadAtRevision(ctx, ref, revision)
}

type localTokenRefusal struct {
	calls       atomic.Int64
	scope       providerauth.AccountRef
	fixedTarget bool
}

func (c *localTokenRefusal) Do(request *http.Request, scope providerauth.AccountRef) (*http.Response, error) {
	c.scope = scope
	c.fixedTarget = request.URL.String() == codexoauth.TokenURL && request.Method == http.MethodPost
	c.calls.Add(1)
	return nil, errors.New("synthetic token dispatch refused")
}

type countedLocalPreparer struct {
	*codexoauth.Manager
	calls atomic.Int64
}

func (p *countedLocalPreparer) Prepare(ctx context.Context, d providerauth.DriverRef, r secretstore.Reference, a providerauth.AccountRef, allow bool) (secretstore.Revision, error) {
	p.calls.Add(1)
	return p.Manager.Prepare(ctx, d, r, a, allow)
}

func TestTypedLocalOAuthRefusesBeforeSecretPreparation(t *testing.T) {
	for _, cold := range []bool{false, true} {
		t.Run(fmt.Sprintf("cold_epoch=%t", cold), func(t *testing.T) { exerciseTypedLocalOAuth(t, "", cold) })
	}
}

func TestTypedLocalManualRefreshUsesActualHTTPSSelection(t *testing.T) {
	for _, mode := range []string{"manual", "manual_unavailable"} {
		t.Run(mode, func(t *testing.T) { exerciseTypedLocalOAuth(t, mode) })
	}
}

func exerciseTypedLocalOAuth(t *testing.T, mode string, coldEpoch ...bool) {
	providertransport.ExerciseLocalDefaultDiscovery(t, func(t *testing.T, origin originidentity.ProviderOrigin, proxy egressprofile.ProfileRevision, secrets secretstore.Store) providertransport.LocalDiscoveryLease {
		now := time.Now().UTC()
		token := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d,"https://api.openai.com/auth":{"chatgpt_account_id":"synthetic-local"}}`, now.Add(time.Minute).Unix()))) + ".synthetic"
		credential, err := codexoauth.ImportAuthJSON([]byte(fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"id_token":%q,"access_token":%q,"refresh_token":"synthetic-refresh","account_id":"synthetic-local"},"last_refresh":%q}`, token, token, now.Format(time.RFC3339Nano))))
		if err != nil {
			t.Fatal(err)
		}
		defer credential.Destroy()
		encoded, err := credential.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		defer clear(encoded)
		counted := &countedLocalSecrets{Store: secrets}
		f := newLocalAccountSetup(t, origin, proxy, counted, providerauth.CodexOAuthDriverRef(), string(encoded))
		if len(coldEpoch) > 0 && coldEpoch[0] {
			if err := f.accounts.Shutdown(t.Context()); err != nil {
				t.Fatal(err)
			}
			f.accounts, err = provideraccount.NewManager(t.Context(), f.store.ProviderAccountRepository(), counted, f.endpoints, provideraccount.BuiltInRealms(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if err = f.accounts.BindEgressDefaults(f.defaults); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := f.accounts.Shutdown(context.Background()); err != nil {
					t.Error(err)
				}
			})
		}
		wire := &localTokenRefusal{}
		oauth, err := codexoauth.NewManager(codexoauth.Options{Secrets: counted, Client: wire, Clock: actualRefreshClock{}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := oauth.Shutdown(ctx); err != nil {
				t.Error(err)
			}
		})
		preparer := &countedLocalPreparer{Manager: oauth}
		if err = f.accounts.BindCredentialPreparer(preparer); err != nil {
			t.Fatal(err)
		}
		counted.inspects.Store(0)
		counted.reads.Store(0)
		if mode != "" {
			if mode == "manual_unavailable" {
				f.repo.unavailable.Store(true)
			}
			_, err := f.accounts.RefreshCredential(t.Context(), f.view.Account.ID, 1)
			if mode == "manual_unavailable" {
				if !errors.Is(err, egressprofile.ErrDefaultUnavailable) || wire.calls.Load() != 0 || counted.inspects.Load() != 0 || counted.reads.Load() != 0 || f.repo.reads.Load() != 1 {
					t.Fatal("manual HTTPS refresh bypassed unavailable global", err)
				}
			} else {
				if !errors.Is(err, codexoauth.ErrRefreshUnavailable) || wire.calls.Load() != 1 || !wire.fixedTarget || wire.scope.ResolvedEgress.Source() != egressprofile.SelectionGlobal || wire.scope.ResolvedEgress.DefaultRevision() != 2 || !wire.scope.EgressProfile.Equal(f.profile) || f.repo.reads.Load() != 1 {
					t.Fatal("manual fixed-HTTPS refresh incorrectly borrowed local exemption", err)
				}
			}
			return providertransport.LocalDiscoveryLease{Rejected: true}
		}
		lease, err := f.accounts.AcquireEndpointCredential(t.Context(), f.view.Account.ID, f.endpoint)
		if lease != nil {
			lease.Release()
		}
		if !errors.Is(err, provideraccount.ErrEndpointMismatch) || lease != nil || counted.inspects.Load() != 0 || counted.reads.Load() != 0 || preparer.calls.Load() != 0 || wire.calls.Load() != 0 || f.repo.reads.Load() != 0 {
			t.Fatalf("unsupported local OAuth reached preparation: expectedMismatch=%t lease=%t inspect=%d read=%d prepare=%d token=%d default=%d", errors.Is(err, provideraccount.ErrEndpointMismatch), lease != nil, counted.inspects.Load(), counted.reads.Load(), preparer.calls.Load(), wire.calls.Load(), f.repo.reads.Load())
		}
		return providertransport.LocalDiscoveryLease{Rejected: true}
	})
}
