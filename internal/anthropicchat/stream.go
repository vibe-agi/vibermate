package anthropicchat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/ssewire"
)

var (
	ErrStreamAlreadyFinished  = errors.New("provider stream was already finished")
	ErrTerminalAlreadyDecided = errors.New("stream terminal was already decided")
	ErrTerminalNotDecoded     = errors.New("stream terminal has not been decoded")
)

type openAIStreamChunkWire struct {
	ID                string                   `json:"id"`
	Object            string                   `json:"object"`
	Created           int64                    `json:"created"`
	Model             string                   `json:"model"`
	Choices           []openAIStreamChoiceWire `json:"choices"`
	Usage             *openAIUsageWire         `json:"usage,omitempty"`
	SystemFingerprint *string                  `json:"system_fingerprint,omitempty"`
	ServiceTier       *string                  `json:"service_tier,omitempty"`
}

type openAIStreamChoiceWire struct {
	Index        int                   `json:"index"`
	Delta        openAIStreamDeltaWire `json:"delta"`
	FinishReason *string               `json:"finish_reason"`
	Logprobs     json.RawMessage       `json:"logprobs,omitempty"`
}

type openAIStreamDeltaWire struct {
	Role             string                     `json:"role,omitempty"`
	Content          *string                    `json:"content,omitempty"`
	ReasoningContent json.RawMessage            `json:"reasoning_content,omitempty"`
	Refusal          *string                    `json:"refusal,omitempty"`
	ToolCalls        []openAIStreamToolCallWire `json:"tool_calls,omitempty"`
	FunctionCall     json.RawMessage            `json:"function_call,omitempty"`
}

type openAIStreamToolCallWire struct {
	Index    int                      `json:"index"`
	ID       string                   `json:"id,omitempty"`
	Type     string                   `json:"type,omitempty"`
	Function openAIStreamFunctionWire `json:"function"`
}

type openAIStreamFunctionWire struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type streamToolAccumulator struct {
	index     int
	id        string
	name      string
	arguments []byte
}

type ProviderStream struct {
	mu sync.Mutex

	codec   *Codec
	request protocolcore.Request
	decoder *ssewire.Decoder

	responseID       string
	reportedModel    string
	messageStarted   bool
	textOpen         bool
	textIndex        int
	nextBlockIndex   int
	barrier          bool
	preToolText      strings.Builder
	heldText         strings.Builder
	decodedBytes     int
	heldBytes        int
	semanticProgress uint64
	tools            map[int]*streamToolAccumulator
	reasoning        [][]byte
	reasoningUsage   *protocolcore.ProviderExtension
	finishReason     protocolcore.StopReason
	usage            protocolcore.Usage
	usageSeen        bool
	done             bool
	failed           bool
	finished         bool
	report           protocolcore.TranslationReport
}

func (codec *Codec) NewProviderStream(
	request protocolcore.Request,
) (*ProviderStream, error) {
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
	decoder, err := ssewire.NewDecoder(codec.options.SSE)
	if err != nil {
		return nil, err
	}
	return &ProviderStream{
		codec:   codec,
		request: request.Clone(),
		decoder: decoder,
		tools:   make(map[int]*streamToolAccumulator),
	}, nil
}

