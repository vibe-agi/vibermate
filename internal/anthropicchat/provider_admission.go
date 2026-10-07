package anthropicchat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

// ValidateTransformedProviderRequest admits the exact Chat wire that is about
// to be sent. It neither manufactures a neutral request nor rewrites unknown
// provider fields: lexical admission charges every occurrence before decoding
// known fields, and the caller retains and sends the complete original bytes.
func (codec *Codec) ValidateTransformedProviderRequest(body []byte) error {
	if len(body) == 0 || len(body) > codec.options.MaxRequestBytes {
		return errors.New("transformed Chat body exceeds wire bound")
	}
	if err := codec.validateRequestJSON(body); err != nil {
		return err
	}
	var wire struct {
		openAIRequestWire
		Messages   []json.RawMessage `json:"messages"`
		ToolChoice json.RawMessage   `json:"tool_choice"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return err
	}
	if wire.Model == "" || len(wire.Model) > protocolcore.MaxModelBytes || !utf8.ValidString(wire.Model) || len(wire.Messages) == 0 {
		return errors.New("invalid Chat model or messages")
	}
	if len(wire.Tools) > protocolcore.MaxToolCount {
		return errors.New("invalid Chat tool count")
	}
	tools := make(map[string]struct{}, len(wire.Tools))
	for _, tool := range wire.Tools {
		if tool.Type != "function" {
			return errors.New("invalid Chat tool type")
		}
		schema, err := protocolcore.NewJSONObject(tool.Function.Parameters, protocolcore.MaxToolJSONBytes)
		if err != nil {
			return err
		}
		definition := protocolcore.ToolDefinition{Name: tool.Function.Name, Description: tool.Function.Description, InputSchema: schema}
		if err := definition.Validate(); err != nil {
			return err
		}
		if _, exists := tools[tool.Function.Name]; exists {
			return errors.New("duplicate Chat tool")
		}
		tools[tool.Function.Name] = struct{}{}
	}
	for index, raw := range wire.Messages {
		var m struct {
			openAIRequestMessageWire
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			return fmt.Errorf("Chat message %d: %w", index, err)
		}
		switch m.Role {
		case "system", "developer", "user", "assistant", "tool":
		default:
			return fmt.Errorf("Chat message %d has invalid role", index)
		}
		present := len(m.Content) > 0 && !bytes.Equal(bytes.TrimSpace(m.Content), []byte("null"))
		if !present && (m.Role != "assistant" || len(m.ToolCalls) == 0) {
			return fmt.Errorf("Chat message %d has no content", index)
		}
		if present {
			if err := codec.validateChatContent(m.Content); err != nil {
				return fmt.Errorf("Chat message %d: %w", index, err)
			}
		}
		if len(m.ToolCalls) > codec.options.MaxToolCalls || len(m.ToolCalls) > 0 && m.Role != "assistant" {
			return errors.New("invalid Chat tool-call owner/count")
		}
		if m.Role == "tool" && (m.ToolCallID == "" || len(m.ToolCallID) > 512 || !utf8.ValidString(m.ToolCallID)) {
			return errors.New("invalid Chat tool result identity")
		}
		if m.Role != "tool" && m.ToolCallID != "" {
			return errors.New("Chat tool result identity on non-tool message")
		}
		for _, call := range m.ToolCalls {
			if call.Type != "function" || call.ID == "" || len(call.ID) > 512 || !utf8.ValidString(call.ID) || call.Function.Name == "" || len(call.Function.Name) > protocolcore.MaxToolNameBytes || !utf8.ValidString(call.Function.Name) {
				return errors.New("invalid Chat tool call identity")
			}
			if _, err := protocolcore.NewJSONObject([]byte(call.Function.Arguments), codec.options.MaxToolArgumentBytes); err != nil {
				return err
			}
		}
	}
	if wire.MaxTokens != nil && *wire.MaxTokens <= 0 || wire.MaxCompletionTokens != nil && *wire.MaxCompletionTokens <= 0 {
		return errors.New("invalid Chat output token bound")
	}
	if wire.Temperature != nil && (*wire.Temperature < 0 || *wire.Temperature > 2) || wire.TopP != nil && (*wire.TopP < 0 || *wire.TopP > 1) {
		return errors.New("invalid Chat sampling value")
	}
	if len(wire.Stop) > protocolcore.MaxStopSequenceCount {
		return errors.New("invalid Chat stop count")
	}
	for _, stop := range wire.Stop {
		if stop == "" || len(stop) > protocolcore.MaxStopSequenceBytes || !utf8.ValidString(stop) {
			return errors.New("invalid Chat stop sequence")
		}
	}
	if len(wire.ToolChoice) > 0 && !bytes.Equal(bytes.TrimSpace(wire.ToolChoice), []byte("null")) {
		var name string
		if json.Unmarshal(wire.ToolChoice, &name) == nil {
			if name != "auto" && name != "none" && name != "required" {
				return errors.New("invalid Chat tool choice")
			}
		} else {
			var named struct {
				Type     string `json:"type"`
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			}
			if json.Unmarshal(wire.ToolChoice, &named) != nil || named.Type != "function" {
				return errors.New("invalid Chat named tool choice")
			}
			if _, ok := tools[named.Function.Name]; !ok {
				return errors.New("Chat tool choice names undeclared tool")
			}
		}
	}
	if f := wire.ResponseFormat; f != nil {
		switch f.Type {
		case "text", "json_object":
		case "json_schema":
			if _, err := protocolcore.NewJSONObject(f.JSONSchema.Schema, protocolcore.MaxOutputSchemaBytes); err != nil {
				return err
			}
		default:
			return errors.New("invalid Chat response format")
		}
	}
	return nil
}
func (codec *Codec) validateChatContent(raw json.RawMessage) error {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if len(text) > codec.options.MaxRequestBytes || !utf8.ValidString(text) {
			return errors.New("invalid Chat text")
		}
		return nil
	}
	// Native multipart content remains on the wire. Validate known part shapes
	// without dropping charged unknown fields or decoding image/audio payloads.
	var parts []struct {
		Type     string  `json:"type"`
		Text     *string `json:"text"`
		ImageURL *struct {
			URL    string `json:"url"`
			Detail string `json:"detail"`
		} `json:"image_url"`
		InputAudio *struct {
			Data   string `json:"data"`
			Format string `json:"format"`
		} `json:"input_audio"`
		Refusal *string `json:"refusal"`
	}
	if json.Unmarshal(raw, &parts) != nil || len(parts) == 0 {
		return errors.New("invalid Chat content")
	}
	for _, part := range parts {
		switch part.Type {
		case "text":
			if part.Text == nil || !utf8.ValidString(*part.Text) {
				return errors.New("invalid Chat text part")
			}
		case "image_url":
			if part.ImageURL == nil || part.ImageURL.URL == "" {
				return errors.New("invalid Chat image part")
			}
		case "input_audio":
			if part.InputAudio == nil || part.InputAudio.Data == "" || (part.InputAudio.Format != "wav" && part.InputAudio.Format != "mp3") {
				return errors.New("invalid Chat audio part")
			}
		case "refusal":
			if part.Refusal == nil {
				return errors.New("invalid Chat refusal part")
			}
		default:
			return errors.New("unsupported Chat content part")
		}
	}
	return nil
}
