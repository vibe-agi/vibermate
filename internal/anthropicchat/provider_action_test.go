package anthropicchat

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func newActionStream(t *testing.T) (protocolcore.Request, func([]string) ([]byte, []protocolcore.ToolIntent, error)) {
	t.Helper()
	path, err := NewMessagesProtocolPath(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	request, _, err := path.Client().DecodeRequest([]byte(`{"model":"claude-test","max_tokens":32,
		"messages":[{"role":"user","content":"browse"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	return request, func(events []string) ([]byte, []protocolcore.ToolIntent, error) {
		stream, err := path.Streaming().NewStream(request)
		if err != nil {
			t.Fatal(err)
		}
		safe, err := stream.Feed(context.Background(), []byte(strings.Join(events, "")))
		if err != nil {
			return safe, nil, err
		}
		terminal, err := stream.FinishDecoded(context.Background())
		if err != nil {
			return safe, nil, err
		}
		return safe, terminal.ToolIntents(), nil
	}
}

func sse(name, data string) string { return "event: " + name + "\ndata: " + data + "\n\n" }

var actionMessageStart = sse("message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":3,"output_tokens":1}}}`)
var actionMessageEnd = sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":5}}`) +
	sse("message_stop", `{"type":"message_stop"}`)

// A content block this dialect does not model may be something the client
// executes (a new client tool family). It is held behind the tool barrier
// with all of its deltas and reaches tool policy as an unproven action.
func TestUnmodelledContentBlockIsAnUnprovenAction(t *testing.T) {
	t.Parallel()
	_, run := newActionStream(t)
	safe, intents, err := run([]string{
		actionMessageStart,
		sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"browser_action_use","id":"bau_1","name":"browser","input":{}}}`),
		sse("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"future_delta","value":"open https://example.com"}}`),
		sse("content_block_stop", `{"type":"content_block_stop","index":0}`),
		actionMessageEnd,
	})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(safe, []byte("browser_action_use")) || bytes.Contains(safe, []byte("future_delta")) {
		t.Fatalf("unproven action escaped before the tool decision:\n%s", safe)
	}
	if len(intents) != 1 || intents[0].Call.EffectiveKind() != protocolcore.ToolKindProviderAction ||
		intents[0].Call.Name != "browser_action_use" || intents[0].Call.Key.WireID() != "bau_1" {
		t.Fatalf("intents = %#v", intents)
	}
	var arguments struct {
		ContentBlock map[string]any   `json:"content_block"`
		Deltas       []map[string]any `json:"deltas"`
	}
	if err := json.Unmarshal(intents[0].Call.Arguments.Bytes(), &arguments); err != nil ||
		arguments.ContentBlock["type"] != "browser_action_use" || len(arguments.Deltas) != 1 ||
		arguments.Deltas[0]["value"] != "open https://example.com" {
		t.Fatalf("action arguments = %s (%v)", intents[0].Call.Arguments.Bytes(), err)
	}
}

// The MCP connector runs remote tools on the provider; its blocks are results
// to show, not client work, and must not stop the stream or ask for approval.
func TestMCPConnectorBlocksStayOpaque(t *testing.T) {
	t.Parallel()
	_, run := newActionStream(t)
	_, intents, err := run([]string{
		actionMessageStart,
		sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"mcp_tool_use","id":"mcptoolu_1","name":"search","server_name":"docs","input":{}}}`),
		sse("content_block_stop", `{"type":"content_block_stop","index":0}`),
		sse("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"mcp_tool_result","tool_use_id":"mcptoolu_1","is_error":false,"content":[{"type":"text","text":"ok"}]}}`),
		sse("content_block_stop", `{"type":"content_block_stop","index":1}`),
		sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":5}}`),
		sse("message_stop", `{"type":"message_stop"}`),
	})
	if err != nil || len(intents) != 0 {
		t.Fatalf("MCP connector blocks: intents=%#v err=%v", intents, err)
	}
}

func TestUnmodelledContentBlockInJSONIsAnUnprovenAction(t *testing.T) {
	t.Parallel()
	request, _ := newActionStream(t)
	codec, err := New(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	response, err := codec.DecodeAnthropicProviderResponse(request, []byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-test",
		"content":[{"type":"browser_action_use","id":"bau_1","name":"browser","input":{"url":"https://example.com"}}],
		"stop_reason":"tool_use","stop_sequence":null,"usage":{"input_tokens":3,"output_tokens":2}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Blocks) != 1 || response.Blocks[0].ToolCall.EffectiveKind() != protocolcore.ToolKindProviderAction {
		t.Fatalf("blocks = %#v", response.Blocks)
	}
}
