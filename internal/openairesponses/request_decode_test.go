package openairesponses

import (
	"bytes"
	"encoding/json"
	"testing"

	openai "github.com/openai/openai-go/v3"
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
				"content":[{"type":"input_text","text":"Be exact."}],
				"internal_chat_message_metadata_passthrough":{
					"turn_id":"019fa87c-10ea-7d91-951b-8c425d40bcd5"
				}
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
		protocolcore.NoticeInternalMessageMetadataNotForwarded,
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

func TestDecodeCompatibleRequestPreservesResponsesReasoningHistory(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model":"gpt-5.6-sol",
		"input":[
			{
				"id":"rs_1",
				"type":"reasoning",
				"summary":[{"type":"summary_text","text":"Checked the repository state."}],
				"content":[],
				"encrypted_content":"opaque-provider-state",
				"status":"completed"
			},
			{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"I will inspect it."}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"Continue."}]}
		],
		"stream":true
	}`)
	request, _, err := newTestCodec(t).DecodeCompatibleClientRequest(body)
	if err != nil {
		t.Fatalf("DecodeCompatibleClientRequest() error = %v", err)
	}
	if len(request.Messages) != 3 || request.Messages[0].Role != protocolcore.RoleAssistant ||
		len(request.Messages[0].Blocks) != 2 {
		t.Fatalf("reasoning history = %#v", request.Messages)
	}
	summary := request.Messages[0].Blocks[0].ProviderExtension
	encrypted := request.Messages[0].Blocks[1].ProviderExtension
	if summary.Source() != protocolcore.ProviderExtensionSourceOpenAIResponses ||
		summary.Kind() != protocolcore.ProviderExtensionReasoningSummary ||
		encrypted.Kind() != protocolcore.ProviderExtensionReasoningEncryptedContent {
		t.Fatalf("reasoning extensions = %#v / %#v", summary, encrypted)
	}
	if !bytes.Contains(summary.Fragments()[0], []byte("Checked the repository state.")) ||
		!bytes.Contains(encrypted.Fragments()[0], []byte("opaque-provider-state")) {
		t.Fatalf("reasoning fragments were not preserved")
	}
}

func TestDecodeCompatibleRequestPreservesOfficialMultiAgentEvidence(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model":"gpt-5.6-sol",
		"input":[
			{
				"id":"agent-message-1",
				"type":"agent_message",
				"author":"root",
				"recipient":"reviewer",
				"agent":{"agent_name":"root"},
				"content":[
					{"type":"input_text","text":"Inspect the protocol boundary."},
					{"type":"encrypted_content","encrypted_content":"opaque-agent-state"}
				]
			},
			{
				"id":"multi-agent-call-1",
				"type":"multi_agent_call",
				"action":"spawn_agent",
				"arguments":"{\"task_name\":\"reviewer\"}",
				"call_id":"agent-call-1"
			},
			{
				"id":"multi-agent-output-1",
				"type":"multi_agent_call_output",
				"action":"spawn_agent",
				"call_id":"agent-call-1",
				"output":[{"type":"output_text","text":"reviewer started"}]
			}
		],
		"store":false,
		"stream":true
	}`)
	var oracle openai.BetaResponseNewParams
	if err := json.Unmarshal(body, &oracle); err != nil {
		t.Fatalf("official OpenAI beta SDK rejected fixture: %v", err)
	}
	oracleItems := oracle.Input.OfInputItemList
	if len(oracleItems) != 3 ||
		oracleItems[0].OfAgentMessage == nil ||
		oracleItems[1].OfMultiAgentCall == nil ||
		oracleItems[2].OfMultiAgentCallOutput == nil {
		t.Fatalf("official OpenAI beta SDK did not classify multi-agent fixture: %#v", oracleItems)
	}

	request, report, err := newTestCodec(t).DecodeCompatibleClientRequest(body)
	if err != nil {
		t.Fatalf("DecodeCompatibleClientRequest() error = %v", err)
	}
	if len(request.Messages) != 3 {
		t.Fatalf("message count = %d, want 3", len(request.Messages))
	}
	agentMessage := request.Messages[0]
	if agentMessage.Agent == nil || agentMessage.Agent.AgentName != "root" ||
		agentMessage.Agent.Author != "root" || agentMessage.Agent.Recipient != "reviewer" ||
		len(agentMessage.Blocks) != 2 ||
		agentMessage.Blocks[1].ProviderExtension.Kind() !=
			protocolcore.ProviderExtensionAgentMessageEncryptedContent {
		t.Fatalf("agent message = %#v", agentMessage)
	}
	callMessage := request.Messages[1]
	call := callMessage.Blocks[0].ToolCall
	if callMessage.Agent != nil || call.Namespace != "multi_agent" ||
		call.Name != "spawn_agent" || call.Key.WireID() != "agent-call-1" {
		t.Fatalf("multi-agent call = %#v", callMessage)
	}
	outputMessage := request.Messages[2]
	if outputMessage.Agent != nil ||
		outputMessage.Blocks[0].ToolResult.Key != call.Key ||
		outputMessage.Blocks[0].ToolResult.Content != "reviewer started" {
		t.Fatalf("multi-agent output = %#v", outputMessage)
	}
	if !reportHasNotice(report, protocolcore.NoticeAgentItemIdentityNotForwarded) {
		t.Fatalf("translation report = %#v", report.Notices())
	}
}

