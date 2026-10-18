package exchange

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/providertransport"
)

// Codex's OnceLock keeps the first response token even when the proxy changes
// credentials. The third request must use B's state without exposing A to B.
func TestManagedCodexTurnStateTranslatesObservedAnchorAfterAccountSwitch(t *testing.T) {
	accounts := []testAccount{{id: "account.a", revision: 3, epoch: 7}, {id: "account.b", revision: 3, epoch: 7}}
	provider := &providerDouble{results: []providerResult{
		{response: codexTurnStateResponse("state-a")},
		{response: codexTurnStateResponse("state-b")},
		{response: codexTurnStateResponse("")},
	}}
	pipeline := newTestPipeline(t, newAccountAuthority(t, accounts...), provider, approvedDecisions(), &attemptObserverDouble{})
	defer shutdownPipeline(t, pipeline)
	for index, account := range []string{"account.a", "account.b", "account.b"} {
		headers := codexTurnStateHeaders()
		if index > 0 {
			headers.Set(codexTurnStateHeader, "state-a")
		}
		request := codexTurnStateRequest(t, fmt.Sprintf("anchor-%d", index), accounts, account, headers, "capture-state", "turn-1", "codex-client-alias")
		if _, err := pipeline.Execute(context.Background(), request, &downstreamRecorder{}); err != nil {
			t.Fatal(err)
		}
	}
	for index, want := range []string{"", "", "state-b"} {
		if got := provider.requestsSnapshot()[index].Headers().Get(codexTurnStateHeader); got != want {
			t.Errorf("request %d routing state = %q, want %q", index+1, got, want)
		}
	}
}

func TestManagedCodexTurnStateWithoutTurnOnlyReplaysSameOwner(t *testing.T) {
	for _, switched := range []bool{false, true} {
		t.Run(fmt.Sprintf("switched=%v", switched), func(t *testing.T) {
			accounts := []testAccount{{id: "account.a", revision: 3, epoch: 7}, {id: "account.b", revision: 3, epoch: 7}}
			provider := &providerDouble{results: []providerResult{{response: codexTurnStateResponse("state-a")}, {response: codexTurnStateResponse("state-b")}, {response: codexTurnStateResponse("")}}}
			pipeline := newTestPipeline(t, newAccountAuthority(t, accounts...), provider, approvedDecisions(), &attemptObserverDouble{})
			defer shutdownPipeline(t, pipeline)
			for index := range 3 {
				account := "account.a"
				if switched && index > 0 {
					account = "account.b"
				}
				headers := codexTurnStateHeaders()
				if index > 0 {
					headers.Set(codexTurnStateHeader, "state-a")
				}
				request := codexTurnStateRequest(t, fmt.Sprintf("missing-turn-%d", index), accounts, account, headers, "capture-state", "", "codex-client-alias")
				if _, err := pipeline.Execute(context.Background(), request, &downstreamRecorder{}); err != nil {
					t.Fatal(err)
				}
			}
			want := "state-a"
			if switched {
				want = ""
			}
			if got := provider.requestsSnapshot()[2].Headers().Get(codexTurnStateHeader); got != want {
				t.Fatalf("missing turn identity: routing token=%q want=%q", got, want)
			}
		})
	}
}

