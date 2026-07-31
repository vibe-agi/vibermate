package anthropicchat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	openai "github.com/openai/openai-go/v3"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/ssewire"
)

func TestRequestTranslationProducesChatWireAndExplicitLossReport(t *testing.T) {
	t.Parallel()

	codec := newTestCodec(t)
	body := []byte(`{
		"model":"claude-client-alias",
		"max_tokens":256,
		"stream":true,
		"system":[{"type":"text","text":"Be exact.","cache_control":{"type":"ephemeral"}}],
		"messages":[
			{"role":"user","content":[{"type":"text","text":"Run it."}]},
			{"role":"assistant","content":[
				{"type":"text","text":"Calling."},
				{"type":"tool_use","id":"call-previous","name":"shell","input":{"cmd":"pwd"}}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"call-previous","content":"/tmp","is_error":false}
			]}
		],
		"tools":[{
			"name":"shell",
			"description":"Run a command.",
			"input_schema":{"type":"object","properties":{"cmd":{"type":"string"}}},
			"cache_control":{"type":"ephemeral"}
		}],
		"tool_choice":{"type":"tool","name":"shell","disable_parallel_tool_use":true},
		"thinking":{"type":"adaptive","display":"omitted"},
		"output_config":{
			"effort":"high",
			"task_budget":{"type":"tokens","total":4000,"remaining":3000}
		},
		"context_management":{
			"edits":[{"type":"clear_thinking_20251015","keep":"all"}]
		},
		"diagnostics":{"previous_message_id":null},
		"metadata":{"user_id":"local"},
		"top_k":40
	}`)

	request, parseReport, err := codec.DecodeClientRequest(body)
	if err != nil {
		t.Fatalf("DecodeClientRequest() error = %v", err)
	}
	if request.RequestedModel != "claude-client-alias" ||
		request.EffectiveModel != "claude-client-alias" ||
		!request.Stream ||
		request.Reasoning.Thinking != protocolcore.ThinkingModeAdaptive ||
		request.Reasoning.Display != protocolcore.ThinkingDisplayOmitted ||
		request.Reasoning.Effort != protocolcore.ReasoningEffortHigh ||
		len(request.Context.Edits) != 1 ||
		request.Context.Edits[0].Kind != protocolcore.ContextEditClearThinking ||
		!request.Context.Edits[0].KeepAll ||
		!request.Diagnostics.Requested ||
		request.Diagnostics.HasPrevious {
		t.Fatalf("decoded request = %#v", request)
	}
	if len(parseReport.Notices()) != 4 {
		t.Fatalf("parse notice count = %d, want 4", len(parseReport.Notices()))
	}

	request, err = request.WithEffectiveModel("gpt-provider-model")
	if err != nil {
		t.Fatalf("WithEffectiveModel() error = %v", err)
	}
	encoded, encodeReport, err := codec.EncodeProviderRequest(request)
	if err != nil {
		t.Fatalf("EncodeProviderRequest() error = %v", err)
	}
	if encodeReport.Empty() {
		t.Fatal("EncodeProviderRequest() report is empty, want content-order notice")
	}
	for _, code := range []protocolcore.NoticeCode{
		protocolcore.NoticeContentOrderNormalized,
		protocolcore.NoticeThinkingModeNotForwarded,
		protocolcore.NoticeThinkingDisplayNotForwarded,
		protocolcore.NoticeReasoningEffortDowngraded,
		protocolcore.NoticeTaskBudgetNotForwarded,
		protocolcore.NoticeContextManagementNotForwarded,
		protocolcore.NoticeDiagnosticsNotForwarded,
	} {
		if !reportHasNotice(encodeReport, code) {
			t.Fatalf("encode report = %#v, want notice %q", encodeReport.Notices(), code)
		}
	}

	var oracle openai.ChatCompletionNewParams
	if err := json.Unmarshal(encoded, &oracle); err != nil {
		t.Fatalf("official OpenAI SDK rejected request: %v\n%s", err, encoded)
	}
	var wire openAIRequestWire
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if wire.Model != "gpt-provider-model" ||
		wire.StreamOptions == nil ||
		!wire.StreamOptions.IncludeUsage ||
		len(wire.Tools) != 1 {
		t.Fatalf("provider request = %#v", wire)
	}
	if wire.MaxTokens == nil ||
		*wire.MaxTokens != 256 ||
		wire.MaxCompletionTokens != nil ||
		wire.ReasoningEffort != "" {
		t.Fatalf("provider request profile = %#v", wire)
	}
	if bytes.Contains(encoded, []byte(`"n":`)) {
		t.Fatalf("provider request emitted an unnecessary choice count: %s", encoded)
	}
	if wire.ParallelToolCalls == nil || *wire.ParallelToolCalls {
		t.Fatalf("parallel_tool_calls = %v, want false", wire.ParallelToolCalls)
	}
	if !bytes.Contains(encoded, []byte(`"tool_call_id":"call-previous"`)) {
		t.Fatalf("provider request did not preserve tool call ID: %s", encoded)
	}
}

func TestProviderRequestProfileSelectsOneTokenFieldAndToolReasoningMode(
	t *testing.T,
) {
	t.Parallel()

	options := DefaultOptions()
	options.ProviderRequest = ProviderRequestProfile{
		completionTokenField: CompletionTokenFieldMaxCompletionTokens,
		toolReasoningMode:    ToolReasoningModeNone,
		disabledReasoning:    DisabledReasoningModeNone,
	}
	codec, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	request := newStreamingRequest(t, codec)
	request.Reasoning.Thinking = protocolcore.ThinkingModeDisabled
	schema, err := protocolcore.NewJSONObject(
		[]byte(`{"type":"object"}`),
		1024,
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Tools = []protocolcore.ToolDefinition{{
		Name:        "shell",
		Description: "Run a command.",
		InputSchema: schema,
	}}
	encoded, _, err := codec.EncodeProviderRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	var wire openAIRequestWire
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.MaxCompletionTokens == nil ||
		*wire.MaxCompletionTokens != request.MaxOutputTokens ||
		wire.MaxTokens != nil ||
		wire.ReasoningEffort != "none" {
		t.Fatalf("alternate provider request profile = %#v", wire)
	}
}

func TestOpenAIChatCompatibilityOmitsDisabledReasoningField(t *testing.T) {
	t.Parallel()

	codec := newTestCodec(t)
	request := newStreamingRequest(t, codec)
	request.Reasoning.Thinking = protocolcore.ThinkingModeDisabled
	encoded, _, err := codec.EncodeProviderRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(`"reasoning_effort"`)) {
		t.Fatalf(
			"compatibility profile emitted disabled reasoning on the wire: %s",
			encoded,
		)
	}
}

