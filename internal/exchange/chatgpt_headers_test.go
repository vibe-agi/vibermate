package exchange

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/messagetransform"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

func TestManagedCodexTurnStateOwnershipAndResponseMetadata(t *testing.T) {
	for _, change := range []string{"same", "account", "credential epoch", "route", "capture", "session", "turn", "expired", "unknown", "hop header"} {
		t.Run(change, func(t *testing.T) {
			account := testAccount{id: "account.selected", revision: 3, epoch: 7}
			other := testAccount{id: "account.other", revision: 3, epoch: 7}
			routeRevision := environment.Revision(6)
			planFor := func(preferred string) environment.RequestPlan {
				return mustEnvironmentRequestPlan(t, testPlanOptions{
					clientProtocol: environment.ClientProtocolOpenAIResponses, chatGPTClient: true,
					destination: environment.DestinationKindUpstream, providerOrigin: "https://chatgpt.com",
					backend: protocolspec.DialectOpenAIResponses, modelMode: environment.ModelModePassthrough,
					accounts: []testAccount{account, other}, preferred: preferred,
					routeRevision: routeRevision,
				})
			}
			first := jsonResponse(http.StatusOK, completeResponsesProviderResponse("codex-client-alias"))
			first.Header.Set(codexTurnStateHeader, "opaque-same-turn")
			first.Header.Set("X-Request-Id", "request-1")
			first.Header.Set("X-Codex-Primary-Used-Percent", "42")
			first.Header.Set("Set-Cookie", "secret-cookie")
			first.Header.Set("X-Private", "private")
			provider := &providerDouble{results: []providerResult{
				{response: first}, {response: jsonResponse(http.StatusOK, completeResponsesProviderResponse("codex-client-alias"))},
			}}
			authority := newAccountAuthority(t, account, other)
			pipeline := newTestPipeline(t, authority, provider, approvedDecisions(), &attemptObserverDouble{})
			defer shutdownPipeline(t, pipeline)
			now := pipeline.now()
			pipeline.now = func() time.Time { return now }
			headers := chatGPTClientHeaderFixture()
			capture := "capture-native-state"
			turn := "turn-1"
			send := func(id string, plan environment.RequestPlan) *downstreamRecorder {
				admission, err := captureadmission.NewManual(capture, 1, "Codex fixture")
				if err != nil {
					t.Fatal(err)
				}
				body := []byte(`{"model":"codex-client-alias","stream":false,"client_metadata":{"turn_id":"` + turn + `"},"input":[{"type":"message","role":"user","content":"hello"}]}`)
				downstream := &downstreamRecorder{}
				_, err = pipeline.Execute(context.Background(), mustClientRequestWithOptions(t, id, plan, body,
					WithOriginalHeaders(headers), WithIngressCorrelation(admission, "connection-state")), downstream)
				if err != nil {
					t.Fatal(err)
				}
				return downstream
			}
			downstream := send("first-state", planFor(account.id))
			responseHeaders := downstream.envelopesSnapshot()[0].Headers()
			for _, name := range []string{codexTurnStateHeader, "X-Request-Id", "X-Codex-Primary-Used-Percent"} {
				if responseHeaders.Get(name) != first.Header.Get(name) {
					t.Fatalf("lost %s: %v", name, responseHeaders)
				}
			}
			for _, name := range []string{"Set-Cookie", "X-Private"} {
				if responseHeaders.Get(name) != "" {
					t.Fatalf("leaked %s", name)
				}
			}
			headers.Set(codexTurnStateHeader, "opaque-same-turn")
			preferred := account.id
			switch change {
			case "account":
				preferred = other.id
			case "credential epoch":
				account.epoch++
				authority.accounts[account.id] = account
			case "route":
				routeRevision++
			case "capture":
				capture = "other-capture"
			case "session":
				headers.Set("Session-Id", "another-session")
			case "turn":
				turn = "turn-2"
			case "expired":
				now = now.Add(25 * time.Hour)
			case "unknown":
				headers.Set(codexTurnStateHeader, "not-returned-by-runtime")
			case "hop header":
				headers.Set("Connection", "x-codex-turn-state")
			}
			send("next-state", planFor(preferred))
			got := provider.requestsSnapshot()[1].Headers().Get(codexTurnStateHeader)
			if (got == "opaque-same-turn") != (change == "same") {
				t.Fatalf("turn state crossed %s: %q", change, got)
			}
		})
	}
}

