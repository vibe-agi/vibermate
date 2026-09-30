package openairesponses

import (
	"bytes"
	"context"
	"testing"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

// A legal incomplete terminal keeps its meaning instead of being replaced by
// a proxy error; the provider's own reason stays in the original wire.
func TestIncompleteTerminalsKeepTheirMeaning(t *testing.T) {
	t.Parallel()
	text := `{"id":"msg_1","type":"message","status":"incomplete","role":"assistant","content":[{"type":"output_text","text":"partial","annotations":[]}]}`
	call := `{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"shell","arguments":"{}"}`
	for _, test := range []struct {
		name, output, reason string
		want                 protocolcore.StopReason
	}{
		{"content filter", text, "content_filter", protocolcore.StopReasonRefusal},
		{"output limit after a tool call", text + "," + call, "max_output_tokens", protocolcore.StopReasonMaxTokens},
		{"unmodelled reason", text, "future_reason", protocolcore.StopReasonIncomplete},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(`{"id":"resp_1","created_at":1,"status":"incomplete","incomplete_details":{"reason":"` + test.reason +
				`"},"model":"provider-model","output":[` + test.output + `],"usage":{}}`)
			response, _, err := newTestCodec(t).DecodeProviderResponse(streamingRequestFixture(t), body)
			if err != nil {
				t.Fatalf("DecodeProviderResponse() error = %v", err)
			}
			if response.StopReason != test.want {
				t.Fatalf("stop reason = %q, want %q", response.StopReason, test.want)
			}
		})
	}
}

// Codex 0.159 consumes incomplete/interrupted as a terminal and can receive it
// before any output item. Keep the native event and usage, without inventing text.
func TestEmptyNativeTerminalsPreserveWireAndUsage(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"completed", "incomplete"} {
		t.Run(status, func(t *testing.T) {
			body := []byte(`{"id":"resp_empty","created_at":1,"status":"` + status + `","model":"provider-model","output":[],"usage":{"input_tokens":5,"output_tokens":0,"input_tokens_details":{"cached_tokens":2}},"incomplete_details":{"reason":"interrupted"}}`)
			response, _, err := newTestCodec(t).DecodeProviderResponse(streamingRequestFixture(t), body)
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Blocks) != 0 || !response.Usage.Output.Known || response.Usage.Output.Tokens != 0 || response.Usage.InputUncached.Tokens != 3 {
				t.Fatalf("empty terminal changed its meaning: %+v", response)
			}
			request := streamingRequestFixture(t)
			request.EffectiveModel = request.RequestedModel
			stream, err := newTestCodec(t).NewProviderStream(request)
			if err != nil {
				t.Fatal(err)
			}
			wire := append([]byte(`data: {"type":"response.`+status+`","response":`), body...)
			wire = append(wire, []byte("}\n\n")...)
			_, err = stream.Feed(context.Background(), wire)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := stream.FinishDecoded(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(decoded.DecodedResponse().Blocks) != 0 {
				t.Fatalf("terminal = %+v", decoded.DecodedResponse())
			}
			held, err := decoded.Approve()
			if err != nil || !bytes.Equal(held, wire) {
				t.Fatalf("native wire changed: %s, %v", held, err)
			}
		})
	}
}

func TestEmptyMessageOutputIsNotAMissingContentField(t *testing.T) {
	t.Parallel()
	for _, content := range []string{`[]`, `null`} {
		body := []byte(`{"id":"resp_empty","created_at":1,"model":"provider-model","status":"incomplete","incomplete_details":{"reason":"interrupted"},"output":[{"type":"message","id":"msg_empty","role":"assistant","status":"incomplete","content":` + content + `}]}`)
		response, _, err := newTestCodec(t).DecodeProviderResponse(streamingRequestFixture(t), body)
		if content == `null` {
			if err == nil {
				t.Fatal("null content was treated as an empty array")
			}
		} else if err != nil || len(response.Blocks) != 0 || response.StopReason != protocolcore.StopReasonIncomplete {
			t.Fatalf("empty interrupted message: %+v, %v", response, err)
		}
	}
}