func TestCodecRejectsMissingProviderRequestProfile(t *testing.T) {
	t.Parallel()

	options := DefaultOptions()
	options.ProviderRequest = ProviderRequestProfile{}
	if _, err := New(options); err == nil {
		t.Fatal("New() accepted a missing provider request profile")
	}
}

func TestRequestReasoningIntentUsesTypedOfficialFields(t *testing.T) {
	t.Parallel()

	codec := newTestCodec(t)
	tests := []struct {
		name       string
		fields     string
		thinking   protocolcore.ThinkingMode
		budget     int
		display    protocolcore.ThinkingDisplay
		effort     protocolcore.ReasoningEffort
		taskBudget protocolcore.TaskBudget
		outputKind protocolcore.StructuredOutputKind
		wantError  protocolcore.Reason
	}{
		{
			name:     "adaptive high effort",
			fields:   `"thinking":{"type":"adaptive"},"output_config":{"effort":"high"},`,
			thinking: protocolcore.ThinkingModeAdaptive,
			effort:   protocolcore.ReasoningEffortHigh,
		},
		{
			name:   "task token budget",
			fields: `"output_config":{"effort":"high","task_budget":{"type":"tokens","total":4000,"remaining":3000}},`,
			effort: protocolcore.ReasoningEffortHigh,
			taskBudget: protocolcore.TaskBudget{
				Present:         true,
				TotalTokens:     4000,
				RemainingKnown:  true,
				RemainingTokens: 3000,
			},
		},
		{
			name:     "enabled bounded budget",
			fields:   `"thinking":{"type":"enabled","budget_tokens":1024,"display":"summarized"},`,
			thinking: protocolcore.ThinkingModeEnabled,
			budget:   1024,
			display:  protocolcore.ThinkingDisplaySummarized,
		},
		{
			name:     "disabled",
			fields:   `"thinking":{"type":"disabled"},`,
			thinking: protocolcore.ThinkingModeDisabled,
		},
		{
			name:      "unknown effort",
			fields:    `"output_config":{"effort":"private"},`,
			wantError: protocolcore.ReasonInvalidClientRequest,
		},
		{
			name:       "structured output",
			fields:     `"output_config":{"format":{"type":"json_schema","schema":{"type":"object"}}},`,
			outputKind: protocolcore.StructuredOutputJSONSchema,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			body := []byte(`{
				"model":"claude-client-alias",
				"max_tokens":2048,
				` + test.fields + `
				"messages":[{"role":"user","content":"hello"}]
			}`)
			var oracle anthropic.BetaMessageNewParams
			if err := json.Unmarshal(body, &oracle); err != nil {
				t.Fatalf("official Anthropic SDK rejected fixture: %v", err)
			}
			request, _, err := codec.DecodeClientRequest(body)
			if test.wantError != "" {
				if protocolcore.ReasonOf(err) != test.wantError {
					t.Fatalf("reason = %q, want %q; error = %v",
						protocolcore.ReasonOf(err),
						test.wantError,
						err,
					)
				}
				return
			}
			if err != nil {
				t.Fatalf("DecodeClientRequest() error = %v", err)
			}
			if request.Reasoning.Thinking != test.thinking ||
				request.Reasoning.BudgetTokens != test.budget ||
				request.Reasoning.Display != test.display ||
				request.Reasoning.Effort != test.effort ||
				request.Reasoning.TaskBudget != test.taskBudget ||
				request.Output.Kind != test.outputKind {
				t.Fatalf("reasoning intent = %#v", request.Reasoning)
			}
		})
	}
}

