package openairesponses

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	openai "github.com/openai/openai-go/v3"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/ssewire"
)

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
	if _, err := stream.Feed(context.Background(), encoded); err != nil {
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
	if !bytes.Equal(released, encoded) {
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

func TestProviderStreamReleasesOnlyPortableConversationHistory(t *testing.T) {
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
	if _, err := stream.Feed(context.Background(), encoded); err != nil {
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
	decoder, err := ssewire.NewDecoder(ssewire.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	events, err := decoder.Feed(released)
	if err != nil {
		t.Fatal(err)
	}
	if err := decoder.Finish(); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("portable client event count = %d; wire=%s", len(events), released)
	}
	var itemEvent struct {
		Type        string          `json:"type"`
		OutputIndex int             `json:"output_index"`
		Item        json.RawMessage `json:"item"`
	}
	if err := json.Unmarshal(events[0].Data, &itemEvent); err != nil {
		t.Fatal(err)
	}
	var item inputTypeWire
	if err := json.Unmarshal(itemEvent.Item, &item); err != nil {
		t.Fatal(err)
	}
	if itemEvent.Type != "response.output_item.done" ||
		itemEvent.OutputIndex != 0 || item.Type != "message" {
		t.Fatalf("portable output item = %#v / %s", itemEvent, itemEvent.Item)
	}
	var completed struct {
		Type     string `json:"type"`
		Response struct {
			Output []json.RawMessage `json:"output"`
		} `json:"response"`
	}
	if err := json.Unmarshal(events[1].Data, &completed); err != nil {
		t.Fatal(err)
	}
	if completed.Type != "response.completed" || len(completed.Response.Output) != 1 {
		t.Fatalf("portable terminal = %#v", completed)
	}
	if err := json.Unmarshal(completed.Response.Output[0], &item); err != nil {
		t.Fatal(err)
	}
	if item.Type != "message" || bytes.Contains(released, []byte(`"type":"reasoning"`)) ||
		bytes.Contains(released, []byte("provider-private plaintext reasoning")) {
		t.Fatalf("released stream retained provider-private reasoning: %s", released)
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
