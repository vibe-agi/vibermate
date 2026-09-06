package exchange

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/messagetransform"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/providerauth"
)

func TestManagedChatGPTRequestAllowsOptionalMetadataWithRecordingOff(t *testing.T) {
	for _, test := range []struct{ name, origin, path string }{
		{"ChatGPT selected account", "https://chatgpt.com", "backend-api/codex/responses"},
		{"standard Responses upstream", "https://api.openai.com", "v1/responses"},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := mustEnvironmentRequestPlan(t, testPlanOptions{
				clientProtocol: environment.ClientProtocolOpenAIResponses, chatGPTClient: true,
				destination: environment.DestinationKindUpstream, providerOrigin: test.origin,
				backend: protocolspec.DialectOpenAIResponses, modelMode: environment.ModelModePassthrough,
				accounts: []testAccount{{id: "account.selected", revision: 3, epoch: 7}}, preferred: "account.selected",
				recording: environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff},
				transform: messagetransform.Policy{
					RequestJavaScript: `
						if (request.path !== "/` + test.path + `") throw new Error("incorrect upstream path");
						if (request.headers.authorization !== undefined || request.headers["chatgpt-account-id"] !== undefined) throw new Error("old credential exposed");
						const payload = JSON.parse(request.body);
						if (payload.input[1].internal_chat_message_metadata_passthrough.turn_id !== null) throw new Error("metadata was rewritten");
						request.headers["x-request-transformed"] = "yes";
						context.marker = "request-transform-ran";
					`,
					ResponseJavaScript: `response.headers["x-response-transformed"] = context.marker;`,
				},
			})
			provider := &providerDouble{results: []providerResult{{response: jsonResponse(http.StatusOK, completeResponsesProviderResponse("codex-client-alias"))}}}
			content := &contentObserverDouble{}
			pipeline := newTestPipelineWithContentObserver(t,
				newAccountAuthority(t, testAccount{id: "account.selected", revision: 3, epoch: 7}),
				provider, approvedDecisions(), &attemptObserverDouble{}, content)
			raw := &rawObserverDouble{}
			pipeline.rawEvidence = raw
			defer shutdownPipeline(t, pipeline)
			body := []byte(`{"model":"codex-client-alias","stream":false,"input":[
				{"type":"message","role":"assistant","content":"previous","internal_chat_message_metadata_passthrough":{"turn_id":"older-turn"}},
				{"type":"message","role":"user","content":"continue","internal_chat_message_metadata_passthrough":{"turn_id":null}}
			]}`)
			downstream := &downstreamRecorder{}
			result, err := pipeline.Execute(context.Background(), mustClientRequestWithOptions(t,
				"exchange-chatgpt-account", plan, body,
				WithOriginalHeaders(http.Header{
					"Authorization": {"Bearer old-client-token"}, "Chatgpt-Account-Id": {"old-client-account"},
				})), downstream)
			if err != nil {
				t.Fatal(err)
			}
			requests := provider.requestsSnapshot()
			if result.Outcome != AttemptSucceeded || result.AccountID != "account.selected" || len(requests) != 1 {
				t.Fatalf("result = %+v, provider requests = %d", result, len(requests))
			}
			request := requests[0]
			if request.RelativePath() != test.path || request.Target().Origin().String() != test.origin ||
				request.CredentialMode() != providerauth.CredentialManaged || request.Headers().Get("X-Request-Transformed") != "yes" ||
				request.Headers().Get("Authorization") != "" || request.Headers().Get("Chatgpt-Account-Id") != "" ||
				!bytes.Contains(request.Body(), []byte(`"turn_id":null`)) {
				t.Fatal("managed request lost its destination, credential boundary, transform, or optional metadata")
			}
			envelopes := downstream.envelopesSnapshot()
			if len(envelopes) != 1 || envelopes[0].Headers().Get("X-Response-Transformed") != "request-transform-ran" {
				t.Fatal("response transform did not share the request transform context")
			}
			if _, recorded := content.latest(); recorded || len(raw.snapshot()) != 0 {
				t.Fatal("recording-off request retained conversation content or raw bytes")
			}
		})
	}
}

func TestManagedChatGPTStreamingPreservesSSEAfterPolicyApproval(t *testing.T) {
	plan := mustEnvironmentRequestPlan(t, testPlanOptions{
		clientProtocol: environment.ClientProtocolOpenAIResponses, chatGPTClient: true,
		destination: environment.DestinationKindUpstream, providerOrigin: "https://chatgpt.com",
		backend: protocolspec.DialectOpenAIResponses, modelMode: environment.ModelModePassthrough,
		accounts: []testAccount{{id: "account.selected", revision: 3, epoch: 7}}, preferred: "account.selected",
	})
	wire := appendSSEFixture(t, "response.output_text.delta", map[string]any{
		"type": "response.output_text.delta", "sequence_number": 0,
		"item_id": "msg_original_responses", "output_index": 0, "content_index": 0,
		"delta": "provider-compatible",
	})
	wire = append(wire, originalResponsesTerminalWire(t)...)
	provider := &providerDouble{results: []providerResult{{response: streamResponse(http.StatusOK,
		&boundedChunkReader{reader: bytes.NewReader(wire), maximum: 97})}}}
	content := &contentObserverDouble{}
	pipeline := newTestPipelineWithContentObserver(t,
		newAccountAuthority(t, testAccount{id: "account.selected", revision: 3, epoch: 7}),
		provider, approvedDecisions(), &attemptObserverDouble{}, content)
	defer shutdownPipeline(t, pipeline)
	body := []byte(`{"model":"codex-client-alias","stream":true,"store":false,"input":[
		{"type":"message","role":"user","content":"continue","internal_chat_message_metadata_passthrough":{}}
	]}`)
	downstream := &downstreamRecorder{}
	result, err := pipeline.Execute(context.Background(), mustClientRequest(t, "exchange-chatgpt-stream", plan, body), downstream)
	if err != nil {
		t.Fatal(err)
	}
	requests := provider.requestsSnapshot()
	// Same-dialect managed Responses deliberately holds the stream until the
	// terminal tool policy is resolved. Preserve that boundary and exact SSE;
	// this compatibility fix must not silently switch to unchecked passthrough.
	if result.Outcome != AttemptSucceeded || len(requests) != 1 ||
		requests[0].RelativePath() != "backend-api/codex/responses" ||
		!result.Ledger.DownstreamTerminal || !bytes.Equal(downstream.bytesSnapshot(), wire) {
		t.Fatalf("native ChatGPT stream did not preserve approved SSE: %+v", result)
	}
	observation, ok := content.latest()
	if !ok || observation.Response == nil || observation.Response.ReportedModel != "codex-client-alias" {
		t.Fatal("recording-on native ChatGPT stream lost its response evidence")
	}
}
