package openairesponses

import (
	"fmt"
	"strings"
	"testing"
)

func TestCompatibleResponsesAllowsOptionalTurnMetadata(t *testing.T) {
	for _, metadata := range []string{`null`, `{}`, `{"turn_id":null}`, `{"turn_id":""}`, `{"other":"opaque"}`} {
		t.Run(metadata, func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"model":"test-model","store":false,"input":[
				{"type":"message","role":"assistant","content":"older","internal_chat_message_metadata_passthrough":{"turn_id":"older-turn"}},
				{"type":"message","role":"user","content":"hello","internal_chat_message_metadata_passthrough":%s}
			]}`, metadata))
			request, _, err := newTestCodec(t).DecodeCompatibleClientRequest(body)
			if err != nil {
				t.Fatalf("optional metadata rejected: %v", err)
			}
			if len(request.Messages) != 2 {
				t.Fatalf("message count = %d", len(request.Messages))
			}
			for _, evidence := range request.ProtocolEvidence {
				if evidence.Name == "openai_responses.turn_id" {
					t.Fatalf("unknown current turn inherited historical identity: %+v", evidence)
				}
			}
		})
	}
}

func TestCompatibleResponsesStillRejectsMalformedTurnMetadata(t *testing.T) {
	for _, metadata := range []string{
		`[]`, `"not-object"`, `{"turn_id":42}`, `{"turn_id":{}}`,
		`{"turn_id":"a","turn_id":"b"}`, `{"turn_id":"line\nbreak"}`,
		`{"turn_id":"` + strings.Repeat("x", 513) + `"}`,
	} {
		body := []byte(`{"model":"test-model","input":[{"type":"message","role":"user","content":"hello","internal_chat_message_metadata_passthrough":` + metadata + `}]}`)
		if _, _, err := newTestCodec(t).DecodeCompatibleClientRequest(body); err == nil {
			t.Fatal("malformed metadata was accepted")
		}
	}
}
