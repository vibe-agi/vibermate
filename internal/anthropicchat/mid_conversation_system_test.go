package anthropicchat_test

import (
	"encoding/json"
	"testing"

	"github.com/vibe-agi/vibermate/internal/anthropicchat"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

// Claude Code sends system messages inside the message list, not only as the
// top-level `system` parameter. Refusing them made the client's very first
// request fail, so the client could not use vibermate at all.
//
// Design 07 §3.2 normalizes roles to system | developer | user | assistant |
// tool and requires the original order and position to survive, which is
// exactly what an instruction placed mid-conversation means.
func TestAMidConversationSystemMessageIsCarriedInPlace(t *testing.T) {
	t.Parallel()

	codec, err := anthropicchat.New(anthropicchat.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{
		"model":"claude-3-5-sonnet",
		"max_tokens":64,
		"messages":[
			{"role":"user","content":[{"type":"text","text":"first"}]},
			{"role":"system","content":[{"type":"text","text":"an instruction"}]},
			{"role":"user","content":[{"type":"text","text":"second"}]}
		]
	}`)
	decoded, _, err := codec.DecodeClientRequest(body)
	if err != nil {
		t.Fatalf("a mid-conversation system message was refused: %v", err)
	}
	messages := decoded.Messages
	if len(messages) != 3 {
		t.Fatalf("message count = %d", len(messages))
	}
	if messages[1].Role != protocolcore.RoleSystem {
		t.Fatalf("role = %q", messages[1].Role)
	}
	if messages[1].Blocks[0].Text != "an instruction" {
		t.Fatalf("instruction = %q", messages[1].Blocks[0].Text)
	}
	// Position is the point. An instruction hoisted to the front would change
	// what the model was told and when.
	if messages[0].Blocks[0].Text != "first" ||
		messages[2].Blocks[0].Text != "second" {
		t.Fatalf("order changed: %+v", messages)
	}
}

// OpenAI chat carries a system message natively, so this crosses without loss.
func TestAMidConversationSystemMessageReachesTheBackendInPlace(t *testing.T) {
	t.Parallel()

	codec, err := anthropicchat.New(anthropicchat.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{
		"model":"claude-3-5-sonnet",
		"max_tokens":64,
		"messages":[
			{"role":"user","content":[{"type":"text","text":"first"}]},
			{"role":"system","content":[{"type":"text","text":"an instruction"}]}
		]
	}`)
	decoded, _, err := codec.DecodeClientRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _, err := codec.EncodeProviderRequest(decoded)
	if err != nil {
		t.Fatalf("encode for the backend: %v", err)
	}
	var wire struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire.Messages) != 2 {
		t.Fatalf("backend messages = %+v", wire.Messages)
	}
	if wire.Messages[1].Role != "system" ||
		wire.Messages[1].Content != "an instruction" {
		t.Fatalf("backend instruction = %+v", wire.Messages[1])
	}
}
