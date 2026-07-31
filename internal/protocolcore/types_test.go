package protocolcore

import (
	"bytes"
	"testing"
)

func TestJSONObjectOwnsInputAndRejectsDuplicateNames(t *testing.T) {
	t.Parallel()

	input := []byte(`{"outer":{"value":1}}`)
	document, err := NewJSONObject(input, 1024)
	if err != nil {
		t.Fatalf("NewJSONObject() error = %v", err)
	}
	input[2] = 'X'
	if got := document.Bytes(); !bytes.Equal(got, []byte(`{"outer":{"value":1}}`)) {
		t.Fatalf("owned JSON = %s", got)
	}
	output := document.Bytes()
	output[2] = 'Y'
	if got := document.Bytes(); !bytes.Equal(got, []byte(`{"outer":{"value":1}}`)) {
		t.Fatalf("JSON getter exposed mutable storage: %s", got)
	}

	for _, value := range [][]byte{
		[]byte(`{"value":1,"value":2}`),
		[]byte(`{"outer":{"value":1,"value":2}}`),
	} {
		if _, err := NewJSONObject(value, 1024); err == nil {
			t.Fatalf("NewJSONObject(%s) succeeded, want duplicate-name failure", value)
		}
	}
}

func TestRequestCloneDoesNotAliasCollectionsOrJSON(t *testing.T) {
	t.Parallel()

	schema, err := NewJSONObject([]byte(`{"type":"object"}`), 1024)
	if err != nil {
		t.Fatal(err)
	}
	text, err := NewTextBlock("hello")
	if err != nil {
		t.Fatal(err)
	}
	request := Request{
		RequestedModel:  "client-model",
		EffectiveModel:  "provider-model",
		MaxOutputTokens: 8,
		Messages: []Message{{
			Role:   RoleUser,
			Blocks: []ContentBlock{text},
		}},
		Tools: []ToolDefinition{{
			Name:        "sample",
			InputSchema: schema,
		}},
		Context: ContextManagementIntent{
			Edits: []ContextEdit{{
				Kind:    ContextEditClearThinking,
				KeepAll: true,
			}},
		},
		Output: StructuredOutputIntent{
			Kind:   StructuredOutputJSONSchema,
			Schema: schema,
		},
		StopSequences: []string{"stop"},
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	cloned := request.Clone()
	request.Messages[0].Blocks[0].Text = "mutated"
	request.Tools[0].Name = "changed"
	request.Context.Edits[0].KeepAll = false
	outputSchema := request.Output.Schema.Bytes()
	outputSchema[0] = '['
	request.StopSequences[0] = "changed"
	if cloned.Messages[0].Blocks[0].Text != "hello" ||
		cloned.Tools[0].Name != "sample" ||
		!cloned.Context.Edits[0].KeepAll ||
		!bytes.Equal(cloned.Output.Schema.Bytes(), []byte(`{"type":"object"}`)) ||
		cloned.StopSequences[0] != "stop" {
		t.Fatalf("clone changed through input alias: %#v", cloned)
	}
}

func TestUnknownUsageIsDistinctFromKnownZero(t *testing.T) {
	t.Parallel()

	unknown := UsageValue{}
	knownZero := UsageValue{Known: true, Source: "oracle"}
	if err := unknown.Validate(); err != nil {
		t.Fatalf("unknown Validate() error = %v", err)
	}
	if err := knownZero.Validate(); err != nil {
		t.Fatalf("known-zero Validate() error = %v", err)
	}
	if unknown == knownZero {
		t.Fatal("unknown usage equals known zero")
	}
}

func TestProviderExtensionOwnsOpaqueFragments(t *testing.T) {
	t.Parallel()

	input := [][]byte{
		[]byte(`"opaque-one"`),
		[]byte(`{"reasoning_tokens":2}`),
	}
	extension, err := NewProviderExtension(
		"openai-chat",
		ProviderExtensionReasoningContent,
		"$.choices[0].delta.reasoning_content",
		input,
	)
	if err != nil {
		t.Fatalf("NewProviderExtension() error = %v", err)
	}
	input[0][1] = 'X'
	fragments := extension.Fragments()
	if !bytes.Equal(fragments[0], []byte(`"opaque-one"`)) {
		t.Fatalf("constructor retained input alias: %q", fragments)
	}
	fragments[0][1] = 'Y'
	if fresh := extension.Fragments(); !bytes.Equal(
		fresh[0],
		[]byte(`"opaque-one"`),
	) {
		t.Fatalf("getter exposed mutable storage: %q", fresh)
	}

	text, err := NewTextBlock("visible")
	if err != nil {
		t.Fatal(err)
	}
	response := Response{
		ID:                 "response-1",
		RequestedModel:     "client-model",
		EffectiveModel:     "provider-model",
		ReportedModel:      "reported-model",
		Blocks:             []ContentBlock{text},
		ProviderExtensions: []ProviderExtension{extension},
		StopReason:         StopReasonEndTurn,
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("Response.Validate() error = %v", err)
	}
	cloned := response.Clone()
	response.ProviderExtensions[0] = ProviderExtension{}
	if fresh := cloned.ProviderExtensions[0].Fragments(); !bytes.Equal(
		fresh[0],
		[]byte(`"opaque-one"`),
	) {
		t.Fatalf("Response.Clone() retained extension alias: %q", fresh)
	}
}
