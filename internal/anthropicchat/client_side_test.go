package anthropicchat

import (
	"bytes"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
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
