package anthropicchat

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
)

type anthropicStreamEncoder struct {
	requestedModel string
}

func newAnthropicStreamEncoder(
	request protocolcore.Request,
) protocolpath.ClientStreamEncoder {
	return anthropicStreamEncoder{requestedModel: request.RequestedModel}
}

func (encoder anthropicStreamEncoder) Start(
	start protocolpath.StreamStart,
) ([]byte, error) {
	var encoded bytes.Buffer
	err := appendEvent(&encoded, "message_start", struct {
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
			ID:      start.ResponseID,
			Type:    "message",
			Role:    "assistant",
			Model:   encoder.requestedModel,
			Content: []any{},
			Usage: anthropicUsageWire{
				CacheCreationInputTokens: int64Pointer(0),
				CacheReadInputTokens:     int64Pointer(0),
			},
			Container:   nil,
			StopDetails: nil,
		},
	})
	return encoded.Bytes(), err
}

func (anthropicStreamEncoder) StartText(index int) ([]byte, error) {
	var encoded bytes.Buffer
	err := appendEvent(&encoded, "content_block_start", struct {
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
	})
	return encoded.Bytes(), err
}

func (anthropicStreamEncoder) AppendText(
	index int,
	text string,
) ([]byte, error) {
	var encoded bytes.Buffer
	err := appendEvent(&encoded, "content_block_delta", struct {
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
	})
	return encoded.Bytes(), err
}

func (anthropicStreamEncoder) StopText(index int) ([]byte, error) {
	var encoded bytes.Buffer
	err := appendEvent(&encoded, "content_block_stop", struct {
		Type  string `json:"type"`
		Index int    `json:"index"`
	}{Type: "content_block_stop", Index: index})
	return encoded.Bytes(), err
}

func (anthropicStreamEncoder) ToolCall(
	index int,
	call protocolcore.ToolCall,
) ([]byte, error) {
	if call.EffectiveKind() != protocolcore.ToolKindFunction {
		return nil, protocolcore.NewFailure(
			protocolcore.ReasonUnsupportedProviderData,
			"$.content",
			errors.New("Anthropic Messages cannot encode a custom tool call"),
		)
	}
	var encoded bytes.Buffer
	if err := appendEvent(&encoded, "content_block_start", struct {
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
		Index: index,
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
		return nil, err
	}
	if err := appendEvent(&encoded, "content_block_delta", struct {
		Type  string `json:"type"`
		Index int    `json:"index"`
		Delta struct {
			Type        string `json:"type"`
			PartialJSON string `json:"partial_json"`
		} `json:"delta"`
	}{
		Type:  "content_block_delta",
		Index: index,
		Delta: struct {
			Type        string `json:"type"`
			PartialJSON string `json:"partial_json"`
		}{
			Type:        "input_json_delta",
			PartialJSON: string(call.Arguments.Bytes()),
		},
	}); err != nil {
		return nil, err
	}
	if err := appendEvent(&encoded, "content_block_stop", struct {
		Type  string `json:"type"`
		Index int    `json:"index"`
	}{Type: "content_block_stop", Index: index}); err != nil {
		return nil, err
	}
	return encoded.Bytes(), nil
}

func (anthropicStreamEncoder) Terminal(
	response protocolcore.Response,
) ([]byte, error) {
	var encoded bytes.Buffer
	if err := appendTerminalEvents(
		&encoded,
		response.StopReason,
		response.Usage,
	); err != nil {
		return nil, err
	}
	return encoded.Bytes(), nil
}

func (anthropicStreamEncoder) TranslationReport() protocolcore.TranslationReport {
	return protocolcore.TranslationReport{}
}