func TestDecodeCurrentResponsesInstructionsIdentityAndHostedToolHonestly(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model":"gpt-client-alias",
		"instructions":"Keep the response exact.",
		"input":[{
			"type":"message",
			"id":"msg_current_1",
			"role":"user",
			"content":[{"type":"input_text","text":"Reply with ready."}]
		}],
		"tools":[{"type":"web_search","external_web_access":true}],
		"tool_choice":"auto",
		"store":false,
		"stream":true
	}`)
	var oracle responses.ResponseNewParams
	if err := json.Unmarshal(body, &oracle); err != nil {
		t.Fatalf("official OpenAI SDK rejected fixture: %v", err)
	}

	request, report, err := newTestCodec(t).DecodeClientRequest(body)
	if err != nil {
		t.Fatalf("DecodeClientRequest() error = %v", err)
	}
	if len(request.System) != 1 ||
		request.System[0].Text != "Keep the response exact." ||
		len(request.Messages) != 1 ||
		request.Messages[0].Role != protocolcore.RoleUser ||
		len(request.Tools) != 0 ||
		len(request.ToolNamespaces) != 0 ||
		request.ToolChoice.Mode != protocolcore.ToolChoiceAuto {
		t.Fatalf("decoded request = %#v", request)
	}
	for _, code := range []protocolcore.NoticeCode{
		protocolcore.NoticeMessageItemIdentityNotForwarded,
		protocolcore.NoticeHostedToolNotForwarded,
	} {
		if !reportHasNotice(report, code) {
			t.Fatalf("translation report = %#v, want %q", report.Notices(), code)
		}
	}

	unknownHostedField := []byte(`{
		"model":"gpt-client-alias",
		"input":[{"type":"message","role":"user","content":"ready"}],
		"tools":[{"type":"web_search","silent_passthrough":true}],
		"stream":true
	}`)
	if _, _, err := newTestCodec(t).DecodeClientRequest(unknownHostedField); err == nil {
		t.Fatal("unknown hosted-tool semantics were silently accepted")
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

	body := []byte(`{
		"model":"gpt-5.6-sol",
		"input":[{"type":"message","role":"user","content":"hello"}],
		"store":false,
		"stream":true,
		"private_extension":true
	}`)
	var oracle responses.ResponseNewParams
	if err := json.Unmarshal(body, &oracle); err != nil {
		t.Fatalf("official OpenAI SDK rejected extension fixture: %v", err)
	}
	_, _, err := newTestCodec(t).DecodeClientRequest(body)
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
			name: "background execution",
			body: `{
				"model":"gpt-5.6-sol",
				"input":[{"type":"message","role":"user","content":"hello"}],
				"background":true
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
			name: "unsupported tool output content kind",
			body: `{
				"model":"gpt-5.6-sol",
				"input":[
					{"type":"message","role":"user","content":"hello"},
					{
						"type":"function_call_output",
						"call_id":"call-1",
						"output":[{
							"type":"input_image",
							"image_url":"data:image/png;base64,AA=="
						}]
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

func TestDecodeResponsesMessageMetadataRejectsInvalidShape(t *testing.T) {
	t.Parallel()

	for _, metadata := range []string{
		`"not-an-object"`,
		`{"turn_id":""}`,
		`{"turn_id":"turn-1","unknown":true}`,
	} {
		_, _, err := newTestCodec(t).DecodeClientRequest([]byte(`{
			"model":"gpt-5.6-sol",
			"input":[{
				"type":"message",
				"role":"user",
				"content":"hello",
				"internal_chat_message_metadata_passthrough":` + metadata + `
			}]
		}`))
		if protocolcore.ReasonOf(err) !=
			protocolcore.ReasonInvalidClientRequest {
			t.Fatalf("metadata %s error = %v", metadata, err)
		}
	}
}

func TestDecodeCodexRequestRetainsLatestInputTurnIDForExactRolloutJoin(t *testing.T) {
	t.Parallel()

	request, _, err := newTestCodec(t).DecodeClientRequest([]byte(`{
		"model":"gpt-5.6-sol",
		"input":[
			{
				"type":"message",
				"role":"assistant",
				"content":[{"type":"output_text","text":"older"}],
				"internal_chat_message_metadata_passthrough":{"turn_id":"turn-parent"}
			},
			{
				"type":"message",
				"role":"user",
				"content":[{"type":"input_text","text":"current"}],
				"internal_chat_message_metadata_passthrough":{"turn_id":"turn-child"}
			}
		],
		"store":false,
		"stream":true
	}`))
	if err != nil {
		t.Fatalf("DecodeClientRequest() error = %v", err)
	}
	if len(request.ProtocolEvidence) != 1 ||
		request.ProtocolEvidence[0].Name != "openai_responses.turn_id" ||
		request.ProtocolEvidence[0].Value != "turn-child" {
		t.Fatalf("protocol evidence = %#v", request.ProtocolEvidence)
	}
}

func TestDecodeResponsesToolHistoryAcceptsBoundedItemMetadataWithoutRetainingIt(
	t *testing.T,
) {
	t.Parallel()

	request, report, err := newTestCodec(t).DecodeClientRequest([]byte(`{
		"model":"gpt-5.6-sol",
		"input":[
			{
				"type":"function_call",
				"id":"item-function-1",
				"call_id":"call-function-1",
				"name":"read_file",
				"arguments":"{\"path\":\"README.md\"}",
				"internal_chat_message_metadata_passthrough":{"turn_id":"turn-1"}
			},
			{
				"type":"function_call_output",
				"call_id":"call-function-1",
				"output":"ok",
				"internal_chat_message_metadata_passthrough":{"turn_id":"turn-1"}
			},
			{
				"type":"custom_tool_call",
				"call_id":"call-custom-1",
				"name":"exec",
				"input":"pwd",
				"internal_chat_message_metadata_passthrough":{"turn_id":"turn-1"}
			},
			{
				"type":"custom_tool_call_output",
				"call_id":"call-custom-1",
				"output":[
					{"type":"input_text","text":"/tmp"},
					{"type":"input_text","text":"complete"}
				],
				"internal_chat_message_metadata_passthrough":{"turn_id":"turn-1"}
			}
		],
		"store":false,
		"stream":true
	}`))
	if err != nil {
		t.Fatalf("DecodeClientRequest() error = %v", err)
	}
	if len(request.Messages) != 4 {
		t.Fatalf("message count = %d, want 4", len(request.Messages))
	}
	if !request.Messages[2].Blocks[0].ToolCall.ItemKey.IsZero() {
		t.Fatalf(
			"absent custom item ID became %#v",
			request.Messages[2].Blocks[0].ToolCall.ItemKey,
		)
	}
	if request.Messages[3].Blocks[0].ToolResult.Content !=
		"/tmp\ncomplete" {
		t.Fatalf(
			"normalized tool output = %q",
			request.Messages[3].Blocks[0].ToolResult.Content,
		)
	}
	var metadataNotices int
	var outputNotices int
	for _, notice := range report.Notices() {
		if notice.Code ==
			protocolcore.NoticeInternalMessageMetadataNotForwarded {
			metadataNotices++
		}
		if notice.Code ==
			protocolcore.NoticeToolOutputContentNormalized {
			outputNotices++
		}
	}
	if metadataNotices != 4 {
		t.Fatalf(
			"metadata notice count = %d, want 4; report=%#v",
			metadataNotices,
			report.Notices(),
		)
	}
	if outputNotices != 1 {
		t.Fatalf(
			"tool output notice count = %d, want 1; report=%#v",
			outputNotices,
			report.Notices(),
		)
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