func TestNativeStreamMetadataBlocksRetryAfterHeaderCommit(t *testing.T) {
	for _, metadata := range []bool{false, true} {
		t.Run(map[bool]string{false: "generic hold", true: "native metadata"}[metadata], func(t *testing.T) {
			account := testAccount{id: "account.selected", revision: 3, epoch: 7}
			plan := mustEnvironmentRequestPlan(t, testPlanOptions{
				clientProtocol: environment.ClientProtocolOpenAIResponses, chatGPTClient: true,
				destination: environment.DestinationKindUpstream, providerOrigin: "https://chatgpt.com",
				backend: protocolspec.DialectOpenAIResponses, modelMode: environment.ModelModePassthrough,
				accounts: []testAccount{account}, preferred: account.id,
			})
			first := streamResponse(200, nil)
			first.Body = io.NopCloser(iotest.ErrReader(io.ErrUnexpectedEOF))
			if metadata {
				first.Header.Set("X-Request-Id", "first-attempt")
			}
			encoded, err := json.Marshal(map[string]any{"type": "response.completed", "response": json.RawMessage(completeResponsesProviderResponse("codex-client-alias"))})
			if err != nil {
				t.Fatal(err)
			}
			terminal := "data: " + string(encoded) + "\n\n"
			provider := &providerDouble{results: []providerResult{{response: first}, {response: streamResponse(200, strings.NewReader(terminal))}}}
			pipeline := newTestPipeline(t, newAccountAuthority(t, account), provider, approvedDecisions(), &attemptObserverDouble{})
			defer shutdownPipeline(t, pipeline)
			pipeline.hold.MaxTransportResends = 1
			pipeline.hold.AllowResendAfterProviderResponse = true
			downstream := &downstreamRecorder{}
			_, err = pipeline.Execute(context.Background(), mustClientRequest(t, "retry-native", plan,
				[]byte(`{"model":"codex-client-alias","stream":true,"input":[{"type":"message","role":"user","content":"hello"}]}`)), downstream)
			if metadata {
				if err == nil || provider.callCount() != 1 || downstream.envelopesSnapshot()[0].Headers().Get("X-Request-Id") != "first-attempt" {
					t.Fatalf("committed metadata retried: calls=%d err=%v", provider.callCount(), err)
				}
			} else if err != nil || provider.callCount() != 2 {
				t.Fatalf("uncommitted attempt could not retry: calls=%d err=%v", provider.callCount(), err)
			}
		})
	}
}

func TestNativeErrorDoesNotDependOnDiagnosticFieldTypes(t *testing.T) {
	body := `{"error":{"code":"future_native_code","message":"native-only","param":["input",1]}}`
	diagnosis := classifyProviderRejection(strings.NewReader(body))
	if diagnosis.code != "" || !strings.Contains(string(diagnosis.native.ForDialect(protocolspec.DialectOpenAIResponses)), "future_native_code") {
		t.Fatal("diagnostic parsing changed native error delivery")
	}
}

