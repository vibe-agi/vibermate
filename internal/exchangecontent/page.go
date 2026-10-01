package exchangecontent

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

// Page limits concern the read model, never the request sent to a provider or
// the immutable retained Record. Page payloads leave room for Exchange metadata
// within the ordinary 2 MiB control-response limit.
const (
	PageMessageLimit   = 24
	PageContentBytes   = 128 << 10
	PageBodyBytes      = 32 << 10
	MaxPageBytes       = 1 << 20
	MaxPageCursorBytes = 2048
)

// DeferredContent is an explicit read-time placeholder, not omitted recording.
// Cursors are scoped to one Exchange and frozen content; they grant no access.
type DeferredContent struct {
	ExchangeID     string `json:"exchangeId"`
	Cursor         string `json:"cursor"`
	EstimatedBytes int    `json:"estimatedBytes"`
}

type ProjectionPage struct {
	RequestOffset              int    `json:"requestOffset"`
	RequestNextCursor          string `json:"requestNextCursor,omitempty"`
	RequestEvidenceNextCursor  string `json:"requestEvidenceNextCursor,omitempty"`
	ResponseEvidenceNextCursor string `json:"responseEvidenceNextCursor,omitempty"`
}

// ContentPage replaces the current page in a reader. Messages preserve order
// within a bounded window; nextCursor walks toward older request history.
// A body page is a UTF-8 fragment, explicitly not a complete JSON argument.
type ContentPage struct {
	BlockKind        string                               `json:"blockKind,omitempty"`
	CallID           string                               `json:"callId,omitempty"`
	ToolName         string                               `json:"toolName,omitempty"`
	ExchangeID       string                               `json:"exchangeId"`
	Kind             string                               `json:"kind"`
	Messages         []Message                            `json:"messages"`
	Blocks           []Block                              `json:"blocks"`
	ProtocolEvidence []protocolcore.ProtocolEvidenceValue `json:"protocolEvidence,omitempty"`
	Text             string                               `json:"text,omitempty"`
	Offset           int                                  `json:"offset"`
	Total            int                                  `json:"total"`
	NextCursor       string                               `json:"nextCursor,omitempty"`
	Parent           ParentRef                            `json:"-"`
	Frozen           FrozenRef                            `json:"-"`
	Mode             environment.ContentRecordingMode     `json:"-"`
}

func (page ContentPage) Validate() error {
	if len(page.CallID) > 512 || len(page.ToolName) > protocolcore.MaxToolNameBytes || page.Total > MaxEncodedBytes ||
		(page.Mode != environment.ContentRecordingFull && page.Text != "") {
		return ErrInvalidEvidence
	}
	if !validIdentity(page.ExchangeID, MaxExchangeIDBytes) || page.Parent.Validate() != nil ||
		page.Frozen.Validate() != nil || (page.Mode != environment.ContentRecordingFull && page.Mode != environment.ContentRecordingMetadataOnly) ||
		page.Offset < 0 || page.Total < page.Offset || !validPageCursor(page.NextCursor) {
		return ErrInvalidEvidence
	}
	switch page.Kind {
	case "protocol":
		if len(page.Messages) != 0 || len(page.Blocks) != 0 || page.Text != "" || len(page.ProtocolEvidence) > PageMessageLimit || page.Offset+len(page.ProtocolEvidence) > page.Total || protocolcore.ValidateProtocolEvidence(page.ProtocolEvidence) != nil {
			return ErrInvalidEvidence
		}
	case "request":
		if len(page.Messages) > PageMessageLimit || len(page.Blocks) != 0 || page.Text != "" ||
			page.Offset+len(page.Messages) > page.Total {
			return ErrInvalidEvidence
		}
	case "message":
		if len(page.Messages) != 0 || len(page.Blocks) > PageMessageLimit || page.Text != "" ||
			page.Offset+len(page.Blocks) > page.Total {
			return ErrInvalidEvidence
		}
	case "text", "arguments":
		if len(page.Messages) != 0 || len(page.Blocks) != 0 || !utf8.ValidString(page.Text) ||
			len(page.Text) > PageBodyBytes || page.Offset+len(page.Text) > page.Total {
			return ErrInvalidEvidence
		}
	default:
		return ErrInvalidEvidence
	}
	if page.Kind != "protocol" && len(page.ProtocolEvidence) != 0 {
		return ErrInvalidEvidence
	}
	if err := validatePageContent(page.Messages, page.Blocks, page.Mode, page.ExchangeID); err != nil {
		return err
	}
	encoded, err := json.Marshal(page)
	if err != nil || len(encoded) > MaxPageBytes {
		return ErrInvalidEvidence
	}
	return nil
}

func validPageCursor(cursor string) bool {
	return len(cursor) <= MaxPageCursorBytes && utf8.ValidString(cursor)
}

func validatePageContent(messages []Message, blocks []Block, mode environment.ContentRecordingMode, exchangeID string) error {
	validateBlocks := func(values []Block) error {
		for _, block := range values {
			if block.Deferred == nil {
				if err := block.Validate(mode); err != nil {
					return err
				}
				continue
			}
			// An unloaded value has no asserted kind, text, tool identity or size.
			d := block.Deferred
			want := Block{Kind: "deferred", Availability: AvailabilityRecorded, Deferred: d}
			left, _ := json.Marshal(block)
			right, _ := json.Marshal(want)
			if string(left) != string(right) || d.ExchangeID != exchangeID || d.Cursor == "" || !validPageCursor(d.Cursor) ||
				d.EstimatedBytes < 0 || d.EstimatedBytes > MaxEncodedBytes {
				return ErrInvalidEvidence
			}
		}
		return nil
	}
	if err := validateBlocks(blocks); err != nil {
		return err
	}
	for _, message := range messages {
		if message.Role == "unknown" {
			if message.Agent != nil || len(message.Blocks) != 1 || message.Blocks[0].Deferred == nil {
				return ErrInvalidEvidence
			}
		} else {
			// Validate role/agent independently without admitting placeholders to
			// the canonical Record contract.
			probe := message
			probe.Blocks = []Block{{Kind: "text", Availability: AvailabilityOmitted}}
			r := Request{RequestedModel: "page", EffectiveModel: "page", Messages: []Message{probe}}
			if err := r.validateProjection(environment.ContentRecordingMetadataOnly); err != nil {
				return err
			}
		}
		if len(message.Blocks) == 0 {
			return ErrInvalidEvidence
		}
		if err := validateBlocks(message.Blocks); err != nil {
			return err
		}
	}
	return nil
}
