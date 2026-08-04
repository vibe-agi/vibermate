package productruntime

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/providertransport"
)

// failThenAnswerProvider drops the first attempt and answers the second, which
// is what one of two upstreams being down looks like from here.
type failThenAnswerProvider struct {
	mu      sync.Mutex
	calls   int
	targets []string
}

func (provider *failThenAnswerProvider) Do(
	_ context.Context,
	request providertransport.Request,
) (*http.Response, providertransport.Evidence, error) {
	provider.mu.Lock()
	provider.calls++
	call := provider.calls
	provider.targets = append(provider.targets, request.TargetRef())
	provider.mu.Unlock()
	if call == 1 {
		return nil, providertransport.Evidence{},
			errors.New("upstream connection dropped")
	}
	body := `{"id":"chatcmpl-1","object":"chat.completion","created":1,` +
		`"model":"gpt-4.1-mini","choices":[{"index":0,"finish_reason":"stop",` +
		`"message":{"role":"assistant","content":"second candidate"}}],` +
		`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}, providertransport.Evidence{}, nil
}

func (provider *failThenAnswerProvider) Shutdown(context.Context) error {
	return nil
}

func (provider *failThenAnswerProvider) seen() []string {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return append([]string(nil), provider.targets...)
}

func failoverRuntime(
	t *testing.T,
	provider *failThenAnswerProvider,
) *Runtime {
	t.Helper()

	builders := productionBuilders()
	builders.provider = fixedProviderBuilder{component: provider}
	runtime, err := startWithBuilders(
		context.Background(),
		testOptions(t, hostcontract.Desktop(), &coordinatorDouble{}),
		builders,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownRuntime(t, runtime) })
	return runtime
}

func failoverRequest(
	t *testing.T,
	runtime *Runtime,
	accessID access.AccessID,
	exchangeID string,
) exchange.ClientRequest {
	t.Helper()

	activePlan, err := runtime.SnapshotResolver().ResolveAccess(accessID)
	if err != nil {
		t.Fatal(err)
	}
	request, err := exchange.NewClientRequest(
		exchangeID,
		activePlan.IngressBinding(),
		runtimeAnthropicOperationEvidence(t),
		[]byte(`{
			"model":"claude-client-alias",
			"max_tokens":32,
			"messages":[{"role":"user","content":"hello"}]
		}`),
		exchange.ReplayGenerationCostOnly,
		access.ApplicationProtocolHTTP1,
	)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

// A RouteSet with a second candidate is what the policy is for: the first
// upstream drops before answering, nothing has reached the client, and the
// request is one that may be sent again — so the second candidate answers it.
func TestAFailedCandidateFallsBackToTheNextOne(t *testing.T) {
	t.Parallel()

	accessID, err := access.NewAccessID("access-failover")
	if err != nil {
		t.Fatal(err)
	}
	provider := &failThenAnswerProvider{}
	runtime := failoverRuntime(t, provider)
	if write, err := runtime.AccessWriter().WriteAccess(
		context.Background(),
		access.WriteCommand{
			ExpectedRevision: 0,
			Aggregate:        twoCandidateAggregate(t, accessID),
		},
	); err != nil || write.Outcome != access.WriteOutcomeCommitted {
		t.Fatalf("write Access result=%+v err=%v", write, err)
	}
	downstream := &runtimeDownstream{}
	result, err := runtime.ExchangeExecutor().Execute(
		context.Background(),
		failoverRequest(t, runtime, accessID, "exchange-failover"),
		downstream,
	)
	if err != nil {
		t.Fatalf("a dropped first candidate was not recovered: %v", err)
	}
	if result.Outcome != exchange.AttemptSucceeded {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(downstream.body.String(), "second candidate") {
		t.Fatalf("the client got %s", downstream.body.Bytes())
	}
	seen := provider.seen()
	if len(seen) != 2 {
		t.Fatalf("attempts = %v", seen)
	}
	// The second attempt went somewhere else. A fallback onto the same target
	// would be a retry wearing a different name.
	if seen[0] == seen[1] {
		t.Fatalf("both attempts used %q", seen[0])
	}
}

// The same failure with the policy left alone is reported rather than retried.
// Nothing about the transport changed; the permission did.
func TestWithoutThePolicyAFailedCandidateIsReported(t *testing.T) {
	t.Parallel()

	accessID, err := access.NewAccessID("access-no-failover")
	if err != nil {
		t.Fatal(err)
	}
	provider := &failThenAnswerProvider{}
	runtime := failoverRuntime(t, provider)
	aggregate := twoCandidateAggregate(t, accessID)
	aggregate.RouteSets[0].Fallback = access.FallbackDisabled
	if write, err := runtime.AccessWriter().WriteAccess(
		context.Background(),
		access.WriteCommand{ExpectedRevision: 0, Aggregate: aggregate},
	); err != nil || write.Outcome != access.WriteOutcomeCommitted {
		t.Fatalf("write Access result=%+v err=%v", write, err)
	}
	if _, err := runtime.ExchangeExecutor().Execute(
		context.Background(),
		failoverRequest(t, runtime, accessID, "exchange-no-failover"),
		&runtimeDownstream{},
	); err == nil {
		t.Fatal("a dropped attempt reported success without a policy")
	}
	if seen := provider.seen(); len(seen) != 1 {
		t.Fatalf("attempts without a policy = %v", seen)
	}
}

// twoCandidateAggregate is one Access with two upstream candidates and the
// policy that permits trying the second.
func twoCandidateAggregate(
	t *testing.T,
	accessID access.AccessID,
) access.Aggregate {
	t.Helper()

	aggregate := runtimeAccessAggregate(t, accessID, 1, "Failover")
	primary := aggregate.Profiles[0]
	secondaryProfileID, err := access.NewEndpointProfileID(
		accessID.String() + "-profile-secondary",
	)
	if err != nil {
		t.Fatal(err)
	}
	secondaryTargetID, err := access.NewProviderTargetID(
		accessID.String() + "-target-secondary",
	)
	if err != nil {
		t.Fatal(err)
	}
	secondaryAccountID, err := access.NewAccountBindingID(
		accessID.String() + "-account-secondary",
	)
	if err != nil {
		t.Fatal(err)
	}
	secondaryOrigin, err := access.NewProviderOrigin(
		"https://api.secondary.example:443/v1",
	)
	if err != nil {
		t.Fatal(err)
	}
	secondarySecret, err := access.NewSecretRef(
		"secret://provider/" + accessID.String() + "-secondary",
	)
	if err != nil {
		t.Fatal(err)
	}

	secondaryProfile := primary
	secondaryProfile.ID = secondaryProfileID
	secondaryProfile.Name = "Secondary"
	secondaryProfile.TargetID = secondaryTargetID
	secondaryProfile.AccountBindingIDs = []access.AccountBindingID{
		secondaryAccountID,
	}
	secondaryProfile.DefaultAccountBindingID = secondaryAccountID
	aggregate.Profiles = append(aggregate.Profiles, secondaryProfile)

	secondaryTarget := aggregate.ProviderTargets[0]
	secondaryTarget.ID = secondaryTargetID
	secondaryTarget.ProfileID = secondaryProfileID
	secondaryTarget.Origin = secondaryOrigin
	aggregate.ProviderTargets = append(
		aggregate.ProviderTargets,
		secondaryTarget,
	)

	secondaryAccount := aggregate.AccountBindings[0]
	secondaryAccount.ID = secondaryAccountID
	secondaryAccount.ProfileID = secondaryProfileID
	secondaryAccount.SecretRef = secondarySecret
	aggregate.AccountBindings = append(
		aggregate.AccountBindings,
		secondaryAccount,
	)

	aggregate.Binding.ProfileIDs = append(
		aggregate.Binding.ProfileIDs,
		secondaryProfileID,
	)
	aggregate.RouteSets[0].CandidateProfileIDs = []access.EndpointProfileID{
		primary.ID,
		secondaryProfileID,
	}
	aggregate.RouteSets[0].Fallback =
		access.FallbackPreFirstByteIdempotentOnly
	return aggregate
}
