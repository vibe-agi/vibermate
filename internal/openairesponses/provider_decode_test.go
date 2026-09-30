package openairesponses

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	openai "github.com/openai/openai-go/v3"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/ssewire"
)

func TestProviderStreamReportsExplicitFailuresWithoutLeakingProviderText(t *testing.T) {
	for _, kind := range []string{"response.failed", "error"} {
		t.Run(kind, func(t *testing.T) {
			stream, err := newTestCodec(t).NewProviderStream(streamingRequestFixture(t))
			if err != nil {
				t.Fatal(err)
			}
			wire := appendResponseEvent(t, kind, map[string]any{
				"type": kind, "error": map[string]string{"message": "private upstream text"},
				"response": map[string]any{"status": "failed", "error": map[string]string{"message": "private upstream text"}},
			})
			released, err := stream.Feed(context.Background(), wire)
			if kind == "error" {
				if err != nil || !bytes.Equal(released, wire) || stream.TerminalReceived() {
					t.Fatalf("notification closed the stream: err=%v bytes=%q", err, released)
				}
				_, err = stream.FinishDecoded(context.Background())
			} else if len(released) != 0 {
				t.Fatal("failed response released content")
			}
			if protocolcore.ReasonOf(err) != protocolcore.ReasonProviderResponseFailed || strings.Contains(err.Error(), "private upstream text") {
				t.Fatalf("provider failure was hidden or leaked: %v", err)
			}
			if _, err := stream.FinishDecoded(context.Background()); err == nil {
				t.Fatal("failed stream became a successful terminal")
			}
		})
	}
}

func TestProviderFailureCodeSurvivesResponsesEnvelopes(t *testing.T) {
	for _, payload := range []string{
		`{"type":"response.failed","response":{"status":"failed","error":{"code":"context_length_exceeded","message":"private"}}}`,
		`{"type":"error","code":"context_length_exceeded","message":"private"}`,
		`{"type":"error","error":{"code":"context_length_exceeded","message":"private"}}`,
	} {
		stream, err := newTestCodec(t).NewProviderStream(streamingRequestFixture(t))
		if err != nil {
			t.Fatal(err)
		}
		_, err = stream.Feed(context.Background(), []byte("data: "+payload+"\n\n"))
		if err == nil {
			_, err = stream.FinishDecoded(context.Background())
		}
		if protocolcore.ProviderErrorCodeOf(err) != "context_length_exceeded" || strings.Contains(err.Error(), "private") {
			t.Fatalf("provider code lost: %v", err)
		}
	}
	_, _, err := newTestCodec(t).DecodeProviderResponse(streamingRequestFixture(t), []byte(`{"status":"failed","error":{"code":"invalid_encrypted_content","message":"private"}}`))
	if protocolcore.ReasonOf(err) != protocolcore.ReasonProviderResponseFailed || protocolcore.ProviderErrorCodeOf(err) != "invalid_encrypted_content" {
		t.Fatalf("JSON failure lost its code: %v", err)
	}
}

