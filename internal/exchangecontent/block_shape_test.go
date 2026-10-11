package exchangecontent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/environment"
)

func TestRetainedBlockShapePreservesBodyIndependentRules(t *testing.T) {
	for _, tc := range []struct {
		name  string
		block Block
		valid bool
	}{
		{"text", Block{Kind: "text", Text: "visible"}, true},
		{"refusal", Block{Kind: "refusal", Text: "no"}, true},
		{"tool call", Block{Kind: "tool_call", CallID: "c", ToolName: "tool", Arguments: json.RawMessage(`{"x":1}`)}, true},
		{"tool result", Block{Kind: "tool_result", CallID: "c", Text: "result"}, true},
		{"reasoning", Block{Kind: "reasoning", ProviderSource: "provider", ProviderKind: "thinking", Text: "thought"}, true},
		{"unbounded metadata", Block{Kind: "text", ProviderSource: strings.Repeat("s", 8192), ProviderKind: strings.Repeat("k", 8192), Fingerprint: strings.Repeat("f", 8192)}, true},
		{"text with args", Block{Kind: "text", Arguments: json.RawMessage(`null`)}, false},
		{"text with tool", Block{Kind: "text", CallID: "c"}, false},
		{"negative size", Block{Kind: "text", OriginalSize: -1}, false},
		{"bad kind", Block{Kind: "unknown"}, false},
		{"deferred", Block{Kind: "text", Deferred: &DeferredContent{}}, false},
		{"missing tool name", Block{Kind: "tool_call", CallID: "c"}, false},
		{"tool call error", Block{Kind: "tool_call", CallID: "c", ToolName: "tool", ToolError: true}, false},
		{"invalid agent", Block{Kind: "text", Agent: &AgentContext{Author: "bad\nname"}}, false},
		{"reasoning fingerprint", Block{Kind: "reasoning", ProviderSource: "p", ProviderKind: "k", Fingerprint: strings.Repeat("a", 64)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.block.Availability = AvailabilityRecorded
			shape := tc.block.RetainedShape()
			if err := shape.Validate(environment.ContentRecordingFull); (err == nil) != tc.valid {
				t.Fatalf("shape valid=%t want=%t: %v", err == nil, tc.valid, err)
			}
			if err := tc.block.Validate(environment.ContentRecordingFull); (err == nil) != tc.valid {
				t.Fatalf("block valid=%t want=%t: %v", err == nil, tc.valid, err)
			}
		})
	}
	for _, mode := range []environment.ContentRecordingMode{environment.ContentRecordingFull, environment.ContentRecordingMetadataOnly} {
		block := Block{Kind: "provider_extension", Availability: AvailabilityOmitted, ProviderSource: "p", ProviderKind: "k", Fingerprint: "sha256:" + strings.Repeat("a", 64)}
		if err := block.RetainedShape().Validate(mode); err != nil {
			t.Fatal(err)
		}
		block.Fingerprint = "invalid"
		if err := block.RetainedShape().Validate(mode); err == nil {
			t.Fatal("invalid fingerprint admitted")
		}
	}
	omitted := Block{Kind: "text", Availability: AvailabilityOmitted, Text: "hidden"}
	if err := omitted.RetainedShape().Validate(environment.ContentRecordingMetadataOnly); err == nil {
		t.Fatal("omitted body admitted")
	}
	// Syntax belongs to the complete body validator, not a body-free shape.
	invalidJSON := Block{Kind: "tool_call", Availability: AvailabilityRecorded, CallID: "c", ToolName: "tool", Arguments: json.RawMessage(`{`)}
	if err := invalidJSON.RetainedShape().Validate(environment.ContentRecordingFull); err != nil {
		t.Fatal(err)
	}
	if err := invalidJSON.Validate(environment.ContentRecordingFull); err == nil {
		t.Fatal("invalid body syntax admitted")
	}
}

func TestRetainedBlockShapeDetachesAgentValue(t *testing.T) {
	block := Block{Kind: "text", Availability: AvailabilityRecorded, Agent: &AgentContext{Author: "original"}}
	shape := block.RetainedShape()
	block.Agent.Author = "changed"
	if shape.Agent.Author != "original" {
		t.Fatal("shape aliases mutable block agent")
	}
}

