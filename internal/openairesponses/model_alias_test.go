package openairesponses

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/vibe-agi/vibermate/internal/ssewire"
)

func TestNativeModelAliasLeavesStreamAuditIdentityTruthful(t *testing.T) {
	for _, test := range []struct{ reported, client string }{
		{"provider-model", "gpt-5.6-sol"}, {"restricted-model", "restricted-model"},
	} {
		t.Run(test.reported, func(t *testing.T) {
			stream, err := newTestCodec(t).NewProviderStream(streamingRequestFixture(t))
			if err != nil {
				t.Fatal(err)
			}
			wire := appendResponseEvent(t, "response.metadata", map[string]any{"type": "response.metadata", "headers": map[string]string{"openai-model": test.reported}})
			wire = append(wire, appendResponseEvent(t, "response.completed", map[string]any{"type": "response.completed", "response": map[string]any{
				"id": "r", "created_at": 1, "status": "completed", "model": test.reported, "output": []any{}, "usage": map[string]any{},
			}})...)
			released, err := stream.Feed(context.Background(), wire)
			if err != nil {
				t.Fatal(err)
			}
			pending, err := stream.FinishDecoded(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			response := pending.DecodedResponse()
			if response.ReportedModel != test.reported || response.EffectiveModel != "provider-model" {
				t.Fatalf("audit identity changed: %+v", response)
			}
			terminal, err := pending.Approve()
			if err != nil {
				t.Fatal(err)
			}
			released = append(released, terminal...)
			if !bytes.Contains(released, []byte(`"openai-model":"`+test.client+`"`)) || !bytes.Contains(released, []byte(`"model":"`+test.client+`"`)) {
				t.Fatalf("client identity = %s", released)
			}
		})
	}
}

func TestNativeEventModelAliasPreservesServerReroutes(t *testing.T) {
	for _, test := range []struct{ name, body, want string }{
		{"mapped model", `{"type":"response.created","response":{"model":"provider-model","id":"r"}}`, `{"type":"response.created","response":{"model":"gpt-5.6-sol","id":"r"}}`},
		{"real reroute", `{"type":"response.created","response":{"model":"restricted-model","id":"r"}}`, `{"type":"response.created","response":{"model":"restricted-model","id":"r"}}`},
		{"metadata headers", `{"type":"response.metadata","headers":{"openai-model":"provider-model","x-private":"unchanged"},"metadata":{"reason":"cyber"}}`, `{"type":"response.metadata","headers":{"openai-model":"gpt-5.6-sol","x-private":"unchanged"},"metadata":{"reason":"cyber"}}`},
		{"nested headers", `{"type":"response.created","headers":{"openai-model":"restricted-model"},"response":{"model":"provider-model","headers":{"X-OpenAI-Model":["provider-model"],"other":42}}}`, `{"type":"response.created","headers":{"openai-model":"restricted-model"},"response":{"model":"gpt-5.6-sol","headers":{"X-OpenAI-Model":["gpt-5.6-sol"],"other":42}}}`},
		{"metadata genuine reroute", `{"type":"response.metadata","headers":{"openai-model":"restricted-model"}}`, `{"type":"response.metadata","headers":{"openai-model":"restricted-model"}}`},
		{"unknown header representation", `{"type":"response.metadata","headers":{"openai-model":{"future":"provider-model"}}}`, `{"type":"response.metadata","headers":{"openai-model":{"future":"provider-model"}}}`},
		{"nested header array", `{"type":"response.metadata","headers":{"openai-model":[["provider-model",null],"provider-model"]}}`, `{"type":"response.metadata","headers":{"openai-model":[["gpt-5.6-sol",null],"provider-model"]}}`},
		{"first header value rerouted", `{"type":"response.metadata","headers":{"openai-model":["restricted-model","provider-model"]}}`, `{"type":"response.metadata","headers":{"openai-model":["restricted-model","provider-model"]}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			stream, err := newTestCodec(t).NewProviderStream(streamingRequestFixture(t))
			if err != nil {
				t.Fatal(err)
			}
			source := []byte(test.body)
			event, err := stream.clientEvent(ssewire.Event{Data: source})
			if err != nil {
				t.Fatal(err)
			}
			var got, want any
			if json.Unmarshal(event.Data, &got) != nil || json.Unmarshal([]byte(test.want), &want) != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("downstream event = %s, want %s", event.Data, test.want)
			}
			if !bytes.Equal(source, []byte(test.body)) {
				t.Fatal("upstream event evidence changed")
			}
			if test.body == test.want && !bytes.Equal(event.Data, source) {
				t.Fatal("unchanged event lost its exact payload")
			}
		})
	}
}
