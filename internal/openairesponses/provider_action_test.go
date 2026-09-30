package openairesponses

import (
	"testing"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func providerOutputBody(item string) []byte {
	return []byte(`{"id":"resp_action","created_at":1,"status":"completed","model":"provider-model","output":[` + item + `],"usage":{}}`)
}

// An output item this dialect does not model may be something the client
// executes. It becomes an unproven action so the Environment tool policy
// decides: Observe releases it, Review asks, Strict stops it.
func TestUnmodelledOutputItemsBecomeUnprovenActions(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ item, name, key string }{
		{`{"type":"tool_search_call","id":"search_1","call_id":"search_call","execution":"client","arguments":{"query":"files"}}`, "tool_search_call", "search_call"},
		{`{"type":"local_shell_call","id":"ls_1","call_id":"shell_1","status":"completed","action":{"type":"exec","command":["pwd"]}}`, "local_shell_call", "shell_1"},
		{`{"type":"shell_call","id":"sh_1","call_id":"shell_2","status":"completed","action":{"commands":["ls"]}}`, "shell_call", "shell_2"},
		{`{"type":"computer_call","id":"cu_1","call_id":"computer_1","status":"completed","action":{"type":"click","x":1,"y":2}}`, "computer_call", "computer_1"},
		{`{"type":"mcp_approval_request","id":"mcpr_1","server_label":"docs","name":"delete","arguments":"{}"}`, "mcp_approval_request", "mcpr_1"},
		{`{"type":"future_client_action","id":"fut_1"}`, "future_client_action", "fut_1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response, _, err := newTestCodec(t).DecodeProviderResponse(streamingRequestFixture(t), providerOutputBody(test.item))
			if err != nil {
				t.Fatalf("DecodeProviderResponse() error = %v", err)
			}
			if len(response.Blocks) != 1 || response.Blocks[0].Kind != protocolcore.BlockToolCall {
				t.Fatalf("decoded output = %#v", response.Blocks)
			}
			call := response.Blocks[0].ToolCall
			if call.EffectiveKind() != protocolcore.ToolKindProviderAction || call.Name != test.name ||
				call.Key.WireID() != test.key || call.Arguments.IsZero() {
				t.Fatalf("unproven action = %#v", call)
			}
			if err := call.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Hosted tools run on the provider; their items are results, not actions the
// client can execute, and pass as opaque provider data.
func TestHostedOutputItemsStayOpaque(t *testing.T) {
	t.Parallel()
	for _, item := range []string{
		`{"type":"tool_search_call","id":"search_1","execution":"server","arguments":{"query":"files"}}`,
		`{"type":"mcp_call","id":"mcp_1","server_label":"docs","name":"search","arguments":"{}","output":"ok"}`,
		`{"type":"mcp_list_tools","id":"mcpl_1","server_label":"docs","tools":[]}`,
		`{"type":"code_interpreter_call","id":"ci_1","status":"completed","code":"1+1","container_id":"c"}`,
		`{"type":"file_search_call","id":"fs_1","status":"completed","queries":["q"]}`,
	} {
		response, _, err := newTestCodec(t).DecodeProviderResponse(streamingRequestFixture(t), providerOutputBody(item))
		if err != nil {
			t.Fatalf("%s: %v", item, err)
		}
		if len(response.Blocks) != 1 || response.Blocks[0].Kind != protocolcore.BlockProviderExtension {
			t.Fatalf("%s decoded as %#v", item, response.Blocks)
		}
	}
}