func TestRetainedBlockShapeReportsMetadataBeforeBodySyntax(t *testing.T) {
	// Shape extraction checks all metadata before full-body JSON validity. Both
	// defects still classify as invalid evidence; namespace now wins this pair.
	block := Block{Kind: "tool_call", Availability: AvailabilityRecorded, CallID: "c", ToolName: "tool", ToolNamespace: "bad\nnamespace", Arguments: json.RawMessage(`{`)}
	err := block.Validate(environment.ContentRecordingFull)
	if !errors.Is(err, ErrInvalidEvidence) || !strings.Contains(err.Error(), "namespace") {
		t.Fatalf("shape/body error precedence: %v", err)
	}
}

func TestRetainedBlockShapeMetadataOnlyParity(t *testing.T) {
	for _, tc := range []struct {
		name  string
		block Block
		valid bool
	}{
		{"text", Block{Kind: "text"}, true},
		{"refusal", Block{Kind: "refusal"}, true},
		{"tool call", Block{Kind: "tool_call", CallID: "c", ToolName: "tool"}, true},
		{"tool result", Block{Kind: "tool_result", CallID: "c"}, true},
		{"reasoning", Block{Kind: "reasoning", ProviderSource: "p", ProviderKind: "k"}, true},
		{"provider extension", Block{Kind: "provider_extension", ProviderSource: "p", ProviderKind: "k", Fingerprint: "sha256:" + strings.Repeat("a", 64)}, true},
		{"omitted text", Block{Kind: "text", Text: "private"}, false},
		{"omitted arguments", Block{Kind: "tool_call", CallID: "c", ToolName: "tool", Arguments: json.RawMessage(`null`)}, false},
		{"result named pair", Block{Kind: "tool_result", CallID: "c", ToolName: "tool", ToolNamespace: "space", ToolError: true}, true},
		{"result only name", Block{Kind: "tool_result", CallID: "c", ToolName: "tool"}, false},
		{"result only namespace", Block{Kind: "tool_result", CallID: "c", ToolNamespace: "space"}, false},
		{"result bad name", Block{Kind: "tool_result", CallID: "c", ToolName: "bad\nname", ToolNamespace: "space"}, false},
		{"call namespaced", Block{Kind: "tool_call", CallID: "c", ToolName: "tool", ToolNamespace: "space"}, true},
		{"call bad namespace", Block{Kind: "tool_call", CallID: "c", ToolName: "tool", ToolNamespace: "bad\nspace"}, false},
		{"call no ID", Block{Kind: "tool_call", ToolName: "tool"}, false},
		{"call bad ID", Block{Kind: "tool_call", CallID: "bad\nid", ToolName: "tool"}, false},
		{"call oversized ID", Block{Kind: "tool_call", CallID: strings.Repeat("c", 513), ToolName: "tool"}, false},
		{"call missing name", Block{Kind: "tool_call", CallID: "c"}, false},
		{"call error flag", Block{Kind: "tool_call", CallID: "c", ToolName: "tool", ToolError: true}, false},
		{"text tool identity", Block{Kind: "text", ToolName: "tool"}, false},
		{"agent all fields", Block{Kind: "text", Agent: &AgentContext{AgentName: "agent", Author: "author", Recipient: "recipient"}}, true},
		{"agent boundary", Block{Kind: "tool_call", CallID: "c", ToolName: "tool", Agent: &AgentContext{Author: strings.Repeat("a", 512), Recipient: "recipient"}}, true},
		{"agent oversized", Block{Kind: "tool_call", CallID: "c", ToolName: "tool", Agent: &AgentContext{Author: strings.Repeat("a", 513), Recipient: "recipient"}}, false},
		{"agent control", Block{Kind: "tool_result", CallID: "c", Agent: &AgentContext{Author: "author", Recipient: "bad\nrecipient"}}, false},
		{"agent empty", Block{Kind: "text", Agent: &AgentContext{}}, false},
		{"agent direction incomplete", Block{Kind: "text", Agent: &AgentContext{Author: "author"}}, false},
		{"deferred", Block{Kind: "text", Deferred: &DeferredContent{}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.block.Availability = AvailabilityOmitted
			for name, err := range map[string]error{"shape": tc.block.RetainedShape().Validate(environment.ContentRecordingMetadataOnly), "complete": tc.block.Validate(environment.ContentRecordingMetadataOnly)} {
				if (err == nil) != tc.valid || (err != nil && !errors.Is(err, ErrInvalidEvidence)) {
					t.Fatalf("%s valid=%t want=%t: %v", name, err == nil, tc.valid, err)
				}
			}
		})
	}
}