func TestManagedCodexTurnStateAnchorNeverCrossesScopeOrOwner(t *testing.T) {
	for _, change := range []string{"unknown", "capture", "session", "thread", "turn", "model", "epoch", "hop", "expired"} {
		t.Run(change, func(t *testing.T) {
			accounts := []testAccount{{id: "account.a", revision: 3, epoch: 7}, {id: "account.b", revision: 3, epoch: 7}}
			provider := &providerDouble{results: []providerResult{{response: codexTurnStateResponse("state-a")}, {response: codexTurnStateResponse("state-b")}, {response: codexTurnStateResponse("")}}}
			authority := newAccountAuthority(t, accounts...)
			pipeline := newTestPipeline(t, authority, provider, approvedDecisions(), &attemptObserverDouble{})
			defer shutdownPipeline(t, pipeline)
			headers := codexTurnStateHeaders()
			send := func(id, account, capture, turn, model string) {
				t.Helper()
				if _, err := pipeline.Execute(context.Background(), codexTurnStateRequest(t, id, accounts, account, headers, capture, turn, model), &downstreamRecorder{}); err != nil {
					t.Fatal(err)
				}
			}
			send("seed-a", "account.a", "capture-state", "turn-1", "codex-client-alias")
			headers.Set(codexTurnStateHeader, "state-a")
			send("seed-b", "account.b", "capture-state", "turn-1", "codex-client-alias")
			capture, turn, model := "capture-state", "turn-1", "codex-client-alias"
			switch change {
			case "unknown":
				headers.Set(codexTurnStateHeader, "never-observed")
			case "capture":
				capture = "other-capture"
			case "session":
				headers.Set("Session-Id", "other-session")
			case "thread":
				headers.Set("Thread-Id", "other-thread")
			case "turn":
				turn = "turn-2"
			case "model":
				model = "other-model"
			case "epoch":
				accounts[1].epoch++
				authority.accounts[accounts[1].id] = accounts[1]
			case "hop":
				headers.Set("Connection", "x-codex-turn-state")
			case "expired":
				now := pipeline.now().Add(25 * time.Hour)
				pipeline.now = func() time.Time { return now }
			}
			send("isolated", "account.b", capture, turn, model)
			if got := provider.requestsSnapshot()[2].Headers().Get(codexTurnStateHeader); got != "" {
				t.Fatalf("%s reused another scope/owner's state", change)
			}
		})
	}
}

func TestManagedCodexTurnStateLateOldResponseCannotReplaceSwitchedOwner(t *testing.T) {
	accounts := []testAccount{{id: "account.a", revision: 3, epoch: 7}, {id: "account.b", revision: 3, epoch: 7}}
	initial := &providerDouble{results: []providerResult{{response: codexTurnStateResponse("state-a")}}}
	pipeline := newTestPipeline(t, newAccountAuthority(t, accounts...), initial, approvedDecisions(), &attemptObserverDouble{})
	defer shutdownPipeline(t, pipeline)
	headers := codexTurnStateHeaders()
	request := func(id, account string) ClientRequest {
		return codexTurnStateRequest(t, id, accounts, account, headers, "capture-state", "turn-1", "codex-client-alias")
	}
	if _, err := pipeline.Execute(context.Background(), request("seed", "account.a"), &downstreamRecorder{}); err != nil {
		t.Fatal(err)
	}
	headers.Set(codexTurnStateHeader, "state-a")
	oldStarted, releaseOld := make(chan struct{}), make(chan struct{})
	observed := make(chan providertransport.Request, 3)
	pipeline.provider = codexTurnStateProviderFunc(func(ctx context.Context, outgoing providertransport.Request) (*http.Response, providertransport.Evidence, error) {
		observed <- outgoing
		if outgoing.ExchangeID() == "old-in-flight" {
			close(oldStarted)
			select {
			case <-releaseOld:
			case <-ctx.Done():
				return nil, providertransport.Evidence{}, context.Cause(ctx)
			}
			return codexTurnStateResponse("late-state-a"), providertransport.Evidence{}, nil
		}
		return codexTurnStateResponse("state-b"), providertransport.Evidence{}, nil
	})
	oldDone := make(chan error, 1)
	oldRequest := request("old-in-flight", "account.a")
	go func() {
		_, err := pipeline.Execute(context.Background(), oldRequest, &downstreamRecorder{})
		oldDone <- err
	}()
	<-oldStarted
	if _, err := pipeline.Execute(context.Background(), request("switch-b", "account.b"), &downstreamRecorder{}); err != nil {
		t.Fatal(err)
	}
	close(releaseOld)
	if err := <-oldDone; err != nil {
		t.Fatal(err)
	}
	if _, err := pipeline.Execute(context.Background(), request("after-late-a", "account.b"), &downstreamRecorder{}); err != nil {
		t.Fatal(err)
	}
	for index, want := range []string{"state-a", "", "state-b"} {
		if got := (<-observed).Headers().Get(codexTurnStateHeader); got != want {
			t.Fatalf("request %d routing state differs after late response: got %q, want %q", index+1, got, want)
		}
	}
}

