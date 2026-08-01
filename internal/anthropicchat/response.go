package anthropicchat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

type openAIResponseWire struct {
	ID                string             `json:"id"`
	Object            string             `json:"object"`
	Created           int64              `json:"created"`
	Model             string             `json:"model"`
	Choices           []openAIChoiceWire `json:"choices"`
	Usage             *openAIUsageWire   `json:"usage,omitempty"`
	SystemFingerprint *string            `json:"system_fingerprint,omitempty"`
	ServiceTier       *string            `json:"service_tier,omitempty"`
}

type openAIChoiceWire struct {
	Index        int                       `json:"index"`
	Message      openAIResponseMessageWire `json:"message"`
	FinishReason string                    `json:"finish_reason"`
	Logprobs     json.RawMessage           `json:"logprobs,omitempty"`
}

type openAIResponseMessageWire struct {
	Role             string               `json:"role"`
	Content          *string              `json:"content"`
	ReasoningContent json.RawMessage      `json:"reasoning_content,omitempty"`
	Refusal          *string              `json:"refusal,omitempty"`
	ToolCalls        []openAIToolCallWire `json:"tool_calls,omitempty"`
	Annotations      json.RawMessage      `json:"annotations,omitempty"`
	Audio            json.RawMessage      `json:"audio,omitempty"`
	FunctionCall     json.RawMessage      `json:"function_call,omitempty"`
}