func TestProviderResponsePreservesOfficialAgentOutputEvidence(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"id":"resp_agents",
		"created_at":1,
		"status":"completed",
		"model":"gpt-5.6-sol",
		"output":[
			{
				"id":"agent-message-1",
				"type":"agent_message",
				"author":"root",
				"recipient":"reviewer",
				"agent":{"agent_name":"root"},
				"content":[
					{"type":"output_text","text":"Inspect the decoder."},
					{"type":"summary_text","text":"Found one missing union arm."},
					{"type":"encrypted_content","encrypted_content":"opaque-agent-state"},
					{"type":"input_file","file_id":"file-1"}
				]
			},
			{
				"id":"multi-agent-call-1",
				"type":"multi_agent_call",
				"action":"spawn_agent",
				"arguments":"{\"task_name\":\"reviewer\"}",
				"call_id":"agent-call-1",
				"agent":{"agent_name":"root"}
			},
			{
				"id":"multi-agent-output-1",
				"type":"multi_agent_call_output",
				"action":"spawn_agent",
				"call_id":"agent-call-1",
				"output":[{"type":"output_text","text":"reviewer started"}],
				"agent":{"agent_name":"reviewer"}
			}
		],
		"usage":{}
	}`)
	var oracle openai.BetaResponse
	if err := json.Unmarshal(body, &oracle); err != nil {
		t.Fatalf("official OpenAI SDK rejected fixture: %v", err)
	}
	if len(oracle.Output) != 3 || oracle.Output[0].AsAgentMessage().ID == "" ||
		oracle.Output[1].AsMultiAgentCall().CallID == "" ||
		oracle.Output[2].AsMultiAgentCallOutput().CallID == "" {
		t.Fatalf("official OpenAI SDK did not classify agent output: %#v", oracle.Output)
	}

	response, _, err := newTestCodec(t).DecodeProviderResponse(streamingRequestFixture(t), body)
	if err != nil {
		t.Fatalf("DecodeProviderResponse() error = %v", err)
	}
	if len(response.Blocks) != 6 {
		t.Fatalf("response blocks = %#v", response.Blocks)
	}
	if response.Blocks[0].Text != "Inspect the decoder." ||
		response.Blocks[0].Agent == nil ||
		response.Blocks[0].Agent.Author != "root" ||
		response.Blocks[0].Agent.Recipient != "reviewer" {
		t.Fatalf("agent text block = %#v", response.Blocks[0])
	}
	if response.Blocks[1].ProviderExtension.Kind() != protocolcore.ProviderExtensionReasoningSummary ||
		response.Blocks[2].ProviderExtension.Kind() != protocolcore.ProviderExtensionAgentMessageEncryptedContent ||
		response.Blocks[3].ProviderExtension.Kind() != protocolcore.ProviderExtensionAgentMessageFile {
		t.Fatalf("agent extension blocks = %#v", response.Blocks[1:4])
	}
	call := response.Blocks[4]
	result := response.Blocks[5]
	if call.ToolCall.Namespace != "multi_agent" || call.ToolCall.Name != "spawn_agent" ||
		call.Agent == nil || call.Agent.AgentName != "root" ||
		result.ToolResult.Key != call.ToolCall.Key || result.ToolResult.Namespace != "multi_agent" ||
		result.ToolResult.Name != "spawn_agent" || result.Agent == nil ||
		result.Agent.AgentName != "reviewer" {
		t.Fatalf("multi-agent lifecycle = %#v / %#v", call, result)
	}
}

func TestProviderResponseAcceptsCurrentOpaqueCodexOutputItems(t *testing.T) {
	t.Parallel()

	for _, item := range []string{
		`{"type":"tool_search_call","call_id":"search_1","execution":"server","arguments":{"query":"files"}}`,
		`{"type":"tool_search_output","call_id":"search_1","status":"completed","execution":"client","tools":[]}`,
		`{"type":"web_search_call","id":"web_1","status":"completed"}`,
		`{"type":"image_generation_call","id":"image_1","status":"completed","result":"opaque"}`,
		`{"type":"compaction","id":"cmp_1","encrypted_content":"opaque-context"}`,
		`{"type":"compaction_summary","encrypted_content":"opaque-context"}`,
		`{"type":"context_compaction","id":"ctx_1","encrypted_content":"opaque-context"}`,
	} {
		var expected struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(item), &expected); err != nil {
			t.Fatal(err)
		}
		t.Run(expected.Type, func(t *testing.T) {
			body := []byte(`{
				"id":"resp_opaque",
				"created_at":1,
				"status":"completed",
				"model":"provider-model",
				"output":[` + item + `],
				"usage":{}
			}`)
			response, _, err := newTestCodec(t).DecodeProviderResponse(
				streamingRequestFixture(t),
				body,
			)
			if err != nil {
				t.Fatalf("DecodeProviderResponse() error = %v", err)
			}
			if len(response.Blocks) != 1 ||
				response.Blocks[0].Kind != protocolcore.BlockProviderExtension ||
				response.Blocks[0].ProviderExtension.Kind() !=
					protocolcore.ProviderExtensionOpaqueItem {
				t.Fatalf("decoded output = %#v", response.Blocks)
			}
		})
	}

}

func TestProviderStreamUsesCompletedItemsWhenTerminalOutputIsEmpty(t *testing.T) {
	t.Parallel()

	request := streamingRequestFixture(t)
	stream, err := newTestCodec(t).NewProviderStream(request)
	if err != nil {
		t.Fatalf("NewProviderStream() error = %v", err)
	}
	item := json.RawMessage(`{
		"id":"msg_1",
		"type":"message",
		"status":"completed",
		"role":"assistant",
		"content":[{"type":"output_text","text":"ready"}]
	}`)
	terminal := json.RawMessage(`{
		"id":"resp_1",
		"created_at":1,
		"status":"completed",
		"model":"provider-model",
		"output":[],
		"usage":{
			"input_tokens":4,
			"input_tokens_details":{"cached_tokens":0},
			"output_tokens":2,
			"output_tokens_details":{"reasoning_tokens":0}
		}
	}`)
	encoded := appendResponseEvent(t, "response.output_item.done", map[string]any{
		"type":            "response.output_item.done",
		"sequence_number": 1,
		"output_index":    0,
		"item":            item,
	})
	encoded = append(encoded, appendResponseEvent(t, "response.completed", map[string]any{
		"type":            "response.completed",
		"sequence_number": 2,
		"response":        terminal,
	})...)

	if _, err := stream.Feed(context.Background(), encoded); err != nil {
		t.Fatalf("Feed() error = %v", err)
	}
	pending, err := stream.FinishDecoded(context.Background())
	if err != nil {
		t.Fatalf("FinishDecoded() error = %v", err)
	}
	response := pending.DecodedResponse()
	if len(response.Blocks) != 1 || response.Blocks[0].Kind != protocolcore.BlockText ||
		response.Blocks[0].Text != "ready" ||
		response.RequestedModel != "gpt-5.6-sol" ||
		response.EffectiveModel != "provider-model" ||
		response.ReportedModel != "provider-model" {
		t.Fatalf("decoded response = %#v", response)
	}
	released, err := pending.Approve()
	if err != nil {
		t.Fatalf("Approve() error = %v", err)
	}
	if !bytes.Contains(released, []byte(`"model":"gpt-5.6-sol"`)) ||
		bytes.Contains(released, []byte(`"model":"provider-model"`)) {
		t.Fatalf("approved stream model was not restored for the client: %s", released)
	}
}

func TestProviderStreamProgressAndApprovalBarrier(t *testing.T) {
	for _, allow := range []bool{false, true} {
		t.Run(fmt.Sprintf("allow=%t", allow), func(t *testing.T) {
			request := streamingRequestFixture(t)
			request, err := request.WithEffectiveModel(request.RequestedModel)
			if err != nil {
				t.Fatal(err)
			}
			stream, err := newTestCodec(t).NewProviderStream(request)
			if err != nil {
				t.Fatal(err)
			}
			progress := appendResponseEvent(t, "response.in_progress", map[string]any{
				"type": "response.in_progress", "response": map[string]any{"id": "resp_1"},
			})
			var safe []byte
			for _, b := range progress {
				part, err := stream.Feed(context.Background(), []byte{b})
				if err != nil {
					t.Fatal(err)
				}
				safe = append(safe, part...)
			}
			if !bytes.Equal(safe, progress) || stream.TerminalReceived() {
				t.Fatalf("progress was buffered or completed early: %q", safe)
			}
			tool := json.RawMessage(`{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{}"}`)
			held := appendResponseEvent(t, "response.output_item.done", map[string]any{
				"type": "response.output_item.done", "output_index": 0, "item": tool,
			})
			if early, err := stream.Feed(context.Background(), held); err != nil || len(early) != 0 {
				t.Fatalf("tool escaped approval: %q, %v", early, err)
			}
			notification := appendResponseEvent(t, "error", map[string]any{
				"type": "error", "code": "notification_fixture", "message": "synthetic notification",
			})
			if early, err := stream.Feed(context.Background(), notification); err != nil || !bytes.Equal(early, notification) || stream.TerminalReceived() {
				t.Fatalf("notification was held, closed the stream or released tools: %q, %v", early, err)
			}
			terminalWire := appendResponseEvent(t, "response.completed", map[string]any{
				"type": "response.completed", "response": map[string]any{
					"id": "resp_1", "created_at": 1, "status": "completed", "model": request.RequestedModel,
					"output": []json.RawMessage{tool},
				},
			})
			held = append(held, terminalWire...)
			if early, err := stream.Feed(context.Background(), terminalWire); err != nil || len(early) != 0 || !stream.TerminalReceived() {
				t.Fatalf("tool/terminal escaped approval: %q, %v", early, err)
			}
			pending, err := stream.FinishDecoded(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(pending.ToolIntents()) != 1 {
				t.Fatal("tool intent missing")
			}
			if allow {
				release, err := pending.Approve()
				if err != nil || !bytes.Equal(release, held) {
					t.Fatalf("release = %q, %v", release, err)
				}
			} else {
				if err := pending.Reject(); err != nil {
					t.Fatal(err)
				}
				if release, err := pending.Approve(); err == nil || len(release) != 0 {
					t.Fatal("rejected tool released")
				}
			}
		})
	}
}