func TestStructuredOutputRoundTripsThroughTypedIRAndOpenAIOracle(t *testing.T) {
	t.Parallel()

	codec := newTestCodec(t)
	body := []byte(`{
		"model":"claude-client-alias",
		"max_tokens":2048,
		"stream":true,
		"messages":[{"role":"user","content":"return a result"}],
		"output_config":{"format":{
			"type":"json_schema",
			"schema":{
				"type":"object",
				"properties":{"answer":{"type":"string"}},
				"required":["answer"],
				"additionalProperties":false
			}
		}}
	}`)
	var anthropicOracle anthropic.BetaMessageNewParams
	if err := json.Unmarshal(body, &anthropicOracle); err != nil {
		t.Fatalf("official Anthropic SDK rejected fixture: %v", err)
	}
	request, _, err := codec.DecodeClientRequest(body)
	if err != nil {
		t.Fatalf("DecodeClientRequest() error = %v", err)
	}
	schemaAliasOffset := bytes.Index(body, []byte(`"answer"`))
	if schemaAliasOffset < 0 {
		t.Fatal("schema fixture does not contain the expected property")
	}
	body[schemaAliasOffset+1] = 'X'
	if !bytes.Contains(request.Output.Schema.Bytes(), []byte(`"answer"`)) {
		t.Fatal("structured-output intent aliases the source request body")
	}
	request, err = request.WithEffectiveModel("gpt-provider-model")
	if err != nil {
		t.Fatalf("WithEffectiveModel() error = %v", err)
	}
	exposedSchema := request.Output.Schema.Bytes()
	exposedSchema[0] = '['

	first, _, err := codec.EncodeProviderRequest(request)
	if err != nil {
		t.Fatalf("EncodeProviderRequest() error = %v", err)
	}
	second, _, err := codec.EncodeProviderRequest(request)
	if err != nil {
		t.Fatalf("second EncodeProviderRequest() error = %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("identical structured-output intent produced different provider wire")
	}
	var openAIOracle openai.ChatCompletionNewParams
	if err := json.Unmarshal(first, &openAIOracle); err != nil {
		t.Fatalf("official OpenAI SDK rejected request: %v\n%s", err, first)
	}
	var wire openAIRequestWire
	if err := json.Unmarshal(first, &wire); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if wire.ResponseFormat == nil ||
		wire.ResponseFormat.Type != "json_schema" ||
		!wire.ResponseFormat.JSONSchema.Strict ||
		wire.ResponseFormat.JSONSchema.Name !=
			structuredOutputName(request.Output.Schema) ||
		len(wire.ResponseFormat.JSONSchema.Name) > 64 ||
		!bytes.Equal(
			wire.ResponseFormat.JSONSchema.Schema,
			request.Output.Schema.Bytes(),
		) {
		t.Fatalf("provider response format = %#v", wire.ResponseFormat)
	}
}

func TestStructuredOutputRejectsUnsupportedOrInvalidSourceShapes(t *testing.T) {
	t.Parallel()

	codec := newTestCodec(t)
	tests := []struct {
		name   string
		format string
		reason protocolcore.Reason
	}{
		{
			name:   "unsupported kind",
			format: `{"type":"regex","schema":{"type":"object"}}`,
			reason: protocolcore.ReasonUnsupportedClientInput,
		},
		{
			name:   "missing schema",
			format: `{"type":"json_schema"}`,
			reason: protocolcore.ReasonInvalidClientRequest,
		},
		{
			name:   "schema is not an object",
			format: `{"type":"json_schema","schema":[]}`,
			reason: protocolcore.ReasonInvalidClientRequest,
		},
		{
			name:   "duplicate schema field",
			format: `{"type":"json_schema","schema":{"type":"object","type":"array"}}`,
			reason: protocolcore.ReasonInvalidClientRequest,
		},
		{
			name:   "unknown format field",
			format: `{"type":"json_schema","schema":{"type":"object"},"private":true}`,
			reason: protocolcore.ReasonInvalidClientRequest,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			body := []byte(`{
				"model":"claude-client-alias",
				"max_tokens":2048,
				"messages":[{"role":"user","content":"hello"}],
				"output_config":{"format":` + test.format + `}
			}`)
			_, _, err := codec.DecodeClientRequest(body)
			if protocolcore.ReasonOf(err) != test.reason {
				t.Fatalf(
					"reason = %q, want %q; error = %v",
					protocolcore.ReasonOf(err),
					test.reason,
					err,
				)
			}
		})
	}
}

func TestRequestContextManagementUsesTypedClearThinkingIntent(t *testing.T) {
	t.Parallel()

	codec := newTestCodec(t)
	tests := []struct {
		name      string
		context   string
		want      protocolcore.ContextEdit
		wantError protocolcore.Reason
	}{
		{
			name:    "fixed Claude keep-all shape",
			context: `{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]}`,
			want: protocolcore.ContextEdit{
				Kind:    protocolcore.ContextEditClearThinking,
				KeepAll: true,
			},
		},
		{
			name:    "bounded thinking turns",
			context: `{"edits":[{"type":"clear_thinking_20251015","keep":{"type":"thinking_turns","value":2}}]}`,
			want: protocolcore.ContextEdit{
				Kind:              protocolcore.ContextEditClearThinking,
				KeepThinkingTurns: 2,
			},
		},
		{
			name:      "unsupported compaction edit",
			context:   `{"edits":[{"type":"compact_20260112"}]}`,
			wantError: protocolcore.ReasonUnsupportedClientInput,
		},
		{
			name:      "invalid retention",
			context:   `{"edits":[{"type":"clear_thinking_20251015","keep":"private"}]}`,
			wantError: protocolcore.ReasonInvalidClientRequest,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			body := []byte(`{
				"model":"claude-client-alias",
				"max_tokens":2048,
				"messages":[{"role":"user","content":"hello"}],
				"context_management":` + test.context + `
			}`)
			var oracle anthropic.BetaMessageNewParams
			if err := json.Unmarshal(body, &oracle); err != nil {
				t.Fatalf("official Anthropic SDK rejected fixture: %v", err)
			}
			request, _, err := codec.DecodeClientRequest(body)
			if test.wantError != "" {
				if protocolcore.ReasonOf(err) != test.wantError {
					t.Fatalf("reason = %q, want %q; error = %v",
						protocolcore.ReasonOf(err),
						test.wantError,
						err,
					)
				}
				return
			}
			if err != nil {
				t.Fatalf("DecodeClientRequest() error = %v", err)
			}
			if len(request.Context.Edits) != 1 ||
				request.Context.Edits[0] != test.want {
				t.Fatalf("context intent = %#v", request.Context)
			}
		})
	}
}

func TestRequestDiagnosticsPreservesOnlyTypedPreviousIdentity(t *testing.T) {
	t.Parallel()

	codec := newTestCodec(t)
	tests := []struct {
		name      string
		value     string
		has       bool
		previous  string
		wantError protocolcore.Reason
	}{
		{
			name:  "first turn",
			value: `{"previous_message_id":null}`,
		},
		{
			name:     "subsequent turn",
			value:    `{"previous_message_id":"msg_previous"}`,
			has:      true,
			previous: "msg_previous",
		},
		{
			name:      "unknown diagnostics field",
			value:     `{"private":"value"}`,
			wantError: protocolcore.ReasonInvalidClientRequest,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			body := []byte(`{
				"model":"claude-client-alias",
				"max_tokens":2048,
				"messages":[{"role":"user","content":"hello"}],
				"diagnostics":` + test.value + `
			}`)
			var oracle anthropic.BetaMessageNewParams
			if err := json.Unmarshal(body, &oracle); err != nil {
				t.Fatalf("official Anthropic SDK rejected fixture: %v", err)
			}
			request, _, err := codec.DecodeClientRequest(body)
			if test.wantError != "" {
				if protocolcore.ReasonOf(err) != test.wantError {
					t.Fatalf("reason = %q, want %q; error = %v",
						protocolcore.ReasonOf(err),
						test.wantError,
						err,
					)
				}
				return
			}
			if err != nil {
				t.Fatalf("DecodeClientRequest() error = %v", err)
			}
			if !request.Diagnostics.Requested ||
				request.Diagnostics.HasPrevious != test.has ||
				request.Diagnostics.PreviousMessageID != test.previous {
				t.Fatalf("diagnostics intent = %#v", request.Diagnostics)
			}
		})
	}
}