func TestManagedCodexTurnStateCancellationDiscardsRetainedState(t *testing.T) {
	accounts := []testAccount{{id: "account.a", revision: 3, epoch: 7}}
	initial := &providerDouble{results: []providerResult{{response: codexTurnStateResponse("state-a")}}}
	pipeline := newTestPipeline(t, newAccountAuthority(t, accounts...), initial, approvedDecisions(), &attemptObserverDouble{})
	defer shutdownPipeline(t, pipeline)
	headers := codexTurnStateHeaders()
	request := func(id string) ClientRequest {
		return codexTurnStateRequest(t, id, accounts, "account.a", headers, "capture-state", "turn-1", "codex-client-alias")
	}
	if _, err := pipeline.Execute(context.Background(), request("seed"), &downstreamRecorder{}); err != nil {
		t.Fatal(err)
	}
	retained := retainedCodexTurnStateBytes(t, pipeline)
	headers.Set(codexTurnStateHeader, "state-a")
	blocked := &blockingProvider{started: make(chan struct{})}
	pipeline.provider = blocked
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	cancelRequest := request("canceled")
	go func() {
		_, err := pipeline.Execute(ctx, cancelRequest, &downstreamRecorder{})
		done <- err
	}()
	<-blocked.started
	cancel()
	if err := <-done; err == nil {
		t.Fatal("canceled request succeeded")
	}
	assertCodexTurnBytesCleared(t, retained)
	final := &providerDouble{results: []providerResult{{response: codexTurnStateResponse("")}}}
	pipeline.provider = final
	if _, err := pipeline.Execute(context.Background(), request("after-cancel"), &downstreamRecorder{}); err != nil {
		t.Fatal(err)
	}
	if final.requestsSnapshot()[0].Headers().Get(codexTurnStateHeader) != "" {
		t.Fatal("canceled operation left replayable state")
	}
}

