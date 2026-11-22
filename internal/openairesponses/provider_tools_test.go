package openairesponses

import (
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

// The name rules are pure functions shared with the Chat catalog, pinned by
// fixed vectors so a call in an old transcript keeps mapping to the same
// provider name.
func TestProviderNameVectors(t *testing.T) {
	t.Parallel()
	limit := strings.Repeat("a", 64)
	for _, test := range []struct{ kind, namespace, name, want string }{
		{kind: "function", name: "Read", want: "Read"},
		{kind: "function", name: "mcp__github__create-issue", want: "mcp__github__create-issue"},
		{kind: "function", name: limit, want: limit},
		{kind: "function", name: "my.tool", want: "vm_tool_fc6ab6a8ba0a09923948ab109206364e"},
		{kind: "function", namespace: "ns", name: "tool", want: "ns__tool"},
	} {
		if got := providerToolName(protocolcore.ToolKind(test.kind), test.namespace, test.name); got != test.want {
			t.Errorf("providerToolName(%q, %q) = %q, want %q", test.namespace, test.name, got, test.want)
		}
	}
	if got := providerToolName(protocolcore.ToolKindFunction, "", limit+"a"); !strings.HasPrefix(got, "vm_tool_") || len(got) != len("vm_tool_")+32 {
		t.Errorf("a 65-byte tool name became %q", got)
	}
	if got, rewritten := providerCallID("toolu_01AbC-9"); got != "toolu_01AbC-9" || rewritten {
		t.Errorf("an accepted call id was rewritten to %q", got)
	}
	if got, rewritten := providerCallID("toolu.with.dots"); got != "call_vm_204da7ce8d729d604ac89638" || !rewritten {
		t.Errorf("providerCallID(toolu.with.dots) = %q, %v", got, rewritten)
	}
	if got, _ := providerCallID(strings.Repeat("c", 65)); !strings.HasPrefix(got, "call_vm_") || len(got) != 32 {
		t.Errorf("a 65-byte call id became %q", got)
	}
	// The Chat encoder names the Haiku title schema the same way (S2 golden
	// claude-interactive.003).
	if got := structuredOutputName([]byte(`{"type":"object","properties":{"title":{"type":"string"}},"required":["title"],"additionalProperties":false}`)); got != "vibermate_output_cfc7c525c81648f54a804f29" {
		t.Errorf("structuredOutputName = %q", got)
	}
	if got, ok := promptCacheKey([]protocolcore.ProtocolEvidenceValue{{Name: "claude.session_id", Value: "session-1"}}); !ok || got != "vm1-0b68497ddbdf46c948051b1b5614df90" {
		t.Errorf("promptCacheKey = %q, %v", got, ok)
	}
	if _, ok := promptCacheKey([]protocolcore.ProtocolEvidenceValue{{Name: "claude.agent_id", Value: "agent-1"}}); ok {
		t.Error("a key was derived without a session")
	}
}

func TestRemoveTopLevelSchemaIdentity(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, schema, want string
		removed            bool
	}{
		{name: "leading $schema", schema: `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{}}`, want: `{"type":"object","properties":{}}`, removed: true},
		{name: "both, anywhere", schema: `{"type":"object","$id":"x","properties":{},"$schema":"s"}`, want: `{"type":"object","properties":{}}`, removed: true},
		{name: "nested ones stay", schema: `{"type":"object","properties":{"a":{"$schema":"s","$id":"i"}}}`, want: `{"type":"object","properties":{"a":{"$schema":"s","$id":"i"}}}`},
		{name: "escaped names are the same names", schema: `{"\u0024schema":"s","type":"object"}`, want: `{"type":"object"}`, removed: true},
		{name: "strings that look like members", schema: `{"description":"\"$schema\":\"x\",","type":"object"}`, want: `{"description":"\"$schema\":\"x\",","type":"object"}`},
		{name: "whitespace between members", schema: "{ \"$schema\" : \"s\" ,\n \"type\" : \"object\" , \"items\": [1, {\"$id\": 2}] }", want: `{"type" : "object","items": [1, {"$id": 2}]}`, removed: true},
		{name: "only identity", schema: `{"$schema":"s","$id":"i"}`, want: `{}`, removed: true},
		{name: "empty", schema: `{}`, want: `{}`},
		{name: "literals and numbers", schema: `{"$id":null,"minimum":-1.5e3,"x":true}`, want: `{"minimum":-1.5e3,"x":true}`, removed: true},
	} {
		got, removed, err := removeTopLevelSchemaIdentity([]byte(test.schema))
		if err != nil || string(got) != test.want || removed != test.removed {
			t.Errorf("%s: got %s, %v, %v; want %s, %v", test.name, got, removed, err, test.want, test.removed)
		}
	}
	for _, malformed := range []string{`[]`, `{"a":1`, `{"a" 1}`, `{"a":1}x`, `{"a":"x}`, ``} {
		if _, _, err := removeTopLevelSchemaIdentity([]byte(malformed)); err == nil {
			t.Errorf("malformed schema %q was accepted", malformed)
		}
	}
}

func TestStrictSchemaCompatible(t *testing.T) {
	t.Parallel()
	for schema, want := range map[string]bool{
		`{"type":"object","properties":{"title":{"type":"string"}},"required":["title"],"additionalProperties":false}`:                                                               true,
		`{"type":"object","properties":{"title":{"type":"string"}},"required":["title"]}`:                                                                                            false,
		`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}},"required":["a"],"additionalProperties":false}`:                                                 false,
		`{"type":"object","properties":{"a":{"type":"object","properties":{"b":{"type":"string"}}}},"required":["a"],"additionalProperties":false}`:                                  false,
		`{"type":"object","properties":{"a":{"type":"array","items":{"type":"object","properties":{},"additionalProperties":false}}},"required":["a"],"additionalProperties":false}`: true,
		`{"type":["object","null"],"properties":{},"additionalProperties":true}`:                                                                                                     false,
		`{"anyOf":[{"type":"string"},{"type":"object","properties":{"x":{"type":"string"}}}]}`:                                                                                       false,
		`{"type":"string"}`: true,
	} {
		if got := strictSchemaCompatible([]byte(schema)); got != want {
			t.Errorf("strictSchemaCompatible(%s) = %v, want %v", schema, got, want)
		}
	}
}
