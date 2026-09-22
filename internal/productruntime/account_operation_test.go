package productruntime

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/accountoperation"
	"github.com/vibe-agi/vibermate/internal/accountselector"
	"github.com/vibe-agi/vibermate/internal/codelibrary"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
	"github.com/vibe-agi/vibermate/internal/upstreamservice"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

// The only substitute is the external HTTP transport. Account storage,
// associations, compilation, leases, authentication and Hold are real.
type accountReadWire struct {
	mu       sync.Mutex
	requests []*http.Request
}

func (wire *accountReadWire) RoundTrip(request *http.Request, _ providertransport.TransportDispatch) (*http.Response, transportprofile.Evidence, error) {
	wire.mu.Lock()
	wire.requests = append(wire.requests, request.Clone(request.Context()))
	wire.mu.Unlock()
	if request.URL.Path == "/backend-api/wham/profiles/me" {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"stats":{"lifetime_tokens":1200}}`))}, transportprofile.Evidence{}, nil
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"plan_type":"pro","account_id":%q,"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":25,"limit_window_seconds":18000,"reset_after_seconds":3600,"reset_at":1800000000}}}`, request.Header.Get("Chatgpt-Account-Id"))))}, transportprofile.Evidence{}, nil
}

type accountReadFixture struct {
	runtime   *Runtime
	reader    *accountoperation.Reader
	wire      *accountReadWire
	plan      environment.RequestPlan
	aggregate environment.Environment
	account   provideraccount.View
	token     string
}

