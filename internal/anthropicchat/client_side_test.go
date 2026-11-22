package anthropicchat

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

func testClientResponse(blocks ...protocolcore.ContentBlock) protocolcore.Response {
	return protocolcore.Response{
		ID:             "msg_vm_test",
		RequestedModel: "claude-opus-5-5",
		EffectiveModel: "gpt-upstream",
		ReportedModel:  "gpt-upstream",
		Blocks:         blocks,
		StopReason:     protocolcore.StopReasonEndTurn,
	}
}

// An empty answer is still a text block, and Anthropic's text block requires
// its text member: {"type":"text"} without it is not a valid content block.
func TestUnaryEmptyTextBlockCarriesItsText(t *testing.T) {
	t.Parallel()

	codec := newTestCodec(t)
	encoded, err := codec.EncodeClientResponse(testClientResponse(protocolcore.ContentBlock{Kind: protocolcore.BlockText}))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"content":[{"type":"text","text":""}]`)) {
		t.Fatalf("empty text block lost its text member: %s", encoded)
	}
}

// Anthropic has no incomplete terminal. Both Anthropic encoders share one
// mapping, so a unary reply and a stream give the same stop_reason.
func TestIncompleteTerminalReadsAsEndTurnInBothEncoders(t *testing.T) {
	t.Parallel()

	codec := newTestCodec(t)
	response := testClientResponse(protocolcore.ContentBlock{Kind: protocolcore.BlockText, Text: "partial"})
	response.StopReason = protocolcore.StopReasonIncomplete
	unary, err := codec.EncodeClientResponse(response)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(unary, []byte(`"stop_reason":"end_turn"`)) || bytes.Contains(unary, []byte("incomplete")) {
		t.Fatalf("unary stop reason: %s", unary)
	}
	stream, err := newAnthropicStreamEncoder(protocolcore.Request{RequestedModel: "claude-opus-5-5"}).Terminal(response)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stream), `"stop_reason":"end_turn"`) || strings.Contains(string(stream), "incomplete") {
		t.Fatalf("stream stop reason: %s", stream)
	}
	for _, reason := range []protocolcore.StopReason{
		protocolcore.StopReasonEndTurn, protocolcore.StopReasonMaxTokens, protocolcore.StopReasonToolUse,
		protocolcore.StopReasonStopSequence, protocolcore.StopReasonRefusal, protocolcore.StopReasonPauseTurn,
		protocolcore.StopReasonContextLimit,
	} {
		if value, approximated := anthropicStopReason(reason); value != string(reason) || approximated {
			t.Fatalf("%s maps to %q (approximated %v)", reason, value, approximated)
		}
	}
}

func TestClientSideUnaryWritesThinkingRefusalAndSafeguards(t *testing.T) {
	t.Parallel()

	side, err := NewClientSide(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	seal := protocolcore.ReasoningSeal{
		Format: protocolcore.SealResponsesEncrypted, ItemID: "rs_a2", Model: "gpt-upstream",
		Binding: "0123456789abcdef0123456789abcdef", Opaque: "ENC-1",
	}
	signature, err := protocolcore.FormatReasoningSeal(seal, protocolcore.ReasoningTextContent)
	if err != nil {
		t.Fatal(err)
	}
	reasoning := func(value protocolcore.Reasoning) protocolcore.ContentBlock {
		block, err := protocolcore.NewReasoningBlock(value)
		if err != nil {
			t.Fatal(err)
		}
		return block
	}
	key, _ := protocolcore.NewCallKey("openai-responses-call", "call_1")
	arguments, _ := protocolcore.NewJSONObject([]byte(`{"command":"ls"}`), 1<<10)
	response := testClientResponse(
		reasoning(protocolcore.Reasoning{Text: "first\n\nsecond", TextSource: protocolcore.ReasoningTextContent, Seal: seal}),
		reasoning(protocolcore.Reasoning{Text: "native", Seal: protocolcore.ReasoningSeal{Format: protocolcore.SealAnthropicSignature, Opaque: "EqQBCkgI"}}),
		reasoning(protocolcore.Reasoning{Text: "unsealed"}),
		reasoning(protocolcore.Reasoning{Redacted: true, Seal: protocolcore.ReasoningSeal{Format: protocolcore.SealAnthropicRedacted, Opaque: "REDACTED"}}),
		protocolcore.ContentBlock{Kind: protocolcore.BlockText, Text: "commentary"},
		protocolcore.ContentBlock{Kind: protocolcore.BlockRefusal, Refusal: "I can't help with that."},
		protocolcore.ContentBlock{Kind: protocolcore.BlockToolCall, ToolCall: protocolcore.ToolCall{Key: key, Name: "Bash", Arguments: arguments}},
	)
	response.StopReason = protocolcore.StopReasonToolUse
	request := protocolcore.Request{
		RequestedModel:     "claude-opus-5-5",
		Reasoning:          protocolcore.ReasoningIntent{Thinking: protocolcore.ThinkingModeAdaptive, Display: protocolcore.ThinkingDisplayUpdates},
		UnservedSafeguards: []protocolcore.SafeguardKind{protocolcore.SafeguardDangerousToolUse},
	}
	encoded, report, err := side.EncodeResponse(request, response)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"msg_vm_test","type":"message","role":"assistant","model":"claude-opus-5-5","content":[` +
		`{"type":"thinking","thinking":"first\n\nsecond","signature":"` + signature + `"},` +
		`{"type":"thinking","thinking":"native","signature":"EqQBCkgI"},` +
		`{"type":"thinking","thinking":"unsealed","signature":""},` +
		`{"type":"redacted_thinking","data":"REDACTED"},` +
		`{"type":"text","text":"commentary"},` +
		`{"type":"text","text":"I can't help with that."},` +
		`{"type":"tool_use","id":"call_1","name":"Bash","input":{"command":"ls"}}],` +
		`"stop_reason":"tool_use","stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0},"container":null,"stop_details":null,` +
		`"safeguard_results":[{"type":"dangerous_tool_use","status":{"type":"unsupported"}}]}`
	if string(encoded) != want {
		t.Fatalf("unary\n got: %s\nwant: %s", encoded, want)
	}
	if got := noticeLines(report); strings.Join(got, ",") != "refusal_rendered_as_text $.content[5]" {
		t.Fatalf("notices = %q", got)
	}

	// With display omitted the client asked not to see reasoning text; the
	// seal still travels, as in the stream.
	request.Reasoning.Display = protocolcore.ThinkingDisplayOmitted
	response.Blocks = response.Blocks[:1]
	response.StopReason = protocolcore.StopReasonIncomplete
	encoded, report, err = side.EncodeResponse(request, response)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`{"type":"thinking","thinking":"","signature":"`+signature+`"}`)) ||
		!bytes.Contains(encoded, []byte(`"stop_reason":"end_turn"`)) {
		t.Fatalf("omitted display: %s", encoded)
	}
	if got := noticeLines(report); strings.Join(got, ",") != "stop_reason_approximated $.stop_reason" {
		t.Fatalf("notices = %q", got)
	}

	// The Chat path's unary encoder is unchanged for its own responses: no
	// safeguards, no notices.
	plain, err := newTestCodec(t).EncodeClientResponse(testClientResponse(protocolcore.ContentBlock{Kind: protocolcore.BlockText, Text: "hi"}))
	if err != nil || bytes.Contains(plain, []byte("safeguard")) {
		t.Fatalf("Chat unary: %s %v", plain, err)
	}
}

