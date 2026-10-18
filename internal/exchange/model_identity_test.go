package exchange

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

func TestMappedModelHeadersPreserveGenuineReroutes(t *testing.T) {
	for _, test := range []struct {
		name, requested, effective string
		values, want               []string
	}{
		{"configured alias", "alias", "upstream", []string{"upstream"}, []string{"alias"}},
		{"server reroute", "alias", "upstream", []string{"restricted-model"}, []string{"restricted-model"}},
		{"exact match only", "alias", "upstream", []string{"Upstream", " upstream"}, []string{"Upstream", " upstream"}},
		{"passthrough", "upstream", "upstream", []string{"upstream"}, []string{"upstream"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, name := range []string{"Openai-Model", "openai-model", "X-OpenAI-Model"} {
				source := http.Header{name: test.values, "X-Request-Id": {"trace"}}
				headers := source.Clone()
				rewriteMappedModelHeaders(headers, test.requested, test.effective)
				if !reflect.DeepEqual(headers[name], test.want) || headers.Get("X-Request-Id") != "trace" {
					t.Fatalf("downstream headers = %v", headers)
				}
				if !reflect.DeepEqual(source[name], test.values) {
					t.Fatal("upstream header evidence was modified")
				}
			}
		})
	}
}

func TestManagedCodexPreservesOnlyExactNativeModeHeaders(t *testing.T) {
	for _, test := range []struct {
		name, header, connection string
		values                   []string
		want                     string
	}{
		{"Lite", "X-Openai-Internal-Codex-Responses-Lite", "", []string{"true"}, "true"},
		{"Lite invalid", "X-Openai-Internal-Codex-Responses-Lite", "", []string{"TRUE"}, ""},
		{"Lite duplicate", "X-Openai-Internal-Codex-Responses-Lite", "", []string{"true", "true"}, ""},
		{"Lite hop", "X-Openai-Internal-Codex-Responses-Lite", "x-openai-internal-codex-responses-lite", []string{"true"}, ""},
		{"Guardian reviewer", "X-Codex-Guardian", "", []string{"reviewer"}, "reviewer"},
		{"Guardian classifier", "X-Codex-Guardian", "", []string{"classifier"}, "classifier"},
		{"Guardian arbitrary", "X-Codex-Guardian", "", []string{"worker"}, ""},
		{"Guardian duplicate", "X-Codex-Guardian", "", []string{"reviewer", "classifier"}, ""},
		{"Guardian hop", "X-Codex-Guardian", "X-Codex-Guardian", []string{"reviewer"}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			account := testAccount{id: "account.selected", revision: 3, epoch: 7}
			plan := mustEnvironmentRequestPlan(t, testPlanOptions{
				clientProtocol: environment.ClientProtocolOpenAIResponses, chatGPTClient: true,
				destination: environment.DestinationKindUpstream, providerOrigin: "https://chatgpt.com",
				backend: protocolspec.DialectOpenAIResponses, modelMode: environment.ModelModePassthrough,
				accounts: []testAccount{account}, preferred: account.id,
			})
			provider := &providerDouble{results: []providerResult{{response: jsonResponse(http.StatusOK, completeResponsesProviderResponse("codex-client-alias"))}}}
			pipeline := newTestPipeline(t, newAccountAuthority(t, account), provider, approvedDecisions(), &attemptObserverDouble{})
			defer shutdownPipeline(t, pipeline)
			headers := http.Header{test.header: test.values, "Cookie": {"private"}, "X-Oai-Attestation": {"private"}, "Authorization": {"Bearer source-secret"}}
			if test.connection != "" {
				headers.Set("Connection", test.connection)
			}
			clientRequest := mustClientRequestWithOptions(t, "native-mode", plan,
				[]byte(`{"model":"codex-client-alias","stream":false,"service_tier":"priority","parallel_tool_calls":false,"reasoning":{"context":"all_turns"},"input":"hello"}`),
				WithOriginalHeaders(headers))
			selection, err := selectFrozenPlan(plan)
			if err != nil {
				t.Fatal(err)
			}
			filtered := nativeChatGPTProtocolHeaders(clientRequest, selection)
			if got := filtered.Get(test.header); got != test.want {
				t.Fatalf("filtered %s = %q, want %q", test.header, got, test.want)
			}
			// Invalid Guardian evidence is rejected later by account finalization;
			// this boundary only proves it cannot become an upstream mode header.
			if test.want == "" {
				return
			}
			_, err = pipeline.Execute(context.Background(), clientRequest, &downstreamRecorder{})
			if err != nil {
				t.Fatal(err)
			}
			request := provider.requestsSnapshot()[0]
			if got := request.Headers().Get(test.header); got != test.want {
				t.Fatalf("%s = %q, want %q", test.header, got, test.want)
			}
			for _, secret := range []string{"Cookie", "X-Oai-Attestation", "Authorization"} {
				if request.Headers().Get(secret) != "" {
					t.Fatalf("forwarded %s", secret)
				}
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(request.Body(), &body); err != nil {
				t.Fatal(err)
			}
			if string(body["service_tier"]) != `"priority"` || string(body["parallel_tool_calls"]) != "false" || string(body["reasoning"]) != `{"context":"all_turns"}` {
				t.Fatalf("native request controls changed: %s", request.Body())
			}
		})
	}
}
