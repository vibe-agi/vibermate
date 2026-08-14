package anthropicchat

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
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

func TestMessagesProtocolPathPreservesThinkingHistoryForLaterTurns(t *testing.T) {
	t.Parallel()

	path, err := NewMessagesProtocolPath(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	source := []byte(`{
		"model":"client-alias",
		"max_tokens":64,
		"messages":[
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"private state","signature":"signed-state"},
				{"type":"text","text":"first answer"}
			]},
			{"role":"user","content":[{"type":"text","text":"follow up"}]}
		],
		"stream":true
	}`)
	request, _, err := path.Client().DecodeRequest(source)
	if err != nil {
		t.Fatalf("DecodeRequest() error = %v", err)
	}
	if len(request.Messages) != 2 || len(request.Messages[0].Blocks) != 2 {
		t.Fatalf("decoded messages = %+v", request.Messages)
	}
	extension := request.Messages[0].Blocks[0]
	if extension.Kind != protocolcore.BlockProviderExtension ||
		extension.ProviderExtension.Source() != protocolcore.ProviderExtensionSourceAnthropicMessages ||
		extension.ProviderExtension.Kind() != protocolcore.ProviderExtensionThinking {
		t.Fatalf("thinking extension = %+v", extension)
	}

	request, err = request.WithEffectiveModel("claude-provider-model")
	if err != nil {
		t.Fatal(err)
	}
	providerRequest, _, err := path.EncodeProviderRequest(request, source, nil)
	if err != nil {
		t.Fatalf("EncodeProviderRequest() error = %v", err)
	}
	var forwarded struct {
		Model    string `json:"model"`
		Messages []struct {
			Content []json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(providerRequest.Body(), &forwarded); err != nil {
		t.Fatal(err)
	}
	if forwarded.Model != "claude-provider-model" || len(forwarded.Messages) != 2 ||
		len(forwarded.Messages[0].Content) != 2 ||
		!bytes.Contains(forwarded.Messages[0].Content[0], []byte(`"signature":"signed-state"`)) {
		t.Fatalf("forwarded thinking history = %s", providerRequest.Body())
	}
}

// Current Claude Code records who originated a tool invocation in assistant
// history. Older builds stripped this field whenever dynamic tool loading was
// disabled, which is why a one-turn fixture did not expose the compatibility
// break. A later turn replays the tool_use block with caller={type:direct}.
// The same-dialect path must validate that official union and retain the exact
// source block when it forwards the next request.
func TestMessagesProtocolPathPreservesToolCallerHistoryForLaterTurns(t *testing.T) {
	t.Parallel()

	path, err := NewMessagesProtocolPath(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	source := []byte(`{
		"model":"client-alias",
		"max_tokens":64,
		"messages":[
			{"role":"assistant","content":[
				{"type":"tool_use","id":"tool_1","name":"Bash","input":{"command":"pwd"},"caller":{"type":"direct"}}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"tool_1","content":"/tmp","is_error":false},
				{"type":"text","text":"continue"}
			]}
		],
		"stream":true
	}`)
	request, report, err := path.Client().DecodeRequest(source)
	if err != nil {
		t.Fatalf("DecodeRequest() rejected official caller history: %v", err)
	}
	if !report.Empty() {
		t.Fatalf("same-dialect decode reported a loss: %+v", report.Notices())
	}
	request, err = request.WithEffectiveModel("claude-provider-model")
	if err != nil {
		t.Fatal(err)
	}
	providerRequest, report, err := path.EncodeProviderRequest(request, source, nil)
	if err != nil {
		t.Fatalf("EncodeProviderRequest() error = %v", err)
	}
	if !report.Empty() {
		t.Fatalf("same-dialect encode reported a loss: %+v", report.Notices())
	}
	if !bytes.Contains(providerRequest.Body(), []byte(`"caller":{"type":"direct"}`)) {
		t.Fatalf("forwarded history lost tool caller: %s", providerRequest.Body())
	}
}

// Claude Code can return a provider-native tool_reference block from a
// subagent tool result. The Anthropic-to-Anthropic path keeps the original
// request body authoritative, so it must retain that history for audit and
// forward the source block unchanged on the next turn.
func TestMessagesProtocolPathPreservesSubagentToolReferenceHistory(t *testing.T) {
	t.Parallel()

	path, err := NewMessagesProtocolPath(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	source := []byte(`{
		"model":"client-alias",
		"max_tokens":64,
		"messages":[
			{"role":"assistant","content":[
				{"type":"tool_use","id":"tool_1","name":"Agent","input":{"prompt":"inspect the workspace"}}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"tool_1","content":[
					{"type":"tool_reference","tool_name":"vibermate-reader"},
					{"type":"text","text":"subagent finished"}
				]}
			]}
		],
		"stream":true
	}`)
	request, report, err := path.Client().DecodeRequest(source)
	if err != nil {
		t.Fatalf("DecodeRequest() rejected subagent tool reference: %v", err)
	}
	if !report.Empty() {
		t.Fatalf("same-dialect decode reported a loss: %+v", report.Notices())
	}
	if len(request.Messages) != 2 || len(request.Messages[1].Blocks) != 1 {
		t.Fatalf("decoded messages = %+v", request.Messages)
	}
	result := request.Messages[1].Blocks[0]
	if result.Kind != protocolcore.BlockToolResult ||
		!strings.Contains(result.ToolResult.Content, `"type":"tool_reference"`) ||
		!strings.Contains(result.ToolResult.Content, `"tool_name":"vibermate-reader"`) {
		t.Fatalf("decoded tool result = %+v", result)
	}

	request, err = request.WithEffectiveModel("claude-provider-model")
	if err != nil {
		t.Fatal(err)
	}
	providerRequest, report, err := path.EncodeProviderRequest(request, source, nil)
	if err != nil {
		t.Fatalf("EncodeProviderRequest() error = %v", err)
	}
	if !report.Empty() {
		t.Fatalf("same-dialect encode reported a loss: %+v", report.Notices())
	}
	if !bytes.Contains(providerRequest.Body(), []byte(`"type":"tool_reference"`)) ||
		!bytes.Contains(providerRequest.Body(), []byte(`"tool_name":"vibermate-reader"`)) {
		t.Fatalf("forwarded history lost tool reference: %s", providerRequest.Body())
	}
}

func TestCrossDialectPathRejectsSubagentToolReferenceHistory(t *testing.T) {
	t.Parallel()

	codec, err := New(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = codec.DecodeClientRequest([]byte(`{
		"model":"client-alias",
		"max_tokens":64,
		"messages":[
			{"role":"assistant","content":[
				{"type":"tool_use","id":"tool_1","name":"Agent","input":{}}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"tool_1","content":[
					{"type":"tool_reference","tool_name":"vibermate-reader"}
				]}
			]}
		]
	}`))
	if err == nil || protocolcore.ReasonOf(err) != protocolcore.ReasonUnsupportedClientInput ||
		!strings.Contains(err.Error(), "$.messages[1].content[0].content[0]") {
		t.Fatalf("cross-dialect tool reference error = %v", err)
	}
}

func TestCrossDialectToolCallerLossIsExplicit(t *testing.T) {
	t.Parallel()

	codec, err := New(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	request, report, err := codec.DecodeClientRequest([]byte(`{
		"model":"client-alias",
		"max_tokens":64,
		"messages":[
			{"role":"assistant","content":[
				{"type":"tool_use","id":"tool_1","name":"Bash","input":{"command":"pwd"},"caller":{"type":"direct"}}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"tool_1","content":"/tmp"}
			]}
		],
		"tools":[{"name":"Bash","description":"Run a command","input_schema":{"type":"object"}}]
	}`))
	if err != nil {
		t.Fatalf("DecodeClientRequest() error = %v", err)
	}
	found := false
	for _, notice := range report.Notices() {
		if notice.Code == protocolcore.NoticeToolCallerNotForwarded &&
			notice.Path == "$.messages[0].content[0].caller" {
			found = true
		}
	}
	if !found {
		t.Fatalf("caller loss was not declared: %+v", report.Notices())
	}
	if _, _, err := codec.EncodeProviderRequest(request); err != nil {
		t.Fatalf("cross-dialect caller history could not be translated: %v", err)
	}
}

func TestMalformedToolCallerIsRejectedAtCallerPath(t *testing.T) {
	t.Parallel()

	codec, err := New(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = codec.DecodeClientRequest([]byte(`{
		"model":"client-alias",
		"max_tokens":64,
		"messages":[{"role":"assistant","content":[
			{"type":"tool_use","id":"tool_1","name":"Bash","input":{},"caller":{"type":"direct","tool_id":"must-not-exist"}}
		]}]
	}`))
	if err == nil || !strings.Contains(err.Error(), "$.messages[0].content[0].caller") {
		t.Fatalf("malformed caller error = %v", err)
	}
}

func TestCrossDialectPathRejectsAnthropicThinkingHistory(t *testing.T) {
	t.Parallel()

	codec, err := New(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	request, _, err := codec.DecodeClientRequest([]byte(`{
		"model":"client-alias",
		"max_tokens":64,
		"messages":[
			{"role":"assistant","content":[{"type":"redacted_thinking","data":"opaque"}]},
			{"role":"user","content":"follow up"}
		]
	}`))
	if err != nil {
		t.Fatalf("DecodeClientRequest() error = %v", err)
	}
	if _, _, err := codec.EncodeProviderRequest(request); err == nil {
		t.Fatal("cross-dialect encoding accepted Anthropic provider history")
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

func TestMessagesProtocolPathRetainsStreamingThinkingAsTypedEvidence(t *testing.T) {
	t.Parallel()

	path, err := NewMessagesProtocolPath(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	source := []byte(`{
		"model":"client-alias",
		"max_tokens":32,
		"messages":[{"role":"user","content":"think carefully"}],
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
		`data: {"type":"message_start","message":{"id":"msg_thinking","type":"message","role":"assistant","model":"claude-sonnet-provider","usage":{"input_tokens":3}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"inspect the repository"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"opaque-signature"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"done"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":1}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n") + "\n"
	immediate, err := stream.Feed(context.Background(), []byte(wire))
	if err != nil {
		t.Fatal(err)
	}
	if string(immediate) != wire {
		t.Fatalf("compatible thinking stream changed:\n%s", immediate)
	}
	terminal, err := stream.FinishDecoded(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	response := terminal.DecodedResponse()
	if len(response.ProviderExtensions) != 1 {
		t.Fatalf("provider extensions = %#v", response.ProviderExtensions)
	}
	extension := response.ProviderExtensions[0]
	if extension.Source() != protocolcore.ProviderExtensionSourceAnthropicMessages ||
		extension.Kind() != protocolcore.ProviderExtensionThinking ||
		extension.Path() != "$.content[0]" {
		t.Fatalf("thinking extension = %#v", extension)
	}
	joined := bytes.Join(extension.Fragments(), nil)
	if !bytes.Contains(joined, []byte("inspect the repository")) ||
		!bytes.Contains(joined, []byte("opaque-signature")) {
		t.Fatalf("thinking fragments = %s", joined)
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
