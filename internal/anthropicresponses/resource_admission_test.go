package anthropicresponses

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
)

// A long translated history is carried whole when the pair's policy admits
// it, even when the nested side options name a conflicting policy, and the
// path keeps the policy it was built with when the caller's copy changes.
func TestPathResourceAdmissionTranslatedLongHistory(t *testing.T) {
	t.Parallel()
	policy := protocolcore.ResourceLimits{
		Request:  protocolcore.ResourceCost{PayloadBytes: 128 << 20, StructureBytes: 128 << 20},
		Response: protocolcore.ResourceCost{PayloadBytes: 128 << 20, StructureBytes: 128 << 20},
	}
	options := DefaultOptions()
	options.Resources = &policy
	conflict := protocolcore.ResourceLimits{}
	options.Client.Resources = &conflict
	options.Provider.Resources = &conflict
	path, err := NewProtocolPath(options)
	if err != nil {
		t.Fatal(err)
	}
	policy.Request = protocolcore.ResourceCost{PayloadBytes: 1, StructureBytes: 1}

	var wire strings.Builder
	wire.WriteString(`{"model":"claude-opus-5-5","max_tokens":1024,"stream":true,"messages":[`)
	const turns = 4111
	for index := 0; index < turns; index++ {
		if index > 0 {
			wire.WriteByte(',')
		}
		role := "user"
		if index%2 == 1 {
			role = "assistant"
		}
		fmt.Fprintf(&wire, `{"role":%q,"content":"history %d"}`, role, index)
	}
	wire.WriteString(`]}`)
	request, _, err := path.Client().DecodeRequest([]byte(wire.String()))
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Messages) != turns {
		t.Fatalf("decoded %d messages, want %d", len(request.Messages), turns)
	}
	request, err = request.WithEffectiveModel("gpt-upstream")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _, err := path.EncodeProviderRequestFor(protocolpath.ProviderContext{}, request, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Input []json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(encoded.Body(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Input) != turns {
		t.Fatalf("encoded %d input items, want %d", len(body.Input), turns)
	}
	if err := path.ValidateTransformedRequest(encoded.Body()); err != nil {
		t.Fatalf("re-admission refused the long history: %v", err)
	}
	if _, err := path.NewStreamFor(protocolpath.ProviderContext{}, request); err != nil {
		t.Fatal(err)
	}
}

// A finite structure budget refuses a client body before its values are
// materialised, and re-admission applies the same budget to the post-script
// provider body.
func TestPathResourceAdmissionStructureBeforeAllocation(t *testing.T) {
	t.Parallel()
	policy := protocolcore.ResourceLimits{
		Request:  protocolcore.ResourceCost{PayloadBytes: 128 << 20, StructureBytes: 1 << 20},
		Response: protocolcore.ResourceCost{PayloadBytes: 128 << 20, StructureBytes: 1 << 20},
	}
	options := DefaultOptions()
	options.Resources = &policy
	path, err := NewProtocolPath(options)
	if err != nil {
		t.Fatal(err)
	}
	small := `{"model":"claude-opus-5-5","max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	if _, _, err := path.Client().DecodeRequest([]byte(small)); err != nil {
		t.Fatalf("a small request was refused: %v", err)
	}
	values := func(count int) string {
		var builder strings.Builder
		for index := 0; index < count; index++ {
			if index > 0 {
				builder.WriteByte(',')
			}
			builder.WriteString(`{"role":"user","content":"x"}`)
		}
		return builder.String()
	}
	large := `{"model":"claude-opus-5-5","max_tokens":1024,"stream":true,"messages":[` + values(20000) + `]}`
	if _, _, err := path.Client().DecodeRequest([]byte(large)); err == nil {
		t.Fatal("the client side admitted a body beyond its structure budget")
	}
	provider := `{"model":"gpt-upstream","stream":true,"store":false,"input":[` +
		strings.ReplaceAll(values(20000), `{"role":"user","content":"x"}`, `{"type":"message","role":"user","content":"x"}`) + `]}`
	if err := path.ValidateTransformedRequest([]byte(provider)); err == nil {
		t.Fatal("re-admission admitted a body beyond its structure budget")
	}
	// The default policy admits both bodies, so only the budget refused them.
	defaults, err := NewProtocolPath(DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := defaults.Client().DecodeRequest([]byte(large)); err != nil {
		t.Fatalf("the default policy refused the large client body: %v", err)
	}
	if err := defaults.ValidateTransformedRequest([]byte(provider)); err != nil {
		t.Fatalf("the default policy refused the large provider body: %v", err)
	}
}