func newAccountReadFixture(t *testing.T) accountReadFixture {
	t.Helper()
	ctx := context.Background()
	gate, err := offlinehold.New(offlinehold.Config{MaxHeldRequests: 8, MaxHeldBytes: 1 << 20, MaxHoldDuration: time.Second, ReleaseConcurrency: 2})
	if err != nil {
		t.Fatal(err)
	}
	options := testOptions(t, hostcontract.Desktop(), gate)
	runtime := startTestRuntime(t, options)
	t.Cleanup(func() { shutdownRuntime(t, runtime) })
	token := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"workspace-B"}}`)) + ".synthetic"
	material, err := providerauth.NewMaterial(token, nil, nil)
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
	view, err := runtime.accounts.Create(ctx, provideraccount.CreateCommand{
		ID: "managed-b", DisplayName: "Managed B", UpstreamEndpointID: upstreamendpoint.ChatGPTOfficialID,
		Driver: providerauth.StaticHeaderDriverRef(), Secret: secret,
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := runtime.endpoints.Get(ctx, upstreamendpoint.ChatGPTOfficialID)
	if err != nil {
		t.Fatal(err)
	}
	aggregate, _, _ := compatibilityEnvironment(t, environment.ClientProtocolOpenAIResponses, protocolspec.DialectOpenAIResponses)
	origin, _ := originidentity.ParseClientOrigin("https://chatgpt.com")
	aggregate.ClientEndpoints[0].ClientOrigin = origin
	route := &aggregate.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0]
	route.BackendProtocol = string(environment.ClientProtocolOpenAIResponses)
	route.ProviderTarget = environment.ProviderTarget{ID: endpoint.ID.String(), Revision: environment.Revision(endpoint.Revision), Origin: endpoint.Origin, RealmID: endpoint.RealmID, Capabilities: endpoint.Capabilities}
	route.AccountPolicy.FixedAccountID = view.Account.ID.String()
	route.AccountPolicy.Accounts = []environment.RouteAccountReference{{ID: view.Account.ID.String(), Revision: environment.Revision(view.Account.Revision), DisplayName: "Managed B"}}
	compiler, err := productionEnvironmentCompiler(runtime.accounts, runtime.endpoints)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := compiler.Compile(aggregate)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := snapshot.ResolveRequest(origin, environment.RequestFacts{Target: protocolspec.RequestTarget{Method: "GET", Path: "/backend-api/wham/usage", Transport: protocolspec.ClientOperationTransportHTTP}, DownstreamProtocol: wireprofile.ApplicationProtocolHTTP1})
	if err != nil {
		t.Fatal(err)
	}
	wire := &accountReadWire{}
	auth, err := providertransport.NewStaticBearerAuthenticator(options.Secrets)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := providertransport.NewClient(providertransport.ClientOptions{Coordinator: gate, Authenticator: auth, Transport: wire, InstanceIDs: options.InstanceIDs, Audit: runtime.egressCompletion})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transport.Shutdown(ctx) })
	reader, err := accountoperation.New(accountoperation.Options{Accounts: runtime.accounts, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	return accountReadFixture{runtime: runtime, reader: reader, wire: wire, plan: plan, aggregate: aggregate, account: view, token: token}
}

func TestCapturedAccountReadUsesFixedAccountBeforeAnyGeneration(t *testing.T) {
	f := newAccountReadFixture(t)
	result, err := f.reader.ReadCaptured(context.Background(), f.plan, accountoperation.Source{ConnectionID: "connection.fixture", OperationID: "operation.fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.wire.requests) != 1 || f.wire.requests[0].Header.Get("Authorization") != "Bearer "+f.token || f.wire.requests[0].Header.Get("Chatgpt-Account-Id") != "workspace-B" {
		t.Fatal("captured account read did not authenticate as frozen managed B")
	}
	if result.Facts.AccountID != "managed-b" || result.Facts.Origin != "https://chatgpt.com" || result.Facts.PlanType != "pro" {
		t.Fatal("quota projection lost its real account provenance")
	}
	if string(result.Body) == "" || !strings.Contains(string(result.Body), `"account_id":"workspace-B"`) {
		t.Fatal("native account response was rewritten to impersonate the original client")
	}
	page, err := f.runtime.EgressAttempts().List(context.Background(), egressaudit.PageRequest{ConnectionID: "connection.fixture"})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("missing account operation audit: %v", err)
	}
	attempt := page.Items[0].Attempt
	if attempt.Parent().Kind != egressaudit.ParentClientOperation || attempt.PayloadClass() != egressaudit.PayloadControl ||
		attempt.Purpose() != egressaudit.PurposeRouteOperation || attempt.Decision().RuleID != "route.test" {
		t.Fatal("account query audit lost the frozen route or became an Exchange")
	}
}

func TestCapturedAccountReadDoesNotRunAnUnboundTurnSelector(t *testing.T) {
	f := newAccountReadFixture(t)
	aggregate := f.aggregate.Clone()
	policy := &aggregate.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0].AccountPolicy
	policy.Mode, policy.FixedAccountID = environment.AccountSelectionJavaScript, ""
	policy.Selector = &codelibrary.AccountSelectorRevision{ID: "selector.fixture", Revision: 1, CollectionID: "routing", DisplayName: "Fixed-looking selector",
		Policy: accountselector.Policy{JavaScript: `selection.accountId = accounts[0].id;`}, PublishedAt: time.Now().UTC()}
	compiler, err := productionEnvironmentCompiler(f.runtime.accounts, f.runtime.endpoints)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := compiler.Compile(aggregate)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := snapshot.ResolveRequest(aggregate.ClientEndpoints[0].ClientOrigin, environment.RequestFacts{
		Target: protocolspec.RequestTarget{Method: "GET", Path: "/backend-api/wham/usage", Transport: protocolspec.ClientOperationTransportHTTP}, DownstreamProtocol: wireprofile.ApplicationProtocolHTTP1})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.reader.ReadCaptured(context.Background(), plan, accountoperation.Source{ConnectionID: "connection.fixture", OperationID: "operation.fixture"})
	if !errors.Is(err, environment.ErrAccountReadAmbiguous) || len(f.wire.requests) != 0 {
		t.Fatal("control query selected a dynamic account without a Turn")
	}
}

func TestCapturedAccountReadRechecksRevokedAssociation(t *testing.T) {
	f := newAccountReadFixture(t)
	_, err := f.runtime.accounts.SetAssociation(context.Background(), provideraccount.AssociationCommand{ID: f.account.Account.ID,
		EndpointID: upstreamendpoint.ChatGPTOfficialID, ExpectedRevision: f.account.Account.AssociationRevision, Linked: false})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.reader.ReadCaptured(context.Background(), f.plan, accountoperation.Source{ConnectionID: "connection.fixture", OperationID: "operation.fixture"})
	if !errors.Is(err, provideraccount.ErrEndpointMismatch) || len(f.wire.requests) != 0 {
		t.Fatal("revoked account link still authorized an account query")
	}
}

func TestOwnerCanReadUnlinkedAccountWithoutGivingCapturesPermission(t *testing.T) {
	f := newAccountReadFixture(t)
	_, err := f.runtime.accounts.SetAssociation(context.Background(), provideraccount.AssociationCommand{ID: f.account.Account.ID,
		EndpointID: upstreamendpoint.ChatGPTOfficialID, ExpectedRevision: f.account.Account.AssociationRevision, Linked: false})
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.reader.ReadOwned(context.Background(), f.account.Account.ID, upstreamservice.CodexRateLimits)
	if err != nil || result.Facts.AccountID != f.account.Account.ID.String() {
		t.Fatalf("owner cannot inspect independent account: %v", err)
	}
	_, err = f.reader.ReadCaptured(context.Background(), f.plan, accountoperation.Source{ConnectionID: "connection.fixture", OperationID: "operation.fixture"})
	if !errors.Is(err, provideraccount.ErrEndpointMismatch) || len(f.wire.requests) != 1 {
		t.Fatal("owner read granted new Capture authority")
	}
}

func TestCapturedAccountReadUsesANewCredentialEpochWithoutChangingAccount(t *testing.T) {
	f := newAccountReadFixture(t)
	material, err := providerauth.NewMaterial(f.token+"-rotated", nil, nil)
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
	_, err = f.runtime.accounts.ReplaceSecret(context.Background(), provideraccount.ReplaceSecretCommand{ID: f.account.Account.ID, ExpectedCredentialEpoch: 1, Secret: secret})
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.reader.ReadCaptured(context.Background(), f.plan, accountoperation.Source{ConnectionID: "connection.fixture", OperationID: "operation.fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Facts.AccountID != f.account.Account.ID.String() || result.Facts.CredentialEpoch != 2 || f.wire.requests[0].Header.Get("Authorization") != "Bearer "+f.token+"-rotated" {
		t.Fatal("query reused a stale credential or changed account identity")
	}
}

func TestCapturedHistoryRequiresItsOwnFrozenRoutePermission(t *testing.T) {
	f := newAccountReadFixture(t)
	compiler, err := productionEnvironmentCompiler(f.runtime.accounts, f.runtime.endpoints)
	if err != nil {
		t.Fatal(err)
	}
	for _, allowed := range []bool{false, true} {
		aggregate := f.aggregate.Clone()
		aggregate.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0].AllowAccountHistory = allowed
		snapshot, err := compiler.Compile(aggregate)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := snapshot.ResolveRequest(aggregate.ClientEndpoints[0].ClientOrigin, environment.RequestFacts{
			Target: protocolspec.RequestTarget{Method: "GET", Path: "/backend-api/wham/profiles/me", Transport: protocolspec.ClientOperationTransportHTTP}, DownstreamProtocol: wireprofile.ApplicationProtocolHTTP1})
		if err != nil {
			t.Fatal(err)
		}
		result, err := f.reader.ReadCaptured(context.Background(), plan, accountoperation.Source{ConnectionID: "connection.fixture", OperationID: "history.fixture"})
		if !allowed {
			if !errors.Is(err, upstreamservice.ErrHistoryDenied) || len(f.wire.requests) != 0 {
				t.Fatal("model route authority leaked account-wide history")
			}
		} else if err != nil || result.Facts.History == nil || result.Facts.History.LifetimeTokens == nil || *result.Facts.History.LifetimeTokens != 1200 {
			t.Fatalf("explicit history grant failed: %v", err)
		}
	}
}

func TestAccountReadCanceledDuringOfflineHoldDoesNotDialOrLeakLeases(t *testing.T) {
	f := newAccountReadFixture(t)
	gate := f.runtime.offlineHold
	if _, err := gate.Enter(context.Background(), gate.Snapshot().Revision); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := f.reader.ReadCaptured(ctx, f.plan, accountoperation.Source{ConnectionID: "connection.held", OperationID: "read.held"})
		done <- err
	}()
	deadline := time.After(time.Second)
	for gate.Snapshot().QueuedRequests != 1 {
		select {
		case <-deadline:
			t.Fatal("account read did not enter Hold")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
	if len(f.wire.requests) != 0 {
		t.Fatal("held account read reached provider")
	}
	state := gate.Snapshot()
	if state.QueuedRequests != 0 || state.ActiveActions != 0 || state.ActiveEgress != 0 {
		t.Fatal("account read leaked Hold leases")
	}
	if _, err := f.runtime.accounts.SetAssociation(context.Background(), provideraccount.AssociationCommand{ID: f.account.Account.ID,
		EndpointID: upstreamendpoint.ChatGPTOfficialID, ExpectedRevision: f.account.Account.AssociationRevision, Linked: false}); err != nil {
		t.Fatalf("canceled query leaked its account lease: %v", err)
	}
}

func TestParallelOwnerAndCapturedReadsCannotExchangeAccounts(t *testing.T) {
	f := newAccountReadFixture(t)
	tokenC := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"workspace-C"}}`)) + ".synthetic"
	material, err := providerauth.NewMaterial(tokenC, nil, nil)
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
	if _, err := f.runtime.accounts.Create(context.Background(), provideraccount.CreateCommand{
		ID: "managed-c", DisplayName: "Managed C", UpstreamEndpointID: upstreamendpoint.ChatGPTOfficialID,
		Driver: providerauth.StaticHeaderDriverRef(), Secret: secret,
	}); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 16)
	for index := range 16 {
		go func() {
			var result accountoperation.Result
			var err error
			expectedID, expectedWorkspace := "managed-b", "workspace-B"
			if index%2 == 0 {
				result, err = f.reader.ReadCaptured(context.Background(), f.plan,
					accountoperation.Source{ConnectionID: "connection.concurrent", OperationID: fmt.Sprintf("read.%d", index)})
			} else {
				expectedID, expectedWorkspace = "managed-c", "workspace-C"
				result, err = f.reader.ReadOwned(context.Background(), "managed-c", upstreamservice.CodexRateLimits)
			}
			if err == nil && (result.Facts.AccountID != expectedID || result.Facts.UpstreamAccountID != expectedWorkspace) {
				err = errors.New("account observation crossed scopes")
			}
			results <- err
		}()
	}
	for range 16 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	f.wire.mu.Lock()
	defer f.wire.mu.Unlock()
	if len(f.wire.requests) != 16 {
		t.Fatal("unexpected account query count")
	}
	for _, request := range f.wire.requests {
		token := f.token
		if request.Header.Get("Chatgpt-Account-Id") == "workspace-C" {
			token = tokenC
		}
		if request.Header.Get("Authorization") != "Bearer "+token {
			t.Fatal("credential crossed account scope")
		}
	}
}
