package openairesponses

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"sync"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
	"github.com/vibe-agi/vibermate/internal/ssewire"
)

type streamEventSpec struct {
	name    string
	payload any
}

type responseLifecycleEventWire struct {
	Type           string       `json:"type"`
	SequenceNumber int64        `json:"sequence_number"`
	Response       responseWire `json:"response"`
}

type responseOutputItemEventWire struct {
	Type           string `json:"type"`
	SequenceNumber int64  `json:"sequence_number"`
	OutputIndex    int64  `json:"output_index"`
	Item           any    `json:"item"`
}

type responseContentPartEventWire struct {
	Type           string `json:"type"`
	SequenceNumber int64  `json:"sequence_number"`
	OutputIndex    int64  `json:"output_index"`
	ItemID         string `json:"item_id"`
	ContentIndex   int64  `json:"content_index"`
	Part           any    `json:"part"`
}

type responseTextDeltaEventWire struct {
	Type           string `json:"type"`
	SequenceNumber int64  `json:"sequence_number"`
	OutputIndex    int64  `json:"output_index"`
	ItemID         string `json:"item_id"`
	ContentIndex   int64  `json:"content_index"`
	Delta          string `json:"delta"`
	Logprobs       []any  `json:"logprobs"`
}

type responseTextDoneEventWire struct {
	Type           string `json:"type"`
	SequenceNumber int64  `json:"sequence_number"`
	OutputIndex    int64  `json:"output_index"`
	ItemID         string `json:"item_id"`
	ContentIndex   int64  `json:"content_index"`
	Text           string `json:"text"`
	Logprobs       []any  `json:"logprobs"`
}

type responseFunctionDeltaEventWire struct {
	Type           string `json:"type"`
	SequenceNumber int64  `json:"sequence_number"`
	OutputIndex    int64  `json:"output_index"`
	ItemID         string `json:"item_id"`
	Delta          string `json:"delta"`
}

type responseFunctionDoneEventWire struct {
	Type           string `json:"type"`
	SequenceNumber int64  `json:"sequence_number"`
	OutputIndex    int64  `json:"output_index"`
	ItemID         string `json:"item_id"`
	Name           string `json:"name"`
	Arguments      string `json:"arguments"`
}

type responseCustomDoneEventWire struct {
	Type           string `json:"type"`
	SequenceNumber int64  `json:"sequence_number"`
	OutputIndex    int64  `json:"output_index"`
	ItemID         string `json:"item_id"`
	Input          string `json:"input"`
}

type streamOutputTextPartWire struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	Annotations []any  `json:"annotations"`
	Logprobs    []any  `json:"logprobs"`
}

type streamTextState struct {
	blockIndex  int
	outputIndex int64
	itemID      string
	text        string
}

type StreamEncoder struct {
	mu sync.Mutex

	codec   *Codec
	request protocolcore.Request

	started      bool
	terminal     bool
	failed       bool
	start        protocolpath.StreamStart
	responseID   string
	sequence     int64
	outputIndex  int64
	encodedBytes int
	openText     *streamTextState
	blocks       map[int]protocolcore.ContentBlock
	report       protocolcore.TranslationReport
}

func (codec *Codec) NewStreamEncoder(
	request protocolcore.Request,
) (*StreamEncoder, error) {
	if codec == nil {
		return nil, errors.New("Responses codec is nil")
	}
	if err := request.Validate(); err != nil {
		return nil, protocolcore.NewFailure(
			protocolcore.ReasonInvalidClientRequest,
			"$",
			err,
		)
	}
	if !request.Stream {
		return nil, protocolcore.NewFailure(
			protocolcore.ReasonInvalidClientRequest,
			"$.stream",
			errors.New("request is not configured for streaming"),
		)
	}
	return &StreamEncoder{
		codec:   codec,
		request: request.Clone(),
		blocks:  make(map[int]protocolcore.ContentBlock),
	}, nil
}

