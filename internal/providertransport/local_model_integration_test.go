package providertransport_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/clientannotation"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/openairesponses"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/responseschat"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
)

func TestCompiledLocalModelPreservesExplicitPriority(t *testing.T) {
	for _, mode := range []string{"flow_direct", "account_direct_over_proxy", "account_proxy_over_direct", "flow_proxy", "authored_dns"} {
		t.Run(mode, func(t *testing.T) {
			providertransport.ExerciseLocalDefaultDiscovery(t, func(t *testing.T, origin originidentity.ProviderOrigin, proxy egressprofile.ProfileRevision, secrets secretstore.Store) providertransport.LocalDiscoveryLease {
				counted := &countedLocalSecrets{Store: secrets}
				f := newLocalAccountSetup(t, origin, proxy, counted, providerauth.StaticHeaderDriverRef(), "synthetic-local-token")
				if err := f.accounts.BindEgressProfiles(f.profiles); err != nil {
					t.Fatal(err)
				}
				flow, wanted := egressprofile.Direct(), egressprofile.Direct()
				source := egressprofile.SelectionFlow
				rejected := mode == "flow_proxy" || mode == "account_proxy_over_direct"
				if mode == "flow_proxy" || mode == "account_direct_over_proxy" || mode == "authored_dns" {
					flow = f.profile
				}
				if strings.HasPrefix(mode, "account_") || mode == "authored_dns" {
					if mode == "account_proxy_over_direct" {
						wanted = f.profile
					}
					if mode == "authored_dns" {
						policy := egressnetwork.DefaultPolicy()
						policy.Resolver = egressnetwork.ResolverPolicy{Kind: egressnetwork.ResolverDoH, DoHURL: "https://" + origin.HTTPAuthority() + "/dns-query", Transport: egressnetwork.ResolverTransportDirect}
						var err error
						wanted, err = f.profiles.Publish(t.Context(), egressprofile.PublishCommand{ID: "authored.direct", DisplayName: "Authored DNS", Policy: policy})
						if err != nil {
							t.Fatal(err)
						}
					}
					if _, err := f.accounts.SetSettings(t.Context(), provideraccount.SettingsCommand{ID: f.view.Account.ID, ExpectedRevision: 1, EgressProfile: wanted}); err != nil {
						t.Fatal(err)
					}
					source = egressprofile.SelectionAccount
				}
				f.repo.unavailable.Store(true)
				counted.reads.Store(0)
				counted.inspects.Store(0)
				selected := providertransport.LocalDiscoveryLease{DefaultReads: f.repo.reads.Load, WantProfile: wanted, WantSource: source, ExpectModelRejected: rejected}
				selected.Model = func(client *providertransport.Client, gate *offlinehold.Gate, hello transportprofile.Observation) providerauth.AccountRef {
					request := syntheticManagedModelRequest(t, f.accounts, f.endpoints, f.endpoint, f.view, flow, hello)
					path, err := responseschat.NewResponsesPassthroughProtocolPath(openairesponses.DefaultOptions())
					if err != nil {
						t.Fatal(err)
					}
					selector, err := protocolpath.NewSelector(path)
					if err != nil {
						t.Fatal(err)
					}
					signer, err := clientannotation.NewSigner(bytes.Repeat([]byte{0x42}, 32))
					if err != nil {
						t.Fatal(err)
					}
					defer signer.Destroy()
					decisions, err := exchange.NewRejectAllToolDecisions("synthetic_no_tools")
					if err != nil {
						t.Fatal(err)
					}
					authority := &observedAccountAuthority{accounts: f.accounts}
					observer := &automaticObserver{}
					pipeline, err := exchange.New(exchange.Options{OwnerContext: t.Context(), Actions: gate, Accounts: authority, ProtocolPaths: selector, Provider: client, ToolDecisions: decisions, RetryWaiter: exchange.TimerRetryWaiter{}, Observer: observer, ContentObserver: observer, ObservationTimeout: time.Second, Hold: exchange.HoldPolicy{MaxDuration: time.Second}, Stream: exchange.DefaultStreamBudgets(), ClientAnnotations: signer, Now: time.Now})
					if err != nil {
						t.Fatal(err)
					}
					defer func() {
						ctx, cancel := context.WithTimeout(context.Background(), time.Second)
						defer cancel()
						if err := pipeline.Shutdown(ctx); err != nil {
							t.Error(err)
						}
					}()
					downstream := &automaticDownstream{}
					result, err := pipeline.Execute(t.Context(), request, downstream)
					if rejected {
						if err == nil || result.Outcome == exchange.AttemptSucceeded || authority.calls != 1 || authority.scope != (providerauth.AccountRef{}) || counted.inspects.Load() != 0 || counted.reads.Load() != 0 || downstream.Len() != 0 {
							t.Fatal("winning proxy was not rejected at acquisition", err)
						}
						return providerauth.AccountRef{}
					}
					if err != nil || result.Outcome != exchange.AttemptSucceeded || authority.calls != 1 || !bytes.Contains(downstream.Bytes(), []byte("local model complete")) {
						t.Fatal("compiled local model failed", err)
					}
					return authority.scope
				}
				return selected
			})
		})
	}
}