func TestRequestDecoderRejectsUnknownAndUnsupportedContent(t *testing.T) {
	t.Parallel()

	codec := newTestCodec(t)
	tests := []struct {
		name   string
		body   string
		reason protocolcore.Reason
	}{
		{
			name: "unknown root field",
			body: `{
				"model":"claude",
				"max_tokens":8,
				"messages":[{"role":"user","content":"hello"}],
				"untracked":true
			}`,
			reason: protocolcore.ReasonInvalidClientRequest,
		},
		{
			name: "duplicate root field",
			body: `{
				"model":"claude",
				"model":"other",
				"max_tokens":8,
				"messages":[{"role":"user","content":"hello"}]
			}`,
			reason: protocolcore.ReasonInvalidClientRequest,
		},
		{
			name: "image block",
			body: `{
				"model":"claude",
				"max_tokens":8,
				"messages":[{"role":"user","content":[{
					"type":"image",
					"source":{"type":"base64","media_type":"image/png","data":"AA=="}
				}]}]
			}`,
			reason: protocolcore.ReasonUnsupportedClientInput,
		},
		{
			name: "unknown thinking mode",
			body: `{
				"model":"claude",
				"max_tokens":8,
				"messages":[{"role":"user","content":"hello"}],
				"thinking":{"type":"private"}
			}`,
			reason: protocolcore.ReasonInvalidClientRequest,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := codec.DecodeClientRequest([]byte(test.body))
			if protocolcore.ReasonOf(err) != test.reason {
				t.Fatalf("reason = %q, want %q; error = %v", protocolcore.ReasonOf(err), test.reason, err)
			}
		})
	}
}

func TestNonStreamingResponseRoundTripsThroughOfficialAnthropicOracle(t *testing.T) {
	t.Parallel()

	codec := newTestCodec(t)
	request := newStreamingRequest(t, codec)
	request.Stream = false
	body := []byte(`{
		"id":"chatcmpl-1",
		"object":"chat.completion",
		"created":1,
		"model":"gpt-provider-model",
		"choices":[{
			"index":0,
			"message":{"role":"assistant","content":"Done.","refusal":null},
			"finish_reason":"stop",
			"logprobs":null
		}],
		"usage":{
			"prompt_tokens":11,
			"completion_tokens":3,
			"total_tokens":14,
			"prompt_tokens_details":{"cached_tokens":4}
		}
	}`)

	response, report, err := codec.DecodeProviderResponse(request, body)
	if err != nil {
		t.Fatalf("DecodeProviderResponse() error = %v", err)
	}
	if !report.Empty() {
		t.Fatalf("translation report = %#v, want empty", report.Notices())
	}
	if response.RequestedModel != "claude-client-alias" ||
		response.ReportedModel != "gpt-provider-model" ||
		response.Usage.InputUncached.Tokens != 7 ||
		response.Usage.CacheRead.Tokens != 4 ||
		response.Usage.Output.Tokens != 3 {
		t.Fatalf("decoded response = %#v", response)
	}
	encoded, err := codec.EncodeClientResponse(response)
	if err != nil {
		t.Fatalf("EncodeClientResponse() error = %v", err)
	}
	var oracle anthropic.Message
	if err := json.Unmarshal(encoded, &oracle); err != nil {
		t.Fatalf("official Anthropic SDK rejected response: %v\n%s", err, encoded)
	}
	if !bytes.Contains(encoded, []byte(`"model":"claude-client-alias"`)) ||
		!bytes.Contains(encoded, []byte(`"cache_read_input_tokens":4`)) {
		t.Fatalf("client response = %s", encoded)
	}
}

func TestNonStreamingReasoningContentRemainsOpaqueAndIsNotForwarded(t *testing.T) {
	t.Parallel()

	codec := newTestCodec(t)
	request := newStreamingRequest(t, codec)
	request.Stream = false
	body := []byte(`{
		"id":"chatcmpl-reasoning",
		"object":"chat.completion",
		"created":1,
		"model":"glm-5",
		"choices":[{
			"index":0,
			"message":{
				"role":"assistant",
				"content":"Visible",
				"reasoning_content":"opaque-reasoning"
			},
			"finish_reason":"stop"
		}],
		"usage":{
			"prompt_tokens":5,
			"completion_tokens":4,
			"total_tokens":9,
			"completion_tokens_details":{"reasoning_tokens":2}
		}
	}`)

	response, report, err := codec.DecodeProviderResponse(request, body)
	if err != nil {
		t.Fatalf("DecodeProviderResponse() error = %v", err)
	}
	for _, notice := range []protocolcore.NoticeCode{
		protocolcore.NoticeReasoningContentNotForwarded,
		protocolcore.NoticeReasoningUsageNotForwarded,
	} {
		if !reportHasNotice(report, notice) {
			t.Fatalf("translation report = %#v, want %q", report.Notices(), notice)
		}
	}
	if len(response.ProviderExtensions) != 2 ||
		!bytes.Equal(
			response.ProviderExtensions[0].Fragments()[0],
			[]byte(`"opaque-reasoning"`),
		) ||
		!bytes.Equal(
			response.ProviderExtensions[1].Fragments()[0],
			[]byte(`{"reasoning_tokens":2}`),
		) {
		t.Fatalf("provider extensions = %#v", response.ProviderExtensions)
	}
	encoded, err := codec.EncodeClientResponse(response)
	if err != nil {
		t.Fatalf("EncodeClientResponse() error = %v", err)
	}
	if bytes.Contains(encoded, []byte("opaque-reasoning")) ||
		!bytes.Contains(encoded, []byte(`"text":"Visible"`)) {
		t.Fatalf("client response = %s", encoded)
	}
}