func (encoder *StreamEncoder) Start(
	start protocolpath.StreamStart,
) ([]byte, error) {
	encoder.mu.Lock()
	defer encoder.mu.Unlock()

	if err := encoder.checkMutable(); err != nil {
		return nil, err
	}
	if encoder.started {
		return nil, encoder.fail(
			"$",
			errors.New("Responses stream was already started"),
		)
	}
	if err := validateStreamStart(start); err != nil {
		return nil, encoder.fail("$", err)
	}
	responseID := streamResponseClientID(encoder.request, start)
	initial := baseResponseWire(
		encoder.request,
		responseID,
		start.CreatedAtUnix,
		encoder.request.RequestedModel,
		"in_progress",
		[]any{},
	)
	events := []streamEventSpec{
		{
			name: "response.created",
			payload: responseLifecycleEventWire{
				Type:           "response.created",
				SequenceNumber: encoder.sequence,
				Response:       initial,
			},
		},
		{
			name: "response.in_progress",
			payload: responseLifecycleEventWire{
				Type:           "response.in_progress",
				SequenceNumber: encoder.sequence + 1,
				Response:       initial,
			},
		},
	}
	encoded, err := encoder.encodeBatch(events)
	if err != nil {
		return nil, err
	}
	encoder.started = true
	encoder.start = start
	encoder.responseID = responseID
	return encoded, nil
}

func (encoder *StreamEncoder) StartText(index int) ([]byte, error) {
	encoder.mu.Lock()
	defer encoder.mu.Unlock()

	if err := encoder.checkStarted(); err != nil {
		return nil, err
	}
	if index < 0 || encoder.openText != nil {
		return nil, encoder.fail(
			"$.output",
			errors.New("text item start is out of order"),
		)
	}
	if _, duplicate := encoder.blocks[index]; duplicate {
		return nil, encoder.fail(
			"$.output",
			errors.New("response block index is duplicated"),
		)
	}
	itemID := stableClientID(
		"msg",
		encoder.responseID,
		strconv.Itoa(index),
	)
	item := responseMessageItemWire{
		ID:      itemID,
		Type:    "message",
		Status:  "in_progress",
		Role:    "assistant",
		Content: []responseMessageContentWire{},
	}
	part := streamOutputTextPartWire{
		Type:        "output_text",
		Text:        "",
		Annotations: []any{},
		Logprobs:    []any{},
	}
	events := []streamEventSpec{
		{
			name: "response.output_item.added",
			payload: responseOutputItemEventWire{
				Type:           "response.output_item.added",
				SequenceNumber: encoder.sequence,
				OutputIndex:    encoder.outputIndex,
				Item:           item,
			},
		},
		{
			name: "response.content_part.added",
			payload: responseContentPartEventWire{
				Type:           "response.content_part.added",
				SequenceNumber: encoder.sequence + 1,
				OutputIndex:    encoder.outputIndex,
				ItemID:         itemID,
				ContentIndex:   0,
				Part:           part,
			},
		},
	}
	encoded, err := encoder.encodeBatch(events)
	if err != nil {
		return nil, err
	}
	encoder.openText = &streamTextState{
		blockIndex:  index,
		outputIndex: encoder.outputIndex,
		itemID:      itemID,
	}
	return encoded, nil
}

func (encoder *StreamEncoder) AppendText(
	index int,
	text string,
) ([]byte, error) {
	encoder.mu.Lock()
	defer encoder.mu.Unlock()

	if err := encoder.checkStarted(); err != nil {
		return nil, err
	}
	if encoder.openText == nil || encoder.openText.blockIndex != index {
		return nil, encoder.fail(
			"$.output",
			errors.New("text delta has no matching open item"),
		)
	}
	if !utf8.ValidString(text) {
		return nil, encoder.fail(
			"$.output",
			errors.New("text delta is not valid UTF-8"),
		)
	}
	if text == "" {
		return nil, nil
	}
	if len(encoder.openText.text)+len(text) > protocolcore.MaxTextBytes {
		return nil, encoder.fail(
			"$.output",
			errors.New("text item exceeds the configured byte limit"),
		)
	}
	event := responseTextDeltaEventWire{
		Type:           "response.output_text.delta",
		SequenceNumber: encoder.sequence,
		OutputIndex:    encoder.openText.outputIndex,
		ItemID:         encoder.openText.itemID,
		ContentIndex:   0,
		Delta:          text,
		Logprobs:       []any{},
	}
	encoded, err := encoder.encodeBatch([]streamEventSpec{{
		name:    event.Type,
		payload: event,
	}})
	if err != nil {
		return nil, err
	}
	encoder.openText.text += text
	return encoded, nil
}