// Feed returns only bytes that are safe to expose immediately. After the first
// tool fragment, every subsequent content block and the normal terminal remain
// held until FinishDecoded and Approve.
func (stream *ProviderStream) Feed(
	ctx context.Context,
	fragment []byte,
) ([]byte, error) {
	stream.mu.Lock()
	defer stream.mu.Unlock()

	if err := stream.checkMutable(ctx); err != nil {
		return nil, err
	}
	events, err := stream.decoder.Feed(fragment)
	if err != nil {
		stream.failed = true
		return nil, wrapSSEError(err)
	}
	var safe bytes.Buffer
	for eventIndex, event := range events {
		if err := contextError(ctx); err != nil {
			stream.failed = true
			return safe.Bytes(), err
		}
		if stream.done {
			stream.failed = true
			return safe.Bytes(), protocolcore.NewFailure(
				protocolcore.ReasonStreamStateViolation,
				fmt.Sprintf("$event[%d]", eventIndex),
				errors.New("event arrived after the provider terminal marker"),
			)
		}
		if event.Name != "message" {
			stream.failed = true
			return safe.Bytes(), protocolcore.NewFailure(
				protocolcore.ReasonMalformedEventStream,
				fmt.Sprintf("$event[%d].event", eventIndex),
				errors.New("provider event name is unsupported"),
			)
		}
		if bytes.Equal(bytes.TrimSpace(event.Data), []byte("[DONE]")) {
			stream.done = true
			stream.semanticProgress++
			continue
		}
		if bytes.Contains(event.Data, []byte("[DONE]")) {
			stream.failed = true
			return safe.Bytes(), protocolcore.NewFailure(
				protocolcore.ReasonMalformedEventStream,
				fmt.Sprintf("$event[%d].data", eventIndex),
				errors.New("provider terminal marker is not a complete event payload"),
			)
		}

		var chunk openAIStreamChunkWire
		if err := decodeStrict(event.Data, &chunk); err != nil {
			stream.failed = true
			return safe.Bytes(), protocolcore.NewFailure(
				protocolcore.ReasonMalformedEventStream,
				fmt.Sprintf("$event[%d].data", eventIndex),
				err,
			)
		}
		if err := stream.consumeChunk(&safe, chunk); err != nil {
			stream.failed = true
			return safe.Bytes(), err
		}
	}
	return bytes.Clone(safe.Bytes()), nil
}