func TestManagedCodexProtocolHeadersStayWithinNativeService(t *testing.T) {
	for _, test := range []struct {
		name, origin              string
		nativeClient, wantHeaders bool
	}{
		{"native", "https://chatgpt.com", true, true},
		{"native base path", "https://chatgpt.com/backend-api/codex", true, true},
		{"public API", "https://api.openai.com", true, false},
		{"relay", "https://relay.example/v1", true, false},
		{"lookalike", "https://chatgpt.com.example", true, false},
		{"unrelated path", "https://chatgpt.com/other", true, false},
		{"different client origin", "https://chatgpt.com", false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			account := testAccount{id: "account.selected", revision: 3, epoch: 7}
			plan := mustEnvironmentRequestPlan(t, testPlanOptions{
				clientProtocol: environment.ClientProtocolOpenAIResponses, chatGPTClient: test.nativeClient,
				destination: environment.DestinationKindUpstream, providerOrigin: test.origin,
				backend: protocolspec.DialectOpenAIResponses, modelMode: environment.ModelModePassthrough,
				accounts: []testAccount{account}, preferred: account.id,
			})
			provider := &providerDouble{results: []providerResult{{response: jsonResponse(http.StatusOK, completeResponsesProviderResponse("codex-client-alias"))}}}
			pipeline := newTestPipeline(t, newAccountAuthority(t, account), provider, approvedDecisions(), &attemptObserverDouble{})
			defer shutdownPipeline(t, pipeline)
			headers := chatGPTClientHeaderFixture()
			// Header names are case-insensitive and Connection can nominate fields
			// for removal. Copying an allowlist must not revive those client fields.
			headers["session-id"] = headers["Session-Id"]
			delete(headers, "Session-Id")
			headers["connection"] = []string{"keep-alive", "Version, x-codex-beta-features"}
			before := headers.Clone()
			_, err := pipeline.Execute(context.Background(), mustClientRequestWithOptions(t, "exchange-header-scope", plan,
				[]byte(`{"model":"codex-client-alias","stream":false,"input":[{"type":"message","role":"user","content":"hello"}]}`),
				WithOriginalHeaders(headers)), &downstreamRecorder{})
			if err != nil {
				t.Fatal(err)
			}
			outgoing := provider.requestsSnapshot()[0].Headers()
			if (outgoing.Get("Session-Id") == "session-fixture") != test.wantHeaders ||
				(outgoing.Get("Originator") == "codex-tui") != test.wantHeaders {
				t.Fatalf("native protocol headers crossed the wrong boundary: %v", outgoing)
			}
			for _, name := range []string{"Authorization", "Chatgpt-Account-Id", "Cookie", "X-Codex-Turn-State",
				"X-Private-Client", "Connection", "Version", "X-Codex-Beta-Features"} {
				if outgoing.Get(name) != "" {
					t.Errorf("managed envelope retained %s", name)
				}
			}
			if !reflect.DeepEqual(headers, before) {
				t.Fatal("client evidence was mutated while constructing provider headers")
			}
		})
	}
}

func TestManagedChatGPTRoutingHintFollowsModelMappingAndScript(t *testing.T) {
	account := testAccount{id: "account.selected", revision: 3, epoch: 7}
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		clientProtocol: environment.ClientProtocolOpenAIResponses, chatGPTClient: true,
		destination: environment.DestinationKindUpstream, providerOrigin: "https://chatgpt.com",
		backend: protocolspec.DialectOpenAIResponses, modelMode: environment.ModelModeMap, mappedModel: "mapped-model",
		accounts: []testAccount{account}, preferred: account.id,
		transform: messagetransform.Policy{RequestJavaScript: `
			if (request.headers["x-codex-routing-hint"][0] !== "model=mapped-model") throw new Error("stale mapped hint");
			const payload = JSON.parse(request.body);
			payload.model = "script-model";
			payload.service_tier = "priority";
			request.body = JSON.stringify(payload);
		`},
	})
	provider := &providerDouble{results: []providerResult{{response: jsonResponse(http.StatusOK, completeResponsesProviderResponse("script-model"))}}}
	pipeline := newTestPipeline(t, newAccountAuthority(t, account), provider, approvedDecisions(), &attemptObserverDouble{})
	defer shutdownPipeline(t, pipeline)
	_, err := pipeline.Execute(context.Background(), mustClientRequestWithOptions(t, "exchange-routing-hint", plan,
		[]byte(`{"model":"codex-client-alias","stream":false,"input":[{"type":"message","role":"user","content":"hello"}]}`),
		WithOriginalHeaders(chatGPTClientHeaderFixture())), &downstreamRecorder{})
	if err != nil {
		t.Fatal(err)
	}
	if got := provider.requestsSnapshot()[0].Headers().Get("X-Codex-Routing-Hint"); got != "model=script-model;tier=priority" {
		t.Fatalf("outgoing routing hint = %q", got)
	}
}

func TestChatGPTRoutingHintPreservesExplicitOverrides(t *testing.T) {
	for _, test := range []struct{ name, hint, after, want string }{
		{"model and tier changed", "model=before", `{"model":"after","service_tier":"priority"}`, "model=after;tier=priority"},
		{"explicit script override", "custom-route", `{"model":"after"}`, "custom-route"},
		{"explicit delete", "", `{"model":"after"}`, ""},
		{"invalid model", "model=before", `{"model":"bad;hint"}`, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			headers := make(http.Header)
			if test.hint != "" {
				headers.Set("X-Codex-Routing-Hint", test.hint)
			}
			refreshChatGPTRoutingHint(headers, []byte(`{"model":"before"}`), []byte(test.after))
			if headers.Get("X-Codex-Routing-Hint") != test.want {
				t.Fatalf("hint = %v", headers)
			}
		})
	}
}