func (encoder *StreamEncoder) StopText(index int) ([]byte, error) {
	encoder.mu.Lock()
	defer encoder.mu.Unlock()

	if err := encoder.checkStarted(); err != nil {
		return nil, err
	}
	if encoder.openText == nil || encoder.openText.blockIndex != index {
		return nil, encoder.fail(
			"$.output",
			errors.New("text item stop has no matching open item"),
		)
	}
	state := *encoder.openText
	block, err := protocolcore.NewTextBlock(state.text)
	if err != nil {
		return nil, encoder.fail("$.output", err)
	}
	content := responseMessageContentWire{
		Type:        "output_text",
		Text:        state.text,
		Annotations: []any{},
		Logprobs:    []any{},
	}
	item := responseMessageItemWire{
		ID:      state.itemID,
		Type:    "message",
		Status:  "completed",
		Role:    "assistant",
		Content: []responseMessageContentWire{content},
	}
	part := streamOutputTextPartWire{
		Type:        "output_text",
		Text:        state.text,
		Annotations: []any{},
		Logprobs:    []any{},
	}
	events := []streamEventSpec{
		{
			name: "response.output_text.done",
			payload: responseTextDoneEventWire{
				Type:           "response.output_text.done",
				SequenceNumber: encoder.sequence,
				OutputIndex:    state.outputIndex,
				ItemID:         state.itemID,
				ContentIndex:   0,
				Text:           state.text,
				Logprobs:       []any{},
			},
		},
		{
			name: "response.content_part.done",
			payload: responseContentPartEventWire{
				Type:           "response.content_part.done",
				SequenceNumber: encoder.sequence + 1,
				OutputIndex:    state.outputIndex,
				ItemID:         state.itemID,
				ContentIndex:   0,
				Part:           part,
			},
		},
		{
			name: "response.output_item.done",
			payload: responseOutputItemEventWire{
				Type:           "response.output_item.done",
				SequenceNumber: encoder.sequence + 2,
				OutputIndex:    state.outputIndex,
				Item:           item,
			},
		},
	}
	encoded, err := encoder.encodeBatch(events)
	if err != nil {
		return nil, err
	}
	encoder.blocks[index] = block
	encoder.openText = nil
	encoder.outputIndex++
	return encoded, nil
}

func (encoder *StreamEncoder) ToolCall(
	index int,
	call protocolcore.ToolCall,
) ([]byte, error) {
	encoder.mu.Lock()
	defer encoder.mu.Unlock()

	if err := encoder.checkStarted(); err != nil {
		return nil, err
	}
	if index < 0 || encoder.openText != nil {
		return nil, encoder.fail(
			"$.output",
			errors.New("tool item is out of order"),
		)
	}
	if _, duplicate := encoder.blocks[index]; duplicate {
		return nil, encoder.fail(
			"$.output",
			errors.New("response block index is duplicated"),
		)
	}
	block, err := protocolcore.NewToolCallBlock(call)
	if err != nil {
		return nil, encoder.fail("$.output", err)
	}
	if err := validateResponseCalls(
		encoder.request,
		protocolcore.Response{
			ID:             encoder.start.ResponseID,
			CreatedAtUnix:  encoder.start.CreatedAtUnix,
			RequestedModel: encoder.request.RequestedModel,
			EffectiveModel: encoder.request.EffectiveModel,
			ReportedModel:  encoder.start.ReportedModel,
			Blocks:         []protocolcore.ContentBlock{block},
			StopReason:     protocolcore.StopReasonToolUse,
		},
	); err != nil {
		return nil, encoder.fail("$.output", err)
	}
	itemID, callID := toolClientIDs(encoder.responseID, index, call)
	events, err := encoder.toolEvents(index, itemID, callID, call)
	if err != nil {
		return nil, encoder.fail("$.output", err)
	}
	encoded, err := encoder.encodeBatch(events)
	if err != nil {
		return nil, err
	}
	encoder.blocks[index] = block
	encoder.outputIndex++
	return encoded, nil
}

