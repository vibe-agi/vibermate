package exchangecontent

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

// Page limits concern the read model, never the request sent to a provider or
// the immutable retained Record. Page payloads leave room for Exchange metadata
// within the ordinary 2 MiB control-response limit.
const (
	PageMessageLimit       = 24
	PageContentBytes       = 128 << 10
	PageBodyBytes          = 32 << 10
	MaxPageBytes           = 1 << 20
	MaxPageCursorBytes     = 2048
	MaxCanonicalBlockBytes = uint64((32<<20)-56) * 16384
)

// BlockPageMetadata is a complete read-only description, not a partial Block.
// Text/Arguments themselves remain in their byte-preserving page streams.
type BlockPageMetadata struct {
	Kind           string        `json:"kind"`
	Availability   Availability  `json:"availability"`
	OriginalSize   int           `json:"originalSize"`
	CallID         string        `json:"callId,omitempty"`
	ToolName       string        `json:"toolName,omitempty"`
	ToolNamespace  string        `json:"toolNamespace,omitempty"`
	ToolError      bool          `json:"toolError,omitempty"`
	ProviderSource string        `json:"providerSource,omitempty"`
	ProviderKind   string        `json:"providerKind,omitempty"`
	Fingerprint    string        `json:"fingerprint,omitempty"`
	Agent          *AgentContext `json:"agent,omitempty"`
	TextBytes      uint64        `json:"textBytes"`
	ArgumentBytes  uint64        `json:"argumentBytes"`
}

func (m BlockPageMetadata) Shape() BlockShape {
	s := BlockShape{Kind: m.Kind, Availability: m.Availability, OriginalSize: m.OriginalSize, CallID: m.CallID, ToolName: m.ToolName, ToolNamespace: m.ToolNamespace, ToolError: m.ToolError, HasText: m.TextBytes > 0, HasArguments: m.ArgumentBytes > 0, HasProviderSource: m.ProviderSource != "", HasProviderKind: m.ProviderKind != "", HasFingerprint: m.Fingerprint != "", FingerprintValid: len(m.Fingerprint) == 71 && strings.HasPrefix(m.Fingerprint, "sha256:")}
	if s.FingerprintValid {
		for _, c := range m.Fingerprint[7:] {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				s.FingerprintValid = false
			}
		}
	}
	if m.Agent != nil {
		s.HasAgent = true
		s.Agent = *m.Agent
	}
	return s
}

// DeferredContent is an explicit read-time placeholder, not omitted recording.
// Cursors are scoped to one Exchange and frozen content; they grant no access.
type DeferredContent struct {
	ExchangeID     string `json:"exchangeId"`
	Cursor         string `json:"cursor"`
	EstimatedBytes int    `json:"estimatedBytes"`
}

type ProjectionPage struct {
	// InputAnchor identifies the observed user group's position, so repeated
	// requests do not repeat it and identical new messages remain distinct.
	InputAnchor                string `json:"inputAnchor,omitempty"`
	RequestOffset              int    `json:"requestOffset"`
	RequestNextCursor          string `json:"requestNextCursor,omitempty"`
	RequestEvidenceNextCursor  string `json:"requestEvidenceNextCursor,omitempty"`
	ResponseEvidenceNextCursor string `json:"responseEvidenceNextCursor,omitempty"`
}

// ContentPage replaces the current page in a reader. Messages preserve order
// within a bounded window; nextCursor walks toward older request history.
// A body page is a UTF-8 fragment, explicitly not a complete JSON argument.
type ContentPage struct {
	// InspectionExpiresAt retains the original source deadline through outer
	// enrichment/encoding. It is not a wire field or a refreshed lifetime.
	InspectionExpiresAt time.Time                            `json:"-"`
	BlockMetadata       *BlockPageMetadata                   `json:"blockMetadata,omitempty"`
	CanonicalCursor     string                               `json:"canonicalCursor,omitempty"`
	Data                []byte                               `json:"data,omitempty"`
	BlockKind           string                               `json:"blockKind,omitempty"`
	CallID              string                               `json:"callId,omitempty"`
	ToolName            string                               `json:"toolName,omitempty"`
	ExchangeID          string                               `json:"exchangeId"`
	Kind                string                               `json:"kind"`
	Messages            []Message                            `json:"messages"`
	Blocks              []Block                              `json:"blocks"`
	ProtocolEvidence    []protocolcore.ProtocolEvidenceValue `json:"protocolEvidence,omitempty"`
	Text                string                               `json:"text,omitempty"`
	Offset              int                                  `json:"offset"`
	Total               int                                  `json:"total"`
	NextCursor          string                               `json:"nextCursor,omitempty"`
	Parent              ParentRef                            `json:"-"`
	Frozen              FrozenRef                            `json:"-"`
	Mode                environment.ContentRecordingMode     `json:"-"`
}

func (page ContentPage) Validate() error {
	l, err := DefaultSourceLimits()
	if err != nil {
		return err
	}
	return page.ValidateWithin(context.Background(), l)
}

func validPageCursor(cursor string) bool {
	return len(cursor) <= MaxPageCursorBytes && utf8.ValidString(cursor)
}
