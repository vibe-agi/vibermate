package openairesponses

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/openai/openai-go/v3/responses"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
	"github.com/vibe-agi/vibermate/internal/ssewire"
)

func TestResponsesStreamEncoderEmitsIncrementalTextAndValidTerminal(
	t *testing.T,
) {
	t.Parallel()

	request := streamingRequestFixture(t)
	response := completeResponseFixture(t)
	response.Blocks = response.Blocks[:1]
	response.StopReason = protocolcore.StopReasonEndTurn
	encoder, err := newTestCodec(t).NewStreamEncoder(request)
	if err != nil {
		t.Fatalf("NewStreamEncoder() error = %v", err)
	}

	var stream bytes.Buffer
	appendOperation(t, &stream, func() ([]byte, error) {
		return encoder.Start(protocolpath.StreamStart{
			ResponseID:    response.ID,
			CreatedAtUnix: response.CreatedAtUnix,
			ReportedModel: response.ReportedModel,
		})
	})
	appendOperation(t, &stream, func() ([]byte, error) {
		return encoder.StartText(0)
	})
	appendOperation(t, &stream, func() ([]byte, error) {
		return encoder.AppendText(0, "Inspect")
	})
	appendOperation(t, &stream, func() ([]byte, error) {
		return encoder.AppendText(0, "ing.")
	})
	appendOperation(t, &stream, func() ([]byte, error) {
		return encoder.StopText(0)
	})
	appendOperation(t, &stream, func() ([]byte, error) {
		return encoder.Terminal(response)
	})

	events := decodeResponsesEvents(t, stream.Bytes())
	wantTypes := []string{
		"response.created",
		"response.in_progress",
		"response.output_item.added",
		"response.content_part.added",
		"response.output_text.delta",
		"response.output_text.delta",
		"response.output_text.done",
		"response.content_part.done",
		"response.output_item.done",
		"response.completed",
	}
	if len(events) != len(wantTypes) {
		t.Fatalf("event count = %d, want %d", len(events), len(wantTypes))
	}
	for index, event := range events {
		if event.Type != wantTypes[index] ||
			event.SequenceNumber != int64(index) {
			t.Fatalf("event %d = %#v, want type %q", index, event, wantTypes[index])
		}
	}
	if events[4].Delta != "Inspect" || events[5].Delta != "ing." {
		t.Fatalf("text deltas = %q, %q", events[4].Delta, events[5].Delta)
	}
	completed := events[len(events)-1].AsResponseCompleted()
	if completed.Response.Status != responses.ResponseStatusCompleted ||
		completed.Response.OutputText() != "Inspecting." ||
		completed.Response.Usage.TotalTokens != 18 {
		t.Fatalf("completed response = %#v", completed.Response)
	}
}