func (encoder *StreamEncoder) Terminal(
	response protocolcore.Response,
) ([]byte, error) {
	encoder.mu.Lock()
	defer encoder.mu.Unlock()

	if err := encoder.checkStarted(); err != nil {
		return nil, err
	}
	if encoder.openText != nil {
		return nil, encoder.fail(
			"$.output",
			errors.New("Responses terminal has an open text item"),
		)
	}
	if err := response.Validate(); err != nil {
		return nil, encoder.fail("$", err)
	}
	if response.ID != encoder.start.ResponseID ||
		response.CreatedAtUnix != encoder.start.CreatedAtUnix ||
		response.ReportedModel != encoder.start.ReportedModel ||
		response.RequestedModel != encoder.request.RequestedModel ||
		response.EffectiveModel != encoder.request.EffectiveModel {
		return nil, encoder.fail(
			"$",
			errors.New("Responses terminal identity changed within the stream"),
		)
	}
	if len(response.Blocks) != len(encoder.blocks) {
		return nil, encoder.fail(
			"$.output",
			errors.New("Responses terminal block count is inconsistent"),
		)
	}
	for index, block := range response.Blocks {
		emitted, exists := encoder.blocks[index]
		if !exists || !reflect.DeepEqual(emitted, block) {
			return nil, encoder.fail(
				fmt.Sprintf("$.output[%d]", index),
				errors.New("Responses terminal block changed after streaming"),
			)
		}
	}
	wire, report, err := buildResponseWire(encoder.request, response)
	if err != nil {
		return nil, encoder.fail("$", err)
	}
	if int64(len(wire.Output)) != encoder.outputIndex {
		return nil, encoder.fail(
			"$.output",
			errors.New("Responses terminal output count is inconsistent"),
		)
	}
	eventType := "response.completed"
	if response.StopReason == protocolcore.StopReasonMaxTokens {
		eventType = "response.incomplete"
	}
	event := responseLifecycleEventWire{
		Type:           eventType,
		SequenceNumber: encoder.sequence,
		Response:       wire,
	}
	encoded, err := encoder.encodeBatch([]streamEventSpec{{
		name:    eventType,
		payload: event,
	}})
	if err != nil {
		return nil, err
	}
	encoder.terminal = true
	encoder.report = report
	return encoded, nil
}

func (encoder *StreamEncoder) TranslationReport() protocolcore.TranslationReport {
	encoder.mu.Lock()
	defer encoder.mu.Unlock()
	return encoder.report
}

func (encoder *StreamEncoder) toolEvents(
	index int,
	itemID string,
	callID string,
	call protocolcore.ToolCall,
) ([]streamEventSpec, error) {
	outputIndex := encoder.outputIndex
	switch call.EffectiveKind() {
	case protocolcore.ToolKindFunction:
		arguments := string(call.Arguments.Bytes())
		added := responseFunctionCallItemWire{
			ID:        itemID,
			Type:      "function_call",
			Status:    "in_progress",
			CallID:    callID,
			Namespace: call.Namespace,
			Name:      call.Name,
			Arguments: "",
		}
		done := added
		done.Status = "completed"
		done.Arguments = arguments
		return []streamEventSpec{
			{
				name: "response.output_item.added",
				payload: responseOutputItemEventWire{
					Type:           "response.output_item.added",
					SequenceNumber: encoder.sequence,
					OutputIndex:    outputIndex,
					Item:           added,
				},
			},
			{
				name: "response.function_call_arguments.delta",
				payload: responseFunctionDeltaEventWire{
					Type:           "response.function_call_arguments.delta",
					SequenceNumber: encoder.sequence + 1,
					OutputIndex:    outputIndex,
					ItemID:         itemID,
					Delta:          arguments,
				},
			},
			{
				name: "response.function_call_arguments.done",
				payload: responseFunctionDoneEventWire{
					Type:           "response.function_call_arguments.done",
					SequenceNumber: encoder.sequence + 2,
					OutputIndex:    outputIndex,
					ItemID:         itemID,
					Name:           call.Name,
					Arguments:      arguments,
				},
			},
			{
				name: "response.output_item.done",
				payload: responseOutputItemEventWire{
					Type:           "response.output_item.done",
					SequenceNumber: encoder.sequence + 3,
					OutputIndex:    outputIndex,
					Item:           done,
				},
			},
		}, nil
	case protocolcore.ToolKindCustom:
		added := responseCustomToolCallItemWire{
			ID:        itemID,
			Type:      "custom_tool_call",
			CallID:    callID,
			Namespace: call.Namespace,
			Name:      call.Name,
			Input:     "",
		}
		done := added
		done.Input = call.Input
		return []streamEventSpec{
			{
				name: "response.output_item.added",
				payload: responseOutputItemEventWire{
					Type:           "response.output_item.added",
					SequenceNumber: encoder.sequence,
					OutputIndex:    outputIndex,
					Item:           added,
				},
			},
			{
				name: "response.custom_tool_call_input.delta",
				payload: responseFunctionDeltaEventWire{
					Type:           "response.custom_tool_call_input.delta",
					SequenceNumber: encoder.sequence + 1,
					OutputIndex:    outputIndex,
					ItemID:         itemID,
					Delta:          call.Input,
				},
			},
			{
				name: "response.custom_tool_call_input.done",
				payload: responseCustomDoneEventWire{
					Type:           "response.custom_tool_call_input.done",
					SequenceNumber: encoder.sequence + 2,
					OutputIndex:    outputIndex,
					ItemID:         itemID,
					Input:          call.Input,
				},
			},
			{
				name: "response.output_item.done",
				payload: responseOutputItemEventWire{
					Type:           "response.output_item.done",
					SequenceNumber: encoder.sequence + 3,
					OutputIndex:    outputIndex,
					Item:           done,
				},
			},
		}, nil
	default:
		return nil, errors.New("tool call kind is unsupported")
	}
}

