package anthropicchat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

type messagesProviderResponseWire struct {
	ID           string            `json:"id"`
	Type         string            `json:"type"`
	Role         string            `json:"role"`
	Model        string            `json:"model"`
	Content      []json.RawMessage `json:"content"`
	StopReason   string            `json:"stop_reason"`
	StopSequence *string           `json:"stop_sequence"`
	Usage        messagesUsageWire `json:"usage"`
}

type messagesUsageWire struct {
	InputTokens              *int64 `json:"input_tokens"`
	OutputTokens             *int64 `json:"output_tokens"`
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     *int64 `json:"cache_read_input_tokens,omitempty"`
}

func (codec *Codec) DecodeAnthropicProviderResponse(
	request protocolcore.Request,
	body []byte,
) (protocolcore.Response, error) {
	if err := request.Validate(); err != nil {
		return protocolcore.Response{}, protocolcore.NewFailure(
			protocolcore.ReasonInvalidClientRequest,
			"$",
			err,
		)
	}
	if len(body) == 0 || len(body) > codec.options.MaxResponseBytes {
		return protocolcore.Response{}, protocolcore.NewFailure(
			protocolcore.ReasonInvalidProviderResponse,
			"$",
			errors.New("response body has an invalid size"),
		)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	var wire messagesProviderResponseWire
	if err := decoder.Decode(&wire); err != nil {
		return protocolcore.Response{}, protocolcore.NewFailure(
			protocolcore.ReasonInvalidProviderResponse,
			"$",
			err,
		)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return protocolcore.Response{}, protocolcore.NewFailure(
			protocolcore.ReasonInvalidProviderResponse,
			"$",
			errors.New("response body has trailing data"),
		)
	}
	return decodeMessagesResponse(request, wire, codec.options.MaxToolArgumentBytes)
}

func decodeMessagesResponse(
	request protocolcore.Request,
	wire messagesProviderResponseWire,
	maxToolArgumentBytes int,
) (protocolcore.Response, error) {
	if wire.Type != "message" {
		return protocolcore.Response{}, messagesProviderFailure(
			"$.type",
			errors.New("provider response type is invalid"),
		)
	}
	if wire.Role != "assistant" {
		return protocolcore.Response{}, messagesProviderFailure(
			"$.role",
			errors.New("provider response role is invalid"),
		)
	}
	blocks := make([]protocolcore.ContentBlock, 0, len(wire.Content))
	knownTools := make(map[string]struct{}, len(request.Tools))
	for _, tool := range request.Tools {
		knownTools[tool.Name] = struct{}{}
	}
	for index, raw := range wire.Content {
		path := fmt.Sprintf("$.content[%d]", index)
		var header struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &header); err != nil {
			return protocolcore.Response{}, messagesProviderFailure(path, err)
		}
		switch header.Type {
		case "text":
			var content struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(raw, &content); err != nil {
				return protocolcore.Response{}, messagesProviderFailure(path, err)
			}
			block, err := protocolcore.NewTextBlock(content.Text)
			if err != nil {
				return protocolcore.Response{}, messagesProviderFailure(path+".text", err)
			}
			blocks = append(blocks, block)
		case "tool_use":
			var content struct {
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			}
			if err := json.Unmarshal(raw, &content); err != nil {
				return protocolcore.Response{}, messagesProviderFailure(path, err)
			}
			if _, exists := knownTools[content.Name]; !exists {
				return protocolcore.Response{}, protocolcore.NewFailure(
					protocolcore.ReasonUnsupportedProviderData,
					path+".name",
					errors.New("provider invoked a tool that the client did not define"),
				)
			}
			key, err := protocolcore.NewCallKey(CallNamespace, content.ID)
			if err != nil {
				return protocolcore.Response{}, messagesProviderFailure(path+".id", err)
			}
			arguments, err := protocolcore.NewJSONObject(content.Input, maxToolArgumentBytes)
			if err != nil {
				return protocolcore.Response{}, messagesProviderFailure(path+".input", err)
			}
			block, err := protocolcore.NewToolCallBlock(protocolcore.ToolCall{
				Key:       key,
				Name:      content.Name,
				Arguments: arguments,
			})
			if err != nil {
				return protocolcore.Response{}, messagesProviderFailure(path, err)
			}
			blocks = append(blocks, block)
		case "thinking", "redacted_thinking":
			// These blocks remain byte-for-byte in the compatible wire response.
			// They are non-actionable provider content, so they do not enter the
			// tool-decision projection.
		default:
			return protocolcore.Response{}, messagesProviderFailure(
				path+".type",
				fmt.Errorf("provider content type %q is unsupported", header.Type),
			)
		}
	}
	if len(blocks) == 0 {
		empty, err := protocolcore.NewTextBlock("")
		if err != nil {
			return protocolcore.Response{}, messagesProviderFailure("$.content", err)
		}
		blocks = append(blocks, empty)
	}
	stopReason, err := decodeMessagesStopReason(wire.StopReason)
	if err != nil {
		return protocolcore.Response{}, err
	}
	stopSequence := ""
	if wire.StopSequence != nil {
		stopSequence = *wire.StopSequence
	}
	usage, err := decodeMessagesUsage(wire.Usage)
	if err != nil {
		return protocolcore.Response{}, err
	}
	response := protocolcore.Response{
		ID:             wire.ID,
		RequestedModel: request.RequestedModel,
		EffectiveModel: request.EffectiveModel,
		ReportedModel:  wire.Model,
		Blocks:         blocks,
		StopReason:     stopReason,
		StopSequence:   stopSequence,
		Usage:          usage,
	}
	if err := response.Validate(); err != nil {
		return protocolcore.Response{}, messagesProviderFailure("$", err)
	}
	return response.Clone(), nil
}