func TestManagedCodexTurnStateLateResponseCannotReviveEarlierGenerationOfSameAccount(t *testing.T) {
	accounts := []testAccount{{id: "account.a", revision: 3, epoch: 7}, {id: "account.b", revision: 3, epoch: 7}}
	initial := &providerDouble{results: []providerResult{{response: codexTurnStateResponse("anchor-a")}}}
	pipeline := newTestPipeline(t, newAccountAuthority(t, accounts...), initial, approvedDecisions(), &attemptObserverDouble{})
	defer shutdownPipeline(t, pipeline)
	headers := codexTurnStateHeaders()
	request := func(id, account string) ClientRequest {
		return codexTurnStateRequest(t, id, accounts, account, headers, "capture-state", "turn-1", "codex-client-alias")
	}
	if _, err := pipeline.Execute(context.Background(), request("seed", "account.a"), &downstreamRecorder{}); err != nil {
		t.Fatal(err)
	}
	headers.Set(codexTurnStateHeader, "anchor-a")
	started, release := make(chan struct{}), make(chan struct{})
	observed := make(chan providertransport.Request, 4)
	pipeline.provider = codexTurnStateProviderFunc(func(ctx context.Context, outgoing providertransport.Request) (*http.Response, providertransport.Evidence, error) {
		observed <- outgoing
		switch outgoing.ExchangeID() {
		case "old-a":
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, providertransport.Evidence{}, context.Cause(ctx)
			}
			return codexTurnStateResponse("obsolete-a"), providertransport.Evidence{}, nil
		case "switch-b":
			return codexTurnStateResponse("current-b"), providertransport.Evidence{}, nil
		default:
			return codexTurnStateResponse("fresh-a"), providertransport.Evidence{}, nil
		}
	})
	done := make(chan error, 1)
	old := request("old-a", "account.a")
	go func() {
		_, err := pipeline.Execute(context.Background(), old, &downstreamRecorder{})
		done <- err
	}()
	<-started
	for _, step := range []struct{ id, account string }{{"switch-b", "account.b"}, {"switch-back-a", "account.a"}} {
		if _, err := pipeline.Execute(context.Background(), request(step.id, step.account), &downstreamRecorder{}); err != nil {
			t.Fatal(err)
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := pipeline.Execute(context.Background(), request("continue-a", "account.a"), &downstreamRecorder{}); err != nil {
		t.Fatal(err)
	}
	for index, want := range []string{"anchor-a", "", "", "fresh-a"} {
		if got := (<-observed).Headers().Get(codexTurnStateHeader); got != want {
			t.Fatalf("request %d reused an obsolete ownership generation: got %q, want %q", index+1, got, want)
		}
	}
}

func TestManagedCodexTurnStateOwnedBytesAreCleared(t *testing.T) {
	for _, boundary := range []string{"account", "epoch", "expiry", "shutdown", "owner cancellation"} {
		t.Run(boundary, func(t *testing.T) {
			accounts := []testAccount{{id: "account.a", revision: 3, epoch: 7}, {id: "account.b", revision: 3, epoch: 7}}
			provider := &providerDouble{results: []providerResult{{response: codexTurnStateResponse("secret-routing-token")}, {response: codexTurnStateResponse("")}}}
			authority := newAccountAuthority(t, accounts...)
			pipeline := newTestPipeline(t, authority, provider, approvedDecisions(), &attemptObserverDouble{})
			defer shutdownPipeline(t, pipeline)
			headers := codexTurnStateHeaders()
			if _, err := pipeline.Execute(context.Background(), codexTurnStateRequest(t, "seed", accounts, "account.a", headers, "capture-state", "turn-1", "codex-client-alias"), &downstreamRecorder{}); err != nil {
				t.Fatal(err)
			}
			retained := retainedCodexTurnStateBytes(t, pipeline)
			account := "account.a"
			switch boundary {
			case "account":
				account = "account.b"
			case "epoch":
				accounts[0].epoch++
				authority.accounts[accounts[0].id] = accounts[0]
			case "expiry":
				now := pipeline.now().Add(25 * time.Hour)
				pipeline.now = func() time.Time { return now }
			case "shutdown":
				pipeline.BeginShutdown()
			case "owner cancellation":
				pipeline.cancelOwner(context.Canceled)
				deadline := time.After(time.Second)
				for {
					pipeline.mu.Lock()
					empty := len(pipeline.turnStates) == 0
					pipeline.mu.Unlock()
					if empty {
						break
					}
					select {
					case <-deadline:
						t.Fatal("owner cancellation retained routing state")
					case <-time.After(time.Millisecond):
					}
				}
			}
			if boundary == "account" || boundary == "epoch" || boundary == "expiry" {
				headers.Set(codexTurnStateHeader, "secret-routing-token")
				if _, err := pipeline.Execute(context.Background(), codexTurnStateRequest(t, "cleanup", accounts, account, headers, "capture-state", "turn-1", "codex-client-alias"), &downstreamRecorder{}); err != nil {
					t.Fatal(err)
				}
			}
			assertCodexTurnBytesCleared(t, retained)
			pipeline.mu.Lock()
			pending := len(pipeline.turnStateRequests)
			pipeline.mu.Unlock()
			if pending != 0 {
				t.Fatal("finished operations retained admission witnesses")
			}
		})
	}
}

func TestManagedCodexTurnStateEvictionClearsBytesAndRejectsEvictedAnchor(t *testing.T) {
	accounts := []testAccount{{id: "account.a", revision: 3, epoch: 7}}
	provider := &providerDouble{}
	for range maxCodexTurnStates + 2 {
		provider.results = append(provider.results, providerResult{response: codexTurnStateResponse("state-a")})
	}
	pipeline := newTestPipeline(t, newAccountAuthority(t, accounts...), provider, approvedDecisions(), &attemptObserverDouble{})
	defer shutdownPipeline(t, pipeline)
	headers := codexTurnStateHeaders()
	var retained []byte
	for index := range maxCodexTurnStates + 1 {
		request := codexTurnStateRequest(t, fmt.Sprintf("capacity-%d", index), accounts, "account.a", headers, fmt.Sprintf("capture-%d", index), "turn-1", "codex-client-alias")
		if _, err := pipeline.Execute(context.Background(), request, &downstreamRecorder{}); err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			retained = retainedCodexTurnStateBytes(t, pipeline)
		}
	}
	assertCodexTurnBytesCleared(t, retained)
	if len(pipeline.turnStates) > maxCodexTurnStates {
		t.Fatal("routing state exceeded its capacity")
	}
	headers.Set(codexTurnStateHeader, "state-a")
	request := codexTurnStateRequest(t, "evicted-anchor", accounts, "account.a", headers, "capture-0", "turn-1", "codex-client-alias")
	if _, err := pipeline.Execute(context.Background(), request, &downstreamRecorder{}); err != nil {
		t.Fatal(err)
	}
	if provider.requestsSnapshot()[maxCodexTurnStates+1].Headers().Get(codexTurnStateHeader) != "" {
		t.Fatal("evicted anchor inherited another capture's identically named token")
	}
}

