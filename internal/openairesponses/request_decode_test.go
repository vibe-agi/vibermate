package openairesponses

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/openai/openai-go/v3/responses"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func TestDecodeFixedCodexRequestProducesTypedIRAndExplicitNotices(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model":"gpt-5.6-sol",
		"input":[
			{
				"type":"additional_tools",
				"role":"developer",
				"tools":[
					{
						"type":"custom",
						"name":"exec",
						"description":"Execute one command.",
						"format":{
							"type":"grammar",
							"syntax":"lark",
							"definition":"start: /.+/"
						}
					},
					{
						"type":"function",
						"name":"wait",
						"description":"Wait for completion.",
						"parameters":{
							"type":"object",
							"properties":{"cell_id":{"type":"string"}},
							"required":["cell_id"],
							"additionalProperties":false
						},
						"strict":false
					},
					{
						"type":"namespace",
						"name":"collaboration",
						"description":"Agent collaboration.",
						"tools":[{
							"type":"function",
							"name":"send_message",
							"description":"Send one message.",
							"parameters":{
								"type":"object",
								"properties":{
									"target":{"type":"string"},
									"message":{"type":"string"}
								},
								"required":["target","message"],
								"additionalProperties":false
							},
							"strict":false
						}]
					}
				]
			},
			{
				"type":"message",
				"role":"developer",
				"content":[{"type":"input_text","text":"Be exact."}]
			},
			{
				"type":"message",
				"role":"user",
				"content":[{"type":"input_text","text":"Inspect one file."}]
			}
		],
		"tool_choice":"auto",
		"parallel_tool_calls":false,
		"reasoning":{"context":"all_turns","effort":"low"},
		"store":false,
		"stream":true,
		"include":["reasoning.encrypted_content"],
		"prompt_cache_key":"thread-1",
		"text":{"verbosity":"low"},
		"client_metadata":{
			"session_id":"session-1",
			"thread_id":"thread-1",
			"turn_id":"turn-1"
		}
	}`)
	var oracle responses.ResponseNewParams
	if err := json.Unmarshal(body, &oracle); err != nil {
		t.Fatalf("official OpenAI SDK rejected fixture: %v", err)
	}

	codec := newTestCodec(t)
	request, report, err := codec.DecodeClientRequest(body)
	if err != nil {
		t.Fatalf("DecodeClientRequest() error = %v", err)
	}
	if request.RequestedModel != "gpt-5.6-sol" ||
		request.EffectiveModel != "gpt-5.6-sol" ||
		request.MaxOutputTokens != 0 ||
		!request.Stream ||
		len(request.Messages) != 2 ||
		request.Messages[0].Role != protocolcore.RoleDeveloper ||
		len(request.Tools) != 2 ||
		len(request.ToolNamespaces) != 1 ||
		request.ToolChoice.Mode != protocolcore.ToolChoiceAuto ||
		!request.ToolChoice.DisableParallel ||
		request.Reasoning.Context != protocolcore.ReasoningContextAllTurns ||
		request.Reasoning.Effort != protocolcore.ReasoningEffortLow ||
		request.OutputVerbosity != protocolcore.TextVerbosityLow {
		t.Fatalf("decoded request = %#v", request)
	}
	custom := request.Tools[0]
	if custom.EffectiveKind() != protocolcore.ToolKindCustom ||
		custom.Name != "exec" ||
		custom.CustomFormat.Kind != protocolcore.CustomToolFormatGrammar ||
		custom.CustomFormat.Syntax != "lark" ||
		custom.CustomFormat.Definition != "start: /.+/" {
		t.Fatalf("custom tool = %#v", custom)
	}
	function := request.Tools[1]
	if function.EffectiveKind() != protocolcore.ToolKindFunction ||
		function.Name != "wait" ||
		function.InputSchema.IsZero() ||
		!function.StrictKnown ||
		function.Strict {
		t.Fatalf("function tool = %#v", function)
	}
	namespace := request.ToolNamespaces[0]
	if namespace.Name != "collaboration" ||
		len(namespace.Tools) != 1 ||
		namespace.Tools[0].Name != "send_message" {
		t.Fatalf("tool namespace = %#v", namespace)
	}
	for _, code := range []protocolcore.NoticeCode{
		protocolcore.NoticeToolPlacementNormalized,
		protocolcore.NoticePromptCacheKeyNotForwarded,
		protocolcore.NoticeClientMetadataNotForwarded,
		protocolcore.NoticeReasoningContextNotForwarded,
		protocolcore.NoticeReasoningIncludeNotForwarded,
		protocolcore.NoticeTextVerbosityNotForwarded,
	} {
		if !reportHasNotice(report, code) {
			t.Fatalf("translation report = %#v, want %q", report.Notices(), code)
		}
	}

	offset := bytes.Index(body, []byte(`"cell_id"`))
	if offset < 0 {
		t.Fatal("fixture does not contain the function schema")
	}
	body[offset+1] = 'X'
	if !bytes.Contains(function.InputSchema.Bytes(), []byte(`"cell_id"`)) {
		t.Fatal("decoded function schema aliases the request body")
	}
}

func TestDecodeResponsesToolHistoryKeepsItemAndCallIdentitiesSeparate(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model":"gpt-5.6-sol",
		"max_output_tokens":128,
		"input":[
			{
				"type":"message",
				"role":"user",
				"content":[{"type":"input_text","text":"Continue."}]
			},
			{
				"type":"function_call",
				"id":"item-function-1",
				"call_id":"call-function-1",
				"name":"read_file",
				"arguments":"{\"path\":\"README.md\"}",
				"status":"completed"
			},
			{
				"type":"function_call_output",
				"call_id":"call-function-1",
				"output":"ok"
			},
			{
				"type":"custom_tool_call",
				"id":"item-custom-1",
				"call_id":"call-custom-1",
				"name":"exec",
				"input":"pwd"
			},
			{
				"type":"custom_tool_call_output",
				"call_id":"call-custom-1",
				"output":"/tmp"
			}
		],
		"store":false,
		"stream":true
	}`)
	var oracle responses.ResponseNewParams
	if err := json.Unmarshal(body, &oracle); err != nil {
		t.Fatalf("official OpenAI SDK rejected fixture: %v", err)
	}

	request, _, err := newTestCodec(t).DecodeClientRequest(body)
	if err != nil {
		t.Fatalf("DecodeClientRequest() error = %v", err)
	}
	if len(request.Messages) != 5 {
		t.Fatalf("message count = %d, want 5", len(request.Messages))
	}
	function := request.Messages[1].Blocks[0].ToolCall
	if function.EffectiveKind() != protocolcore.ToolKindFunction ||
		function.Key.WireID() != "call-function-1" ||
		function.ItemKey.WireID() != "item-function-1" ||
		function.Key == function.ItemKey {
		t.Fatalf("function call identity = %#v", function)
	}
	custom := request.Messages[3].Blocks[0].ToolCall
	if custom.EffectiveKind() != protocolcore.ToolKindCustom ||
		custom.Key.WireID() != "call-custom-1" ||
		custom.ItemKey.WireID() != "item-custom-1" ||
		custom.Input != "pwd" {
		t.Fatalf("custom call identity = %#v", custom)
	}
	if request.Messages[2].Blocks[0].ToolResult.Key != function.Key ||
		request.Messages[4].Blocks[0].ToolResult.Key != custom.Key {
		t.Fatal("tool output did not retain the corresponding call identity")
	}
}

