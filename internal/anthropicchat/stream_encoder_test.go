package anthropicchat

import (
	"bytes"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
	"github.com/vibe-agi/vibermate/internal/ssewire"
)

func newTestClientStreamEncoder(t *testing.T, request protocolcore.Request) *clientStreamEncoder {
	t.Helper()
	if request.RequestedModel == "" {
		request.RequestedModel = "claude-opus-5-5"
	}
	encoder, err := NewClientStreamEncoder(request)
	if err != nil {
		t.Fatal(err)
	}
	return encoder.(*clientStreamEncoder)
}

func testResponsesSeal() protocolcore.ReasoningSeal {
	return protocolcore.ReasoningSeal{
		Format:  protocolcore.SealResponsesEncrypted,
		ItemID:  "rs_a2",
		Model:   "gpt-upstream",
		Binding: "0123456789abcdef0123456789abcdef",
		Opaque:  "ENC-1",
	}
}

func testToolCall(t *testing.T, id, name, arguments string) protocolcore.ToolCall {
	t.Helper()
	key, err := protocolcore.NewCallKey("openai-responses-call", id)
	if err != nil {
		t.Fatal(err)
	}
	document, err := protocolcore.NewJSONObject([]byte(arguments), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return protocolcore.ToolCall{Key: key, Name: name, Arguments: document}
}

func sseEvents(t *testing.T, stream []byte) []ssewire.Event {
	t.Helper()
	decoder, err := ssewire.NewDecoder(ssewire.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	events, err := decoder.Feed(stream)
	if err != nil {
		t.Fatal(err)
	}
	if err := decoder.Finish(); err != nil {
		t.Fatal(err)
	}
	return events
}

// mustEncoded returns a check that fails t on an encoder error.
func mustEncoded(t *testing.T) func([]byte, error) []byte {
	return func(encoded []byte, err error) []byte {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
}

// For text, tool calls and the terminal the validating encoder writes the
// Chat path's bytes exactly, so Claude Code sees one Anthropic stream shape
// whatever the upstream.
func TestClientStreamEncoderWritesTheChatPathsBytes(t *testing.T) {
	t.Parallel()
	mustEncode := mustEncoded(t)

	request := protocolcore.Request{RequestedModel: "claude-opus-5-5"}
	stateless := newAnthropicStreamEncoder(request)
	validating := newTestClientStreamEncoder(t, request)
	call := testToolCall(t, "call_1", "Bash", `{"command":"ls"}`)
	response := testClientResponse(protocolcore.ContentBlock{Kind: protocolcore.BlockText, Text: "hi"})
	response.Usage = protocolcore.Usage{
		InputUncached: protocolcore.UsageValue{Tokens: 7, Known: true},
		Output:        protocolcore.UsageValue{Tokens: 3, Known: true},
	}
	steps := []func(protocolpath.ClientStreamEncoder) ([]byte, error){
		func(encoder protocolpath.ClientStreamEncoder) ([]byte, error) {
			return encoder.Start(protocolpath.StreamStart{ResponseID: "msg_1"})
		},
		func(encoder protocolpath.ClientStreamEncoder) ([]byte, error) { return encoder.StartText(0) },
		func(encoder protocolpath.ClientStreamEncoder) ([]byte, error) {
			return encoder.AppendText(0, "<hi> & \u00e9")
		},
		func(encoder protocolpath.ClientStreamEncoder) ([]byte, error) { return encoder.StopText(0) },
		func(encoder protocolpath.ClientStreamEncoder) ([]byte, error) { return encoder.ToolCall(1, call) },
		func(encoder protocolpath.ClientStreamEncoder) ([]byte, error) { return encoder.Terminal(response) },
	}
	for index, step := range steps {
		want := mustEncode(step(stateless))
		got := mustEncode(step(validating))
		if !bytes.Equal(got, want) {
			t.Fatalf("step %d differs\n got: %s\nwant: %s", index, got, want)
		}
	}
	if notices := validating.TranslationReport().Notices(); len(notices) != 0 {
		t.Fatalf("notices = %+v", notices)
	}
}

func TestClientStreamEncoderWritesThinkingWithItsSeal(t *testing.T) {
	t.Parallel()
	mustEncode := mustEncoded(t)

	seal := testResponsesSeal()
	signature, err := protocolcore.FormatReasoningSeal(seal, "")
	if err != nil {
		t.Fatal(err)
	}
	encoder := newTestClientStreamEncoder(t, protocolcore.Request{})
	var stream bytes.Buffer
	stream.Write(mustEncode(encoder.Start(protocolpath.StreamStart{ResponseID: "msg_1"})))
	stream.Write(mustEncode(encoder.StartReasoning(0)))
	stream.Write(mustEncode(encoder.AppendReasoning(0, "first")))
	stream.Write(mustEncode(encoder.AppendReasoning(0, "")))
	stream.Write(mustEncode(encoder.StopReasoning(0, protocolcore.Reasoning{Text: "first", Seal: seal})))
	stream.Write(mustEncode(encoder.StartReasoning(1)))
	stream.Write(mustEncode(encoder.StopReasoning(1, protocolcore.Reasoning{Text: "unsealed"})))
	events := sseEvents(t, stream.Bytes())
	want := []string{
		`content_block_start {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`content_block_delta {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"first"}}`,
		`content_block_delta {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":""}}`,
		`content_block_delta {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"` + signature + `"}}`,
		`content_block_stop {"type":"content_block_stop","index":0}`,
		`content_block_start {"type":"content_block_start","index":1,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`content_block_stop {"type":"content_block_stop","index":1}`,
	}
	if len(events) != len(want)+1 {
		t.Fatalf("events = %d, want %d", len(events), len(want)+1)
	}
	for index, line := range want {
		if got := events[index+1].Name + " " + string(events[index+1].Data); got != line {
			t.Fatalf("event %d\n got: %s\nwant: %s", index, got, line)
		}
	}
	// An Anthropic signature is written as it came.
	anthropic := newTestClientStreamEncoder(t, protocolcore.Request{})
	mustEncode(anthropic.Start(protocolpath.StreamStart{ResponseID: "msg_1"}))
	mustEncode(anthropic.StartReasoning(0))
	stop := mustEncode(anthropic.StopReasoning(0, protocolcore.Reasoning{Text: "x",
		Seal: protocolcore.ReasoningSeal{Format: protocolcore.SealAnthropicSignature, Opaque: "EqQBCkgI"}}))
	if !strings.Contains(string(stop), `"signature":"EqQBCkgI"`) {
		t.Fatalf("Anthropic signature: %s", stop)
	}
}

func TestClientStreamEncoderReportsUnservedSafeguardsAndApproximation(t *testing.T) {
	t.Parallel()
	mustEncode := mustEncoded(t)

	encoder := newTestClientStreamEncoder(t, protocolcore.Request{
		UnservedSafeguards: []protocolcore.SafeguardKind{protocolcore.SafeguardDangerousToolUse},
	})
	mustEncode(encoder.Start(protocolpath.StreamStart{ResponseID: "msg_1"}))
	response := testClientResponse()
	response.StopReason = protocolcore.StopReasonIncomplete
	events := sseEvents(t, mustEncode(encoder.Terminal(response)))
	if len(events) != 2 || events[0].Name != "message_delta" || events[1].Name != "message_stop" {
		t.Fatalf("events = %+v", events)
	}
	want := `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null,"container":null,"stop_details":null,` +
		`"safeguard_results":[{"type":"dangerous_tool_use","status":{"type":"unsupported"}}]},` +
		`"usage":{"input_tokens":0,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}`
	if string(events[0].Data) != want {
		t.Fatalf("message_delta\n got: %s\nwant: %s", events[0].Data, want)
	}
	notices := encoder.TranslationReport().Notices()
	if len(notices) != 1 || notices[0].Code != protocolcore.NoticeStopReasonApproximated || notices[0].Path != "$.stop_reason" {
		t.Fatalf("notices = %+v", notices)
	}
}

func TestClientStreamEncoderRejectsInvalidOrdering(t *testing.T) {
	t.Parallel()

	call := testToolCall(t, "call_1", "Bash", `{}`)
	// prepare steps must succeed; an error there panics the subtest.
	mustEncode := func(_ []byte, err error) {
		if err != nil {
			panic(err)
		}
	}
	started := func(encoder *clientStreamEncoder) {
		mustEncode(encoder.Start(protocolpath.StreamStart{ResponseID: "msg_1"}))
	}
	tests := map[string]struct {
		request protocolcore.Request
		prepare func(*clientStreamEncoder)
		step    func(*clientStreamEncoder) ([]byte, error)
		reason  protocolcore.Reason
	}{
		"start twice": {prepare: started, step: func(e *clientStreamEncoder) ([]byte, error) {
			return e.Start(protocolpath.StreamStart{ResponseID: "msg_1"})
		}},
		"text before start": {step: func(e *clientStreamEncoder) ([]byte, error) { return e.StartText(0) }},
		"first index is not zero": {prepare: started,
			step: func(e *clientStreamEncoder) ([]byte, error) { return e.StartText(1) }},
		"second block while one is open": {prepare: func(e *clientStreamEncoder) { started(e); mustEncode(e.StartText(0)) },
			step: func(e *clientStreamEncoder) ([]byte, error) { return e.StartReasoning(1) }},
		"index reused after close": {prepare: func(e *clientStreamEncoder) {
			started(e)
			mustEncode(e.StartText(0))
			mustEncode(e.StopText(0))
		}, step: func(e *clientStreamEncoder) ([]byte, error) { return e.StartText(0) }},
		"text into a closed block": {prepare: func(e *clientStreamEncoder) {
			started(e)
			mustEncode(e.StartText(0))
			mustEncode(e.StopText(0))
		}, step: func(e *clientStreamEncoder) ([]byte, error) { return e.AppendText(0, "x") }},
		"reasoning into a text block": {prepare: func(e *clientStreamEncoder) { started(e); mustEncode(e.StartText(0)) },
			step: func(e *clientStreamEncoder) ([]byte, error) { return e.AppendReasoning(0, "x") }},
		"text stop for a thinking block": {prepare: func(e *clientStreamEncoder) { started(e); mustEncode(e.StartReasoning(0)) },
			step: func(e *clientStreamEncoder) ([]byte, error) { return e.StopText(0) }},
		"tool call while a block is open": {prepare: func(e *clientStreamEncoder) { started(e); mustEncode(e.StartText(0)) },
			step: func(e *clientStreamEncoder) ([]byte, error) { return e.ToolCall(1, call) }},
		"tool call skips an index": {prepare: started,
			step: func(e *clientStreamEncoder) ([]byte, error) { return e.ToolCall(1, call) }},
		"terminal while a block is open": {prepare: func(e *clientStreamEncoder) { started(e); mustEncode(e.StartReasoning(0)) },
			step: func(e *clientStreamEncoder) ([]byte, error) { return e.Terminal(testClientResponse()) }},
		"content after the terminal": {prepare: func(e *clientStreamEncoder) { started(e); mustEncode(e.Terminal(testClientResponse())) },
			step: func(e *clientStreamEncoder) ([]byte, error) { return e.StartText(0) }},
		"terminal twice": {prepare: func(e *clientStreamEncoder) { started(e); mustEncode(e.Terminal(testClientResponse())) },
			step: func(e *clientStreamEncoder) ([]byte, error) { return e.Terminal(testClientResponse()) }},
		"reasoning text with display omitted": {
			request: protocolcore.Request{Reasoning: protocolcore.ReasoningIntent{
				Thinking: protocolcore.ThinkingModeAdaptive, Display: protocolcore.ThinkingDisplayOmitted,
			}},
			prepare: func(e *clientStreamEncoder) { started(e); mustEncode(e.StartReasoning(0)) },
			step:    func(e *clientStreamEncoder) ([]byte, error) { return e.AppendReasoning(0, "secret") }},
		"redacted reasoning after a thinking start": {
			prepare: func(e *clientStreamEncoder) { started(e); mustEncode(e.StartReasoning(0)) },
			step: func(e *clientStreamEncoder) ([]byte, error) {
				return e.StopReasoning(0, protocolcore.Reasoning{Redacted: true,
					Seal: protocolcore.ReasoningSeal{Format: protocolcore.SealAnthropicRedacted, Opaque: "x"}})
			},
			reason: protocolcore.ReasonUnsupportedProviderData},
		"invalid reasoning": {
			prepare: func(e *clientStreamEncoder) { started(e); mustEncode(e.StartReasoning(0)) },
			step: func(e *clientStreamEncoder) ([]byte, error) {
				return e.StopReasoning(0, protocolcore.Reasoning{TextSource: "guess"})
			},
			reason: protocolcore.ReasonInvalidProviderResponse},
		"custom tool call": {prepare: started, step: func(e *clientStreamEncoder) ([]byte, error) {
			key, _ := protocolcore.NewCallKey("openai-responses-call", "call_2")
			return e.ToolCall(0, protocolcore.ToolCall{Kind: protocolcore.ToolKindCustom, Key: key, Name: "apply", Input: "x"})
		}, reason: protocolcore.ReasonUnsupportedProviderData},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			encoder := newTestClientStreamEncoder(t, test.request)
			if test.prepare != nil {
				test.prepare(encoder)
			}
			want := test.reason
			if want == "" {
				want = protocolcore.ReasonStreamStateViolation
			}
			encoded, err := test.step(encoder)
			if protocolcore.ReasonOf(err) != want || encoded != nil {
				t.Fatalf("error = %v (%d bytes), want %s", err, len(encoded), want)
			}
			// A failed encoder stays failed.
			if _, err := encoder.Terminal(testClientResponse()); protocolcore.ReasonOf(err) != protocolcore.ReasonStreamStateViolation {
				t.Fatalf("encoder recovered after a failure: %v", err)
			}
		})
	}
	if _, err := NewClientStreamEncoder(protocolcore.Request{}); err == nil {
		t.Fatal("an encoder without the requested model was built")
	}
}
