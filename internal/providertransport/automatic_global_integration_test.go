package providertransport_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/clientannotation"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/openairesponses"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/responseschat"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

func syntheticManagedModelRequest(t *testing.T, accounts *provideraccount.Manager, endpoints *upstreamendpoint.Manager, endpoint upstreamendpoint.Endpoint, view provideraccount.View, flow egressprofile.ProfileRevision, hello transportprofile.Observation) exchange.ClientRequest {
	t.Helper()
	origin, err := originidentity.ParseClientOrigin("https://chatgpt.com")
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
	protocols, err := protocolspec.NewCatalog(ops.Definitions(), []protocolspec.CodecPairDefinition{{ID: pair, Revision: responseschat.ResponsesPassthroughCodecRevision, ClientDialect: protocolspec.DialectOpenAIResponses, ProviderDialect: protocolspec.DialectOpenAIResponses, ClientOperationIDs: ops.SemanticOperationIDs(protocolspec.DialectOpenAIResponses), RequiredCapabilities: []protocolspec.ProviderCapability{protocolspec.ProviderCapabilityMessages, protocolspec.ProviderCapabilityStreaming, protocolspec.ProviderCapabilityToolCalls}}})
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
	route := environment.UpstreamRoute{
		ID: "route.synthetic", Revision: 1,
		ProviderTarget:  environment.ProviderTarget{ID: endpoint.ID.String(), Revision: environment.Revision(endpoint.Revision), Origin: endpoint.Origin, RealmID: endpoint.RealmID, Capabilities: endpoint.Capabilities},
		BackendProtocol: string(environment.ClientProtocolOpenAIResponses),
		AccountPolicy:   environment.RouteAccountPolicy{Revision: 1, Mode: environment.AccountSelectionFixed, FixedAccountID: view.Account.ID.String(), Accounts: []environment.RouteAccountReference{{ID: view.Account.ID.String(), Revision: environment.Revision(view.Account.Revision), DisplayName: view.Account.DisplayName}}},
		ModelPolicy:     environment.ModelPolicy{Revision: 1, Mode: environment.ModelModePassthrough},
		WireProfileRef:  wireprofile.UpstreamWireProfileFollowClientValue,
	}
	aggregate := environment.Environment{
		ID: "env.synthetic", Name: "Synthetic", State: environment.StateActive, Revision: 1,
		ContentRecording: environment.DefaultContentRecordingPolicy(),
		ClientEndpoints: []environment.ClientEndpoint{{ID: "endpoint.synthetic", Revision: 1, ClientOrigin: origin,
			ProtocolPlans: []environment.ClientProtocolPlan{{
				ID: "protocol.synthetic", Revision: 1, ClientProtocol: environment.ClientProtocolOpenAIResponses,
				ClientAdapterPolicy: environment.ClientAdapterPolicy{ID: "adapter.synthetic", Revision: 1}, EgressProfile: flow,
				Destination: environment.DestinationPlan{Kind: environment.DestinationKindUpstream, Upstream: &environment.UpstreamPlan{
					DefaultRouteID: route.ID, RouteSet: environment.RouteSet{ID: "routes.synthetic", Revision: 1, CandidateRouteIDs: []environment.UpstreamRouteID{route.ID}}, Routes: []environment.UpstreamRoute{route},
				}},
			}},
		}},
	}
	snapshot, err := compiler.Compile(aggregate)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := snapshot.ResolveRequest(origin, environment.RequestFacts{Target: protocolspec.RequestTarget{Method: http.MethodPost, Path: "/backend-api/codex/responses", Transport: protocolspec.ClientOperationTransportHTTP}, DownstreamProtocol: wireprofile.ApplicationProtocolHTTP1})
	if err != nil {
		t.Fatal(err)
	}
	operation := plan.Operation()
	evidence, err := exchange.NewClientOperationEvidence(operation.ID(), operation.Revision(), http.MethodPost, operation.PathPattern(), "")
	if err != nil {
		t.Fatal(err)
	}
	request, err := exchange.NewClientRequest("exchange.synthetic", plan, evidence, []byte(`{"model":"synthetic","input":"synthetic request","stream":false}`), exchange.ReplayGenerationCostOnly, wireprofile.ApplicationProtocolHTTP1, exchange.WithClientHelloObservation(hello))
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestAutomaticRefreshAndModelKeepExactExplicitSelection(t *testing.T) {
	for _, choice := range []string{"account_over_flow_direct", "flow"} {
		t.Run(choice, func(t *testing.T) { testOwnedGlobalWorkflow(t, true, choice) })
	}
}

func TestExplicitDirectNeverBorrowsGlobalSOCKSForRefresh(t *testing.T) {
	for _, choice := range []string{"account_direct", "flow_direct"} {
		t.Run(choice, func(t *testing.T) { testOwnedGlobalWorkflow(t, true, choice) })
	}
}

type observedAccountAuthority struct {
	accounts *provideraccount.Manager
	scope    providerauth.AccountRef
	calls    int
}

func (a *observedAccountAuthority) Acquire(ctx context.Context, request exchange.AccountLeaseRequest) (providerauth.Lease, error) {
	a.calls++
	lease, err := a.accounts.Acquire(ctx, request)
	if lease != nil {
		a.scope, _ = lease.Account()
	}
	return lease, err
}

type automaticObserver struct{ starts, terminals int }

func (o *automaticObserver) ObserveStart(context.Context, exchange.StartObservation) error {
	o.starts++
	return nil
}
func (o *automaticObserver) ObserveTerminal(context.Context, exchange.AttemptObservation) error {
	o.terminals++
	return nil
}
func (*automaticObserver) ObserveContent(context.Context, exchange.ContentObservation) error {
	return nil
}

type automaticDownstream struct{ bytes.Buffer }

func (*automaticDownstream) Begin(context.Context, exchange.ResponseEnvelope) error { return nil }
func (d *automaticDownstream) Write(_ context.Context, b []byte) (int, error) {
	return d.Buffer.Write(b)
}
func (*automaticDownstream) Keepalive(context.Context) error {
	return errors.New("unexpected short-fixture keepalive")
}
func (*automaticDownstream) Abort(context.Context, exchange.FailureNotice) error { return nil }

func runAutomaticModelWorkflow(t *testing.T, w *providertransport.AccountEgressWorkflow, choice string, accounts *provideraccount.Manager, endpoints *upstreamendpoint.Manager, defaults *egressprofile.DefaultManager, profile egressprofile.ProfileRevision, view provideraccount.View, client *providertransport.Client) {
	t.Helper()
	ctx := t.Context()
	flow := profile
	wantSource := egressprofile.SelectionFlow
	if choice == "account_over_flow_direct" || choice == "account_direct" {
		accountProfile := profile
		if choice == "account_direct" {
			accountProfile = egressprofile.Direct()
		}
		if _, err := accounts.SetSettings(ctx, provideraccount.SettingsCommand{ID: view.Account.ID, ExpectedRevision: 1, EgressProfile: accountProfile, AutomaticRefresh: true}); err != nil {
			t.Fatal(err)
		}
		if choice == "account_over_flow_direct" {
			flow = egressprofile.Direct()
		}
		wantSource = egressprofile.SelectionAccount
	}
	if choice == "flow_direct" {
		flow = egressprofile.Direct()
	}
	// A different configured default cannot override either explicit choice.
	if !w.ExpectDirectRefusal {
		if _, err := defaults.SetDefault(ctx, egressprofile.SetDefaultCommand{ExpectedRevision: 2, Profile: &egressprofile.ProfileRef{ID: egressprofile.DirectID, Revision: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	changed := make(chan error, 1)
	w.AfterToken = func() {
		_, err := defaults.SetDefault(ctx, egressprofile.SetDefaultCommand{ExpectedRevision: 3})
		changed <- err
	}
	endpoint, err := endpoints.Get(ctx, upstreamendpoint.ChatGPTOfficialID)
	if err != nil {
		t.Fatal(err)
	}
	request := syntheticManagedModelRequest(t, accounts, endpoints, endpoint, view, flow, w.ClientHello)
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
	authority := &observedAccountAuthority{accounts: accounts}
	observer := &automaticObserver{}
	pipeline, err := exchange.New(exchange.Options{OwnerContext: ctx, Actions: w.Actions, Accounts: authority, ProtocolPaths: selector, Provider: client, ToolDecisions: decisions, RetryWaiter: exchange.TimerRetryWaiter{}, Observer: observer, ContentObserver: observer, ObservationTimeout: time.Second, Hold: exchange.HoldPolicy{MaxDuration: time.Second}, Stream: exchange.DefaultStreamBudgets(), ClientAnnotations: signer, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		drain, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := pipeline.Shutdown(drain); err != nil {
			t.Error(err)
		}
	}()
	downstream := &automaticDownstream{}
	result, err := pipeline.Execute(ctx, request, downstream)
	if w.ExpectDirectRefusal {
		if err == nil || result.Outcome == exchange.AttemptSucceeded || authority.calls != 1 || downstream.Len() != 0 {
			t.Fatal("refused explicit Direct became a successful model send")
		}
		audits := w.Completed()
		if len(audits) != 1 {
			t.Fatal("failed token exit retried or reached model")
		}
		d := audits[0].Decision()
		if !audits[0].Terminal() || d.SelectionSource != wantSource || d.DefaultRevision != 0 || d.ProxyID != egressprofile.DirectID.String() || d.ProxyRevision != 1 {
			t.Fatal("explicit Direct failure borrowed global or lost audit")
		}
		return
	}
	if err != nil || result.Outcome != exchange.AttemptSucceeded {
		t.Fatalf("real managed pipeline failed: %v outcome=%s", err, result.Outcome)
	}
	if err := <-changed; err != nil {
		t.Fatal(err)
	}
	if authority.calls != 1 || authority.scope.CredentialEpoch != 2 || authority.scope.ResolvedEgress.Source() != wantSource || authority.scope.ResolvedEgress.DefaultRevision() != 0 || !authority.scope.EgressProfile.Equal(profile) {
		t.Fatal("automatic preparation changed the admitted frozen exit")
	}
	if observer.starts != 1 || observer.terminals != 1 || !bytes.Contains(downstream.Bytes(), []byte("synthetic complete")) {
		t.Fatal("model/observation did not complete exactly once")
	}
	audits := w.Completed()
	if len(audits) != 2 {
		t.Fatalf("expected token and model physical terminal audits, got %d", len(audits))
	}
	for _, audit := range audits {
		d := audit.Decision()
		if !audit.Terminal() || d.SelectionSource != wantSource || d.DefaultRevision != 0 || d.ProxyID != profile.ID.String() || d.ProxyRevision != uint64(profile.Revision) || d.AccountID != view.Account.ID.String() || d.AccountSettingsRevision != authority.scope.SettingsRevision {
			t.Fatal("token/model audit differs from one actual frozen lease")
		}
	}
}