func TestResponsesStreamEncoderKeepsToolItemAndCallIdentitiesDistinct(
	t *testing.T,
) {
	t.Parallel()

	request := streamingRequestFixture(t)
	response := completeResponseFixture(t)
	encoder, err := newTestCodec(t).NewStreamEncoder(request)
	if err != nil {
		t.Fatal(err)
	}
	var stream bytes.Buffer
	appendOperation(t, &stream, func() ([]byte, error) {
		return encoder.Start(protocolpath.StreamStart{
			ResponseID:    response.ID,
			CreatedAtUnix: response.CreatedAtUnix,
			ReportedModel: response.ReportedModel,
		})
	})
	appendOperation(t, &stream, func() ([]byte, error) {
		return encoder.StartText(0)
	})
	appendOperation(t, &stream, func() ([]byte, error) {
		return encoder.AppendText(0, "Inspecting.")
	})
	appendOperation(t, &stream, func() ([]byte, error) {
		return encoder.StopText(0)
	})
	appendOperation(t, &stream, func() ([]byte, error) {
		return encoder.ToolCall(1, response.Blocks[1].ToolCall)
	})
	appendOperation(t, &stream, func() ([]byte, error) {
		return encoder.ToolCall(2, response.Blocks[2].ToolCall)
	})
	appendOperation(t, &stream, func() ([]byte, error) {
		return encoder.Terminal(response)
	})

	events := decodeResponsesEvents(t, stream.Bytes())
	var functionAdded responses.ResponseOutputItemAddedEvent
	var customAdded responses.ResponseOutputItemAddedEvent
	var functionDelta responses.ResponseFunctionCallArgumentsDeltaEvent
	var customDelta responses.ResponseCustomToolCallInputDeltaEvent
	for _, event := range events {
		switch event.Type {
		case "response.output_item.added":
			added := event.AsResponseOutputItemAdded()
			switch added.Item.Type {
			case "function_call":
				functionAdded = added
			case "custom_tool_call":
				customAdded = added
			}
		case "response.function_call_arguments.delta":
			functionDelta = event.AsResponseFunctionCallArgumentsDelta()
		case "response.custom_tool_call_input.delta":
			customDelta = event.AsResponseCustomToolCallInputDelta()
		}
	}
	function := functionAdded.Item.AsFunctionCall()
	custom := customAdded.Item.AsCustomToolCall()
	if function.ID == "" ||
		function.CallID == "" ||
		function.ID == function.CallID ||
		functionDelta.ItemID != function.ID ||
		functionDelta.Delta != `{"path":"README.md"}` {
		t.Fatalf("function events = %#v, %#v", functionAdded, functionDelta)
	}
	if custom.ID == "" ||
		custom.CallID == "" ||
		custom.ID == custom.CallID ||
		customDelta.ItemID != custom.ID ||
		customDelta.Delta != "pwd" {
		t.Fatalf("custom events = %#v, %#v", customAdded, customDelta)
	}
}

func TestResponsesStreamEncoderRejectsMalformedOrdering(t *testing.T) {
	t.Parallel()

	request := streamingRequestFixture(t)
	response := completeResponseFixture(t)
	tests := []struct {
		name string
		run  func(*StreamEncoder) error
	}{
		{
			name: "delta before start",
			run: func(encoder *StreamEncoder) error {
				_, err := encoder.AppendText(0, "invalid")
				return err
			},
		},
		{
			name: "terminal with open text",
			run: func(encoder *StreamEncoder) error {
				if _, err := encoder.Start(protocolpath.StreamStart{
					ResponseID:    response.ID,
					CreatedAtUnix: response.CreatedAtUnix,
					ReportedModel: response.ReportedModel,
				}); err != nil {
					return err
				}
				if _, err := encoder.StartText(0); err != nil {
					return err
				}
				_, err := encoder.Terminal(response)
				return err
			},
		},
		{
			name: "duplicate terminal",
			run: func(encoder *StreamEncoder) error {
				if _, err := encoder.Start(protocolpath.StreamStart{
					ResponseID:    response.ID,
					CreatedAtUnix: response.CreatedAtUnix,
					ReportedModel: response.ReportedModel,
				}); err != nil {
					return err
				}
				if _, err := encoder.StartText(0); err != nil {
					return err
				}
				if _, err := encoder.AppendText(0, "Inspecting."); err != nil {
					return err
				}
				if _, err := encoder.StopText(0); err != nil {
					return err
				}
				if _, err := encoder.Terminal(response); err != nil {
					return err
				}
				_, err := encoder.Terminal(response)
				return err
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			encoder, err := newTestCodec(t).NewStreamEncoder(request)
			if err != nil {
				t.Fatal(err)
			}
			if err := test.run(encoder); err == nil {
				t.Fatal("malformed ordering succeeded")
			}
		})
	}
}