func TestProviderStreamKeepaliveDoesNotArmOrReleaseApprovalBarrier(t *testing.T) {
	for _, approve := range []bool{false, true} {
		t.Run(fmt.Sprintf("approve=%t", approve), func(t *testing.T) {
			request := streamingRequestFixture(t)
			request, err := request.WithEffectiveModel(request.RequestedModel)
			if err != nil {
				t.Fatal(err)
			}
			stream, err := newTestCodec(t).NewProviderStream(request)
			if err != nil {
				t.Fatal(err)
			}
			heartbeat := appendResponseEvent(t, "keepalive", map[string]any{"type": "keepalive", "sequence_number": 1})
			var delivered []byte
			for _, b := range heartbeat {
				part, err := stream.Feed(context.Background(), []byte{b})
				if err != nil {
					t.Fatal(err)
				}
				delivered = append(delivered, part...)
			}
			if !bytes.Equal(delivered, heartbeat) || stream.TerminalReceived() || stream.SemanticProgress() == 0 {
				t.Fatalf("native keepalive was not delivered immediately: %q", delivered)
			}
			text := appendResponseEvent(t, "response.output_text.delta", map[string]any{"type": "response.output_text.delta", "delta": "visible text"})
			if early, err := stream.Feed(context.Background(), text); err != nil || !bytes.Equal(early, text) {
				t.Fatalf("keepalive incorrectly armed the tool barrier: %q, %v", early, err)
			}
			tool := json.RawMessage(`{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{}"}`)
			held := appendResponseEvent(t, "response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 0, "item": tool})
			if early, err := stream.Feed(context.Background(), held); err != nil || len(early) != 0 {
				t.Fatalf("tool escaped approval: %q, %v", early, err)
			}
			if early, err := stream.Feed(context.Background(), heartbeat); err != nil || !bytes.Equal(early, heartbeat) {
				t.Fatalf("keepalive was held behind a tool or released held content: %q, %v", early, err)
			}
			terminal := appendResponseEvent(t, "response.completed", map[string]any{"type": "response.completed", "response": map[string]any{
				"id": "resp_1", "created_at": 1, "status": "completed", "model": request.RequestedModel, "output": []json.RawMessage{tool},
			}})
			if early, err := stream.Feed(context.Background(), terminal); err != nil || len(early) != 0 {
				t.Fatalf("terminal escaped approval: %q, %v", early, err)
			}
			pending, err := stream.FinishDecoded(context.Background())
			if err != nil || len(pending.ToolIntents()) != 1 {
				t.Fatalf("lost tool approval: %v", err)
			}
			if approve {
				released, err := pending.Approve()
				if err != nil || !bytes.Equal(released, append(held, terminal...)) {
					t.Fatalf("held stream changed or heartbeat duplicated: %q, %v", released, err)
				}
			} else {
				if err := pending.Reject(); err != nil {
					t.Fatal(err)
				}
				if released, err := pending.Approve(); err == nil || len(released) != 0 {
					t.Fatal("heartbeat bypassed tool rejection")
				}
			}
		})
	}
}

func TestProviderStreamBoundsNormalizedWire(t *testing.T) {
	options := DefaultOptions()
	options.MaxResponseBytes = 1024
	codec, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := codec.NewProviderStream(streamingRequestFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	// SSE ids persist between frames. Re-encoding must not amplify a short
	// source into an unbounded held/client representation.
	wire := "id: " + strings.Repeat("i", 256) + "\n" + strings.Repeat("data: {\"type\":\"response.in_progress\"}\n\n", 10)
	if len(wire) >= options.MaxResponseBytes {
		t.Fatal("fixture must fit the input limit")
	}
	if _, err := stream.Feed(context.Background(), []byte(wire)); protocolcore.ReasonOf(err) != protocolcore.ReasonStreamLimitExceeded {
		t.Fatalf("normalized limit = %v", err)
	}
}

func TestProviderStreamRejectsContradictoryTerminalTool(t *testing.T) {
	stream, err := newTestCodec(t).NewProviderStream(streamingRequestFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	item := json.RawMessage(`{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{\"path\":\"private\"}"}`)
	wire := appendResponseEvent(t, "response.output_item.done", map[string]any{
		"type": "response.output_item.done", "output_index": 0, "item": item,
	})
	wire = append(wire, appendResponseEvent(t, "response.completed", map[string]any{
		"type": "response.completed", "response": map[string]any{
			"id": "resp_1", "created_at": 1, "status": "completed", "model": "provider-model",
			"output": []json.RawMessage{bytes.ReplaceAll(item, []byte("private"), []byte("public"))},
		},
	})...)
	if safe, err := stream.Feed(context.Background(), wire); err == nil || len(safe) != 0 {
		t.Fatalf("contradictory call escaped approval: %q, %v", safe, err)
	}
}

func TestProviderReasoningOnlyTerminalRetainsOpaqueAuditBlocks(t *testing.T) {
	body := []byte(`{"id":"resp_1","created_at":1,"status":"completed","model":"provider-model","output":[{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"opaque"}]}`)
	response, _, err := newTestCodec(t).DecodeProviderResponse(streamingRequestFixture(t), body)
	if err != nil || len(response.Blocks) != 1 || response.Blocks[0].Kind != protocolcore.BlockProviderExtension {
		t.Fatalf("reasoning-only projection: %#v, %v", response, err)
	}
}

func TestProviderStreamPreservesExactWireWithoutModelMapping(t *testing.T) {
	t.Parallel()

	request := streamingRequestFixture(t)
	var err error
	request, err = request.WithEffectiveModel(request.RequestedModel)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := newTestCodec(t).NewProviderStream(request)
	if err != nil {
		t.Fatalf("NewProviderStream() error = %v", err)
	}
	item := json.RawMessage(`{
		"id":"msg_passthrough","type":"message","status":"completed",
		"role":"assistant","content":[{"type":"output_text","text":"ready"}]
	}`)
	terminal := json.RawMessage(`{
		"id":"resp_passthrough","created_at":1,"status":"completed",
		"model":"provider-model","output":[],
		"usage":{"input_tokens":4,"input_tokens_details":{"cached_tokens":0},
		"output_tokens":2,"output_tokens_details":{"reasoning_tokens":0}}
	}`)
	encoded := appendResponseEvent(t, "response.output_item.done", map[string]any{
		"type":            "response.output_item.done",
		"sequence_number": 1,
		"output_index":    0,
		"item":            item,
	})
	encoded = append(encoded, appendResponseEvent(t, "response.completed", map[string]any{
		"type":            "response.completed",
		"sequence_number": 2,
		"response":        terminal,
	})...)
	safe, err := stream.Feed(context.Background(), encoded)
	if err != nil {
		t.Fatalf("Feed() error = %v", err)
	}
	pending, err := stream.FinishDecoded(context.Background())
	if err != nil {
		t.Fatalf("FinishDecoded() error = %v", err)
	}
	released, err := pending.Approve()
	if err != nil {
		t.Fatalf("Approve() error = %v", err)
	}
	if !bytes.Equal(append(safe, released...), encoded) {
		t.Fatal("unmapped stream did not preserve the exact provider wire")
	}
}

func TestProviderStreamRecordsReasoningSummaryWithoutExposingEncryptedStateAsText(t *testing.T) {
	t.Parallel()

	request := streamingRequestFixture(t)
	stream, err := newTestCodec(t).NewProviderStream(request)
	if err != nil {
		t.Fatalf("NewProviderStream() error = %v", err)
	}
	reasoning := json.RawMessage(`{
		"id":"rs_1",
		"type":"reasoning",
		"summary":[{"type":"summary_text","text":"Inspected the requested files."}],
		"content":[],
		"encrypted_content":"opaque-provider-state",
		"status":"completed"
	}`)
	message := json.RawMessage(`{
		"id":"msg_1",
		"type":"message",
		"status":"completed",
		"role":"assistant",
		"metadata":{"turn_id":"turn_1"},
		"internal_chat_message_metadata_passthrough":{"turn_id":"turn_1"},
		"content":[{"type":"output_text","text":"ready"}]
	}`)
	terminal := json.RawMessage(`{
		"id":"resp_1",
		"created_at":1,
		"status":"completed",
		"model":"provider-model",
		"output":[],
		"usage":{}
	}`)
	encoded := appendResponseEvent(t, "response.output_item.done", map[string]any{
		"type": "response.output_item.done", "sequence_number": 1,
		"output_index": 0, "item": reasoning,
	})
	encoded = append(encoded, appendResponseEvent(t, "response.output_item.done", map[string]any{
		"type": "response.output_item.done", "sequence_number": 2,
		"output_index": 1, "item": message,
	})...)
	encoded = append(encoded, appendResponseEvent(t, "response.completed", map[string]any{
		"type": "response.completed", "sequence_number": 3, "response": terminal,
	})...)
	if _, err := stream.Feed(context.Background(), encoded); err != nil {
		t.Fatalf("Feed() error = %v", err)
	}
	pending, err := stream.FinishDecoded(context.Background())
	if err != nil {
		t.Fatalf("FinishDecoded() error = %v", err)
	}
	response := pending.DecodedResponse()
	if len(response.Blocks) != 1 || response.Blocks[0].Text != "ready" ||
		len(response.ProviderExtensions) != 2 {
		t.Fatalf("decoded response = %#v", response)
	}
	if response.ProviderExtensions[0].Kind() != protocolcore.ProviderExtensionReasoningSummary ||
		response.ProviderExtensions[1].Kind() != protocolcore.ProviderExtensionReasoningEncryptedContent {
		t.Fatalf("reasoning extensions = %#v", response.ProviderExtensions)
	}
	if len(response.ProtocolEvidence) != 4 ||
		response.ProtocolEvidence[0].Name != "openai_responses.output.0000.id" ||
		response.ProtocolEvidence[0].Value != "rs_1" ||
		response.ProtocolEvidence[1].Name != "openai_responses.output.0001.id" ||
		response.ProtocolEvidence[1].Value != "msg_1" ||
		response.ProtocolEvidence[2].Name != "openai_responses.output.0001.internal_chat_message_metadata_passthrough.turn_id" ||
		response.ProtocolEvidence[2].Value != "turn_1" ||
		response.ProtocolEvidence[3].Name != "openai_responses.output.0001.metadata.turn_id" ||
		response.ProtocolEvidence[3].Value != "turn_1" {
		t.Fatalf("response protocol evidence = %#v", response.ProtocolEvidence)
	}
}

func TestProviderStreamPreservesNativeConversationHistory(t *testing.T) {
	t.Parallel()

	request := streamingRequestFixture(t)
	var err error
	request, err = request.WithEffectiveModel(request.RequestedModel)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := newTestCodec(t).NewProviderStream(request)
	if err != nil {
		t.Fatalf("NewProviderStream() error = %v", err)
	}
	reasoning := json.RawMessage(`{
		"id":"rs_private",
		"type":"reasoning",
		"summary":[{"type":"summary_text","text":"portable-looking provider summary"}],
		"content":[{"type":"reasoning_text","text":"provider-private plaintext reasoning"}],
		"encrypted_content":null,
		"status":"completed"
	}`)
	message := json.RawMessage(`{
		"id":"msg_portable",
		"type":"message",
		"status":"completed",
		"role":"assistant",
		"content":[{"type":"output_text","text":"portable answer"}]
	}`)
	terminal := map[string]any{
		"id":         "resp_portable",
		"created_at": 1,
		"status":     "completed",
		"model":      request.RequestedModel,
		"output":     []json.RawMessage{reasoning, message},
		"usage":      map[string]any{},
	}
	encoded := appendResponseEvent(t, "response.output_item.done", map[string]any{
		"type": "response.output_item.done", "sequence_number": 1,
		"output_index": 0, "item": reasoning,
	})
	encoded = append(encoded, appendResponseEvent(t, "response.reasoning_text.delta", map[string]any{
		"type": "response.reasoning_text.delta", "sequence_number": 2,
		"output_index": 0, "item_id": "rs_private", "content_index": 0,
		"delta": "provider-private plaintext reasoning",
	})...)
	encoded = append(encoded, appendResponseEvent(t, "response.output_item.done", map[string]any{
		"type": "response.output_item.done", "sequence_number": 3,
		"output_index": 1, "item": message,
	})...)
	encoded = append(encoded, appendResponseEvent(t, "response.completed", map[string]any{
		"type": "response.completed", "sequence_number": 4, "response": terminal,
	})...)
	safe, err := stream.Feed(context.Background(), encoded)
	if err != nil {
		t.Fatalf("Feed() error = %v", err)
	}
	pending, err := stream.FinishDecoded(context.Background())
	if err != nil {
		t.Fatalf("FinishDecoded() error = %v", err)
	}
	response := pending.DecodedResponse()
	if len(response.ProviderExtensions) != 2 ||
		response.ProviderExtensions[0].Kind() != protocolcore.ProviderExtensionReasoningSummary ||
		response.ProviderExtensions[1].Kind() != protocolcore.ProviderExtensionReasoningContent {
		t.Fatalf("provider reasoning evidence = %#v", response.ProviderExtensions)
	}
	released, err := pending.Approve()
	if err != nil {
		t.Fatalf("Approve() error = %v", err)
	}

	if !bytes.Equal(append(safe, released...), encoded) {
		t.Fatalf("native stream replay state or indexes changed: %s", released)
	}
}

func TestProviderStreamRejectsNonContiguousCompletedItems(t *testing.T) {
	t.Parallel()

	stream, err := newTestCodec(t).NewProviderStream(streamingRequestFixture(t))
	if err != nil {
		t.Fatalf("NewProviderStream() error = %v", err)
	}
	item := json.RawMessage(`{
		"id":"msg_2",
		"type":"message",
		"status":"completed",
		"role":"assistant",
		"content":[{"type":"output_text","text":"ready"}]
	}`)
	encoded := appendResponseEvent(t, "response.output_item.done", map[string]any{
		"type":         "response.output_item.done",
		"output_index": 1,
		"item":         item,
	})
	encoded = append(encoded, appendResponseEvent(t, "response.completed", map[string]any{
		"type": "response.completed",
		"response": json.RawMessage(`{
			"id":"resp_2",
			"created_at":1,
			"status":"completed",
			"model":"provider-model",
			"output":[],
			"usage":{}
		}`),
	})...)

	if _, err := stream.Feed(context.Background(), encoded); err == nil {
		t.Fatal("Feed() accepted non-contiguous completed output items")
	}
}

func appendResponseEvent(t *testing.T, name string, payload any) []byte {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := ssewire.Encode(ssewire.Event{Name: name, Data: data})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
