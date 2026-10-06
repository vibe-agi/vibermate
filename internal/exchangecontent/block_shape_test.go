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