func TestOfficialSDKOracleDistinguishesFailedAndCancelledResponses(
	t *testing.T,
) {
	t.Parallel()

	failedResponse := `{
		"id":"resp_failed_fixture",
		"object":"response",
		"created_at":1785470000,
		"status":"failed",
		"error":{"code":"server_error","message":"fixture failure"},
		"incomplete_details":null,
		"instructions":null,
		"metadata":{},
		"model":"gpt-5.6-sol",
		"output":[],
		"parallel_tool_calls":false,
		"temperature":null,
		"tool_choice":"auto",
		"tools":[],
		"top_p":null,
		"background":false,
		"max_output_tokens":null,
		"max_tool_calls":null,
		"previous_response_id":null,
		"prompt_cache_key":null,
		"reasoning":{"effort":null,"summary":null},
		"safety_identifier":null,
		"service_tier":null,
		"text":{"format":{"type":"text"}},
		"truncation":"disabled",
		"usage":null,
		"prompt_cache_retention":null
	}`
	var failed responses.ResponseStreamEventUnion
	if err := json.Unmarshal(
		[]byte(`{
			"type":"response.failed",
			"sequence_number":7,
			"response":`+failedResponse+`
		}`),
		&failed,
	); err != nil {
		t.Fatalf("official SDK rejected failed fixture: %v", err)
	}
	if failed.Type != "response.failed" ||
		failed.AsResponseFailed().Response.Status !=
			responses.ResponseStatusFailed {
		t.Fatalf("failed fixture = %#v", failed)
	}

	cancelledJSON := bytes.Replace(
		[]byte(failedResponse),
		[]byte(`"status":"failed"`),
		[]byte(`"status":"cancelled"`),
		1,
	)
	cancelledJSON = bytes.Replace(
		cancelledJSON,
		[]byte(`"error":{"code":"server_error","message":"fixture failure"}`),
		[]byte(`"error":null`),
		1,
	)
	var cancelled responses.Response
	if err := json.Unmarshal(cancelledJSON, &cancelled); err != nil {
		t.Fatalf("official SDK rejected cancelled fixture: %v", err)
	}
	if cancelled.Status != responses.ResponseStatusCancelled {
		t.Fatalf("cancelled fixture status = %q", cancelled.Status)
	}
}

func streamingRequestFixture(t *testing.T) protocolcore.Request {
	t.Helper()
	block, err := protocolcore.NewTextBlock("Inspect one file.")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := protocolcore.NewJSONObject(
		[]byte(`{
			"type":"object",
			"properties":{"path":{"type":"string"}},
			"required":["path"],
			"additionalProperties":false
		}`),
		1024,
	)
	if err != nil {
		t.Fatal(err)
	}
	request := protocolcore.Request{
		RequestedModel: "gpt-5.6-sol",
		EffectiveModel: "provider-model",
		Stream:         true,
		Messages: []protocolcore.Message{{
			Role:   protocolcore.RoleUser,
			Blocks: []protocolcore.ContentBlock{block},
		}},
		Tools: []protocolcore.ToolDefinition{
			{
				Kind:        protocolcore.ToolKindFunction,
				Name:        "read_file",
				InputSchema: schema,
			},
			{
				Kind: protocolcore.ToolKindCustom,
				Name: "exec",
				CustomFormat: protocolcore.CustomToolFormat{
					Kind: protocolcore.CustomToolFormatText,
				},
			},
		},
		ToolChoice: protocolcore.ToolChoice{
			Mode:            protocolcore.ToolChoiceAuto,
			DisableParallel: true,
		},
	}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	return request
}

func appendOperation(
	t *testing.T,
	destination *bytes.Buffer,
	operation func() ([]byte, error),
) {
	t.Helper()
	encoded, err := operation()
	if err != nil {
		t.Fatal(err)
	}
	destination.Write(encoded)
}

func decodeResponsesEvents(
	t *testing.T,
	encoded []byte,
) []responses.ResponseStreamEventUnion {
	t.Helper()
	decoder, err := ssewire.NewDecoder(ssewire.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	framed, err := decoder.Feed(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if err := decoder.Finish(); err != nil {
		t.Fatal(err)
	}
	decoded := make([]responses.ResponseStreamEventUnion, len(framed))
	for index, event := range framed {
		if event.Name == "message" {
			t.Fatalf("event %d has no typed SSE name", index)
		}
		if err := json.Unmarshal(event.Data, &decoded[index]); err != nil {
			t.Fatalf(
				"official SDK rejected event %d (%s): %v\n%s",
				index,
				event.Name,
				err,
				event.Data,
			)
		}
		if decoded[index].Type != event.Name {
			t.Fatalf(
				"event %d name/type mismatch: %q != %q",
				index,
				event.Name,
				decoded[index].Type,
			)
		}
	}
	return decoded
}