func TestNonStreamingToolResponseRequiresCompleteJSONObject(t *testing.T) {
	t.Parallel()

	codec := newTestCodec(t)
	request := newStreamingRequest(t, codec)
	request.Stream = false
	body := []byte(`{
		"id":"chatcmpl-tool",
		"object":"chat.completion",
		"created":1,
		"model":"gpt-provider-model",
		"choices":[{
			"index":0,
			"message":{"role":"assistant","content":null,"tool_calls":[{
				"id":"call-1",
				"type":"function",
				"function":{"name":"shell","arguments":"{\"cmd\":"}
			}]},
			"finish_reason":"tool_calls"
		}]
	}`)
	_, _, err := codec.DecodeProviderResponse(request, body)
	if protocolcore.ReasonOf(err) != protocolcore.ReasonToolCallIncomplete {
		t.Fatalf("reason = %q, want %q; error = %v",
			protocolcore.ReasonOf(err),
			protocolcore.ReasonToolCallIncomplete,
			err,
		)
	}
}

func TestStreamingTextIsIncrementalAndTerminalWaitsForApproval(t *testing.T) {
	t.Parallel()

	codec := newTestCodec(t)
	stream, err := codec.NewProviderStream(newStreamingRequest(t, codec))
	if err != nil {
		t.Fatalf("NewProviderStream() error = %v", err)
	}
	wire := joinProviderEvents(t,
		`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-provider-model","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"},"finish_reason":null}]}`,
		`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-provider-model","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":null}]}`,
		`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-provider-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-provider-model","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7,"prompt_tokens_details":{"cached_tokens":1}}}`,
		`[DONE]`,
	)

	var immediate bytes.Buffer
	for _, fragment := range wire {
		output, feedErr := stream.Feed(context.Background(), []byte{fragment})
		if feedErr != nil {
			t.Fatalf("Feed() error = %v", feedErr)
		}
		immediate.Write(output)
	}
	if !bytes.Contains(immediate.Bytes(), []byte(`"text":"Hel"`)) ||
		!bytes.Contains(immediate.Bytes(), []byte(`"text":"lo"`)) {
		t.Fatalf("immediate output did not stream text: %s", immediate.Bytes())
	}
	if bytes.Contains(immediate.Bytes(), []byte("message_stop")) {
		t.Fatalf("normal terminal was visible before approval: %s", immediate.Bytes())
	}

	pending, err := stream.FinishDecoded(context.Background())
	if err != nil {
		t.Fatalf("FinishDecoded() error = %v", err)
	}
	if len(pending.ToolIntents()) != 0 {
		t.Fatalf("tool intent count = %d, want 0", len(pending.ToolIntents()))
	}
	release, err := pending.Approve()
	if err != nil {
		t.Fatalf("Approve() error = %v", err)
	}
	if !bytes.Contains(release, []byte("content_block_stop")) ||
		!bytes.Contains(release, []byte("message_stop")) {
		t.Fatalf("approved terminal = %s", release)
	}
	events := decodeClientEvents(t, append(immediate.Bytes(), release...))
	assertEventOrder(t, events,
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	)
	for index, event := range events {
		var oracle anthropic.MessageStreamEventUnion
		if err := json.Unmarshal(event.Data, &oracle); err != nil {
			t.Fatalf("official Anthropic SDK rejected event %d: %v\n%s", index, err, event.Data)
		}
	}
}

func TestStreamingReasoningContentIsExplicitlyReportedAcrossDialects(t *testing.T) {
	t.Parallel()

	codec := newTestCodec(t)
	stream, err := codec.NewProviderStream(newStreamingRequest(t, codec))
	if err != nil {
		t.Fatalf("NewProviderStream() error = %v", err)
	}
	wire := joinProviderEvents(t,
		`{"id":"chatcmpl-reasoning","object":"chat.completion.chunk","created":1,"model":"glm-5","choices":[{"index":0,"delta":{"role":"assistant","content":"","reasoning_content":"opaque-one"},"finish_reason":null}]}`,
		`{"id":"chatcmpl-reasoning","object":"chat.completion.chunk","created":1,"model":"glm-5","choices":[{"index":0,"delta":{"content":"Visible","reasoning_content":"opaque-two"},"finish_reason":null}]}`,
		`{"id":"chatcmpl-reasoning","object":"chat.completion.chunk","created":1,"model":"glm-5","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{"id":"chatcmpl-reasoning","object":"chat.completion.chunk","created":1,"model":"glm-5","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":4,"total_tokens":9,"completion_tokens_details":{"reasoning_tokens":2}}}`,
		`[DONE]`,
	)

	immediate, err := feedFragments(stream, wire, 11)
	if err != nil {
		t.Fatalf("Feed() error = %v", err)
	}
	if bytes.Contains(immediate, []byte("opaque-one")) ||
		bytes.Contains(immediate, []byte("opaque-two")) {
		t.Fatalf("provider reasoning leaked into Anthropic wire: %s", immediate)
	}
	if !bytes.Contains(immediate, []byte(`"text":"Visible"`)) {
		t.Fatalf("visible provider content was not streamed: %s", immediate)
	}
	pending, err := stream.FinishDecoded(context.Background())
	if err != nil {
		t.Fatalf("FinishDecoded() error = %v", err)
	}
	report := pending.TranslationReport()
	for _, notice := range []protocolcore.NoticeCode{
		protocolcore.NoticeReasoningContentNotForwarded,
		protocolcore.NoticeReasoningUsageNotForwarded,
	} {
		if !reportHasNotice(report, notice) {
			t.Fatalf("translation report = %#v, want %q", report.Notices(), notice)
		}
	}
	response := pending.DecodedResponse()
	if len(response.ProviderExtensions) != 2 {
		t.Fatalf(
			"provider extension count = %d, want 2",
			len(response.ProviderExtensions),
		)
	}
	reasoning := response.ProviderExtensions[0]
	if reasoning.Source() != SourceOpenAIChat ||
		reasoning.Kind() != protocolcore.ProviderExtensionReasoningContent ||
		reasoning.Path() != "$.choices[0].delta.reasoning_content" {
		t.Fatalf("reasoning extension = %#v", reasoning)
	}
	fragments := reasoning.Fragments()
	if len(fragments) != 2 ||
		!bytes.Equal(fragments[0], []byte(`"opaque-one"`)) ||
		!bytes.Equal(fragments[1], []byte(`"opaque-two"`)) {
		t.Fatalf("reasoning fragments = %q", fragments)
	}
	fragments[0][1] = 'X'
	fresh := pending.DecodedResponse().ProviderExtensions[0].Fragments()
	if !bytes.Equal(fresh[0], []byte(`"opaque-one"`)) {
		t.Fatalf("reasoning getter exposed mutable storage: %q", fresh)
	}
}