type openAIUsageWire struct {
	PromptTokens            int64                      `json:"prompt_tokens"`
	CompletionTokens        int64                      `json:"completion_tokens"`
	TotalTokens             int64                      `json:"total_tokens"`
	PromptTokensDetails     *openAIPromptUsageWire     `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *openAICompletionUsageWire `json:"completion_tokens_details,omitempty"`
}

type openAIPromptUsageWire struct {
	CachedTokens int64 `json:"cached_tokens"`
	AudioTokens  int64 `json:"audio_tokens,omitempty"`
}

type openAICompletionUsageWire struct {
	ReasoningTokens          int64 `json:"reasoning_tokens,omitempty"`
	AudioTokens              int64 `json:"audio_tokens,omitempty"`
	AcceptedPredictionTokens int64 `json:"accepted_prediction_tokens,omitempty"`
	RejectedPredictionTokens int64 `json:"rejected_prediction_tokens,omitempty"`
	reasoningTokensPresent   bool
	raw                      json.RawMessage
}

func (wire *openAICompletionUsageWire) UnmarshalJSON(value []byte) error {
	var decoded struct {
		ReasoningTokens          int64 `json:"reasoning_tokens,omitempty"`
		AudioTokens              int64 `json:"audio_tokens,omitempty"`
		AcceptedPredictionTokens int64 `json:"accepted_prediction_tokens,omitempty"`
		RejectedPredictionTokens int64 `json:"rejected_prediction_tokens,omitempty"`
	}
	if err := decodeStrict(value, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(value, &fields); err != nil {
		return err
	}
	_, reasoningTokensPresent := fields["reasoning_tokens"]
	*wire = openAICompletionUsageWire{
		ReasoningTokens:          decoded.ReasoningTokens,
		AudioTokens:              decoded.AudioTokens,
		AcceptedPredictionTokens: decoded.AcceptedPredictionTokens,
		RejectedPredictionTokens: decoded.RejectedPredictionTokens,
		reasoningTokensPresent:   reasoningTokensPresent,
		raw:                      bytes.Clone(value),
	}
	return nil
}

func (codec *Codec) DecodeProviderResponse(
	request protocolcore.Request,
	body []byte,
) (protocolcore.Response, protocolcore.TranslationReport, error) {
	if err := request.Validate(); err != nil {
		return protocolcore.Response{}, protocolcore.TranslationReport{},
			protocolcore.NewFailure(protocolcore.ReasonInvalidClientRequest, "$", err)
	}
	if len(body) == 0 || len(body) > codec.options.MaxResponseBytes {
		return protocolcore.Response{}, protocolcore.TranslationReport{},
			protocolcore.NewFailure(
				protocolcore.ReasonInvalidProviderResponse,
				"$",
				errors.New("response body has an invalid size"),
			)
	}
	toolCatalog, err := buildProviderToolCatalog(request)
	if err != nil {
		return protocolcore.Response{}, protocolcore.TranslationReport{},
			protocolcore.NewFailure(
				protocolcore.ReasonInvalidClientRequest,
				"$.tools",
				err,
			)
	}

	var wire openAIResponseWire
	if err := decodeStrict(body, &wire); err != nil {
		return protocolcore.Response{}, protocolcore.TranslationReport{},
			protocolcore.NewFailure(protocolcore.ReasonInvalidProviderResponse, "$", err)
	}
	if wire.Object != "chat.completion" {
		return protocolcore.Response{}, protocolcore.TranslationReport{},
			protocolcore.NewFailure(
				protocolcore.ReasonInvalidProviderResponse,
				"$.object",
				errors.New("provider response object is invalid"),
			)
	}
	if len(wire.Choices) != 1 || wire.Choices[0].Index != 0 {
		return protocolcore.Response{}, protocolcore.TranslationReport{},
			protocolcore.NewFailure(
				protocolcore.ReasonUnsupportedProviderData,
				"$.choices",
				errors.New("exactly one choice with index zero is required"),
			)
	}
	choice := wire.Choices[0]
	if choice.Message.Role != "assistant" {
		return protocolcore.Response{}, protocolcore.TranslationReport{},
			protocolcore.NewFailure(
				protocolcore.ReasonInvalidProviderResponse,
				"$.choices[0].message.role",
				errors.New("provider response role is not assistant"),
			)
	}
	if rawPresent(choice.Logprobs) {
		return protocolcore.Response{}, protocolcore.TranslationReport{},
			protocolcore.NewFailure(
				protocolcore.ReasonUnsupportedProviderData,
				"$.choices[0].logprobs",
				errors.New("log probabilities are unsupported"),
			)
	}
	if choice.Message.Refusal != nil ||
		rawPresent(choice.Message.Annotations) ||
		rawPresent(choice.Message.Audio) ||
		rawPresent(choice.Message.FunctionCall) {
		return protocolcore.Response{}, protocolcore.TranslationReport{},
			protocolcore.NewFailure(
				protocolcore.ReasonUnsupportedProviderData,
				"$.choices[0].message",
				errors.New("provider response contains an unsupported content type"),
			)
	}

	report := protocolcore.TranslationReport{}
	providerExtensions := make([]protocolcore.ProviderExtension, 0, 2)
	if rawPresent(choice.Message.ReasoningContent) {
		extension, err := reasoningContentExtension(
			choice.Message.ReasoningContent,
			"$.choices[0].message.reasoning_content",
		)
		if err != nil {
			return protocolcore.Response{}, protocolcore.TranslationReport{}, err
		}
		providerExtensions = append(providerExtensions, extension)
		report = report.Merge(reasoningContentNotice(
			"$.choices[0].message.reasoning_content",
		))
	}

	blocks := make([]protocolcore.ContentBlock, 0, 1+len(choice.Message.ToolCalls))
	if choice.Message.Content != nil {
		block, err := protocolcore.NewTextBlock(*choice.Message.Content)
		if err != nil {
			return protocolcore.Response{}, protocolcore.TranslationReport{},
				protocolcore.NewFailure(
					protocolcore.ReasonInvalidProviderResponse,
					"$.choices[0].message.content",
					err,
				)
		}
		blocks = append(blocks, block)
	}
	if len(choice.Message.ToolCalls) > codec.options.MaxToolCalls {
		return protocolcore.Response{}, protocolcore.TranslationReport{},
			protocolcore.NewFailure(
				protocolcore.ReasonStreamLimitExceeded,
				"$.choices[0].message.tool_calls",
				errors.New("tool call count exceeds the configured limit"),
			)
	}
	toolCallIDs := make(map[string]struct{}, len(choice.Message.ToolCalls))
	for index, toolCall := range choice.Message.ToolCalls {
		if _, duplicate := toolCallIDs[toolCall.ID]; duplicate {
			return protocolcore.Response{}, protocolcore.TranslationReport{},
				protocolcore.NewFailure(
					protocolcore.ReasonInvalidProviderResponse,
					fmt.Sprintf("$.choices[0].message.tool_calls[%d].id", index),
					errors.New("provider tool call ID is duplicated"),
				)
		}
		toolCallIDs[toolCall.ID] = struct{}{}
		block, err := codec.decodeCompleteToolCall(
			toolCatalog,
			toolCall,
			fmt.Sprintf("$.choices[0].message.tool_calls[%d]", index),
		)
		if err != nil {
			return protocolcore.Response{}, protocolcore.TranslationReport{}, err
		}
		blocks = append(blocks, block)
	}
	if len(blocks) == 0 {
		return protocolcore.Response{}, protocolcore.TranslationReport{},
			protocolcore.NewFailure(
				protocolcore.ReasonInvalidProviderResponse,
				"$.choices[0].message",
				errors.New("provider response contains no content"),
			)
	}

	stopReason, err := decodeFinishReason(choice.FinishReason)
	if err != nil {
		return protocolcore.Response{}, protocolcore.TranslationReport{}, err
	}
	if (stopReason == protocolcore.StopReasonToolUse) != (len(choice.Message.ToolCalls) > 0) {
		return protocolcore.Response{}, protocolcore.TranslationReport{},
			protocolcore.NewFailure(
				protocolcore.ReasonInvalidProviderResponse,
				"$.choices[0].finish_reason",
				errors.New("finish reason and tool calls are inconsistent"),
			)
	}
	if wire.Usage == nil {
		return protocolcore.Response{}, protocolcore.TranslationReport{},
			protocolcore.NewFailure(
				protocolcore.ReasonInvalidProviderResponse,
				"$.usage",
				errors.New("provider response usage is missing"),
			)
	}
	usage, err := decodeUsage(wire.Usage)
	if err != nil {
		return protocolcore.Response{}, protocolcore.TranslationReport{}, err
	}
	if extension, present, extensionErr := reasoningUsageExtension(wire.Usage); extensionErr != nil {
		return protocolcore.Response{}, protocolcore.TranslationReport{}, extensionErr
	} else if present {
		providerExtensions = append(providerExtensions, extension)
		report = report.Merge(reasoningUsageNotice(
			"$.usage.completion_tokens_details",
		))
	}
	response := protocolcore.Response{
		ID:                 wire.ID,
		CreatedAtUnix:      wire.Created,
		RequestedModel:     request.RequestedModel,
		EffectiveModel:     request.EffectiveModel,
		ReportedModel:      wire.Model,
		Blocks:             blocks,
		ProviderExtensions: providerExtensions,
		StopReason:         stopReason,
		Usage:              usage,
	}
	if err := response.Validate(); err != nil {
		return protocolcore.Response{}, protocolcore.TranslationReport{},
			protocolcore.NewFailure(protocolcore.ReasonInvalidProviderResponse, "$", err)
	}
	if wire.ServiceTier != nil {
		report = report.Merge(protocolcore.NewTranslationReport(
			protocolcore.TranslationNotice{
				Code: protocolcore.NoticeServiceTierNotForwarded,
				Path: "$.service_tier",
			},
		))
	}
	return response.Clone(), report, nil
}

func (codec *Codec) decodeCompleteToolCall(
	toolCatalog providerToolCatalog,
	wire openAIToolCallWire,
	path string,
) (protocolcore.ContentBlock, error) {
	if wire.Type != "function" {
		return protocolcore.ContentBlock{}, protocolcore.NewFailure(
			protocolcore.ReasonUnsupportedProviderData,
			path+".type",
			errors.New("provider tool call type is unsupported"),
		)
	}
	key, err := protocolcore.NewCallKey(CallNamespace, wire.ID)
	if err != nil {
		return protocolcore.ContentBlock{}, protocolcore.NewFailure(
			protocolcore.ReasonInvalidProviderResponse,
			path+".id",
			err,
		)
	}
	entry, err := toolCatalog.clientEntryForProvider(wire.Function.Name)
	if err != nil {
		return protocolcore.ContentBlock{}, protocolcore.NewFailure(
			protocolcore.ReasonUnsupportedProviderData,
			path+".function.name",
			err,
		)
	}
	call, err := entry.clientCall(
		key,
		[]byte(wire.Function.Arguments),
		codec.options.MaxToolArgumentBytes,
	)
	if err != nil {
		return protocolcore.ContentBlock{}, protocolcore.NewFailure(
			protocolcore.ReasonToolCallIncomplete,
			path+".function.arguments",
			err,
		)
	}
	block, err := protocolcore.NewToolCallBlock(call)
	if err != nil {
		return protocolcore.ContentBlock{}, protocolcore.NewFailure(
			protocolcore.ReasonInvalidProviderResponse,
			path,
			err,
		)
	}
	return block, nil
}

func decodeFinishReason(value string) (protocolcore.StopReason, error) {
	switch value {
	case "stop":
		return protocolcore.StopReasonEndTurn, nil
	case "length":
		return protocolcore.StopReasonMaxTokens, nil
	case "tool_calls":
		return protocolcore.StopReasonToolUse, nil
	default:
		return "", protocolcore.NewFailure(
			protocolcore.ReasonUnsupportedProviderData,
			"$.choices[0].finish_reason",
			fmt.Errorf("provider finish reason %q is unsupported", value),
		)
	}
}

func decodeUsage(wire *openAIUsageWire) (protocolcore.Usage, error) {
	if wire == nil {
		return protocolcore.Usage{}, nil
	}
	if wire.PromptTokens < 0 || wire.CompletionTokens < 0 || wire.TotalTokens < 0 {
		return protocolcore.Usage{}, protocolcore.NewFailure(
			protocolcore.ReasonInvalidProviderResponse,
			"$.usage",
			errors.New("usage token count is negative"),
		)
	}
	if wire.TotalTokens != wire.PromptTokens+wire.CompletionTokens {
		return protocolcore.Usage{}, protocolcore.NewFailure(
			protocolcore.ReasonInvalidProviderResponse,
			"$.usage.total_tokens",
			errors.New("total tokens do not equal prompt plus completion tokens"),
		)
	}
	cached := int64(0)
	cachedReported := false
	if wire.PromptTokensDetails != nil {
		if wire.PromptTokensDetails.CachedTokens < 0 ||
			wire.PromptTokensDetails.AudioTokens != 0 {
			return protocolcore.Usage{}, protocolcore.NewFailure(
				protocolcore.ReasonUnsupportedProviderData,
				"$.usage.prompt_tokens_details",
				errors.New("prompt usage details are unsupported"),
			)
		}
		cached = wire.PromptTokensDetails.CachedTokens
		cachedReported = true
	}
	if cached > wire.PromptTokens {
		return protocolcore.Usage{}, protocolcore.NewFailure(
			protocolcore.ReasonInvalidProviderResponse,
			"$.usage.prompt_tokens_details.cached_tokens",
			errors.New("cached tokens exceed prompt tokens"),
		)
	}
	if wire.CompletionTokensDetails != nil {
		if wire.CompletionTokensDetails.ReasoningTokens < 0 {
			return protocolcore.Usage{}, protocolcore.NewFailure(
				protocolcore.ReasonInvalidProviderResponse,
				"$.usage.completion_tokens_details.reasoning_tokens",
				errors.New("reasoning token count is negative"),
			)
		}
		if wire.CompletionTokensDetails.AudioTokens != 0 ||
			wire.CompletionTokensDetails.AcceptedPredictionTokens != 0 ||
			wire.CompletionTokensDetails.RejectedPredictionTokens != 0 {
			return protocolcore.Usage{}, protocolcore.NewFailure(
				protocolcore.ReasonUnsupportedProviderData,
				"$.usage.completion_tokens_details",
				errors.New("completion usage details are unsupported"),
			)
		}
	}
	source := SourceOpenAIChat
	usage := protocolcore.Usage{
		InputUncached: protocolcore.UsageValue{
			Tokens: wire.PromptTokens - cached,
			Known:  true,
			Source: source,
		},
		// An absent prompt_tokens_details object means the provider did not
		// report a cached split, not that the split was zero. A reported zero
		// stays known because the provider did state it.
		CacheRead: cachedUsageValue(cached, cachedReported, source),
		// OpenAI Chat has no cache-write concept in its usage object, so any
		// value here would assert a fact the wire never stated and would
		// mis-price a provider that bills cache writes.
		CacheWrite: protocolcore.UsageValue{},
		Output: protocolcore.UsageValue{
			Tokens: wire.CompletionTokens,
			Known:  true,
			Source: source,
		},
	}
	if wire.CompletionTokensDetails != nil &&
		wire.CompletionTokensDetails.reasoningTokensPresent {
		usage.Reasoning = protocolcore.UsageValue{
			Tokens: wire.CompletionTokensDetails.ReasoningTokens,
			Known:  true,
			Source: source,
		}
	}
	if err := usage.Validate(); err != nil {
		return protocolcore.Usage{}, protocolcore.NewFailure(
			protocolcore.ReasonInvalidProviderResponse,
			"$.usage",
			err,
		)
	}
	return usage, nil
}

func reasoningContentExtension(
	raw json.RawMessage,
	path string,
) (protocolcore.ProviderExtension, error) {
	if err := validateReasoningContent(raw, path); err != nil {
		return protocolcore.ProviderExtension{}, err
	}
	extension, err := protocolcore.NewProviderExtension(
		SourceOpenAIChat,
		protocolcore.ProviderExtensionReasoningContent,
		path,
		[][]byte{raw},
	)
	if err != nil {
		return protocolcore.ProviderExtension{}, protocolcore.NewFailure(
			protocolcore.ReasonInvalidProviderResponse,
			path,
			err,
		)
	}
	return extension, nil
}

func validateReasoningContent(raw json.RawMessage, path string) error {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return protocolcore.NewFailure(
			protocolcore.ReasonInvalidProviderResponse,
			path,
			errors.New("provider reasoning content is not a string"),
		)
	}
	return nil
}

func reasoningUsageExtension(
	wire *openAIUsageWire,
) (protocolcore.ProviderExtension, bool, error) {
	if wire == nil ||
		wire.CompletionTokensDetails == nil ||
		!wire.CompletionTokensDetails.reasoningTokensPresent {
		return protocolcore.ProviderExtension{}, false, nil
	}
	const path = "$.usage.completion_tokens_details"
	extension, err := protocolcore.NewProviderExtension(
		SourceOpenAIChat,
		protocolcore.ProviderExtensionReasoningUsage,
		path,
		[][]byte{wire.CompletionTokensDetails.raw},
	)
	if err != nil {
		return protocolcore.ProviderExtension{}, false, protocolcore.NewFailure(
			protocolcore.ReasonInvalidProviderResponse,
			path,
			err,
		)
	}
	return extension, true, nil
}

func reasoningContentNotice(path string) protocolcore.TranslationReport {
	return protocolcore.NewTranslationReport(protocolcore.TranslationNotice{
		Code: protocolcore.NoticeReasoningContentNotForwarded,
		Path: path,
	})
}

func reasoningUsageNotice(path string) protocolcore.TranslationReport {
	return protocolcore.NewTranslationReport(protocolcore.TranslationNotice{
		Code: protocolcore.NoticeReasoningUsageNotForwarded,
		Path: path,
	})
}

type anthropicResponseWire struct {
	ID           string                         `json:"id"`
	Type         string                         `json:"type"`
	Role         string                         `json:"role"`
	Model        string                         `json:"model"`
	Content      []anthropicResponseContentWire `json:"content"`
	StopReason   string                         `json:"stop_reason"`
	StopSequence *string                        `json:"stop_sequence"`
	Usage        anthropicUsageWire             `json:"usage"`
	Container    any                            `json:"container"`
	StopDetails  any                            `json:"stop_details"`
}

type anthropicResponseContentWire struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

type anthropicUsageWire struct {
	InputTokens              int64  `json:"input_tokens"`
	OutputTokens             int64  `json:"output_tokens"`
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     *int64 `json:"cache_read_input_tokens,omitempty"`
}

func (codec *Codec) EncodeClientResponse(response protocolcore.Response) ([]byte, error) {
	if err := response.Validate(); err != nil {
		return nil, protocolcore.NewFailure(
			protocolcore.ReasonInvalidProviderResponse,
			"$",
			err,
		)
	}
	content := make([]anthropicResponseContentWire, len(response.Blocks))
	for index, block := range response.Blocks {
		switch block.Kind {
		case protocolcore.BlockText:
			content[index] = anthropicResponseContentWire{
				Type: "text",
				Text: block.Text,
			}
		case protocolcore.BlockToolCall:
			content[index] = anthropicResponseContentWire{
				Type:  "tool_use",
				ID:    block.ToolCall.Key.WireID(),
				Name:  block.ToolCall.Name,
				Input: block.ToolCall.Arguments.Bytes(),
			}
		default:
			return nil, protocolcore.NewFailure(
				protocolcore.ReasonUnsupportedProviderData,
				fmt.Sprintf("$.content[%d]", index),
				errors.New("response block cannot be encoded for Anthropic Messages"),
			)
		}
	}
	stopReason := string(response.StopReason)
	var stopSequence *string
	if response.StopReason == protocolcore.StopReasonStopSequence {
		value := response.StopSequence
		stopSequence = &value
	}
	usage := anthropicUsageWire{}
	if response.Usage.InputUncached.Known {
		usage.InputTokens = response.Usage.InputUncached.Tokens
	}
	if response.Usage.Output.Known {
		usage.OutputTokens = response.Usage.Output.Tokens
	}
	if response.Usage.CacheWrite.Known {
		value := response.Usage.CacheWrite.Tokens
		usage.CacheCreationInputTokens = &value
	}
	if response.Usage.CacheRead.Known {
		value := response.Usage.CacheRead.Tokens
		usage.CacheReadInputTokens = &value
	}
	encoded, err := json.Marshal(anthropicResponseWire{
		ID:           response.ID,
		Type:         "message",
		Role:         "assistant",
		Model:        response.RequestedModel,
		Content:      content,
		StopReason:   stopReason,
		StopSequence: stopSequence,
		Usage:        usage,
		Container:    nil,
		StopDetails:  nil,
	})
	if err != nil {
		return nil, protocolcore.NewFailure(
			protocolcore.ReasonInvalidProviderResponse,
			"$",
			err,
		)
	}
	return encoded, nil
}

func cachedUsageValue(
	tokens int64,
	reported bool,
	source string,
) protocolcore.UsageValue {
	if !reported {
		return protocolcore.UsageValue{}
	}
	return protocolcore.UsageValue{
		Tokens: tokens,
		Known:  true,
		Source: source,
	}
}