func (encoder *StreamEncoder) encodeBatch(
	events []streamEventSpec,
) ([]byte, error) {
	var encoded bytes.Buffer
	for _, event := range events {
		data, err := json.Marshal(event.payload)
		if err != nil {
			encoder.failed = true
			return nil, protocolcore.NewFailure(
				protocolcore.ReasonInvalidProviderResponse,
				"$",
				err,
			)
		}
		frame, err := ssewire.Encode(ssewire.Event{
			Name: event.name,
			Data: data,
		})
		if err != nil {
			encoder.failed = true
			return nil, protocolcore.NewFailure(
				protocolcore.ReasonMalformedEventStream,
				"$",
				err,
			)
		}
		if encoder.encodedBytes+encoded.Len()+len(frame) >
			encoder.codec.options.MaxResponseBytes {
			encoder.failed = true
			return nil, protocolcore.NewFailure(
				protocolcore.ReasonStreamLimitExceeded,
				"$",
				errors.New("encoded Responses stream exceeds the configured byte limit"),
			)
		}
		encoded.Write(frame)
	}
	encoder.encodedBytes += encoded.Len()
	encoder.sequence += int64(len(events))
	return bytes.Clone(encoded.Bytes()), nil
}

func (encoder *StreamEncoder) checkMutable() error {
	if encoder == nil || encoder.codec == nil {
		return errors.New("Responses stream encoder is nil")
	}
	if encoder.failed {
		return protocolcore.NewFailure(
			protocolcore.ReasonStreamStateViolation,
			"$",
			errors.New("Responses stream encoder is failed"),
		)
	}
	if encoder.terminal {
		return protocolcore.NewFailure(
			protocolcore.ReasonStreamStateViolation,
			"$",
			errors.New("Responses stream encoder is terminal"),
		)
	}
	return nil
}

func (encoder *StreamEncoder) checkStarted() error {
	if err := encoder.checkMutable(); err != nil {
		return err
	}
	if !encoder.started {
		return encoder.fail(
			"$",
			errors.New("Responses stream has not started"),
		)
	}
	return nil
}

func (encoder *StreamEncoder) fail(path string, cause error) error {
	encoder.failed = true
	return protocolcore.NewFailure(
		protocolcore.ReasonStreamStateViolation,
		path,
		cause,
	)
}

func validateStreamStart(start protocolpath.StreamStart) error {
	if err := validateBoundedString(start.ResponseID, 512, false); err != nil {
		return fmt.Errorf("response ID: %w", err)
	}
	if start.CreatedAtUnix < 0 {
		return errors.New("response creation time is negative")
	}
	if err := validateBoundedString(
		start.ReportedModel,
		protocolcore.MaxModelBytes,
		false,
	); err != nil {
		return fmt.Errorf("reported model: %w", err)
	}
	return nil
}

func streamResponseClientID(
	request protocolcore.Request,
	start protocolpath.StreamStart,
) string {
	return stableClientID(
		"resp",
		start.ResponseID,
		strconv.FormatInt(start.CreatedAtUnix, 10),
		request.RequestedModel,
		request.EffectiveModel,
	)
}

func toolClientIDs(
	responseID string,
	index int,
	call protocolcore.ToolCall,
) (string, string) {
	itemLabel := "fc"
	if call.EffectiveKind() == protocolcore.ToolKindCustom {
		itemLabel = "ctc"
	}
	parts := []string{
		responseID,
		strconv.Itoa(index),
		call.Key.Source(),
		call.Key.WireID(),
	}
	return stableClientID(itemLabel, parts...),
		stableClientID("call", parts...)
}
