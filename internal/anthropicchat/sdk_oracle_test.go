package anthropicchat

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	anthropic "github.com/anthropics/anthropic-sdk-go"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
)

// These oracles hold the Anthropic client side to the official SDK
// (anthropic-sdk-go, test-only per design F40): every streamed event must
// parse as the SDK's event union and accumulate into the message the SDK
// would hand Claude Code, the unary body must parse as a Message, and what
// the SDK replays from that message must decode back to the same neutral
// reasoning.

type oracleStream struct {
	message anthropic.Message
	// delta is the raw message_delta, for members the SDK does not model.
	delta  map[string]json.RawMessage
	report protocolcore.TranslationReport
	bytes  []byte
}

// runAssembled drives the client side's stream encoder through the shared
// Assembler, as a provider side does, approves the terminal and accumulates
// every client byte with the SDK.
func runAssembled(t *testing.T, request protocolcore.Request, script func(*protocolpath.Assembler) ([]byte, error)) oracleStream {
	t.Helper()
	side, err := NewClientSide(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	encoder, err := side.NewStreamEncoder(request)
	if err != nil {
		t.Fatal(err)
	}
	assembler, err := protocolpath.NewAssembler(request, encoder, protocolpath.AssemblerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	live, err := script(assembler)
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := assembler.Finish()
	if err != nil {
		t.Fatal(err)
	}
	release, err := terminal.Approve()
	if err != nil {
		t.Fatal(err)
	}
	stream := append(live, release...)
	result := oracleStream{report: terminal.TranslationReport(), bytes: stream}
	for _, event := range sseEvents(t, stream) {
		var union anthropic.MessageStreamEventUnion
		if err := union.UnmarshalJSON(event.Data); err != nil {
			t.Fatalf("SDK cannot parse %s: %v", event.Data, err)
		}
		if union.Type != event.Name {
			t.Fatalf("event %q carries type %q", event.Name, union.Type)
		}
		if err := result.message.Accumulate(union); err != nil {
			t.Fatalf("SDK cannot accumulate %s: %v", event.Data, err)
		}
		if event.Name == "message_delta" {
			var delta struct {
				Delta map[string]json.RawMessage `json:"delta"`
			}
			if err := json.Unmarshal(event.Data, &delta); err != nil {
				t.Fatal(err)
			}
			result.delta = delta.Delta
		}
	}
	return result
}

// steps runs assembler calls in order and joins their live bytes.
func steps(calls ...func() ([]byte, error)) ([]byte, error) {
	var live bytes.Buffer
	for _, call := range calls {
		encoded, err := call()
		if err != nil {
			return nil, err
		}
		live.Write(encoded)
	}
	return live.Bytes(), nil
}

func oracleRequest(display protocolcore.ThinkingDisplay, safeguards ...protocolcore.SafeguardKind) protocolcore.Request {
	return protocolcore.Request{
		RequestedModel:     "claude-opus-5-5",
		EffectiveModel:     "gpt-upstream",
		Stream:             true,
		Reasoning:          protocolcore.ReasoningIntent{Thinking: protocolcore.ThinkingModeAdaptive, Display: display},
		UnservedSafeguards: safeguards,
	}
}

func TestSDKAccumulatesReasoningTextAndToolsAndReplaysTheSeal(t *testing.T) {
	t.Parallel()

	seal := testResponsesSeal()
	call := testToolCall(t, "call_vm_1", "Bash", `{"command":"ls"}`)
	reasoning := protocolcore.Reasoning{Text: "first\n\nsecond", Seal: seal}
	reasoningBlock, err := protocolcore.NewReasoningBlock(reasoning)
	if err != nil {
		t.Fatal(err)
	}
	terminal := testClientResponse(reasoningBlock,
		protocolcore.ContentBlock{Kind: protocolcore.BlockText, Text: "Let me check."},
		protocolcore.ContentBlock{Kind: protocolcore.BlockToolCall, ToolCall: call})
	terminal.StopReason = protocolcore.StopReasonToolUse
	terminal.Usage = protocolcore.Usage{
		InputUncached: protocolcore.UsageValue{Tokens: 1000, Known: true, Source: "openai-responses"},
		CacheRead:     protocolcore.UsageValue{Tokens: 4000, Known: true, Source: "openai-responses"},
		CacheWrite:    protocolcore.UsageValue{Tokens: 0, Known: true, Source: "openai-responses"},
		Output:        protocolcore.UsageValue{Tokens: 321, Known: true, Source: "openai-responses"},
	}
	request := oracleRequest(protocolcore.ThinkingDisplayUpdates, protocolcore.SafeguardDangerousToolUse)
	result := runAssembled(t, request, func(a *protocolpath.Assembler) ([]byte, error) {
		return steps(
			func() ([]byte, error) {
				return a.Start(protocolpath.StreamStart{ResponseID: "msg_vm_1", ReportedModel: "gpt-upstream"})
			},
			func() ([]byte, error) { return a.Reasoning("rs", "") },
			func() ([]byte, error) { return a.Reasoning("rs", "first") },
			func() ([]byte, error) { return a.Reasoning("rs", "\n\n") },
			func() ([]byte, error) { return a.Reasoning("rs", "second") },
			func() ([]byte, error) { return a.ReasoningDone("rs", reasoning) },
			func() ([]byte, error) { return a.Text("msg", "Let me ") },
			func() ([]byte, error) { return a.TextDone("msg", "Let me check.") },
			func() ([]byte, error) { a.ArmBarrier(); return a.ToolCall("fc", call) },
			func() ([]byte, error) { return nil, a.Terminal(terminal) },
		)
	})
	message := result.message
	if message.ID != "msg_vm_1" || string(message.Model) != "claude-opus-5-5" || message.StopReason != anthropic.StopReasonToolUse {
		t.Fatalf("message = %s %s %s", message.ID, message.Model, message.StopReason)
	}
	if message.Usage.InputTokens != 1000 || message.Usage.CacheReadInputTokens != 4000 || message.Usage.OutputTokens != 321 {
		t.Fatalf("usage = %+v", message.Usage)
	}
	if len(message.Content) != 3 {
		t.Fatalf("content = %+v", message.Content)
	}
	thinking := message.Content[0].AsThinking()
	if message.Content[0].Type != "thinking" || thinking.Thinking != "first\n\nsecond" {
		t.Fatalf("thinking = %+v", thinking)
	}
	if parsed, _, ok := protocolcore.ParseReasoningSeal(thinking.Signature); !ok || parsed != seal {
		t.Fatalf("signature %q does not carry the seal", thinking.Signature)
	}
	if text := message.Content[1].AsText(); text.Text != "Let me check." {
		t.Fatalf("text = %+v", text)
	}
	tool := message.Content[2].AsToolUse()
	if tool.ID != "call_vm_1" || tool.Name != "Bash" || string(tool.Input) != `{"command":"ls"}` {
		t.Fatalf("tool_use = %+v %s", tool, tool.Input)
	}
	if got := string(result.delta["safeguard_results"]); got != `[{"type":"dangerous_tool_use","status":{"type":"unsupported"}}]` {
		t.Fatalf("safeguard_results = %s", got)
	}

	// Claude Code replays the accumulated message as the next assistant
	// turn (design F32); the translated decoder recovers the same reasoning,
	// with the seal intact, right before the text and call it produced.
	replay, err := json.Marshal(anthropic.MessageNewParams{
		Model:     "claude-opus-5-5",
		MaxTokens: 1024,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("list files")),
			message.ToParam(),
			anthropic.NewUserMessage(anthropic.NewToolResultBlock("call_vm_1", "a.txt", false)),
		},
		Tools: []anthropic.ToolUnionParam{{OfTool: &anthropic.ToolParam{
			Name: "Bash", InputSchema: anthropic.ToolInputSchemaParam{Properties: map[string]any{}},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	decoded, _, err := newTranslatedTestCodec(t).DecodeClientRequest(replay)
	if err != nil {
		t.Fatalf("the SDK's replay does not decode: %v\n%s", err, replay)
	}
	blocks := decoded.Messages[1].Blocks
	if len(blocks) != 3 || blocks[0].Kind != protocolcore.BlockReasoning || *blocks[0].Reasoning != reasoning ||
		blocks[1].Text != "Let me check." || blocks[2].ToolCall.Key.WireID() != "call_vm_1" {
		t.Fatalf("replayed assistant turn = %+v", blocks)
	}

	// The unary body parses as the SDK's Message with the same content.
	unary, _, err := (&ClientSide{codec: newTestCodec(t)}).EncodeResponse(request, terminal)
	if err != nil {
		t.Fatal(err)
	}
	var parsed anthropic.Message
	if err := json.Unmarshal(unary, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Content) != 3 || parsed.Content[0].AsThinking().Signature != thinking.Signature ||
		parsed.Content[1].AsText().Text != "Let me check." || parsed.Content[2].AsToolUse().Name != "Bash" ||
		parsed.StopReason != anthropic.StopReasonToolUse {
		t.Fatalf("unary = %s", unary)
	}
	if field, ok := parsed.JSON.ExtraFields["safeguard_results"]; !ok ||
		field.Raw() != `[{"type":"dangerous_tool_use","status":{"type":"unsupported"}}]` {
		t.Fatalf("unary safeguard_results = %s", unary)
	}
}

// With display omitted, the reasoning text is never on the wire; empty
// thinking deltas keep the stream alive and the seal still travels.
func TestSDKAccumulatesOmittedReasoningAsSignatureOnly(t *testing.T) {
	t.Parallel()

	seal := testResponsesSeal()
	reasoning := protocolcore.Reasoning{Text: "hidden summary", Seal: seal}
	block, err := protocolcore.NewReasoningBlock(reasoning)
	if err != nil {
		t.Fatal(err)
	}
	terminal := testClientResponse(block, protocolcore.ContentBlock{Kind: protocolcore.BlockText, Text: "Done."})
	result := runAssembled(t, oracleRequest(protocolcore.ThinkingDisplayOmitted), func(a *protocolpath.Assembler) ([]byte, error) {
		return steps(
			func() ([]byte, error) { return a.Start(protocolpath.StreamStart{ResponseID: "msg_vm_2"}) },
			func() ([]byte, error) { return a.Reasoning("rs", "") },
			func() ([]byte, error) { return a.Reasoning("rs", "hidden") },
			func() ([]byte, error) { return a.Reasoning("rs", "") },
			func() ([]byte, error) { return a.ReasoningDone("rs", reasoning) },
			func() ([]byte, error) { return a.TextDone("msg", "Done.") },
			func() ([]byte, error) { return nil, a.Terminal(terminal) },
		)
	})
	if bytes.Contains(result.bytes, []byte("hidden")) {
		t.Fatalf("omitted reasoning reached the client: %s", result.bytes)
	}
	if !bytes.Contains(result.bytes, []byte(`"delta":{"type":"thinking_delta","thinking":""}`)) {
		t.Fatalf("no heartbeat delta: %s", result.bytes)
	}
	thinking := result.message.Content[0].AsThinking()
	if thinking.Thinking != "" || thinking.Signature == "" || result.message.Content[1].AsText().Text != "Done." {
		t.Fatalf("content = %+v", result.message.Content)
	}
	if _, ok := result.delta["safeguard_results"]; ok {
		t.Fatal("safeguard_results reported for a request that asked for none")
	}
}

func TestSDKAccumulatesRefusalEmptyAndIncompleteResponses(t *testing.T) {
	t.Parallel()

	refusal := testClientResponse(protocolcore.ContentBlock{Kind: protocolcore.BlockRefusal, Refusal: "I can't help with that."})
	result := runAssembled(t, oracleRequest(""), func(a *protocolpath.Assembler) ([]byte, error) {
		return steps(
			func() ([]byte, error) { return a.Start(protocolpath.StreamStart{ResponseID: "msg_vm_3"}) },
			func() ([]byte, error) { return a.Refusal("r", "I can't") },
			func() ([]byte, error) { return a.RefusalDone("r", "I can't help with that.") },
			func() ([]byte, error) { return nil, a.Terminal(refusal) },
		)
	})
	if len(result.message.Content) != 1 || result.message.Content[0].AsText().Text != "I can't help with that." ||
		result.message.StopReason != anthropic.StopReasonEndTurn {
		t.Fatalf("refusal = %+v %s", result.message.Content, result.message.StopReason)
	}
	if got := noticeLines(result.report); strings.Join(got, ",") != "refusal_rendered_as_text $" {
		t.Fatalf("refusal notices = %q", got)
	}

	empty := testClientResponse()
	result = runAssembled(t, oracleRequest(""), func(a *protocolpath.Assembler) ([]byte, error) {
		return steps(
			func() ([]byte, error) { return a.Start(protocolpath.StreamStart{ResponseID: "msg_vm_4"}) },
			func() ([]byte, error) { return nil, a.Terminal(empty) },
		)
	})
	if len(result.message.Content) != 1 || result.message.Content[0].Type != "text" || result.message.Content[0].AsText().Text != "" {
		t.Fatalf("empty = %+v", result.message.Content)
	}

	incomplete := testClientResponse(protocolcore.ContentBlock{Kind: protocolcore.BlockText, Text: "partial"})
	incomplete.StopReason = protocolcore.StopReasonIncomplete
	result = runAssembled(t, oracleRequest(""), func(a *protocolpath.Assembler) ([]byte, error) {
		return steps(
			func() ([]byte, error) { return a.Start(protocolpath.StreamStart{ResponseID: "msg_vm_5"}) },
			func() ([]byte, error) { return a.TextDone("msg", "partial") },
			func() ([]byte, error) { return nil, a.Terminal(incomplete) },
		)
	})
	if result.message.StopReason != anthropic.StopReasonEndTurn {
		t.Fatalf("incomplete stop reason = %s", result.message.StopReason)
	}
	if got := noticeLines(result.report); strings.Join(got, ",") != "stop_reason_approximated $.stop_reason" {
		t.Fatalf("incomplete notices = %q", got)
	}
}

// A request the SDK builds with every block the translated profile maps
// (images, a PDF, a plain-text document, rich tool results with
// tool_reference loads, deferred tools, replayed thinking) decodes with the
// SDK's own wire spelling.
func TestSDKRequestDecodesUnderTheTranslatedProfile(t *testing.T) {
	t.Parallel()

	seal := testResponsesSeal()
	signature, err := protocolcore.FormatReasoningSeal(seal, "")
	if err != nil {
		t.Fatal(err)
	}
	image := anthropic.BetaImageBlockParam{Source: anthropic.BetaImageBlockParamSourceUnion{
		OfBase64: &anthropic.BetaBase64ImageSourceParam{Data: "iVBORw0KGgo=", MediaType: anthropic.BetaBase64ImageSourceMediaTypeImagePNG},
	}}
	pdf := anthropic.BetaRequestDocumentBlockParam{
		Source: anthropic.BetaRequestDocumentBlockSourceUnionParam{OfBase64: &anthropic.BetaBase64PDFSourceParam{Data: "JVBERi0="}},
		Title:  anthropic.String("Spec"),
	}
	schema := anthropic.BetaToolInputSchemaParam{Properties: map[string]any{}}
	params := anthropic.BetaMessageNewParams{
		Model:     "claude-opus-5-5",
		MaxTokens: 128000,
		System: []anthropic.BetaTextBlockParam{
			{Text: "x-anthropic-billing-header: cc_version=2.1.329.580; cch=40026;"},
			{Text: "You are a Claude agent."},
		},
		Messages: []anthropic.BetaMessageParam{
			anthropic.NewBetaUserMessage(
				anthropic.NewBetaTextBlock("compare these"),
				anthropic.BetaContentBlockParamUnion{OfImage: &image},
				anthropic.BetaContentBlockParamUnion{OfDocument: &pdf},
				anthropic.NewBetaDocumentBlock(anthropic.BetaPlainTextSourceParam{Data: "plain body"}),
			),
			{Role: anthropic.BetaMessageParamRoleAssistant, Content: []anthropic.BetaContentBlockParamUnion{
				anthropic.NewBetaThinkingBlock(signature, "plan"),
				anthropic.NewBetaTextBlock("Searching."),
				anthropic.NewBetaToolUseBlock("toolu_search", map[string]any{"query": "select:WebFetch"}, "ToolSearch"),
				anthropic.NewBetaToolUseBlock("toolu_read", map[string]any{"file_path": "a.pdf"}, "Read"),
			}},
			anthropic.NewBetaUserMessage(
				anthropic.BetaContentBlockParamUnion{OfToolResult: &anthropic.BetaToolResultBlockParam{
					ToolUseID: "toolu_search",
					Content: []anthropic.BetaToolResultBlockParamContentUnion{
						{OfToolReference: &anthropic.BetaToolReferenceBlockParam{ToolName: "WebFetch"}},
					},
				}},
				anthropic.BetaContentBlockParamUnion{OfToolResult: &anthropic.BetaToolResultBlockParam{
					ToolUseID: "toolu_read",
					Content: []anthropic.BetaToolResultBlockParamContentUnion{
						{OfText: &anthropic.BetaTextBlockParam{Text: "PDF file read"}},
						{OfDocument: &pdf},
						{OfImage: &image},
					},
				}},
			),
		},
		Tools: []anthropic.BetaToolUnionParam{
			{OfTool: &anthropic.BetaToolParam{Name: "ToolSearch", InputSchema: schema}},
			{OfTool: &anthropic.BetaToolParam{Name: "Read", InputSchema: schema}},
			{OfTool: &anthropic.BetaToolParam{Name: "WebFetch", InputSchema: schema, DeferLoading: anthropic.Bool(true)}},
			{OfTool: &anthropic.BetaToolParam{Name: "DeferredToolPlaceholder", InputSchema: schema, DeferLoading: anthropic.Bool(true)}},
		},
	}
	body, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	request, report, err := newTranslatedTestCodec(t).DecodeClientRequest(body)
	if err != nil {
		t.Fatalf("decode: %v\n%s", err, body)
	}
	if len(request.System) != 1 || request.System[0].Text != "You are a Claude agent." {
		t.Fatalf("system = %+v", request.System)
	}
	if got := shapeOf(request); got.loadedTools != 3 || got.kinds["user/media"] != 3 || got.kinds["part/media"] != 2 ||
		got.kinds["part/tool_reference"] != 1 || got.kinds["assistant/reasoning"] != 1 {
		t.Fatalf("shape = %+v", got)
	}
	if reasoning := request.Messages[1].Blocks[0].Reasoning; reasoning == nil || reasoning.Seal != seal || reasoning.Text != "plan" {
		t.Fatalf("reasoning = %+v", reasoning)
	}
	if got := noticeLines(report); strings.Join(got, ",") != "client_attribution_not_forwarded $.system[0]" {
		t.Fatalf("notices = %q", got)
	}
}