func TestCodexTurnStateAdmissionWitnessesAreBoundedAndReleased(t *testing.T) {
	accounts := []testAccount{{id: "account.a", revision: 3, epoch: 7}}
	pipeline := newTestPipeline(t, newAccountAuthority(t, accounts...), &providerDouble{}, approvedDecisions(), &attemptObserverDouble{})
	defer shutdownPipeline(t, pipeline)
	pipeline.mu.Lock()
	for index := range maxCodexTurnRequests {
		if pipeline.beginCodexTurnRequestLocked(fmt.Sprintf("pending-%d", index), context.Background(), context.Background()) == nil {
			pipeline.mu.Unlock()
			t.Fatal("witness admission failed below capacity")
		}
	}
	if pipeline.beginCodexTurnRequestLocked("overflow", context.Background(), context.Background()) != nil {
		pipeline.mu.Unlock()
		t.Fatal("witness admission exceeded capacity")
	}
	for _, witness := range pipeline.turnStateRequests {
		pipeline.finishCodexTurnRequestLocked(witness)
	}
	if len(pipeline.turnStateRequests) != 0 {
		pipeline.mu.Unlock()
		t.Fatal("finished witnesses were retained")
	}
	pipeline.mu.Unlock()
}

func TestManagedCodexTurnStateUnknownClientTokenNeverBecomesAnAlias(t *testing.T) {
	accounts := []testAccount{{id: "account.a", revision: 3, epoch: 7}}
	provider := &providerDouble{results: []providerResult{{response: codexTurnStateResponse("observed-state")}, {response: codexTurnStateResponse("")}, {response: codexTurnStateResponse("")}}}
	pipeline := newTestPipeline(t, newAccountAuthority(t, accounts...), provider, approvedDecisions(), &attemptObserverDouble{})
	defer shutdownPipeline(t, pipeline)
	for index, token := range []string{"foreign-client-state", "foreign-client-state", "observed-state"} {
		headers := codexTurnStateHeaders()
		headers.Set(codexTurnStateHeader, token)
		request := codexTurnStateRequest(t, fmt.Sprintf("untrusted-%d", index), accounts, "account.a", headers, "capture-state", "turn-1", "codex-client-alias")
		if _, err := pipeline.Execute(context.Background(), request, &downstreamRecorder{}); err != nil {
			t.Fatal(err)
		}
	}
	for index, want := range []string{"", "", "observed-state"} {
		if got := provider.requestsSnapshot()[index].Headers().Get(codexTurnStateHeader); got != want {
			t.Fatalf("request %d accepted an unobserved client anchor: got %q, want %q", index+1, got, want)
		}
	}
}