func decodeMessagesStopReason(value string) (protocolcore.StopReason, error) {
	reason := protocolcore.StopReason(value)
	switch reason {
	case protocolcore.StopReasonEndTurn,
		protocolcore.StopReasonMaxTokens,
		protocolcore.StopReasonToolUse,
		protocolcore.StopReasonStopSequence:
		return reason, nil
	default:
		return "", messagesProviderFailure(
			"$.stop_reason",
			fmt.Errorf("provider stop reason %q is unsupported", value),
		)
	}
}

func decodeMessagesUsage(wire messagesUsageWire) (protocolcore.Usage, error) {
	if wire.InputTokens == nil || wire.OutputTokens == nil {
		return protocolcore.Usage{}, messagesProviderFailure(
			"$.usage",
			errors.New("provider usage is incomplete"),
		)
	}
	values := []*int64{
		wire.InputTokens,
		wire.OutputTokens,
		wire.CacheCreationInputTokens,
		wire.CacheReadInputTokens,
	}
	for _, value := range values {
		if value != nil && *value < 0 {
			return protocolcore.Usage{}, messagesProviderFailure(
				"$.usage",
				errors.New("provider usage contains a negative token count"),
			)
		}
	}
	known := func(value *int64) protocolcore.UsageValue {
		if value == nil {
			return protocolcore.UsageValue{}
		}
		return protocolcore.UsageValue{
			Tokens: *value,
			Known:  true,
			Source: SourceAnthropicMessages,
		}
	}
	usage := protocolcore.Usage{
		InputUncached: known(wire.InputTokens),
		CacheWrite:    known(wire.CacheCreationInputTokens),
		CacheRead:     known(wire.CacheReadInputTokens),
		Output:        known(wire.OutputTokens),
	}
	if err := usage.Validate(); err != nil {
		return protocolcore.Usage{}, messagesProviderFailure("$.usage", err)
	}
	return usage, nil
}

func messagesProviderFailure(path string, err error) error {
	return protocolcore.NewFailure(
		protocolcore.ReasonInvalidProviderResponse,
		path,
		err,
	)
}
