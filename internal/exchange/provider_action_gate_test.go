package exchange

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

// Output items the Responses dialect does not model may be work the client
// executes (Codex runs local_shell_call itself). They are unproven actions:
// they reach the client only through the tool decision gate, never around it.
func TestUnmodelledResponsesOutputItemsPassOnlyThroughTheToolGate(t *testing.T) {
	for _, item := range []string{
		`{"type":"tool_search_call","id":"search_fixture","call_id":"search_1","execution":"client","arguments":{"query":"files"}}`,
		`{"type":"local_shell_call","id":"ls_fixture","call_id":"call_local","status":"completed","action":{"type":"exec","command":["rm","-rf","/"]}}`,
		`{"type":"shell_call","id":"sh_fixture","call_id":"call_shell","status":"completed","action":{"commands":["rm -rf /"]}}`,
		`{"type":"computer_call","id":"cu_fixture","call_id":"call_computer","status":"completed","action":{"type":"click","x":1,"y":1}}`,
		`{"type":"future_client_action","id":"future_fixture","payload":{"run":"anything"}}`,
	} {
		var kind struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(item), &kind); err != nil {
			t.Fatal(err)
		}
		for _, approve := range []bool{false, true} {
			t.Run(kind.Type+map[bool]string{false: "/rejected", true: "/approved"}[approve], func(t *testing.T) {
				account := testAccount{id: "account.selected", revision: 3, epoch: 7}
				plan := mustEnvironmentRequestPlan(t, testPlanOptions{
					clientProtocol: environment.ClientProtocolOpenAIResponses, chatGPTClient: true,
					destination: environment.DestinationKindUpstream, providerOrigin: "https://chatgpt.com",
					backend: protocolspec.DialectOpenAIResponses, modelMode: environment.ModelModePassthrough,
					accounts: []testAccount{account}, preferred: account.id,
				})
				raw := json.RawMessage(item)
				wire := appendSSEFixture(t, "response.output_item.done", map[string]any{
					"type": "response.output_item.done", "sequence_number": 0, "output_index": 0, "item": raw,
				})
				wire = append(wire, appendSSEFixture(t, "response.completed", map[string]any{
					"type": "response.completed", "sequence_number": 1,
					"response": map[string]any{
						"id": "resp_action_fixture", "created_at": 1, "status": "completed",
						"model": "codex-client-alias", "output": []json.RawMessage{raw},
						"usage": map[string]any{"input_tokens": 4, "output_tokens": 2},
					},
				})...)
				provider := &providerDouble{results: []providerResult{{response: &http.Response{
					StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(wire)),
				}}}}
				decision := ToolDecision{Outcome: ToolDecisionRejected, ReasonCode: "tool_policy_strict"}
				if approve {
					decision = ToolDecision{Outcome: ToolDecisionApproved}
				}
				decisions := &decisionDouble{decision: decision}
				pipeline := newTestPipeline(t, newAccountAuthority(t, account), provider, decisions, &attemptObserverDouble{})
				defer shutdownPipeline(t, pipeline)
				downstream := &downstreamRecorder{}
				result, err := pipeline.Execute(context.Background(), mustClientRequest(t, "exchange-provider-action", plan,
					[]byte(`{"model":"codex-client-alias","stream":true,"input":[{"type":"message","role":"user","content":"hello"}]}`)), downstream)
				if decisions.callCount() != 1 {
					t.Fatalf("unmodelled item bypassed the tool decision gate: calls=%d err=%v", decisions.callCount(), err)
				}
				decisions.mu.Lock()
				intents := decisions.requests[0].ToolIntents()
				decisions.mu.Unlock()
				if len(intents) != 1 || intents[0].Call.EffectiveKind() != protocolcore.ToolKindProviderAction ||
					intents[0].Call.Name != kind.Type {
					t.Fatalf("decision intents = %#v", intents)
				}
				if approve {
					if err != nil || result.Outcome != AttemptSucceeded || !bytes.Equal(downstream.bytesSnapshot(), wire) {
						t.Fatalf("approved action stream = %+v err=%v", result, err)
					}
				} else if ReasonOf(err) != ReasonToolDecisionRejected || len(downstream.bytesSnapshot()) != 0 {
					t.Fatalf("rejected action bytes escaped: %+v err=%v bytes=%q", result, err, downstream.bytesSnapshot())
				}
			})
		}
	}
}