// fakeResponsesSide is the smallest provider side Compose accepts; its
// stream drives the client encoder through the shared Assembler with one
// scripted response.
type fakeResponsesSide struct {
	class protocolcore.ProviderError
}

func (fakeResponsesSide) Dialect() protocolspec.Dialect { return protocolspec.DialectOpenAIResponses }

func (side fakeResponsesSide) EncodeRequest(request protocolcore.Request) (protocolpath.ProviderRequest, protocolcore.TranslationReport, error) {
	return side.EncodeRequestFor(protocolpath.ProviderContext{}, request)
}

func (side fakeResponsesSide) DecodeResponse(request protocolcore.Request, body []byte) (protocolcore.Response, protocolcore.TranslationReport, error) {
	return side.DecodeResponseFor(protocolpath.ProviderContext{}, request, body)
}

func (fakeResponsesSide) ValidateTransformedRequest([]byte) error { return nil }

func (fakeResponsesSide) EncodeRequestFor(protocolpath.ProviderContext, protocolcore.Request) (protocolpath.ProviderRequest, protocolcore.TranslationReport, error) {
	request, err := protocolpath.NewProviderRequest(http.MethodPost, "v1/responses", nil, []byte("{}"))
	return request, protocolcore.TranslationReport{}, err
}

func (fakeResponsesSide) DecodeResponseFor(protocolpath.ProviderContext, protocolcore.Request, []byte) (protocolcore.Response, protocolcore.TranslationReport, error) {
	return protocolcore.Response{}, protocolcore.TranslationReport{}, errors.New("not used")
}

func (fakeResponsesSide) NewProviderStream(_ protocolpath.ProviderContext, request protocolcore.Request, encoder protocolpath.ClientStreamEncoder) (protocolpath.Stream, error) {
	assembler, err := protocolpath.NewAssembler(request, encoder, protocolpath.AssemblerOptions{})
	if err != nil {
		return nil, err
	}
	return &scriptedStream{assembler: assembler}, nil
}

func (fakeResponsesSide) NewAggregatingStream(protocolpath.ProviderContext, protocolcore.Request) (protocolpath.Stream, error) {
	return nil, errors.New("not used")
}

func (side fakeResponsesSide) ClassifyRejection(protocolcore.Request, int, http.Header, []byte) protocolcore.ProviderError {
	return side.class
}

