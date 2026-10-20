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

// A first-turn native declaration is transported unchanged, but declaring it
// cannot authorize the action the provider subsequently asks the client to run.
func TestAdditionalNativeToolsReachProviderWithoutBypassingActionDecision(t *testing.T) {
	for _, approve := range []bool{false, true} {
		t.Run(map[bool]string{false: "rejected", true: "approved"}[approve], func(t *testing.T) {
			account := testAccount{id: "account.selected", revision: 3, epoch: 7}
			plan := mustEnvironmentRequestPlan(t, testPlanOptions{
				clientProtocol: environment.ClientProtocolOpenAIResponses, chatGPTClient: true,
				destination: environment.DestinationKindUpstream, providerOrigin: "https://chatgpt.com",
				backend: protocolspec.DialectOpenAIResponses, modelMode: environment.ModelModePassthrough,
				accounts: []testAccount{account}, preferred: account.id,
			})
			body := []byte(`{"model":"codex-client-alias","stream":true,"input":[{"type":"additional_tools","role":"developer","tools":[{"type":"tool_search","execution":"client"}]},{"type":"message","role":"user","content":"synthetic first turn"}]}`)
			item := json.RawMessage(`{"type":"tool_search_call","id":"search_fixture","call_id":"search_1","execution":"client","arguments":{"query":"files"}}`)
			wire := appendSSEFixture(t, "response.output_item.done", map[string]any{
				"type": "response.output_item.done", "sequence_number": 0, "output_index": 0, "item": item,
			})
			wire = append(wire, appendSSEFixture(t, "response.completed", map[string]any{
				"type": "response.completed", "sequence_number": 1,
				"response": map[string]any{
					"id": "resp_additional_tools", "created_at": 1, "status": "completed", "model": "codex-client-alias",
					"output": []json.RawMessage{item}, "usage": map[string]any{"input_tokens": 4, "output_tokens": 2},
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
			result, err := pipeline.Execute(context.Background(), mustClientRequest(t, "exchange-additional-tools", plan, body), downstream)
			outbound := provider.requestsSnapshot()
			if len(outbound) != 1 {
				t.Fatalf("valid first-turn native declaration never reached provider: sends=%d reason=%s path=%s err=%v", len(outbound), ReasonOf(err), ClientPathOf(err), err)
			}
			var source, forwarded map[string]json.RawMessage
			if json.Unmarshal(body, &source) != nil || json.Unmarshal(outbound[0].Body(), &forwarded) != nil || !bytes.Equal(source["input"], forwarded["input"]) {
				t.Fatal("native tool declaration or input was rewritten")
			}
			if decisions.callCount() != 1 {
				t.Fatalf("native action bypassed decision: %v", err)
			}
			decisions.mu.Lock()
			intents := decisions.requests[0].ToolIntents()
			decisions.mu.Unlock()
			if len(intents) != 1 || intents[0].Call.EffectiveKind() != protocolcore.ToolKindProviderAction || intents[0].Call.Name != "tool_search_call" {
				t.Fatalf("native action gained a modeled tool authority: %+v", intents)
			}
			if approve {
				if err != nil || result.Outcome != AttemptSucceeded || !bytes.Equal(downstream.bytesSnapshot(), wire) {
					t.Fatalf("approved native action: %+v %v", result, err)
				}
			} else if ReasonOf(err) != ReasonToolDecisionRejected || len(downstream.bytesSnapshot()) != 0 {
				t.Fatalf("rejected native action escaped: %+v %v", result, err)
			}
		})
	}
}
