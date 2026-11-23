package toolpolicy

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/vibe-agi/vibermate/internal/anthropicchat"
	"github.com/vibe-agi/vibermate/internal/anthropicresponses"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
)

// claudeToolsRequest is Claude Code 2.1.329's first tool-round request (the
// S2 corpus), so the gate reads Claude Code's own Read definition.
const claudeToolsRequest = "../productruntime/testdata/protocolgolden/requests/claude-tools.007.json"

// pathIntents runs one upstream turn through a production path and returns
// the decoded request and the intents its terminal hands the gate.
func pathIntents(t *testing.T, path *protocolpath.Path, body, wire []byte) (protocolcore.Request, []protocolcore.ToolIntent) {
	t.Helper()
	request, _, err := path.Client().DecodeRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err = request.WithEffectiveModel("upstream-model")
	if err != nil {
		t.Fatal(err)
	}
	stream, err := path.NewStreamFor(protocolpath.ProviderContext{}, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Feed(t.Context(), wire); err != nil {
		t.Fatal(err)
	}
	terminal, err := stream.FinishDecoded(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return request, terminal.ToolIntents()
}

// responsesReadTurn is a Responses upstream calling Read.
func responsesReadTurn(file string) []byte {
	call := fmt.Sprintf(`{"id":"fc_1","type":"function_call","status":"completed","arguments":%q,"call_id":"call_read","name":"Read"}`,
		`{"file_path":`+quotedJSON(file)+`}`)
	var wire bytes.Buffer
	for _, payload := range []string{
		`{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response","created_at":1,"status":"in_progress","model":"upstream-model","output":[]}}`,
		`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"fc_1","type":"function_call","status":"in_progress","arguments":"","call_id":"call_read","name":"Read"}}`,
		`{"type":"response.output_item.done","sequence_number":2,"output_index":0,"item":` + call + `}`,
		`{"type":"response.completed","sequence_number":3,"response":{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"upstream-model","output":[` + call + `],` +
			`"usage":{"input_tokens":5,"input_tokens_details":{"cached_tokens":0},"output_tokens":3,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":8}}}`,
	} {
		fmt.Fprintf(&wire, "data: %s\n\n", payload)
	}
	return wire.Bytes()
}

// anthropicReadTurn is the native Anthropic upstream making the same call.
func anthropicReadTurn(file string) []byte {
	var wire bytes.Buffer
	for _, event := range [][2]string{
		{"message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"upstream-model","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":5,"output_tokens":0}}}`},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_read","name":"Read","input":{}}}`},
		{"content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":%q}}`, `{"file_path":`+quotedJSON(file)+`}`)},
		{"content_block_stop", `{"type":"content_block_stop","index":0}`},
		{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":3}}`},
		{"message_stop", `{"type":"message_stop"}`},
	} {
		fmt.Fprintf(&wire, "event: %s\ndata: %s\n\n", event[0], event[1])
	}
	return wire.Bytes()
}

// Design 15.4: with structured workspace-tool evidence, a Read whose intent
// comes from a Responses function_call is decided exactly as the native
// path's Read: approved inside the workspace under Review and Strict without
// a question, asked under Review and rejected under Strict outside it.
func TestTranslatedReadIsDecidedAsTheNativeRead(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(inside, []byte("notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(claudeToolsRequest)
	if err != nil {
		t.Fatal(err)
	}
	translated, err := anthropicresponses.NewProtocolPath(anthropicresponses.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	native, err := anthropicchat.NewMessagesProtocolPath(anthropicchat.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []environment.ToolPolicyMode{environment.ToolPolicyReview, environment.ToolPolicyStrict} {
		for _, target := range []struct {
			name, file string
			safe       bool
		}{{"inside", inside, true}, {"outside", outside, false}} {
			t.Run(string(mode)+"/"+target.name, func(t *testing.T) {
				decide := func(path *protocolpath.Path, wire []byte, source string) (exchange.ToolDecision, int) {
					t.Helper()
					request, intents := pathIntents(t, path, body, wire)
					if len(intents) != 1 || intents[0].Call.Name != "Read" || intents[0].Call.Key.Source() != source {
						t.Fatalf("intents = %+v, want one Read keyed %s", intents, source)
					}
					decisionContext, err := exchange.NewToolDecisionContextForBasis(
						environment.PolicySet{ToolMode: mode}, root, clientadapter.ProtectionExactRelease,
						request.Tools, request.ToolNamespaces,
					)
					if err != nil {
						t.Fatal(err)
					}
					environmentID, err := environment.NewEnvironmentID("claude-openai")
					if err != nil {
						t.Fatal(err)
					}
					routeID, err := environment.NewUpstreamRouteID("route-openai")
					if err != nil {
						t.Fatal(err)
					}
					decisionRequest, err := exchange.NewToolDecisionRequest(
						"exchange-translated", environmentID, 1, environment.CandidateDigest{1},
						routeID, 1, decisionContext, intents,
					)
					if err != nil {
						t.Fatal(err)
					}
					review := &reviewDouble{}
					gate, err := New(review)
					if err != nil {
						t.Fatal(err)
					}
					decision, err := gate.Decide(context.Background(), decisionRequest)
					if err != nil {
						t.Fatal(err)
					}
					return decision, review.calls
				}
				fromResponses, asked := decide(translated, responsesReadTurn(target.file), "openai-responses-call")
				fromNative, nativeAsked := decide(native, anthropicReadTurn(target.file), "anthropic-messages-openai-chat")
				if fromResponses != fromNative || asked != nativeAsked {
					t.Fatalf("translated decision %+v (asked %d) differs from native %+v (asked %d)", fromResponses, asked, fromNative, nativeAsked)
				}
				want, wantReason, wantAsked := exchange.ToolDecisionApproved, "", 0
				switch {
				case !target.safe && mode == environment.ToolPolicyReview:
					wantAsked = 1
				case !target.safe && mode == environment.ToolPolicyStrict:
					want, wantReason = exchange.ToolDecisionRejected, ReasonStrictPolicy
				}
				if fromResponses.Outcome != want || fromResponses.ReasonCode != wantReason || asked != wantAsked {
					t.Fatalf("decision = %+v asked %d; want %s %q asked %d", fromResponses, asked, want, wantReason, wantAsked)
				}
			})
		}
	}
}