func TestStreamingToolBarrierHidesCompleteToolBlockUntilApproval(t *testing.T) {
	t.Parallel()

	codec := newTestCodec(t)
	stream, err := codec.NewProviderStream(newStreamingRequest(t, codec))
	if err != nil {
		t.Fatalf("NewProviderStream() error = %v", err)
	}
	wire := joinProviderEvents(t,
		`{"id":"chatcmpl-tool","object":"chat.completion.chunk","created":1,"model":"gpt-provider-model","choices":[{"index":0,"delta":{"role":"assistant","content":"Before tool."},"finish_reason":null}]}`,
		`{"id":"chatcmpl-tool","object":"chat.completion.chunk","created":1,"model":"gpt-provider-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-secret","type":"function","function":{"name":"shell","arguments":"{\"cmd\":\""}}]},"finish_reason":null}]}`,
		`{"id":"chatcmpl-tool","object":"chat.completion.chunk","created":1,"model":"gpt-provider-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"pwd\"}"}}]},"finish_reason":null}]}`,
		`{"id":"chatcmpl-tool","object":"chat.completion.chunk","created":1,"model":"gpt-provider-model","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"id":"chatcmpl-tool","object":"chat.completion.chunk","created":1,"model":"gpt-provider-model","choices":[],"usage":{"prompt_tokens":8,"completion_tokens":4,"total_tokens":12}}`,
		`[DONE]`,
	)
	immediate, err := feedFragments(stream, wire, 7)
	if err != nil {
		t.Fatalf("Feed() error = %v", err)
	}
	for _, secret := range []string{"call-secret", `"shell"`, `pwd`, "tool_use", "input_json_delta", "message_stop"} {
		if bytes.Contains(immediate, []byte(secret)) {
			t.Fatalf("immediate output leaked %q: %s", secret, immediate)
		}
	}
	if !bytes.Contains(immediate, []byte("Before tool.")) {
		t.Fatalf("immediate output did not preserve safe prefix: %s", immediate)
	}

	pending, err := stream.FinishDecoded(context.Background())
	if err != nil {
		t.Fatalf("FinishDecoded() error = %v", err)
	}
	intents := pending.ToolIntents()
	if len(intents) != 1 ||
		intents[0].Call.Key.WireID() != "call-secret" ||
		intents[0].Call.Name != "shell" ||
		string(intents[0].Call.Arguments.Bytes()) != `{"cmd":"pwd"}` {
		t.Fatalf("tool intents = %#v", intents)
	}
	release, err := pending.Approve()
	if err != nil {
		t.Fatalf("Approve() error = %v", err)
	}
	for _, expected := range []string{"call-secret", `"shell"`, `pwd`, "tool_use", "input_json_delta", "message_stop"} {
		if !bytes.Contains(release, []byte(expected)) {
			t.Fatalf("approved release is missing %q: %s", expected, release)
		}
	}
}

func TestStreamingIncompleteToolArgumentsAbortWithoutLeak(t *testing.T) {
	t.Parallel()

	codec := newTestCodec(t)
	stream, err := codec.NewProviderStream(newStreamingRequest(t, codec))
	if err != nil {
		t.Fatalf("NewProviderStream() error = %v", err)
	}
	wire := joinProviderEvents(t,
		`{"id":"chatcmpl-tool","object":"chat.completion.chunk","created":1,"model":"gpt-provider-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-hidden","type":"function","function":{"name":"shell","arguments":"{\"cmd\":"}}]},"finish_reason":null}]}`,
		`{"id":"chatcmpl-tool","object":"chat.completion.chunk","created":1,"model":"gpt-provider-model","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`[DONE]`,
	)
	immediate, err := feedFragments(stream, wire, 13)
	if err != nil {
		t.Fatalf("Feed() error = %v", err)
	}
	if bytes.Contains(immediate, []byte("call-hidden")) ||
		bytes.Contains(immediate, []byte("shell")) {
		t.Fatalf("immediate output leaked incomplete tool data: %s", immediate)
	}
	_, err = stream.FinishDecoded(context.Background())
	if protocolcore.ReasonOf(err) != protocolcore.ReasonToolCallIncomplete {
		t.Fatalf("reason = %q, want %q; error = %v",
			protocolcore.ReasonOf(err),
			protocolcore.ReasonToolCallIncomplete,
			err,
		)
	}
}

func TestStreamingParallelToolFragmentsAreCompletedAsOneDecisionGroup(t *testing.T) {
	t.Parallel()

	codec := newTestCodec(t)
	stream, err := codec.NewProviderStream(newStreamingRequest(t, codec))
	if err != nil {
		t.Fatalf("NewProviderStream() error = %v", err)
	}
	wire := joinProviderEvents(t,
		`{"id":"chatcmpl-parallel","object":"chat.completion.chunk","created":1,"model":"gpt-provider-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-0","type":"function","function":{"name":"shell","arguments":"{\"cmd\":\""}},{"index":1,"id":"call-1","type":"function","function":{"name":"shell","arguments":"{\"cmd\":\""}}]},"finish_reason":null}]}`,
		`{"id":"chatcmpl-parallel","object":"chat.completion.chunk","created":1,"model":"gpt-provider-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"second\"}"}},{"index":0,"function":{"arguments":"first\"}"}}]},"finish_reason":null}]}`,
		`{"id":"chatcmpl-parallel","object":"chat.completion.chunk","created":1,"model":"gpt-provider-model","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"id":"chatcmpl-parallel","object":"chat.completion.chunk","created":1,"model":"gpt-provider-model","choices":[],"usage":{"prompt_tokens":8,"completion_tokens":6,"total_tokens":14}}`,
		`[DONE]`,
	)
	immediate, err := feedFragments(stream, wire, 11)
	if err != nil {
		t.Fatalf("Feed() error = %v", err)
	}
	if bytes.Contains(immediate, []byte("call-0")) ||
		bytes.Contains(immediate, []byte("call-1")) {
		t.Fatalf("parallel tool identifiers leaked before decision: %s", immediate)
	}
	pending, err := stream.FinishDecoded(context.Background())
	if err != nil {
		t.Fatalf("FinishDecoded() error = %v", err)
	}
	intents := pending.ToolIntents()
	if len(intents) != 2 ||
		intents[0].Ordinal != 0 ||
		intents[1].Ordinal != 1 ||
		string(intents[0].Call.Arguments.Bytes()) != `{"cmd":"first"}` ||
		string(intents[1].Call.Arguments.Bytes()) != `{"cmd":"second"}` {
		t.Fatalf("parallel tool intents = %#v", intents)
	}
	release, err := pending.Approve()
	if err != nil {
		t.Fatalf("Approve() error = %v", err)
	}
	if bytes.Index(release, []byte("call-0")) > bytes.Index(release, []byte("call-1")) {
		t.Fatalf("approved tool blocks are not in canonical index order: %s", release)
	}
}

