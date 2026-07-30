package anthropicchat

import (
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
	Role         string               `json:"role"`
	Content      *string              `json:"content"`
	Refusal      *string              `json:"refusal,omitempty"`
	ToolCalls    []openAIToolCallWire `json:"tool_calls,omitempty"`
	Annotations  json.RawMessage      `json:"annotations,omitempty"`
	Audio        json.RawMessage      `json:"audio,omitempty"`
	FunctionCall json.RawMessage      `json:"function_call,omitempty"`
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
	response := protocolcore.Response{
		ID:             wire.ID,
		RequestedModel: request.RequestedModel,
		EffectiveModel: request.EffectiveModel,
		ReportedModel:  wire.Model,
		Blocks:         blocks,
		StopReason:     stopReason,
		Usage:          usage,
	}
	if err := response.Validate(); err != nil {
		return protocolcore.Response{}, protocolcore.TranslationReport{},
			protocolcore.NewFailure(protocolcore.ReasonInvalidProviderResponse, "$", err)
	}
	report := protocolcore.TranslationReport{}
	if wire.ServiceTier != nil {
		report = protocolcore.NewTranslationReport(protocolcore.TranslationNotice{
			Code: protocolcore.NoticeServiceTierNotForwarded,
			Path: "$.service_tier",
		})
	}
	return response.Clone(), report, nil
}

func (codec *Codec) decodeCompleteToolCall(
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
	arguments, err := protocolcore.NewJSONObject(
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
	block, err := protocolcore.NewToolCallBlock(protocolcore.ToolCall{
		Key:       key,
		Name:      wire.Function.Name,
		Arguments: arguments,
	})
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
	}
	if cached > wire.PromptTokens {
		return protocolcore.Usage{}, protocolcore.NewFailure(
			protocolcore.ReasonInvalidProviderResponse,
			"$.usage.prompt_tokens_details.cached_tokens",
			errors.New("cached tokens exceed prompt tokens"),
		)
	}
	if wire.CompletionTokensDetails != nil &&
		(wire.CompletionTokensDetails.ReasoningTokens != 0 ||
			wire.CompletionTokensDetails.AudioTokens != 0 ||
			wire.CompletionTokensDetails.AcceptedPredictionTokens != 0 ||
			wire.CompletionTokensDetails.RejectedPredictionTokens != 0) {
		return protocolcore.Usage{}, protocolcore.NewFailure(
			protocolcore.ReasonUnsupportedProviderData,
			"$.usage.completion_tokens_details",
			errors.New("completion usage details are unsupported"),
		)
	}
	source := SourceOpenAIChat
	usage := protocolcore.Usage{
		InputUncached: protocolcore.UsageValue{
			Tokens: wire.PromptTokens - cached,
			Known:  true,
			Source: source,
		},
		CacheRead: protocolcore.UsageValue{
			Tokens: cached,
			Known:  true,
			Source: source,
		},
		CacheWrite: protocolcore.UsageValue{
			Tokens: 0,
			Known:  true,
			Source: source,
		},
		Output: protocolcore.UsageValue{
			Tokens: wire.CompletionTokens,
			Known:  true,
			Source: source,
		},
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
