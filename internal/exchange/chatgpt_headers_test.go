package exchange

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/messagetransform"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

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
