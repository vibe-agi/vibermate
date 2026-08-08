package anthropicchat

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

func TestMessagesProtocolPathPreservesCompatibleWireAndAppliesModel(t *testing.T) {
	t.Parallel()

	path, err := NewMessagesProtocolPath(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if path.Client().Dialect() != protocolspec.DialectAnthropicMessages ||
		path.Backend().Dialect() != protocolspec.DialectAnthropicMessages {
		t.Fatal("messages protocol path dialect edges are incomplete")
	}
	source := []byte(`{
		"model":"client-alias",
		"max_tokens":16,
		"messages":[{"role":"user","content":"hello"}],
		"metadata":{"user_id":"local-user"},
		"stream":false
	}`)
	request, _, err := path.Client().DecodeRequest(source)
	if err != nil {
		t.Fatal(err)
	}
	request, err = request.WithEffectiveModel("claude-sonnet-provider")
	if err != nil {
		t.Fatal(err)
	}
	sourceHeaders := make(http.Header)
	sourceHeaders.Set("Anthropic-Beta", "claude-code-20250219")
	providerRequest, _, err := path.EncodeProviderRequest(
		request,
		source,
		sourceHeaders,
	)
	if err != nil {
		t.Fatal(err)
	}
	if providerRequest.RelativePath() != MessagesProviderRelativePath ||
		providerRequest.Headers().Get("Anthropic-Version") != AnthropicVersion ||
		providerRequest.Headers().Get("Anthropic-Beta") != "claude-code-20250219" ||
		providerRequest.Headers().Get("Accept") != "application/json" {
		t.Fatalf("provider request = %+v", providerRequest)
	}
	var forwarded map[string]json.RawMessage
	if err := json.Unmarshal(providerRequest.Body(), &forwarded); err != nil {
		t.Fatal(err)
	}
	var model string
	if err := json.Unmarshal(forwarded["model"], &model); err != nil {
		t.Fatal(err)
	}
	if model != "claude-sonnet-provider" || forwarded["metadata"] == nil {
		t.Fatalf("forwarded body = %s", providerRequest.Body())
	}

	providerBody := []byte(`{
		"id":"msg_compatible",
		"type":"message",
		"role":"assistant",
		"model":"claude-sonnet-provider",
		"content":[{"type":"text","text":"hello back","citations":[]}],
		"stop_reason":"end_turn",
		"stop_sequence":null,
		"usage":{"input_tokens":4,"output_tokens":2}
	}`)
	response, _, err := path.Backend().DecodeResponse(request, providerBody)
	if err != nil {
		t.Fatal(err)
	}
	clientBody, _, err := path.EncodeClientResponse(request, response, providerBody)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(clientBody, providerBody) {
		t.Fatalf("compatible response was rewritten:\n%s", clientBody)
	}
}

func TestMessagesProtocolPathHoldsStreamingToolCallUntilApproval(t *testing.T) {
	t.Parallel()

	path, err := NewMessagesProtocolPath(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	source := []byte(`{
		"model":"client-alias",
		"max_tokens":32,
		"messages":[{"role":"user","content":"list files"}],
		"tools":[{"name":"shell","description":"run a command","input_schema":{"type":"object"}}],
		"stream":true
	}`)
	request, _, err := path.Client().DecodeRequest(source)
	if err != nil {
		t.Fatal(err)
	}
	request, err = request.WithEffectiveModel("claude-sonnet-provider")
	if err != nil {
		t.Fatal(err)
	}
	stream, err := path.Streaming().NewStream(request)
	if err != nil {
		t.Fatal(err)
	}
	wire := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_tool","type":"message","role":"assistant","model":"claude-sonnet-provider","usage":{"input_tokens":8}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tool_1","name":"shell","input":{}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":\"pwd\"}"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":4}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n") + "\n"
	var immediate bytes.Buffer
	for _, fragment := range []string{wire[:len(wire)/2], wire[len(wire)/2:]} {
		safe, feedErr := stream.Feed(context.Background(), []byte(fragment))
		if feedErr != nil {
			t.Fatal(feedErr)
		}
		_, _ = immediate.Write(safe)
	}
	if bytes.Contains(immediate.Bytes(), []byte("tool_1")) ||
		bytes.Contains(immediate.Bytes(), []byte("input_json_delta")) {
		t.Fatalf("tool-bearing stream exposed a tool before approval:\n%s", immediate.Bytes())
	}
	if stream.SemanticProgress() == 0 {
		t.Fatal("validated events did not advance semantic progress")
	}
	terminal, err := stream.FinishDecoded(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	intents := terminal.ToolIntents()
	if len(intents) != 1 || intents[0].Call.Name != "shell" ||
		string(intents[0].Call.Arguments.Bytes()) != `{"cmd":"pwd"}` {
		t.Fatalf("tool intents = %+v", intents)
	}
	released, err := terminal.Approve()
	if err != nil {
		t.Fatal(err)
	}
	combined := append(bytes.Clone(immediate.Bytes()), released...)
	if string(combined) != wire {
		t.Fatalf("compatible stream changed across the approval barrier:\n%s", combined)
	}
}

func TestMessagesProtocolPathStreamsTextWithoutWaitingForTerminalApproval(t *testing.T) {
	t.Parallel()

	path, err := NewMessagesProtocolPath(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	source := []byte(`{
		"model":"client-alias",
		"max_tokens":32,
		"messages":[{"role":"user","content":"say hello"}],
		"stream":true
	}`)
	request, _, err := path.Client().DecodeRequest(source)
	if err != nil {
		t.Fatal(err)
	}
	request, err = request.WithEffectiveModel("claude-sonnet-provider")
	if err != nil {
		t.Fatal(err)
	}
	stream, err := path.Streaming().NewStream(request)
	if err != nil {
		t.Fatal(err)
	}
	wire := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_text","type":"message","role":"assistant","model":"claude-sonnet-provider","usage":{"input_tokens":3}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n") + "\n"
	immediate, err := stream.Feed(context.Background(), []byte(wire))
	if err != nil {
		t.Fatal(err)
	}
	if string(immediate) != wire || !bytes.Contains(immediate, []byte(`"text":"hello"`)) {
		t.Fatalf("text stream was not released immediately:\n%s", immediate)
	}
	terminal, err := stream.FinishDecoded(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release, err := terminal.Approve()
	if err != nil {
		t.Fatal(err)
	}
	if len(release) != 0 || len(terminal.ToolIntents()) != 0 {
		t.Fatalf("text-only terminal release=%q intents=%+v", release, terminal.ToolIntents())
	}
}

func TestMessagesProtocolPathRejectsUndefinedProviderTool(t *testing.T) {
	t.Parallel()

	path, err := NewMessagesProtocolPath(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	source := []byte(`{
		"model":"client-alias",
		"max_tokens":16,
		"messages":[{"role":"user","content":"hello"}],
		"stream":false
	}`)
	request, _, err := path.Client().DecodeRequest(source)
	if err != nil {
		t.Fatal(err)
	}
	request, err = request.WithEffectiveModel("provider-model")
	if err != nil {
		t.Fatal(err)
	}
	providerBody := []byte(`{
		"id":"msg_bad_tool","type":"message","role":"assistant","model":"provider-model",
		"content":[{"type":"tool_use","id":"tool_1","name":"unknown","input":{}}],
		"stop_reason":"tool_use","stop_sequence":null,
		"usage":{"input_tokens":1,"output_tokens":1}
	}`)
	if _, _, err := path.Backend().DecodeResponse(request, providerBody); err == nil {
		t.Fatal("undefined provider tool was accepted")
	}
}
