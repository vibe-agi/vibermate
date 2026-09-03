package exchangecontent

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

const (
	// MaxRequestPreviewBatch bounds the amount of content-addressed evidence a
	// list request may resolve in one operation. The Activity API has the same
	// page ceiling, so a caller never needs to split an ordinary page.
	MaxRequestPreviewBatch = 200
	MaxRequestPreviewRunes = 180
)

type RequestPreview struct {
	Kind      string `json:"kind"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

func (preview RequestPreview) Validate() error {
	if preview.Text == "" || !utf8.ValidString(preview.Text) ||
		len([]rune(preview.Text)) > MaxRequestPreviewRunes {
		return fmt.Errorf("%w: request preview text is invalid", ErrInvalidEvidence)
	}
	switch protocolcore.BlockKind(preview.Kind) {
	case protocolcore.BlockText,
		protocolcore.BlockRefusal,
		protocolcore.BlockToolCall,
		protocolcore.BlockToolResult:
		return nil
	default:
		return fmt.Errorf("%w: request preview kind is invalid", ErrInvalidEvidence)
	}
}

// PreviewRequestMessage derives a bounded, single-line list projection from
// the final request message. It never exposes reasoning or provider-extension
// blocks and it never invents content when recording was disabled.
func PreviewRequestMessage(message Message) (RequestPreview, bool) {
	for _, block := range message.Blocks {
		if block.Availability != AvailabilityRecorded {
			continue
		}
		kind := protocolcore.BlockKind(block.Kind)
		var value string
		switch kind {
		case protocolcore.BlockText, protocolcore.BlockRefusal:
			value = block.Text
		case protocolcore.BlockToolCall:
			value = qualifiedToolName(block.ToolNamespace, block.ToolName)
		case protocolcore.BlockToolResult:
			value = block.Text
			if value == "" {
				value = qualifiedToolName(block.ToolNamespace, block.ToolName)
			}
		default:
			continue
		}
		value = singleLinePreview(value)
		if value == "" {
			continue
		}
		runes := []rune(value)
		preview := RequestPreview{Kind: string(kind), Text: value}
		if len(runes) > MaxRequestPreviewRunes {
			preview.Text = string(runes[:MaxRequestPreviewRunes])
			preview.Truncated = true
		}
		return preview, true
	}
	return RequestPreview{}, false
}

func qualifiedToolName(namespace, name string) string {
	if namespace == "" {
		return name
	}
	return namespace + "." + name
}

func singleLinePreview(value string) string {
	value = strings.Map(func(character rune) rune {
		if unicode.IsSpace(character) || unicode.IsControl(character) || character == '\ufeff' {
			return ' '
		}
		return character
	}, value)
	return strings.Join(strings.Fields(value), " ")
}