func TestRejectedTerminalCannotLaterBeApproved(t *testing.T) {
	t.Parallel()

	codec := newTestCodec(t)
	stream, err := codec.NewProviderStream(newStreamingRequest(t, codec))
	if err != nil {
		t.Fatalf("NewProviderStream() error = %v", err)
	}
	wire := joinProviderEvents(t,
		`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-provider-model","choices":[{"index":0,"delta":{"content":"text"},"finish_reason":"stop"}]}`,
		`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-provider-model","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`,
		`[DONE]`,
	)
	if _, err := stream.Feed(context.Background(), wire); err != nil {
		t.Fatalf("Feed() error = %v", err)
	}
	pending, err := stream.FinishDecoded(context.Background())
	if err != nil {
		t.Fatalf("FinishDecoded() error = %v", err)
	}
	if err := pending.Reject(); err != nil {
		t.Fatalf("Reject() error = %v", err)
	}
	if release, err := pending.Approve(); !errors.Is(err, ErrTerminalAlreadyDecided) || release != nil {
		t.Fatalf("Approve() = %q, %v; want nil, ErrTerminalAlreadyDecided", release, err)
	}
}

func TestProviderStreamCountsOnlyValidatedSemanticProgress(t *testing.T) {
	t.Parallel()

	codec := newTestCodec(t)
	stream, err := codec.NewProviderStream(newStreamingRequest(t, codec))
	if err != nil {
		t.Fatalf("NewProviderStream() error = %v", err)
	}
	if _, err := stream.Feed(
		context.Background(),
		[]byte(": provider keepalive\n\n"),
	); err != nil {
		t.Fatalf("feed provider keepalive: %v", err)
	}
	if stream.SemanticProgress() != 0 {
		t.Fatal("provider wire keepalive counted as semantic progress")
	}

	wire := joinProviderEvents(
		t,
		`{"id":"chatcmpl-progress","object":"chat.completion.chunk","created":1,"model":"gpt-provider-model","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`[DONE]`,
	)
	split := len(wire) / 2
	if _, err := stream.Feed(context.Background(), wire[:split]); err != nil {
		t.Fatalf("feed incomplete provider event: %v", err)
	}
	if stream.SemanticProgress() != 0 {
		t.Fatal("incomplete provider event counted as semantic progress")
	}
	if _, err := stream.Feed(context.Background(), wire[split:]); err != nil {
		t.Fatalf("feed completed provider events: %v", err)
	}
	if stream.SemanticProgress() != 2 {
		t.Fatalf(
			"semantic progress = %d, want two validated payloads",
			stream.SemanticProgress(),
		)
	}
}

func TestProviderStreamRepeatedNoOpChunksDoNotAdvanceProgress(t *testing.T) {
	t.Parallel()

	codec := newTestCodec(t)
	stream, err := codec.NewProviderStream(newStreamingRequest(t, codec))
	if err != nil {
		t.Fatalf("NewProviderStream() error = %v", err)
	}
	first := `{"id":"chatcmpl-noop","object":"chat.completion.chunk","created":1,"model":"gpt-provider-model","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`
	noop := `{"id":"chatcmpl-noop","object":"chat.completion.chunk","created":1,"model":"gpt-provider-model","choices":[{"index":0,"delta":{"content":""},"finish_reason":null}]}`
	usage := `{"id":"chatcmpl-noop","object":"chat.completion.chunk","created":1,"model":"gpt-provider-model","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":0,"total_tokens":2}}`
	if _, err := stream.Feed(
		context.Background(),
		joinProviderEvents(t, first, noop, noop, usage, usage),
	); err != nil {
		t.Fatalf("Feed() error = %v", err)
	}
	if stream.SemanticProgress() != 1 {
		t.Fatalf(
			"semantic progress = %d, want message start only",
			stream.SemanticProgress(),
		)
	}
}

func TestPendingTerminalHasExactlyOneConcurrentDecision(t *testing.T) {
	t.Parallel()

	codec := newTestCodec(t)
	stream, err := codec.NewProviderStream(newStreamingRequest(t, codec))
	if err != nil {
		t.Fatalf("NewProviderStream() error = %v", err)
	}
	wire := joinProviderEvents(t,
		`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-provider-model","choices":[{"index":0,"delta":{"content":"text"},"finish_reason":"stop"}]}`,
		`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-provider-model","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`,
		`[DONE]`,
	)
	if _, err := stream.Feed(context.Background(), wire); err != nil {
		t.Fatalf("Feed() error = %v", err)
	}
	pending, err := stream.FinishDecoded(context.Background())
	if err != nil {
		t.Fatalf("FinishDecoded() error = %v", err)
	}

	var successes atomic.Int32
	var wait sync.WaitGroup
	for index := 0; index < 32; index++ {
		wait.Add(1)
		go func(approve bool) {
			defer wait.Done()
			var decisionErr error
			if approve {
				_, decisionErr = pending.Approve()
			} else {
				decisionErr = pending.Reject()
			}
			if decisionErr == nil {
				successes.Add(1)
			} else if !errors.Is(decisionErr, ErrTerminalAlreadyDecided) {
				t.Errorf("decision error = %v", decisionErr)
			}
		}(index%2 == 0)
	}
	wait.Wait()
	if successes.Load() != 1 {
		t.Fatalf("successful decision count = %d, want 1", successes.Load())
	}
}

func TestStreamCancellationAndTruncationAreAborts(t *testing.T) {
	t.Parallel()

	codec := newTestCodec(t)
	request := newStreamingRequest(t, codec)

	t.Run("canceled", func(t *testing.T) {
		stream, err := codec.NewProviderStream(request)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := stream.Feed(ctx, []byte("data: {}\n\n")); protocolcore.ReasonOf(err) != protocolcore.ReasonOperationCanceled {
			t.Fatalf("Feed() error = %v", err)
		}
	})

	t.Run("missing terminal", func(t *testing.T) {
		stream, err := codec.NewProviderStream(request)
		if err != nil {
			t.Fatal(err)
		}
		wire := joinProviderEvents(t,
			`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-provider-model","choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":null}]}`,
		)
		if _, err := stream.Feed(context.Background(), wire); err != nil {
			t.Fatal(err)
		}
		if _, err := stream.FinishDecoded(context.Background()); protocolcore.ReasonOf(err) != protocolcore.ReasonTruncatedEventStream {
			t.Fatalf("FinishDecoded() error = %v", err)
		}
	})
}

func newTestCodec(t *testing.T) *Codec {
	t.Helper()
	codec, err := New(DefaultOptions())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return codec
}

func newStreamingRequest(t *testing.T, codec *Codec) protocolcore.Request {
	t.Helper()
	request, _, err := codec.DecodeClientRequest([]byte(`{
		"model":"claude-client-alias",
		"max_tokens":64,
		"stream":true,
		"messages":[{"role":"user","content":"hello"}],
		"tools":[{
			"name":"shell",
			"description":"Run a command.",
			"input_schema":{"type":"object","properties":{"cmd":{"type":"string"}}}
		}]
	}`))
	if err != nil {
		t.Fatalf("DecodeClientRequest() error = %v", err)
	}
	request, err = request.WithEffectiveModel("gpt-provider-model")
	if err != nil {
		t.Fatalf("WithEffectiveModel() error = %v", err)
	}
	return request
}

func joinProviderEvents(t *testing.T, payloads ...string) []byte {
	t.Helper()
	var wire bytes.Buffer
	for _, payload := range payloads {
		encoded, err := ssewire.Encode(ssewire.Event{
			Name: "message",
			Data: []byte(payload),
		})
		if err != nil {
			t.Fatalf("Encode() error = %v", err)
		}
		wire.Write(encoded)
	}
	return wire.Bytes()
}

func feedFragments(
	stream *ProviderStream,
	wire []byte,
	fragmentSize int,
) ([]byte, error) {
	var output bytes.Buffer
	for offset := 0; offset < len(wire); {
		end := min(offset+fragmentSize, len(wire))
		produced, err := stream.Feed(context.Background(), wire[offset:end])
		output.Write(produced)
		if err != nil {
			return output.Bytes(), err
		}
		offset = end
	}
	return output.Bytes(), nil
}

func decodeClientEvents(t *testing.T, wire []byte) []ssewire.Event {
	t.Helper()
	decoder, err := ssewire.NewDecoder(ssewire.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	events, err := decoder.Feed(wire)
	if err != nil {
		t.Fatalf("Feed() error = %v", err)
	}
	if err := decoder.Finish(); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	return events
}

func assertEventOrder(t *testing.T, events []ssewire.Event, names ...string) {
	t.Helper()
	if len(events) != len(names) {
		t.Fatalf("event count = %d, want %d", len(events), len(names))
	}
	for index, name := range names {
		if events[index].Name != name {
			t.Fatalf("event %d name = %q, want %q", index, events[index].Name, name)
		}
	}
}

func reportHasNotice(
	report protocolcore.TranslationReport,
	code protocolcore.NoticeCode,
) bool {
	for _, notice := range report.Notices() {
		if notice.Code == code {
			return true
		}
	}
	return false
}

func FuzzDecodeClientRequest(f *testing.F) {
	f.Add([]byte(`{"model":"claude","max_tokens":8,"messages":[{"role":"user","content":"hello"}]}`))
	f.Add([]byte(`{"model":"claude","max_tokens":8,"messages":[]}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		options := DefaultOptions()
		options.MaxRequestBytes = 64 << 10
		codec, err := New(options)
		if err != nil {
			t.Fatal(err)
		}
		_, _, _ = codec.DecodeClientRequest(body)
	})
}

func TestTranslationReasonsAreLanguageIndependent(t *testing.T) {
	t.Parallel()
	for _, reason := range []protocolcore.Reason{
		protocolcore.ReasonInvalidClientRequest,
		protocolcore.ReasonUnsupportedClientInput,
		protocolcore.ReasonInvalidProviderResponse,
		protocolcore.ReasonMalformedEventStream,
		protocolcore.ReasonToolCallIncomplete,
	} {
		if strings.ContainsAny(string(reason), "ABCDEFGHIJKLMNOPQRSTUVWXYZ -") {
			t.Fatalf("reason code %q is not a stable lowercase token", reason)
		}
	}
}
