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
	Error        json.RawMessage   `json:"error,omitempty"`
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
	if err := rejectDuplicateJSONNames(body); err != nil {
		return protocolcore.Response{}, protocolcore.NewFailure(
			protocolcore.ReasonInvalidProviderResponse,
			"$",
			err,
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
	return decodeMessagesResponse(request, wire, codec.options.MaxToolArgumentBytes, nil)
}

// decodeMessagesResponse decodes one complete message. actionDeltas holds, by
// content index, the streamed deltas of blocks that decode as provider actions.
func decodeMessagesResponse(
	request protocolcore.Request,
	wire messagesProviderResponseWire,
	maxToolArgumentBytes int,
	actionDeltas map[int][]json.RawMessage,
) (protocolcore.Response, error) {
	if wire.Type == "error" {
		return protocolcore.Response{}, protocolcore.NewProviderFailure("$.error", wire.Error)
	}
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
	extensions := make([]protocolcore.ProviderExtension, 0)
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
		default:
			kind, recognized := messagesExtensionKind(header.Type)
			if !recognized {
				block, err := messagesProviderActionBlock(header.Type, index, raw, actionDeltas[index], maxToolArgumentBytes)
				if err != nil {
					return protocolcore.Response{}, messagesProviderFailure(path, err)
				}
				blocks = append(blocks, block)
				continue
			}
			extension, err := protocolcore.NewProviderExtension(
				protocolcore.ProviderExtensionSourceAnthropicMessages,
				kind,
				path,
				[][]byte{bytes.Clone(raw)},
			)
			if err != nil {
				return protocolcore.Response{}, messagesProviderFailure(path, err)
			}
			extensions = append(extensions, extension)
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
		ID:                 wire.ID,
		RequestedModel:     request.RequestedModel,
		EffectiveModel:     request.EffectiveModel,
		ReportedModel:      wire.Model,
		Blocks:             blocks,
		ProviderExtensions: extensions,
		StopReason:         stopReason,
		StopSequence:       stopSequence,
		Usage:              usage,
	}
	if err := response.Validate(); err != nil {
		return protocolcore.Response{}, messagesProviderFailure("$", err)
	}
	return response.Clone(), nil
}

// These are provider-side evidence, not proposals for the client to execute.
// Client tool_use blocks always take the separate tool-intent/approval path,
// including when their request definition uses a native built-in tool type.
func messagesExtensionKind(kind string) (protocolcore.ProviderExtensionKind, bool) {
	switch kind {
	case "thinking":
		return protocolcore.ProviderExtensionThinking, true
	case "redacted_thinking":
		return protocolcore.ProviderExtensionRedactedThinking, true
	// Work the provider already ran: results to show, never client actions.
	case "server_tool_use", "web_search_tool_result", "web_fetch_tool_result",
		"code_execution_tool_result", "bash_code_execution_tool_result",
		"text_editor_code_execution_tool_result", "tool_search_tool_result", "container_upload",
		"mcp_tool_use", "mcp_tool_result":
		return protocolcore.ProviderExtensionOpaqueItem, true
	default:
		return "", false
	}
}

func decodeMessagesStopReason(value string) (protocolcore.StopReason, error) {
	reason := protocolcore.StopReason(value)
	if err := reason.Validate(); err != nil {
		return "", messagesProviderFailure(
			"$.stop_reason",
			fmt.Errorf("provider stop reason %q: %w", value, err),
		)
	}
	return reason, nil
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

// messagesProviderActionBlock carries a content block this dialect does not
// model as an unproven client action. Its arguments are the native block and
// any streamed deltas, so a reviewer sees everything the client would receive.
func messagesProviderActionBlock(
	kind string,
	index int,
	raw json.RawMessage,
	deltas []json.RawMessage,
	maxBytes int,
) (protocolcore.ContentBlock, error) {
	var identity struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &identity); err != nil {
		return protocolcore.ContentBlock{}, err
	}
	if identity.ID == "" {
		identity.ID = fmt.Sprintf("content-%d", index)
	}
	key, err := protocolcore.NewCallKey(CallNamespace, identity.ID)
	if err != nil {
		return protocolcore.ContentBlock{}, err
	}
	encoded, err := json.Marshal(struct {
		ContentBlock json.RawMessage   `json:"content_block"`
		Deltas       []json.RawMessage `json:"deltas,omitempty"`
	}{ContentBlock: raw, Deltas: deltas})
	if err != nil {
		return protocolcore.ContentBlock{}, err
	}
	arguments, err := protocolcore.NewJSONObject(encoded, maxBytes)
	if err != nil {
		return protocolcore.ContentBlock{}, err
	}
	return protocolcore.NewToolCallBlock(protocolcore.ToolCall{
		Kind: protocolcore.ToolKindProviderAction, Key: key, Name: kind, Arguments: arguments,
	})
}
