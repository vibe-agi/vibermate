package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/ssewire"
)

// assistantBlocks reads the Anthropic stream Claude Code would receive and
// returns the assistant content it would replay next turn (F32): thinking
// with its signature, text and tool_use, in order.
func assistantBlocks(stream []byte) ([]map[string]any, error) {
	decoder, err := ssewire.NewDecoder(ssewire.DefaultOptions())
	if err != nil {
		return nil, err
	}
	events, err := decoder.Feed(stream)
	if err != nil {
		return nil, err
	}
	if err := decoder.Finish(); err != nil {
		return nil, err
	}
	type open struct {
		kind, text, signature, id, name string
		input                           strings.Builder
	}
	var blocks []map[string]any
	var current *open
	for _, event := range events {
		var wire struct {
			Type         string `json:"type"`
			ContentBlock struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
				Text string `json:"text"`
			} `json:"content_block"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Thinking    string `json:"thinking"`
				Signature   string `json:"signature"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
		}
		if err := json.Unmarshal(event.Data, &wire); err != nil {
			return nil, fmt.Errorf("client event %s: %w", event.Name, err)
		}
		switch wire.Type {
		case "content_block_start":
			current = &open{kind: wire.ContentBlock.Type, id: wire.ContentBlock.ID, name: wire.ContentBlock.Name, text: wire.ContentBlock.Text}
		case "content_block_delta":
			if current == nil {
				return nil, errors.New("client delta outside a block")
			}
			switch wire.Delta.Type {
			case "text_delta":
				current.text += wire.Delta.Text
			case "thinking_delta":
				current.text += wire.Delta.Thinking
			case "signature_delta":
				current.signature += wire.Delta.Signature
			case "input_json_delta":
				current.input.WriteString(wire.Delta.PartialJSON)
			}
		case "content_block_stop":
			if current == nil {
				return nil, errors.New("client stop outside a block")
			}
			switch current.kind {
			case "thinking":
				blocks = append(blocks, map[string]any{"type": "thinking", "thinking": current.text, "signature": current.signature})
			case "text":
				blocks = append(blocks, map[string]any{"type": "text", "text": current.text})
			case "tool_use":
				input := json.RawMessage(current.input.String())
				if len(input) == 0 {
					input = json.RawMessage("{}")
				}
				if !json.Valid(input) {
					return nil, errors.New("client tool input is not JSON")
				}
				blocks = append(blocks, map[string]any{"type": "tool_use", "id": current.id, "name": current.name, "input": input})
			default:
				return nil, fmt.Errorf("client block type %q cannot be replayed", current.kind)
			}
			current = nil
		}
	}
	return blocks, nil
}

// probeToolOutput answers every tool call a replayed turn made: the probe
// runs no tool.
const probeToolOutput = "Synthetic probe tool output: the tool was not run."

// nextTurn is the request Claude Code sends after a turn: the previous body
// with the assistant blocks replayed verbatim, a tool_result for each
// tool_use, and the next prompt.
func nextTurn(previous []byte, assistant []map[string]any, prompt string, edit string) (*claudeBody, error) {
	if len(assistant) == 0 {
		return nil, errors.New("the turn gave no assistant content to replay")
	}
	body, err := parseClaudeBody(previous)
	if err != nil {
		return nil, err
	}
	var user []map[string]any
	for _, block := range assistant {
		if block["type"] == "tool_use" {
			user = append(user, map[string]any{"type": "tool_result", "tool_use_id": block["id"], "content": probeToolOutput})
		}
	}
	user = append(user, map[string]any{"type": "text", "text": prompt})
	if err := body.appendTurn(assistant, user, edit); err != nil {
		return nil, err
	}
	return body, nil
}

// replayableReasoning reports whether assistant blocks hold a ViberMate
// seal with a payload that a text or tool_use follows, the condition for a
// replay (design 6.3).
func replayableReasoning(assistant []map[string]any) bool {
	for index, block := range assistant {
		if block["type"] != "thinking" {
			continue
		}
		signature, _ := block["signature"].(string)
		seal, _, ok := protocolcore.ParseReasoningSeal(signature)
		if !ok || seal.Format != protocolcore.SealResponsesEncrypted || seal.Opaque == "" {
			continue
		}
		for _, later := range assistant[index+1:] {
			if later["type"] == "text" || later["type"] == "tool_use" {
				return true
			}
		}
	}
	return false
}

// tamperSeals rewrites the payload of every ViberMate seal in a body's
// thinking blocks: the middle of the upstream's encrypted_content changes,
// its length and alphabet do not, and the seal header still passes every
// replay check, so the path sends the damaged value.
func tamperSeals(body *claudeBody) (int, error) {
	messages, err := body.messages()
	if err != nil {
		return 0, err
	}
	tampered := 0
	for _, message := range messages {
		if messageRole(message) != "assistant" {
			continue
		}
		var blocks []map[string]any
		if json.Unmarshal(message["content"], &blocks) != nil {
			continue
		}
		for _, block := range blocks {
			signature, _ := block["signature"].(string)
			seal, source, ok := protocolcore.ParseReasoningSeal(signature)
			if block["type"] != "thinking" || !ok || seal.Format != protocolcore.SealResponsesEncrypted || seal.Opaque == "" {
				continue
			}
			seal.Opaque = tamperOpaque(seal.Opaque)
			if block["signature"], err = protocolcore.FormatReasoningSeal(seal, source); err != nil {
				return 0, err
			}
			tampered++
		}
		if message["content"], err = marshalNoEscape(blocks); err != nil {
			return 0, err
		}
	}
	if tampered == 0 {
		return 0, nil
	}
	return tampered, body.set("messages", messages, fmt.Sprintf("encrypted_content of %d replayed reasoning item(s) tampered", tampered))
}

// tamperOpaque shifts up to 24 characters in the middle of value within
// their own class (lower, upper, digit), keeping every other byte.
func tamperOpaque(value string) string {
	characters := []byte(value)
	start := len(characters) / 3
	changed := 0
	for index := start; index < len(characters) && changed < 24; index++ {
		character := characters[index]
		switch {
		case character >= 'a' && character <= 'z':
			characters[index] = 'a' + (character-'a'+7)%26
		case character >= 'A' && character <= 'Z':
			characters[index] = 'A' + (character-'A'+7)%26
		case character >= '0' && character <= '9':
			characters[index] = '0' + (character-'0'+3)%10
		default:
			continue
		}
		changed++
	}
	return string(characters)
}

// syntheticSealTurn is the Q10 fallback when no real reasoning item exists:
// a replayed turn whose thinking carries a seal minted for this route with
// a payload no provider issued.
func syntheticSealTurn(previous []byte, model, binding string) (*claudeBody, error) {
	signature, err := protocolcore.FormatReasoningSeal(protocolcore.ReasoningSeal{
		Format:  protocolcore.SealResponsesEncrypted,
		ItemID:  "rs_responses_probe_synthetic",
		Model:   model,
		Binding: binding,
		Opaque:  "gAAAAABprobe" + strings.Repeat("SyntheticEncryptedContent0", 12),
	}, "")
	if err != nil {
		return nil, err
	}
	assistant := []map[string]any{
		{"type": "thinking", "thinking": "Synthetic probe reasoning.", "signature": signature},
		{"type": "text", "text": "ok"},
	}
	return nextTurn(previous, assistant, "Reply with the single word ok.", "synthetic replayed turn with a seal no provider issued")
}