// scriptedStream answers its first Feed with the text of the fragment as one
// text block and ends on FinishDecoded.
type scriptedStream struct {
	assembler *protocolpath.Assembler
}

func (stream *scriptedStream) Feed(_ context.Context, fragment []byte) ([]byte, error) {
	start, err := stream.assembler.Start(protocolpath.StreamStart{ResponseID: "resp_1"})
	if err != nil {
		return nil, err
	}
	text, err := stream.assembler.TextDone("message", string(fragment))
	if err != nil {
		return nil, err
	}
	return append(start, text...), stream.assembler.Terminal(testClientResponse(protocolcore.ContentBlock{Kind: protocolcore.BlockText, Text: string(fragment)}))
}

func (stream *scriptedStream) SemanticProgress() uint64 { return stream.assembler.SemanticProgress() }

func (*scriptedStream) TerminalReceived() bool { return true }

func (stream *scriptedStream) FinishDecoded(context.Context) (protocolpath.PendingTerminal, error) {
	return stream.assembler.Finish()
}

func composeTestPath(t *testing.T, side *ClientSide, provider fakeResponsesSide) *protocolpath.Path {
	t.Helper()
	identifier, err := protocolspec.NewCodecPairID("anthropic-messages-to-test-responses")
	if err != nil {
		t.Fatal(err)
	}
	operation, err := protocolspec.NewClientOperationID(operationcatalog.AnthropicMessagesCreateID)
	if err != nil {
		t.Fatal(err)
	}
	path, err := protocolpath.Compose(protocolpath.ComposeOptions{
		ID: identifier, Revision: 1, ClientOperationIDs: []protocolspec.ClientOperationID{operation},
		Client: side, Provider: provider,
	})
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestClientSideComposesWithAProviderSide(t *testing.T) {
	t.Parallel()

	options := DefaultOptions()
	options.ClientInput = TranslatedClientInputProfile()
	side, err := NewClientSide(options)
	if err != nil {
		t.Fatal(err)
	}
	var client any = side
	if _, forwards := client.(protocolpath.SourceResponseEncoder); forwards {
		t.Fatal("the client side forwards source responses")
	}
	path := composeTestPath(t, side, fakeResponsesSide{class: protocolcore.ProviderError{
		Class: protocolcore.ProviderErrorReasoningState, Status: 400, Code: "invalid_encrypted_content",
	}})

	// Decode goes through the options' profile.
	request, report, err := path.Client().DecodeRequest([]byte(`{"model":"claude-opus-5-5","max_tokens":64,"stream":true,
		"system":[{"type":"text","text":"x-anthropic-billing-header: cch=1;"},{"type":"text","text":"prompt"}],
		"messages":[{"role":"user","content":"hi"}],"safeguards":[{"type":"dangerous_tool_use"}]}`))
	if err != nil || len(request.System) != 1 || len(request.UnservedSafeguards) != 1 || len(report.Notices()) != 2 {
		t.Fatalf("decode: %v %+v %q", err, request, noticeLines(report))
	}

	// Its stream encoder carries reasoning but leaves refusals to the
	// Assembler, which renders them as text.
	encoder, err := side.NewStreamEncoder(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := encoder.(protocolpath.ReasoningStreamEncoder); !ok {
		t.Fatal("the stream encoder cannot carry reasoning")
	}
	if _, ok := encoder.(protocolpath.RefusalStreamEncoder); ok {
		t.Fatal("the stream encoder claims a refusal block Anthropic does not have")
	}

	// A composed stream runs through the encoder and reports the safeguard.
	stream, err := path.NewStreamFor(protocolpath.ProviderContext{}, request)
	if err != nil {
		t.Fatal(err)
	}
	live, err := stream.Feed(context.Background(), []byte("hello"))
	if err != nil || !bytes.Contains(live, []byte(`"text_delta","text":"hello"`)) {
		t.Fatalf("live: %s %v", live, err)
	}
	terminal, err := stream.FinishDecoded(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release, err := terminal.Approve()
	if err != nil || !bytes.Contains(release, []byte(`"safeguard_results":[{"type":"dangerous_tool_use","status":{"type":"unsupported"}}]`)) {
		t.Fatalf("release: %s %v", release, err)
	}

	// A classified rejection becomes the token Claude Code heals from.
	native, class, ok := path.TranslateProviderRejection(request, 400, nil, nil)
	if !ok || class.Class != protocolcore.ProviderErrorReasoningState ||
		native.ClientStatus(protocolspec.DialectAnthropicMessages) != http.StatusBadRequest ||
		!bytes.Contains(native.ForDialect(protocolspec.DialectAnthropicMessages), []byte(`"message":"capability_rejected: thinking_signature"`)) {
		t.Fatalf("rejection: %v %+v %s", ok, class, native.ForDialect(protocolspec.DialectAnthropicMessages))
	}

	// count_tokens has its own decoder on the side.
	if _, _, err := side.DecodeCountTokensRequest([]byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"foo"}]}`)); err != nil {
		t.Fatal(err)
	}
}
