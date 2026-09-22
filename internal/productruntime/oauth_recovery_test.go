package productruntime

import (
	"bytes"
	"context"
	"slices"
	"testing"

	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

func TestProductRuntimeRestoresPublishedRoutesAcrossOAuthCapabilityUpgrade(t *testing.T) {
	for _, previouslyUpgraded := range []bool{false, true} {
		name := "first upgrade"
		if previouslyUpgraded {
			name = "retry after profile was already upgraded"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			options := testOptions(t, hostcontract.Desktop(), &coordinatorDouble{})
			store, err := runtimepersistence.Open(ctx, runtimepersistence.Options{
				DatabasePath: options.Paths.DatabasePath(), BusyTimeout: runtimepersistence.DefaultBusyTimeout,
				CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer store.Shutdown(ctx)
			builtins, err := upstreamendpoint.BuiltInCommands()
			if err != nil {
				t.Fatal(err)
			}
			for index := range builtins {
				if builtins[index].ID == upstreamendpoint.ChatGPTOfficialID {
					builtins[index].Drivers = []providerauth.DriverRef{providerauth.StaticHeaderDriverRef()}
				}
			}
			endpoints, err := upstreamendpoint.NewManager(ctx, store.UpstreamEndpointRepository(), builtins, SystemClock{})
			if err != nil {
				t.Fatal(err)
			}
			accounts, err := provideraccount.NewManager(ctx, store.ProviderAccountRepository(), options.Secrets, endpoints, provideraccount.BuiltInRealms(), SystemClock{})
			if err != nil {
				t.Fatal(err)
			}
			material, err := providerauth.NewMaterial("synthetic-upgrade-token", map[string]string{"User-Agent": "frozen-upgrade-agent"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer material.Destroy()
			encoded, err := material.MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}
			defer clear(encoded)
			secret, err := secretstore.NewValue(encoded)
			if err != nil {
				t.Fatal(err)
			}
			defer secret.Destroy()
			account, err := accounts.Create(ctx, provideraccount.CreateCommand{
				ID: "account.upgrade", DisplayName: "Existing account", UpstreamEndpointID: upstreamendpoint.ChatGPTOfficialID,
				Driver: providerauth.StaticHeaderDriverRef(), Secret: secret,
			})
			if err != nil {
				t.Fatal(err)
			}
			endpoint, err := endpoints.Get(ctx, upstreamendpoint.ChatGPTOfficialID)
			if err != nil || endpoint.Revision != 1 {
				t.Fatalf("original profile = %+v, %v", endpoint, err)
			}
			value, clientOrigin, _ := compatibilityEnvironment(t, environment.ClientProtocolOpenAIResponses, protocolspec.DialectOpenAIResponses)
			route := &value.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0]
			route.ProviderTarget = environment.ProviderTarget{
				ID: endpoint.ID.String(), Revision: 1, Origin: endpoint.Origin,
				RealmID: endpoint.RealmID, Capabilities: endpoint.Capabilities,
			}
			route.BackendProtocol = endpoint.BackendProtocols[0]
			route.AccountPolicy.FixedAccountID = account.Account.ID.String()
			route.AccountPolicy.Accounts = []environment.RouteAccountReference{{ID: account.Account.ID.String(), Revision: 1, DisplayName: account.Account.DisplayName}}
			compiler, err := productionEnvironmentCompiler(accounts, endpoints)
			if err != nil {
				t.Fatal(err)
			}
			policies, err := environment.NewManager(ctx, store.EnvironmentRepository(), compiler, environment.NewAtomicProjection(), nil)
			if err != nil {
				t.Fatal(err)
			}
			draft, err := policies.SaveDraft(ctx, environment.DraftCommand{Candidate: value})
			if err != nil {
				t.Fatal(err)
			}
			preview, err := policies.Preview(ctx, value.ID, draft.Revision)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := policies.Publish(ctx, preview); err != nil {
				t.Fatal(err)
			}
			before, err := policies.Get(ctx, value.ID)
			if err != nil {
				t.Fatal(err)
			}
			beforeJSON, _ := environment.CanonicalJSON(before.Aggregate())
			if err := accounts.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			if err := endpoints.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			if previouslyUpgraded {
				upgraded, err := upstreamendpoint.NewManager(ctx, store.UpstreamEndpointRepository(), builtins, SystemClock{})
				if err != nil {
					t.Fatal(err)
				}
				if err := upgraded.Shutdown(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}

			for restart := 0; restart < 2; restart++ {
				runtime := startTestRuntime(t, options)
				func() {
					defer shutdownRuntime(t, runtime)
					if status := runtime.Status(); status.EnvironmentProjection.State != environment.ProjectionStateHealthy {
						t.Fatalf("startup projection unhealthy: %+v", status)
					}
					current, err := runtime.UpstreamEndpoints().Get(ctx, endpoint.ID)
					if err != nil || current.Revision != 2 || !slices.Contains(current.Drivers, providerauth.CodexOAuthDriverRef()) {
						t.Fatalf("OAuth capability not installed exactly once: %+v %v", current, err)
					}
					for name, read := range map[string]func() (environment.EnvironmentSnapshot, error){
						"active": func() (environment.EnvironmentSnapshot, error) { return runtime.Environments().Get(ctx, value.ID) },
						"history": func() (environment.EnvironmentSnapshot, error) {
							return runtime.Environments().GetRevision(ctx, value.ID, 1)
						},
					} {
						got, err := read()
						if err != nil {
							t.Fatalf("%s: %v", name, err)
						}
						gotJSON, _ := environment.CanonicalJSON(got.Aggregate())
						if got.Digest() != before.Digest() || !bytes.Equal(gotJSON, beforeJSON) {
							t.Fatalf("%s silently republished or rewrote the saved policy", name)
						}
					}
					listed, err := runtime.Environments().List(ctx)
					if err != nil || len(listed) != 2 {
						t.Fatalf("list existing policies: %v", err)
					}
					capture, err := captureidentity.New(captureidentity.KindManagedRun, "capture.recovery")
					if err != nil {
						t.Fatal(err)
					}
					if restart == 0 {
						if _, err := runtime.CaptureAssignments().Create(ctx, captureassignment.CreateCommand{
							Capture: capture, EnvironmentID: value.ID, Source: captureassignment.SourceLaunch,
						}); err != nil {
							t.Fatal(err)
						}
					}
					originalOrigin, err := originidentity.ParseProviderOrigin(clientOrigin.String())
					if err != nil {
						t.Fatal(err)
					}
					connection, err := runtime.assignments.RegisterProviderConnection(ctx, capture, "connection.recovery", originalOrigin)
					if err != nil {
						t.Fatalf("capture historical resolver: %v", err)
					}
					defer connection.Close()
					request, err := runtime.assignments.BeginRequest(ctx, capture, "connection.recovery", environment.RequestFacts{
						Target:             protocolspec.RequestTarget{Method: "POST", Path: "/v1/responses", Transport: protocolspec.ClientOperationTransportHTTP},
						DownstreamProtocol: wireprofile.ApplicationProtocolHTTP1,
					})
					if err != nil {
						t.Fatalf("frozen request planning: %v", err)
					}
					defer request.Release()
					frozenRoute, ok := request.Plan().UpstreamRoute()
					if !ok || frozenRoute.ProviderTarget().Revision != 1 || request.Plan().EnvironmentRevision() != 1 {
						t.Fatal("capture resolver advanced the frozen route to the current profile")
					}
					// Both leased credential use and overwrite readback must honor an
					// existing r1 route, even though the live profile now advertises r2.
					lease, err := runtime.ProviderAccounts().AcquireEndpointCredential(ctx, account.Account.ID, endpoint)
					if err != nil {
						t.Fatalf("old route credential: %v", err)
					}
					lease.Release()
					ua, err := runtime.accounts.ReadOverwriteHeader(ctx, providerauth.HeaderLookup{
						AccountID: account.Account.ID.String(), AccountRevision: 1, CredentialEpoch: 1,
						UpstreamEndpointID: endpoint.ID.String(), UpstreamEndpointRevision: 1, Name: "User-Agent",
					})
					if err != nil || ua != "frozen-upgrade-agent" {
						t.Fatalf("old route overwrite readback: %v", err)
					}
				}()
			}
		})
	}
}