func TestManagedCodexTurnStateBoundsAliasesWithoutLosingFirstAnchor(t *testing.T) {
	accounts := []testAccount{{id: "account.a", revision: 3, epoch: 7}, {id: "account.b", revision: 3, epoch: 7}}
	provider := &providerDouble{}
	for index := range maxCodexTurnAliases + 4 {
		provider.results = append(provider.results, providerResult{response: codexTurnStateResponse(fmt.Sprintf("state-%d", index))})
	}
	pipeline := newTestPipeline(t, newAccountAuthority(t, accounts...), provider, approvedDecisions(), &attemptObserverDouble{})
	defer shutdownPipeline(t, pipeline)
	headers := codexTurnStateHeaders()
	var account string
	for index := range maxCodexTurnAliases + 3 {
		account = accounts[index%2].id
		if index > 0 {
			headers.Set(codexTurnStateHeader, "state-0")
		}
		request := codexTurnStateRequest(t, fmt.Sprintf("alias-%d", index), accounts, account, headers, "capture-state", "turn-1", "codex-client-alias")
		if _, err := pipeline.Execute(context.Background(), request, &downstreamRecorder{}); err != nil {
			t.Fatal(err)
		}
	}
	request := codexTurnStateRequest(t, "continue-last-owner", accounts, account, headers, "capture-state", "turn-1", "codex-client-alias")
	if _, err := pipeline.Execute(context.Background(), request, &downstreamRecorder{}); err != nil {
		t.Fatal(err)
	}
	if got, want := provider.requestsSnapshot()[maxCodexTurnAliases+3].Headers().Get(codexTurnStateHeader), fmt.Sprintf("state-%d", maxCodexTurnAliases+2); got != want {
		t.Fatalf("bounded aliases lost first anchor: got %q, want %q", got, want)
	}
	for _, state := range pipeline.turnStates {
		if len(state.aliases) > maxCodexTurnAliases {
			t.Fatal("routing token aliases exceeded capacity")
		}
	}
}

func retainedCodexTurnStateBytes(t *testing.T, pipeline *Pipeline) []byte {
	t.Helper()
	pipeline.mu.Lock()
	defer pipeline.mu.Unlock()
	for _, state := range pipeline.turnStates {
		if len(state.token) != 0 {
			return state.token
		}
	}
	t.Fatal("accepted response did not retain scoped routing state")
	return nil
}

func assertCodexTurnBytesCleared(t *testing.T, retained []byte) {
	t.Helper()
	if !bytes.Equal(retained, make([]byte, len(retained))) {
		t.Fatal("retired routing token bytes were not cleared")
	}
}

type codexTurnStateProviderFunc func(context.Context, providertransport.Request) (*http.Response, providertransport.Evidence, error)

func (provider codexTurnStateProviderFunc) Do(ctx context.Context, request providertransport.Request) (*http.Response, providertransport.Evidence, error) {
	return provider(ctx, request)
}

func codexTurnStateResponse(token string) *http.Response {
	response := jsonResponse(http.StatusOK, completeResponsesProviderResponse("codex-client-alias"))
	if token != "" {
		response.Header.Set(codexTurnStateHeader, token)
	}
	return response
}

func codexTurnStateHeaders() http.Header {
	headers := chatGPTClientHeaderFixture()
	headers.Del(codexTurnStateHeader)
	return headers
}

func codexTurnStateRequest(t *testing.T, id string, accounts []testAccount, account string, headers http.Header, capture, turn, model string) ClientRequest {
	t.Helper()
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		clientProtocol: environment.ClientProtocolOpenAIResponses, chatGPTClient: true,
		destination: environment.DestinationKindUpstream, providerOrigin: "https://chatgpt.com",
		backend: protocolspec.DialectOpenAIResponses, modelMode: environment.ModelModePassthrough,
		accounts: accounts, preferred: account,
	})
	admission, err := captureadmission.NewManual(capture, 1, "Codex continuity fixture")
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(fmt.Sprintf(`{"model":%q,"stream":false,"client_metadata":{"turn_id":%q},"input":"hello"}`, model, turn))
	if turn == "" {
		body = []byte(fmt.Sprintf(`{"model":%q,"stream":false,"input":"hello"}`, model))
	}
	return mustClientRequestWithOptions(t, id, plan, body, WithOriginalHeaders(headers), WithIngressCorrelation(admission, "connection-state"))
}