// SemanticProgress reports how many validated provider payloads have crossed
// SSE framing. Wire comments and incomplete fragments do not count as provider
// progress, while held tool fragments and the terminal marker do.
func (stream *ProviderStream) SemanticProgress() uint64 {
	if stream == nil {
		return 0
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.semanticProgress
}

func (stream *ProviderStream) FinishDecoded(
	ctx context.Context,
) (*PendingTerminal, error) {
	stream.mu.Lock()
	defer stream.mu.Unlock()

	if err := stream.checkMutable(ctx); err != nil {
		return nil, err
	}
	if !stream.done {
		stream.failed = true
		return nil, protocolcore.NewFailure(
			protocolcore.ReasonTruncatedEventStream,
			"$",
			errors.New("provider terminal marker was not received"),
		)
	}
	if err := stream.decoder.Finish(); err != nil {
		stream.failed = true
		return nil, wrapSSEError(err)
	}
	if !stream.messageStarted || stream.finishReason == "" {
		stream.failed = true
		return nil, protocolcore.NewFailure(
			protocolcore.ReasonStreamStateViolation,
			"$",
			errors.New("provider stream is missing response metadata or finish reason"),
		)
	}

	toolIndexes := make([]int, 0, len(stream.tools))
	for index := range stream.tools {
		toolIndexes = append(toolIndexes, index)
	}
	sort.Ints(toolIndexes)
	for ordinal, index := range toolIndexes {
		if index != ordinal {
			stream.failed = true
			return nil, protocolcore.NewFailure(
				protocolcore.ReasonStreamStateViolation,
				"$.choices[0].delta.tool_calls",
				errors.New("provider tool call indexes are not contiguous"),
			)
		}
	}
	if (stream.finishReason == protocolcore.StopReasonToolUse) != (len(toolIndexes) > 0) {
		stream.failed = true
		return nil, protocolcore.NewFailure(
			protocolcore.ReasonStreamStateViolation,
			"$.choices[0].finish_reason",
			errors.New("finish reason and streamed tool calls are inconsistent"),
		)
	}
	var release bytes.Buffer
	responseBlocks := make([]protocolcore.ContentBlock, 0, 2+len(toolIndexes))
	if stream.preToolText.Len() > 0 {
		block, err := protocolcore.NewTextBlock(stream.preToolText.String())
		if err != nil {
			stream.failed = true
			return nil, protocolcore.NewFailure(
				protocolcore.ReasonInvalidProviderResponse,
				"$.choices[0].delta.content",
				err,
			)
		}
		responseBlocks = append(responseBlocks, block)
	}
	if stream.textOpen {
		if err := appendEvent(&release, "content_block_stop", struct {
			Type  string `json:"type"`
			Index int    `json:"index"`
		}{Type: "content_block_stop", Index: stream.textIndex}); err != nil {
			stream.failed = true
			return nil, err
		}
	}

	intents := make([]protocolcore.ToolIntent, 0, len(toolIndexes))
	toolCallIDs := make(map[string]struct{}, len(toolIndexes))
	for _, index := range toolIndexes {
		accumulator := stream.tools[index]
		if _, duplicate := toolCallIDs[accumulator.id]; duplicate {
			stream.failed = true
			return nil, protocolcore.NewFailure(
				protocolcore.ReasonToolCallIncomplete,
				fmt.Sprintf("$.choices[0].delta.tool_calls[%d].id", index),
				errors.New("provider tool call ID is duplicated"),
			)
		}
		toolCallIDs[accumulator.id] = struct{}{}
		key, err := protocolcore.NewCallKey(CallNamespace, accumulator.id)
		if err != nil {
			stream.failed = true
			return nil, protocolcore.NewFailure(
				protocolcore.ReasonToolCallIncomplete,
				fmt.Sprintf("$.choices[0].delta.tool_calls[%d].id", index),
				err,
			)
		}
		arguments, err := protocolcore.NewJSONObject(
			accumulator.arguments,
			stream.codec.options.MaxToolArgumentBytes,
		)
		if err != nil {
			stream.failed = true
			return nil, protocolcore.NewFailure(
				protocolcore.ReasonToolCallIncomplete,
				fmt.Sprintf("$.choices[0].delta.tool_calls[%d].function.arguments", index),
				err,
			)
		}
		call := protocolcore.ToolCall{
			Key:       key,
			Name:      accumulator.name,
			Arguments: arguments,
		}
		if err := call.Validate(); err != nil {
			stream.failed = true
			return nil, protocolcore.NewFailure(
				protocolcore.ReasonToolCallIncomplete,
				fmt.Sprintf("$.choices[0].delta.tool_calls[%d]", index),
				err,
			)
		}
		block, err := protocolcore.NewToolCallBlock(call)
		if err != nil {
			stream.failed = true
			return nil, err
		}
		responseBlocks = append(responseBlocks, block)
		intents = append(intents, protocolcore.ToolIntent{
			ResponseID: stream.responseID,
			Ordinal:    index,
			Call:       call.Clone(),
		})
		blockIndex := stream.nextBlockIndex
		stream.nextBlockIndex++
		if err := appendEvent(&release, "content_block_start", struct {
			Type         string `json:"type"`
			Index        int    `json:"index"`
			ContentBlock struct {
				Type  string          `json:"type"`
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			} `json:"content_block"`
		}{
			Type:  "content_block_start",
			Index: blockIndex,
			ContentBlock: struct {
				Type  string          `json:"type"`
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			}{
				Type:  "tool_use",
				ID:    call.Key.WireID(),
				Name:  call.Name,
				Input: json.RawMessage("{}"),
			},
		}); err != nil {
			stream.failed = true
			return nil, err
		}
		if err := appendEvent(&release, "content_block_delta", struct {
			Type  string `json:"type"`
			Index int    `json:"index"`
			Delta struct {
				Type        string `json:"type"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
		}{
			Type:  "content_block_delta",
			Index: blockIndex,
			Delta: struct {
				Type        string `json:"type"`
				PartialJSON string `json:"partial_json"`
			}{
				Type:        "input_json_delta",
				PartialJSON: string(call.Arguments.Bytes()),
			},
		}); err != nil {
			stream.failed = true
			return nil, err
		}
		if err := appendEvent(&release, "content_block_stop", struct {
			Type  string `json:"type"`
			Index int    `json:"index"`
		}{Type: "content_block_stop", Index: blockIndex}); err != nil {
			stream.failed = true
			return nil, err
		}
	}

	if !stream.usageSeen {
		stream.failed = true
		return nil, protocolcore.NewFailure(
			protocolcore.ReasonInvalidProviderResponse,
			"$.usage",
			errors.New("provider stream completed without final usage"),
		)
	}

	if stream.heldText.Len() > 0 {
		held := stream.heldText.String()
		block, err := protocolcore.NewTextBlock(held)
		if err != nil {
			stream.failed = true
			return nil, err
		}
		responseBlocks = append(responseBlocks, block)
		blockIndex := stream.nextBlockIndex
		stream.nextBlockIndex++
		if err := appendTextBlock(&release, blockIndex, held); err != nil {
			stream.failed = true
			return nil, err
		}
		if len(toolIndexes) > 0 {
			stream.report = stream.report.Merge(protocolcore.NewTranslationReport(
				protocolcore.TranslationNotice{
					Code: protocolcore.NoticeContentOrderNormalized,
					Path: "$.choices[0].delta",
				},
			))
		}
	}
	if len(responseBlocks) == 0 {
		block, err := protocolcore.NewTextBlock("")
		if err != nil {
			stream.failed = true
			return nil, err
		}
		responseBlocks = append(responseBlocks, block)
	}

	if stream.usageSeen && stream.usage.InputUncached.Known {
		stream.report = stream.report.Merge(protocolcore.NewTranslationReport(
			protocolcore.TranslationNotice{
				Code: protocolcore.NoticeLateUsageAccounting,
				Path: "$.usage",
			},
		))
	}
	if err := appendTerminalEvents(&release, stream.finishReason, stream.usage); err != nil {
		stream.failed = true
		return nil, err
	}
	providerExtensions := make([]protocolcore.ProviderExtension, 0, 2)
	if len(stream.reasoning) > 0 {
		extension, err := protocolcore.NewProviderExtension(
			SourceOpenAIChat,
			protocolcore.ProviderExtensionReasoningContent,
			"$.choices[0].delta.reasoning_content",
			stream.reasoning,
		)
		if err != nil {
			stream.failed = true
			return nil, protocolcore.NewFailure(
				protocolcore.ReasonInvalidProviderResponse,
				"$.choices[0].delta.reasoning_content",
				err,
			)
		}
		providerExtensions = append(providerExtensions, extension)
	}
	if stream.reasoningUsage != nil {
		providerExtensions = append(
			providerExtensions,
			stream.reasoningUsage.Clone(),
		)
	}
	response := protocolcore.Response{
		ID:                 stream.responseID,
		RequestedModel:     stream.request.RequestedModel,
		EffectiveModel:     stream.request.EffectiveModel,
		ReportedModel:      stream.reportedModel,
		Blocks:             responseBlocks,
		ProviderExtensions: providerExtensions,
		StopReason:         stream.finishReason,
		Usage:              stream.usage,
	}
	if err := response.Validate(); err != nil {
		stream.failed = true
		return nil, protocolcore.NewFailure(
			protocolcore.ReasonInvalidProviderResponse,
			"$",
			err,
		)
	}
	stream.finished = true
	return newPendingTerminal(release.Bytes(), response, intents, stream.report), nil
}

func (stream *ProviderStream) checkMutable(ctx context.Context) error {
	if err := contextError(ctx); err != nil {
		stream.failed = true
		return err
	}
	if stream.failed {
		return protocolcore.NewFailure(
			protocolcore.ReasonStreamStateViolation,
			"$",
			errors.New("provider stream is failed"),
		)
	}
	if stream.finished {
		return ErrStreamAlreadyFinished
	}
	return nil
}

func (stream *ProviderStream) consumeChunk(
	safe *bytes.Buffer,
	chunk openAIStreamChunkWire,
) error {
	if chunk.Object != "chat.completion.chunk" {
		return protocolcore.NewFailure(
			protocolcore.ReasonInvalidProviderResponse,
			"$.object",
			errors.New("provider stream object is invalid"),
		)
	}
	if err := stream.ensureMessageStarted(safe, chunk.ID, chunk.Model); err != nil {
		return err
	}
	if chunk.ServiceTier != nil {
		stream.report = stream.report.Merge(protocolcore.NewTranslationReport(
			protocolcore.TranslationNotice{
				Code: protocolcore.NoticeServiceTierNotForwarded,
				Path: "$.service_tier",
			},
		))
	}
	if chunk.Usage != nil {
		usage, err := decodeUsage(chunk.Usage)
		if err != nil {
			return err
		}
		if stream.usageSeen && !reflect.DeepEqual(stream.usage, usage) {
			return protocolcore.NewFailure(
				protocolcore.ReasonStreamStateViolation,
				"$.usage",
				errors.New("provider stream usage changed after publication"),
			)
		}
		stream.usage = usage
		stream.usageSeen = true
		if extension, present, extensionErr := reasoningUsageExtension(chunk.Usage); extensionErr != nil {
			return extensionErr
		} else if present {
			if stream.reasoningUsage != nil &&
				!reflect.DeepEqual(
					stream.reasoningUsage.Fragments(),
					extension.Fragments(),
				) {
				return protocolcore.NewFailure(
					protocolcore.ReasonStreamStateViolation,
					"$.usage.completion_tokens_details",
					errors.New("provider reasoning usage changed after publication"),
				)
			}
			if stream.reasoningUsage == nil {
				cloned := extension.Clone()
				stream.reasoningUsage = &cloned
				stream.report = stream.report.Merge(reasoningUsageNotice(
					"$.usage.completion_tokens_details",
				))
			}
		}
	}
	if len(chunk.Choices) == 0 {
		if chunk.Usage == nil {
			return protocolcore.NewFailure(
				protocolcore.ReasonInvalidProviderResponse,
				"$.choices",
				errors.New("provider stream chunk has no choice or usage"),
			)
		}
		return nil
	}
	if len(chunk.Choices) != 1 || chunk.Choices[0].Index != 0 {
		return protocolcore.NewFailure(
			protocolcore.ReasonUnsupportedProviderData,
			"$.choices",
			errors.New("exactly one choice with index zero is required"),
		)
	}
	choice := chunk.Choices[0]
	if rawPresent(choice.Logprobs) {
		return protocolcore.NewFailure(
			protocolcore.ReasonUnsupportedProviderData,
			"$.choices[0].logprobs",
			errors.New("log probabilities are unsupported"),
		)
	}
	if choice.Delta.Role != "" && choice.Delta.Role != "assistant" {
		return protocolcore.NewFailure(
			protocolcore.ReasonInvalidProviderResponse,
			"$.choices[0].delta.role",
			errors.New("provider stream role is not assistant"),
		)
	}
	if choice.Delta.Refusal != nil || rawPresent(choice.Delta.FunctionCall) {
		return protocolcore.NewFailure(
			protocolcore.ReasonUnsupportedProviderData,
			"$.choices[0].delta",
			errors.New("provider stream contains an unsupported delta type"),
		)
	}

	if rawPresent(choice.Delta.ReasoningContent) {
		if err := stream.consumeReasoning(
			choice.Delta.ReasoningContent,
		); err != nil {
			return err
		}
	}
	if choice.Delta.Content != nil {
		if err := stream.consumeText(safe, *choice.Delta.Content); err != nil {
			return err
		}
	}
	if len(choice.Delta.ToolCalls) > 0 {
		if err := stream.consumeToolFragments(safe, choice.Delta.ToolCalls); err != nil {
			return err
		}
	}
	if choice.FinishReason != nil {
		if stream.finishReason != "" {
			return protocolcore.NewFailure(
				protocolcore.ReasonStreamStateViolation,
				"$.choices[0].finish_reason",
				errors.New("provider finish reason was repeated"),
			)
		}
		reason, err := decodeFinishReason(*choice.FinishReason)
		if err != nil {
			return err
		}
		stream.finishReason = reason
		stream.semanticProgress++
	}
	return nil
}

func (stream *ProviderStream) consumeReasoning(raw json.RawMessage) error {
	const path = "$.choices[0].delta.reasoning_content"
	if err := validateReasoningContent(raw, path); err != nil {
		return err
	}
	stream.decodedBytes += len(raw)
	if stream.decodedBytes > stream.codec.options.MaxResponseBytes {
		return protocolcore.NewFailure(
			protocolcore.ReasonStreamLimitExceeded,
			path,
			errors.New("provider stream exceeds the configured byte limit"),
		)
	}
	if len(stream.reasoning) == 0 {
		stream.report = stream.report.Merge(reasoningContentNotice(path))
	}
	stream.reasoning = append(stream.reasoning, bytes.Clone(raw))
	var content string
	_ = json.Unmarshal(raw, &content)
	if content != "" {
		stream.semanticProgress++
	}
	return nil
}

func (stream *ProviderStream) ensureMessageStarted(
	safe *bytes.Buffer,
	responseID string,
	reportedModel string,
) error {
	if responseID == "" || len(responseID) > 512 || !utf8.ValidString(responseID) {
		return protocolcore.NewFailure(
			protocolcore.ReasonInvalidProviderResponse,
			"$.id",
			errors.New("provider response ID is invalid"),
		)
	}
	if reportedModel == "" ||
		len(reportedModel) > protocolcore.MaxModelBytes ||
		!utf8.ValidString(reportedModel) {
		return protocolcore.NewFailure(
			protocolcore.ReasonInvalidProviderResponse,
			"$.model",
			errors.New("provider reported model is invalid"),
		)
	}
	if stream.messageStarted {
		if responseID != stream.responseID || reportedModel != stream.reportedModel {
			return protocolcore.NewFailure(
				protocolcore.ReasonStreamStateViolation,
				"$",
				errors.New("provider response identity changed within the stream"),
			)
		}
		return nil
	}
	stream.responseID = responseID
	stream.reportedModel = reportedModel
	stream.messageStarted = true
	stream.semanticProgress++
	return appendEvent(safe, "message_start", struct {
		Type    string `json:"type"`
		Message struct {
			ID           string             `json:"id"`
			Type         string             `json:"type"`
			Role         string             `json:"role"`
			Model        string             `json:"model"`
			Content      []any              `json:"content"`
			StopReason   *string            `json:"stop_reason"`
			StopSequence *string            `json:"stop_sequence"`
			Usage        anthropicUsageWire `json:"usage"`
			Container    any                `json:"container"`
			StopDetails  any                `json:"stop_details"`
		} `json:"message"`
	}{
		Type: "message_start",
		Message: struct {
			ID           string             `json:"id"`
			Type         string             `json:"type"`
			Role         string             `json:"role"`
			Model        string             `json:"model"`
			Content      []any              `json:"content"`
			StopReason   *string            `json:"stop_reason"`
			StopSequence *string            `json:"stop_sequence"`
			Usage        anthropicUsageWire `json:"usage"`
			Container    any                `json:"container"`
			StopDetails  any                `json:"stop_details"`
		}{
			ID:      responseID,
			Type:    "message",
			Role:    "assistant",
			Model:   stream.request.RequestedModel,
			Content: []any{},
			Usage: anthropicUsageWire{
				CacheCreationInputTokens: int64Pointer(0),
				CacheReadInputTokens:     int64Pointer(0),
			},
			Container:   nil,
			StopDetails: nil,
		},
	})
}

func (stream *ProviderStream) consumeText(safe *bytes.Buffer, text string) error {
	if !utf8.ValidString(text) {
		return protocolcore.NewFailure(
			protocolcore.ReasonInvalidProviderResponse,
			"$.choices[0].delta.content",
			errors.New("provider text delta is not valid UTF-8"),
		)
	}
	if text == "" {
		return nil
	}
	stream.semanticProgress++
	stream.decodedBytes += len(text)
	if stream.decodedBytes > stream.codec.options.MaxResponseBytes {
		return protocolcore.NewFailure(
			protocolcore.ReasonStreamLimitExceeded,
			"$.choices[0].delta.content",
			errors.New("decoded response exceeds the configured byte limit"),
		)
	}
	if stream.barrier {
		stream.heldBytes += len(text)
		if stream.heldBytes > stream.codec.options.MaxHeldSuffixBytes {
			return protocolcore.NewFailure(
				protocolcore.ReasonStreamLimitExceeded,
				"$.choices[0].delta.content",
				errors.New("held stream suffix exceeds the configured byte limit"),
			)
		}
		stream.heldText.WriteString(text)
		return nil
	}
	stream.preToolText.WriteString(text)
	if !stream.textOpen {
		stream.textIndex = stream.nextBlockIndex
		stream.nextBlockIndex++
		stream.textOpen = true
		if err := appendEvent(safe, "content_block_start", struct {
			Type         string `json:"type"`
			Index        int    `json:"index"`
			ContentBlock struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content_block"`
		}{
			Type:  "content_block_start",
			Index: stream.textIndex,
			ContentBlock: struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{Type: "text", Text: ""},
		}); err != nil {
			return err
		}
	}
	return appendEvent(safe, "content_block_delta", struct {
		Type  string `json:"type"`
		Index int    `json:"index"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	}{
		Type:  "content_block_delta",
		Index: stream.textIndex,
		Delta: struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{Type: "text_delta", Text: text},
	})
}

func (stream *ProviderStream) consumeToolFragments(
	safe *bytes.Buffer,
	fragments []openAIStreamToolCallWire,
) error {
	if !stream.barrier {
		if stream.textOpen {
			if err := appendEvent(safe, "content_block_stop", struct {
				Type  string `json:"type"`
				Index int    `json:"index"`
			}{Type: "content_block_stop", Index: stream.textIndex}); err != nil {
				return err
			}
			stream.textOpen = false
		}
		stream.barrier = true
	}
	for _, fragment := range fragments {
		if fragment.Index < 0 {
			return protocolcore.NewFailure(
				protocolcore.ReasonInvalidProviderResponse,
				"$.choices[0].delta.tool_calls.index",
				errors.New("provider tool call index is negative"),
			)
		}
		accumulator, exists := stream.tools[fragment.Index]
		if !exists {
			if len(stream.tools) >= stream.codec.options.MaxToolCalls {
				return protocolcore.NewFailure(
					protocolcore.ReasonStreamLimitExceeded,
					"$.choices[0].delta.tool_calls",
					errors.New("tool call count exceeds the configured limit"),
				)
			}
			accumulator = &streamToolAccumulator{index: fragment.Index}
			stream.tools[fragment.Index] = accumulator
		}
		if fragment.Type != "" && fragment.Type != "function" {
			return protocolcore.NewFailure(
				protocolcore.ReasonUnsupportedProviderData,
				"$.choices[0].delta.tool_calls.type",
				errors.New("provider tool call type is unsupported"),
			)
		}
		if fragment.ID != "" {
			if accumulator.id != "" && accumulator.id != fragment.ID {
				return protocolcore.NewFailure(
					protocolcore.ReasonStreamStateViolation,
					"$.choices[0].delta.tool_calls.id",
					errors.New("provider tool call ID changed within the stream"),
				)
			}
			if accumulator.id == "" {
				stream.semanticProgress++
			}
			accumulator.id = fragment.ID
		}
		if fragment.Function.Name != "" {
			if accumulator.name != "" && accumulator.name != fragment.Function.Name {
				return protocolcore.NewFailure(
					protocolcore.ReasonStreamStateViolation,
					"$.choices[0].delta.tool_calls.function.name",
					errors.New("provider tool call name changed within the stream"),
				)
			}
			if accumulator.name == "" {
				stream.semanticProgress++
			}
			accumulator.name = fragment.Function.Name
		}
		if len(accumulator.id) > 512 ||
			len(accumulator.name) > protocolcore.MaxToolNameBytes {
			return protocolcore.NewFailure(
				protocolcore.ReasonStreamLimitExceeded,
				"$.choices[0].delta.tool_calls",
				errors.New("provider tool identity exceeds the configured limit"),
			)
		}
		if fragment.Function.Arguments != "" {
			stream.semanticProgress++
			argumentBytes := []byte(fragment.Function.Arguments)
			if len(accumulator.arguments)+len(argumentBytes) >
				stream.codec.options.MaxToolArgumentBytes {
				return protocolcore.NewFailure(
					protocolcore.ReasonStreamLimitExceeded,
					"$.choices[0].delta.tool_calls.function.arguments",
					errors.New("tool arguments exceed the configured byte limit"),
				)
			}
			accumulator.arguments = append(accumulator.arguments, argumentBytes...)
			stream.decodedBytes += len(argumentBytes)
			stream.heldBytes += len(argumentBytes)
			if stream.decodedBytes > stream.codec.options.MaxResponseBytes ||
				stream.heldBytes > stream.codec.options.MaxHeldSuffixBytes {
				return protocolcore.NewFailure(
					protocolcore.ReasonStreamLimitExceeded,
					"$.choices[0].delta.tool_calls.function.arguments",
					errors.New("held stream suffix exceeds the configured byte limit"),
				)
			}
		}
	}
	return nil
}

type terminalDecision uint8

const (
	terminalUndecided terminalDecision = iota
	terminalApproved
	terminalRejected
)

type PendingTerminal struct {
	mu sync.Mutex

	release  []byte
	response protocolcore.Response
	intents  []protocolcore.ToolIntent
	report   protocolcore.TranslationReport
	decision terminalDecision
}

func newPendingTerminal(
	release []byte,
	response protocolcore.Response,
	intents []protocolcore.ToolIntent,
	report protocolcore.TranslationReport,
) *PendingTerminal {
	clonedIntents := make([]protocolcore.ToolIntent, len(intents))
	for index, intent := range intents {
		clonedIntents[index] = intent.Clone()
	}
	return &PendingTerminal{
		release:  bytes.Clone(release),
		response: response.Clone(),
		intents:  clonedIntents,
		report:   report,
	}
}

func (terminal *PendingTerminal) ToolIntents() []protocolcore.ToolIntent {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	intents := make([]protocolcore.ToolIntent, len(terminal.intents))
	for index, intent := range terminal.intents {
		intents[index] = intent.Clone()
	}
	return intents
}

func (terminal *PendingTerminal) DecodedResponse() protocolcore.Response {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	return terminal.response.Clone()
}

func (terminal *PendingTerminal) TranslationReport() protocolcore.TranslationReport {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	return terminal.report
}

func (terminal *PendingTerminal) Approve() ([]byte, error) {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	if terminal.decision != terminalUndecided {
		return nil, ErrTerminalAlreadyDecided
	}
	terminal.decision = terminalApproved
	return bytes.Clone(terminal.release), nil
}

func (terminal *PendingTerminal) Reject() error {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	if terminal.decision != terminalUndecided {
		return ErrTerminalAlreadyDecided
	}
	terminal.decision = terminalRejected
	terminal.release = nil
	return nil
}

func appendTextBlock(destination *bytes.Buffer, index int, text string) error {
	if err := appendEvent(destination, "content_block_start", struct {
		Type         string `json:"type"`
		Index        int    `json:"index"`
		ContentBlock struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content_block"`
	}{
		Type:  "content_block_start",
		Index: index,
		ContentBlock: struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{Type: "text", Text: ""},
	}); err != nil {
		return err
	}
	if err := appendEvent(destination, "content_block_delta", struct {
		Type  string `json:"type"`
		Index int    `json:"index"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	}{
		Type:  "content_block_delta",
		Index: index,
		Delta: struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{Type: "text_delta", Text: text},
	}); err != nil {
		return err
	}
	return appendEvent(destination, "content_block_stop", struct {
		Type  string `json:"type"`
		Index int    `json:"index"`
	}{Type: "content_block_stop", Index: index})
}

func appendTerminalEvents(
	destination *bytes.Buffer,
	reason protocolcore.StopReason,
	usage protocolcore.Usage,
) error {
	outputTokens := int64(0)
	if usage.Output.Known {
		outputTokens = usage.Output.Tokens
	}
	if err := appendEvent(destination, "message_delta", struct {
		Type  string `json:"type"`
		Delta struct {
			StopReason   string  `json:"stop_reason"`
			StopSequence *string `json:"stop_sequence"`
			Container    any     `json:"container"`
			StopDetails  any     `json:"stop_details"`
		} `json:"delta"`
		Usage anthropicUsageWire `json:"usage"`
	}{
		Type: "message_delta",
		Delta: struct {
			StopReason   string  `json:"stop_reason"`
			StopSequence *string `json:"stop_sequence"`
			Container    any     `json:"container"`
			StopDetails  any     `json:"stop_details"`
		}{
			StopReason:  string(reason),
			Container:   nil,
			StopDetails: nil,
		},
		Usage: anthropicUsageWire{
			InputTokens:              knownTokens(usage.InputUncached),
			OutputTokens:             outputTokens,
			CacheCreationInputTokens: int64Pointer(knownTokens(usage.CacheWrite)),
			CacheReadInputTokens:     int64Pointer(knownTokens(usage.CacheRead)),
		},
	}); err != nil {
		return err
	}
	return appendEvent(destination, "message_stop", struct {
		Type string `json:"type"`
	}{Type: "message_stop"})
}

func knownTokens(value protocolcore.UsageValue) int64 {
	if !value.Known {
		return 0
	}
	return value.Tokens
}

func int64Pointer(value int64) *int64 {
	return &value
}

func appendEvent(destination *bytes.Buffer, name string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	encoded, err := ssewire.Encode(ssewire.Event{Name: name, Data: data})
	if err != nil {
		return err
	}
	destination.Write(encoded)
	return nil
}

func wrapSSEError(err error) error {
	switch {
	case errors.Is(err, ssewire.ErrLimitExceeded):
		return protocolcore.NewFailure(protocolcore.ReasonStreamLimitExceeded, "$", err)
	case errors.Is(err, ssewire.ErrTruncated):
		return protocolcore.NewFailure(protocolcore.ReasonTruncatedEventStream, "$", err)
	default:
		return protocolcore.NewFailure(protocolcore.ReasonMalformedEventStream, "$", err)
	}
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return protocolcore.NewFailure(
			protocolcore.ReasonOperationCanceled,
			"$",
			errors.New("protocol operation context is nil"),
		)
	}
	if err := ctx.Err(); err != nil {
		return protocolcore.NewFailure(
			protocolcore.ReasonOperationCanceled,
			"$",
			err,
		)
	}
	return nil
}
