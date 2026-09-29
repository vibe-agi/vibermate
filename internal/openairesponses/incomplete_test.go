package openairesponses

import (
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