func TestDecodeResponsesUnknownFieldFailsClosed(t *testing.T) {
	t.Parallel()

	_, _, err := newTestCodec(t).DecodeClientRequest([]byte(`{
		"model":"gpt-5.6-sol",
		"input":[{"type":"message","role":"user","content":"hello"}],
		"store":false,
		"stream":true,
		"private_extension":true
	}`))
	if protocolcore.ReasonOf(err) != protocolcore.ReasonInvalidClientRequest {
		t.Fatalf("DecodeClientRequest() error = %v", err)
	}
}

func TestDecodeResponsesRejectsAmbiguousOrUnsupportedControls(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{
			name: "stored state",
			body: `{
				"model":"gpt-5.6-sol",
				"input":[{"type":"message","role":"user","content":"hello"}],
				"store":true
			}`,
		},
		{
			name: "explicit zero output limit",
			body: `{
				"model":"gpt-5.6-sol",
				"max_output_tokens":0,
				"input":[{"type":"message","role":"user","content":"hello"}]
			}`,
		},
		{
			name: "duplicate nested field",
			body: `{
				"model":"gpt-5.6-sol",
				"input":[{
					"type":"message",
					"role":"user",
					"role":"assistant",
					"content":"hello"
				}]
			}`,
		},
		{
			name: "unknown nested field",
			body: `{
				"model":"gpt-5.6-sol",
				"input":[{
					"type":"message",
					"role":"user",
					"content":"hello",
					"private_extension":true
				}]
			}`,
		},
		{
			name: "unsupported output format",
			body: `{
				"model":"gpt-5.6-sol",
				"input":[{"type":"message","role":"user","content":"hello"}],
				"text":{"format":{"type":"json_object"}}
			}`,
		},
		{
			name: "unsupported include",
			body: `{
				"model":"gpt-5.6-sol",
				"input":[{"type":"message","role":"user","content":"hello"}],
				"include":["message.output_text.logprobs"]
			}`,
		},
		{
			name: "unknown tool kind",
			body: `{
				"model":"gpt-5.6-sol",
				"input":[{
					"type":"additional_tools",
					"role":"developer",
					"tools":[{"type":"computer","name":"desktop"}]
				}]
			}`,
		},
		{
			name: "unsupported grammar",
			body: `{
				"model":"gpt-5.6-sol",
				"input":[{
					"type":"additional_tools",
					"role":"developer",
					"tools":[{
						"type":"custom",
						"name":"exec",
						"format":{
							"type":"grammar",
							"syntax":"unknown",
							"definition":"start: /.+/"
						}
					}]
				}]
			}`,
		},
		{
			name: "content-array tool output",
			body: `{
				"model":"gpt-5.6-sol",
				"input":[
					{"type":"message","role":"user","content":"hello"},
					{
						"type":"function_call_output",
						"call_id":"call-1",
						"output":[{"type":"input_text","text":"value"}]
					}
				]
			}`,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := newTestCodec(t).DecodeClientRequest(
				[]byte(test.body),
			)
			if protocolcore.ReasonOf(err) !=
				protocolcore.ReasonInvalidClientRequest {
				t.Fatalf("DecodeClientRequest() error = %v", err)
			}
		})
	}
}

func TestDecodeResponsesAssistantRefusalUsesItsTypedField(t *testing.T) {
	t.Parallel()

	request, _, err := newTestCodec(t).DecodeClientRequest([]byte(`{
		"model":"gpt-5.6-sol",
		"input":[{
			"type":"message",
			"role":"assistant",
			"content":[{"type":"refusal","refusal":"Request refused."}]
		}]
	}`))
	if err != nil {
		t.Fatalf("DecodeClientRequest() error = %v", err)
	}
	block := request.Messages[0].Blocks[0]
	if block.Kind != protocolcore.BlockRefusal ||
		block.Refusal != "Request refused." {
		t.Fatalf("decoded refusal = %#v", block)
	}
}

func newTestCodec(t *testing.T) *Codec {
	t.Helper()
	codec, err := New(DefaultOptions())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return codec
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
	f.Add([]byte(`{
		"model":"gpt-5.6-sol",
		"input":[{"type":"message","role":"user","content":"hello"}],
		"store":false,
		"stream":true
	}`))
	f.Add([]byte(`{
		"model":"gpt-5.6-sol",
		"input":[{
			"type":"additional_tools",
			"role":"developer",
			"tools":[{"type":"custom","name":"exec"}]
		}]
	}`))
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
