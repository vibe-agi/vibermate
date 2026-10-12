package providertransport_test

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/hostsecret"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/responseschat"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

func TestCapturedAccountProbeClosedProducerAndConcreteTLSReachability(t *testing.T) {
	providertransport.ExerciseCapturedAccountProbe(t, func(t *testing.T, originString, path, accountID string, history bool, profile egressprofile.ProfileRevision) (environment.RequestPlan, providerauth.Lease) {
		ctx := t.Context()
		dir := t.TempDir()
		store, err := runtimepersistence.Open(ctx, runtimepersistence.Options{DatabasePath: filepath.Join(dir, "runtime.db"), BusyTimeout: runtimepersistence.DefaultBusyTimeout, CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := store.Shutdown(context.Background()); err != nil {
				t.Error(err)
			}
		})
		factory, err := hostsecret.NewServerFileFactory(dir)
		if err != nil {
			t.Fatal(err)
		}
		secrets, err := factory.Open(ctx)
		if err != nil {
			t.Fatal(err)
		}
		profiles, err := egressprofile.NewManager(store.EgressProfileRepository(), nil)
		if err != nil {
			t.Fatal(err)
		}
		published, err := profiles.Publish(ctx, egressprofile.PublishCommand{ID: profile.ID, DisplayName: profile.DisplayName, Policy: profile.Policy})
		if err != nil {
			t.Fatal(err)
		}
		defaults, err := egressprofile.NewDefaultManager(store.EgressDefaultRepository(), profiles)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := defaults.SetDefault(ctx, egressprofile.SetDefaultCommand{ExpectedRevision: 1, Profile: &egressprofile.ProfileRef{ID: published.ID, Revision: published.Revision}}); err != nil {
			t.Fatal(err)
		}
		endpoints, err := upstreamendpoint.NewManager(ctx, store.UpstreamEndpointRepository(), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		origin, err := originidentity.ParseProviderOrigin(originString)
		if err != nil {
			t.Fatal(err)
		}
		endpoint, err := endpoints.Create(ctx, upstreamendpoint.CreateCommand{ID: "target.captured-probe", DisplayName: "Private captured probe", Origin: origin, RealmID: "openai.chatgpt", BackendProtocols: []string{"openai_responses"}, Drivers: []providerauth.DriverRef{providerauth.StaticHeaderDriverRef()}, Capabilities: []protocolspec.ProviderCapability{protocolspec.ProviderCapabilityMessages, protocolspec.ProviderCapabilityStreaming, protocolspec.ProviderCapabilityToolCalls}})
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
		if err := accounts.BindEgressDefaults(defaults); err != nil {
			t.Fatal(err)
		}
		id := accountID
		if id == "original" {
			id = "captured-b"
		}
		material, err := providerauth.NewMaterial("synthetic-managed-probe-token", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer material.Destroy()
		bytes, err := material.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		defer clear(bytes)
		secret, err := secretstore.NewValue(bytes)
		if err != nil {
			t.Fatal(err)
		}
		defer secret.Destroy()
		view, err := accounts.Create(ctx, provideraccount.CreateCommand{ID: provideraccount.ID(id), DisplayName: id, UpstreamEndpointID: endpoint.ID, Driver: providerauth.StaticHeaderDriverRef(), Secret: secret})
		if err != nil {
			t.Fatal(err)
		}
		ops, err := operationcatalog.BuiltIn()
		if err != nil {
			t.Fatal(err)
		}
		pair, err := protocolspec.NewCodecPairID(responseschat.ResponsesPassthroughCodecPairID)
		if err != nil {
			t.Fatal(err)
		}
		protocols, err := protocolspec.NewCatalog(ops.Definitions(), []protocolspec.CodecPairDefinition{{ID: pair, Revision: responseschat.ResponsesPassthroughCodecRevision, ClientDialect: protocolspec.DialectOpenAIResponses, ProviderDialect: protocolspec.DialectOpenAIResponses, ClientOperationIDs: ops.SemanticOperationIDs(protocolspec.DialectOpenAIResponses), RequiredCapabilities: endpoint.Capabilities}})
		if err != nil {
			t.Fatal(err)
		}
		wires, err := wireprofile.BuiltInCatalog()
		if err != nil {
			t.Fatal(err)
		}
		compiler, err := environment.NewCompiler(accounts, endpoints, protocols, wires)
		if err != nil {
			t.Fatal(err)
		}
		clientOrigin, err := originidentity.ParseClientOrigin("https://chatgpt.com")
		if err != nil {
			t.Fatal(err)
		}
		route := environment.UpstreamRoute{ID: "route.captured-probe", Revision: 1, ProviderTarget: environment.ProviderTarget{ID: endpoint.ID.String(), Revision: environment.Revision(endpoint.Revision), Origin: endpoint.Origin, RealmID: endpoint.RealmID, Capabilities: endpoint.Capabilities}, BackendProtocol: "openai_responses", AllowAccountHistory: history, AccountPolicy: environment.RouteAccountPolicy{Revision: 1, Mode: environment.AccountSelectionFixed, FixedAccountID: view.Account.ID.String(), Accounts: []environment.RouteAccountReference{{ID: view.Account.ID.String(), Revision: environment.Revision(view.Account.Revision), DisplayName: view.Account.DisplayName}}}, ModelPolicy: environment.ModelPolicy{Revision: 1, Mode: environment.ModelModePassthrough}, WireProfileRef: wireprofile.UpstreamWireProfileFollowClientValue}
		aggregate := environment.Environment{ID: "environment.captured-probe", Name: "Captured probe", State: environment.StateActive, Revision: 1, ContentRecording: environment.DefaultContentRecordingPolicy(), ClientEndpoints: []environment.ClientEndpoint{{ID: "endpoint.captured-probe", Revision: 1, ClientOrigin: clientOrigin, ProtocolPlans: []environment.ClientProtocolPlan{{ID: "protocol.captured-probe", Revision: 1, ClientProtocol: environment.ClientProtocolOpenAIResponses, ClientAdapterPolicy: environment.ClientAdapterPolicy{ID: "adapter.captured-probe", Revision: 1}, EgressProfile: egressprofile.Inherit(), Destination: environment.DestinationPlan{Kind: environment.DestinationKindUpstream, Upstream: &environment.UpstreamPlan{DefaultRouteID: route.ID, RouteSet: environment.RouteSet{ID: "routes.captured-probe", Revision: 1, CandidateRouteIDs: []environment.UpstreamRouteID{route.ID}}, Routes: []environment.UpstreamRoute{route}}}}}}}}
		snapshot, err := compiler.Compile(aggregate)
		if err != nil {
			t.Fatal(err)
		}
		facts := environment.RequestFacts{Target: protocolspec.RequestTarget{Method: http.MethodGet, Path: path, Transport: protocolspec.ClientOperationTransportHTTP}, DownstreamProtocol: wireprofile.ApplicationProtocolHTTP1}
		plan, err := snapshot.ResolveRequest(clientOrigin, facts)
		if err != nil {
			t.Fatal(err)
		}
		lease, err := accounts.AcquireReadCredential(ctx, plan)
		if err != nil {
			t.Fatal(err)
		}
		if accountID == "original" {
			aggregate.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0].AccountPolicy = environment.RouteAccountPolicy{Revision: 1, Mode: environment.AccountSelectionOriginal}
			snapshot, err = compiler.Compile(aggregate)
			if err != nil {
				t.Fatal(err)
			}
			plan, err = snapshot.ResolveRequest(clientOrigin, facts)
			if err != nil {
				t.Fatal(err)
			}
		}
		if _, err := snapshot.ResolveRequest(clientOrigin, environment.RequestFacts{Target: protocolspec.RequestTarget{Method: http.MethodGet, Path: "/closed-unknown-read", Transport: protocolspec.ClientOperationTransportHTTP}, DownstreamProtocol: wireprofile.ApplicationProtocolHTTP1}); err == nil {
			t.Fatal("production operation catalog granted unknown read")
		}
		return plan, lease
	})
}
