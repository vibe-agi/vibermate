package exchangecontent

import (
	"context"
	"fmt"
	"math"
	"unicode/utf8"
	"unsafe"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

// This is the existing storage identity ceiling, not a business history policy.
const storedMessageMaximum = 100001

func withinContext(ctx context.Context, l SourceLimits) error {
	if ctx == nil {
		return ErrInvalidEvidence
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return l.Validate()
}

func reserveViewEncoding(l SourceLimits) error {
	b, err := protocolcore.NewResourceBudget(l.Scratch)
	if err != nil {
		return fmt.Errorf("%w: view encoding workspace: %w", ErrInvalidEvidence, err)
	}
	// The embedded byte array is payload; only writer/counter control cells
	// are structure. Count the buffer exactly once in its own currency.
	if err := b.Reserve(protocolcore.ResourceCost{PayloadBytes: uint64(unsafe.Sizeof(canonicalWriter{}.scratch)), StructureBytes: uint64(unsafe.Sizeof(canonicalWriter{})) - uint64(unsafe.Sizeof(canonicalWriter{}.scratch)) + uint64(unsafe.Sizeof(wireCounter{}))}); err != nil {
		return fmt.Errorf("%w: view encoding workspace: %w", ErrInvalidEvidence, err)
	}
	return nil
}

func projectionMetadata(p Projection) RecordMetadata {
	r := p.Request
	m := RecordMetadata{ExchangeID: p.ExchangeID, Parent: p.Parent, Frozen: p.Frozen, Mode: p.Mode, RecordedAt: p.RecordedAt, ExpiresAt: p.ExpiresAt, Request: RequestMetadata{RequestedModel: r.RequestedModel, EffectiveModel: r.EffectiveModel, MaxOutputTokens: r.MaxOutputTokens, Stream: r.Stream, Tools: r.Tools, ProtocolEvidence: r.ProtocolEvidence}}
	if v := p.Response; v != nil {
		m.Response = &ResponseMetadata{ID: v.ID, RequestedModel: v.RequestedModel, EffectiveModel: v.EffectiveModel, ReportedModel: v.ReportedModel, StopReason: v.StopReason, Usage: v.Usage, ProtocolEvidence: v.ProtocolEvidence, EmptyOutput: len(v.Blocks) == 0}
	}
	return m
}

// ValidateWithin validates only the supplied read representation. A partial
// view cannot prove costs or corruption properties of its invisible history.
// Logical retained cost uses Record's exact per-occurrence rules. The finite
// Go-only container allowance is sizeof(Projection)-sizeof(Record), plus an
// optional sizeof(ProjectionPage) and three bounded cursor strings. Those
// fields do not narrow an otherwise admitted complete Record's read budget.
func (p Projection) ValidateWithin(ctx context.Context, l SourceLimits) error {
	if err := withinContext(ctx, l); err != nil {
		return err
	}
	if err := reserveViewEncoding(l); err != nil {
		return err
	}
	if !validIdentity(p.ExchangeID, MaxExchangeIDBytes) || p.Parent.Validate() != nil || p.Frozen.Validate() != nil || p.RecordedAt.IsZero() || p.ExpiresAt.IsZero() || !p.ExpiresAt.After(p.RecordedAt) || (p.Mode != environment.ContentRecordingFull && p.Mode != environment.ContentRecordingMetadataOnly) || p.TotalMessageCount < 1 || p.TotalMessageCount > storedMessageMaximum {
		return ErrInvalidEvidence
	}
	if _, err := p.RecordedAt.MarshalJSON(); err != nil {
		return fmt.Errorf("%w: recorded time: %w", ErrInvalidEvidence, err)
	}
	if _, err := p.ExpiresAt.MarshalJSON(); err != nil {
		return fmt.Errorf("%w: expiry time: %w", ErrInvalidEvidence, err)
	}
	i := p.Presentation.InheritedMessageCount
	if i < 0 || i > p.TotalMessageCount {
		return ErrInvalidEvidence
	}
	switch p.Presentation.Mode {
	case RequestPresentationCheckpoint:
		if i != 0 {
			return ErrInvalidEvidence
		}
	case RequestPresentationIncremental:
		if i == 0 || i >= p.TotalMessageCount {
			return ErrInvalidEvidence
		}
	case RequestPresentationSameTranscript:
		if i != p.TotalMessageCount {
			return ErrInvalidEvidence
		}
	default:
		return ErrInvalidEvidence
	}
	want := p.TotalMessageCount
	switch p.View {
	case RequestViewFull:
	case RequestViewIncremental:
		want -= i
	default:
		return ErrInvalidEvidence
	}
	if page := p.Page; page != nil {
		if page.RequestOffset < p.TotalMessageCount-want || page.RequestOffset > p.TotalMessageCount || len(p.Request.Messages) > p.TotalMessageCount-page.RequestOffset || len(p.Request.Messages) > PageMessageLimit || !validPageCursor(page.RequestNextCursor) || !validPageCursor(page.RequestEvidenceNextCursor) || !validPageCursor(page.ResponseEvidenceNextCursor) {
			return ErrInvalidEvidence
		}
	} else if len(p.Request.Messages) != want {
		return ErrInvalidEvidence
	}
	c, err := newRetainedCost(l)
	if err != nil {
		return err
	}
	if err := c.metadata(projectionMetadata(p)); err != nil {
		return err
	}
	// Validate metadata and actual content separately. No synthetic message or
	// complete Record is needed for either an empty replay or a deferred page.
	r := p.Request
	r.System, r.Messages = nil, nil
	if err := r.validateProjectionWithin(p.Mode, false); err != nil {
		return err
	}
	if p.Response != nil {
		v := *p.Response
		v.Blocks = nil
		if err := v.validateWithin(p.Mode, false); err != nil {
			return err
		}
	}
	if err := validateVisible(ctx, c, p.Request.Messages, p.Request.System, p.Mode, p.ExchangeID, l, p.Page != nil, canonicalBlockParentDepth); err != nil {
		return err
	}
	if p.Response != nil {
		if err := validateVisible(ctx, c, nil, p.Response.Blocks, p.Mode, p.ExchangeID, l, p.Page != nil, canonicalBlockParentDepth); err != nil {
			return err
		}
	}
	limit := l.CanonicalBytes
	if p.Page != nil {
		limit = min(limit, MaxPageBytes)
	}
	return countProjectionWire(ctx, limit, p)
}

func validateVisible(ctx context.Context, c *retainedCost, messages []Message, blocks []Block, mode environment.ContentRecordingMode, id string, l SourceLimits, paged bool, envelope int) error {
	checkBlocks := func(bs []Block, depth int) error {
		for _, b := range bs {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := c.block(b); err != nil {
				return err
			}
			if b.Deferred != nil {
				if !paged || validateDeferredShell(b, id, l.CanonicalBytes) != nil {
					return ErrInvalidEvidence
				}
			} else {
				if err := b.Validate(mode); err != nil {
					return err
				}
				if argumentDepthOverflow(b.Arguments, depth) != 0 {
					return fmt.Errorf("%w: canonical arguments exceed parent depth", ErrInvalidEvidence)
				}
			}
			if d := b.Deferred; d != nil {
				if err := c.add(c.strings(d.ExchangeID, d.Cursor), uint64(unsafe.Sizeof(*d))); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := checkBlocks(blocks, envelope); err != nil {
		return err
	}
	for _, m := range messages {
		if err := ctx.Err(); err != nil {
			return err
		}
		if paged && m.Role == "unknown" {
			if m.Agent != nil || len(m.Blocks) != 1 || m.Blocks[0].Deferred == nil {
				return ErrInvalidEvidence
			}
		} else if err := validateMessageHeader(m); err != nil {
			return err
		}
		if len(m.Blocks) == 0 {
			return ErrInvalidEvidence
		}
		if err := c.message(MessageHeader{Role: m.Role, Agent: m.Agent}); err != nil {
			return err
		}
		if err := checkBlocks(m.Blocks, canonicalMessageParentDepth); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func validateDeferredShell(b Block, id string, logicalLimit uint64) error {
	d := b.Deferred
	if d == nil || b.Kind != "deferred" || b.Availability != AvailabilityRecorded || b.Text != "" || b.OriginalSize != 0 || b.CallID != "" || b.ToolName != "" || b.ToolNamespace != "" || len(b.Arguments) != 0 || b.ToolError || b.ProviderSource != "" || b.ProviderKind != "" || b.Fingerprint != "" || b.Agent != nil || d.ExchangeID != id || d.Cursor == "" || !validPageCursor(d.Cursor) || d.EstimatedBytes < 0 || uint64(d.EstimatedBytes) > logicalLimit {
		return ErrInvalidEvidence
	}
	return nil
}

// ProjectWithin admits the complete borrowed input before cloning. Only the
// selected suffix is cloned; hidden prefixes never enter the output allocator.
func ProjectWithin(ctx context.Context, l SourceLimits, r Record, view RequestView) (Projection, error) {
	if err := withinContext(ctx, l); err != nil {
		return Projection{}, err
	}
	p := Projection{ExchangeID: r.ExchangeID, Parent: r.Parent, Frozen: r.Frozen, Mode: r.Mode, RecordedAt: r.RecordedAt, ExpiresAt: r.ExpiresAt, Request: r.Request, Response: r.Response, Presentation: r.Presentation, View: RequestViewFull, TotalMessageCount: len(r.Request.Messages)}
	// This is the genuinely complete input, not an invented partial Record.
	// Its typed full preflight proves all retained semantics and logical costs
	// before allocation. No second owned-leaf Source traversal is needed.
	if err := p.ValidateWithin(ctx, l); err != nil {
		return Projection{}, err
	}
	p.View = view
	// Validate presentation arithmetic before using inherited as a slice index.
	if i := r.Presentation.InheritedMessageCount; i < 0 || i > len(r.Request.Messages) {
		return Projection{}, ErrInvalidEvidence
	}
	if view == RequestViewIncremental {
		p.Request.Messages = p.Request.Messages[r.Presentation.InheritedMessageCount:]
	}
	if err := p.ValidateWithin(ctx, l); err != nil {
		return Projection{}, err
	}
	workspace, err := presentationWorkspaceFor(p.Response)
	if err != nil {
		return Projection{}, err
	}
	// The variable presentation workspace is derived from already-admitted
	// cells, separately from Source's leaf-sized projection Scratch currency.
	// Reserve its entire finite envelope before the output clone or map grows.
	if workspace.bytes != 0 {
		budget, err := protocolcore.NewResourceBudget(protocolcore.ResourceCost{PayloadBytes: 1, StructureBytes: workspace.bytes})
		if err != nil {
			return Projection{}, fmt.Errorf("%w: presentation workspace: %w", ErrInvalidEvidence, err)
		}
		if err := budget.Reserve(protocolcore.ResourceCost{StructureBytes: workspace.bytes}); err != nil {
			return Projection{}, fmt.Errorf("%w: presentation workspace: %w", ErrInvalidEvidence, err)
		}
	}
	p = p.Clone()
	if p.Response != nil {
		p.Response.Blocks, err = foldPresentation(ctx, p.Response.Blocks, workspace.candidates)
		if err != nil {
			return Projection{}, err
		}
	}
	if err := p.ValidateWithin(ctx, l); err != nil {
		return Projection{}, err
	}
	return p, nil
}

// Comparable fields borrow the same tuple as readableReasoningKey. Agent
// identifiers exclude NUL, so even text containing NUL has identical grouping.
type presentationKey struct {
	size                               int
	text, agentName, author, recipient string
}
type presentationWorkspace struct {
	candidates int
	bytes      uint64
}

func presentationWorkspaceFor(r *Response) (presentationWorkspace, error) {
	var p presentationWorkspace
	if r == nil {
		return p, nil
	}
	for _, b := range r.Blocks {
		if b.Kind == BlockKindReasoning && b.Availability == AvailabilityRecorded && b.Text != "" {
			p.candidates++
		}
	}
	if p.candidates == 0 {
		return p, nil
	}
	// Logical map cells: sizeof(key)+sizeof(index). Conservative Go1.26 map
	// allowance: 16 simultaneous slots per candidate (an eight-slot small map
	// plus old/new growth overlap), eight control/alignment bytes per slot,
	// 128 descriptor/directory bytes per candidate, and 64 fixed map bytes.
	// make's hint is the complete candidate upper bound, never hidden history.
	const perCandidate = 16*(uint64(unsafe.Sizeof(presentationKey{}))+uint64(unsafe.Sizeof(int(0)))+8) + 128
	if uint64(p.candidates) > (math.MaxInt64-64)/perCandidate {
		return p, fmt.Errorf("%w: presentation workspace overflow", ErrInvalidEvidence)
	}
	p.bytes = 64 + uint64(p.candidates)*perCandidate
	return p, nil
}

// Compaction runs only on the checked owned clone; no key copies a body and
// no second output Block slice is grown. Clear discarded references at return.
func foldPresentation(ctx context.Context, bs []Block, candidates int) ([]Block, error) {
	if candidates == 0 {
		return bs, ctx.Err()
	}
	seen := make(map[presentationKey]int, candidates)
	used := 0
	for _, b := range bs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if b.Kind != BlockKindReasoning || b.Availability != AvailabilityRecorded || b.Text == "" {
			bs[used] = b
			used++
			continue
		}
		k := presentationKey{size: b.OriginalSize, text: b.Text}
		if a := b.Agent; a != nil {
			k.agentName, k.author, k.recipient = a.AgentName, a.Author, a.Recipient
		}
		if index, found := seen[k]; found && bs[index].ProviderKind != b.ProviderKind {
			if reasoningKindPriority(b.ProviderKind) > reasoningKindPriority(bs[index].ProviderKind) {
				bs[index] = b
			}
			continue
		}
		if _, found := seen[k]; !found {
			seen[k] = used
		}
		bs[used] = b
		used++
	}
	clear(bs[used:])
	return bs[:used], ctx.Err()
}

// ValidateWithin keeps page wire/window bounds independent of logical totals.
// The Store proves actual roots, fragment identities and logical extents.
func (p ContentPage) ValidateWithin(ctx context.Context, l SourceLimits) error {
	if err := withinContext(ctx, l); err != nil {
		return err
	}
	if err := reserveViewEncoding(l); err != nil {
		return err
	}
	if !validIdentity(p.ExchangeID, MaxExchangeIDBytes) || p.Parent.Validate() != nil || p.Frozen.Validate() != nil || (p.Mode != environment.ContentRecordingFull && p.Mode != environment.ContentRecordingMetadataOnly) || p.Offset < 0 || p.Total < p.Offset || !validPageCursor(p.NextCursor) || len(p.CallID) > 512 || len(p.ToolName) > protocolcore.MaxToolNameBytes || (p.Mode != environment.ContentRecordingFull && p.Text != "") {
		return ErrInvalidEvidence
	}
	remaining := p.Total - p.Offset
	switch p.Kind {
	case "protocol":
		if p.Total > protocolcore.MaxProtocolEvidenceValues || len(p.Messages) != 0 || len(p.Blocks) != 0 || p.Text != "" || len(p.ProtocolEvidence) > PageMessageLimit || len(p.ProtocolEvidence) > remaining {
			return ErrInvalidEvidence
		}
	case "request":
		if p.Total > storedMessageMaximum || len(p.Messages) > PageMessageLimit || len(p.Blocks) != 0 || p.Text != "" || len(p.Messages) > remaining {
			return ErrInvalidEvidence
		}
	case "message":
		if uint64(p.Total) > l.StructureBytes/uint64(unsafe.Sizeof(Block{})) || len(p.Messages) != 0 || len(p.Blocks) > PageMessageLimit || p.Text != "" || len(p.Blocks) > remaining {
			return ErrInvalidEvidence
		}
	case "text", "arguments":
		if uint64(p.Total) > l.RetainedBytes || len(p.Messages) != 0 || len(p.Blocks) != 0 || !utf8.ValidString(p.Text) || len(p.Text) > PageBodyBytes || len(p.Text) > remaining {
			return ErrInvalidEvidence
		}
	default:
		return ErrInvalidEvidence
	}
	if p.Kind != "protocol" && len(p.ProtocolEvidence) != 0 {
		return ErrInvalidEvidence
	}
	c, err := newRetainedCost(l)
	if err != nil {
		return err
	}
	f := p.Frozen
	if err := c.add(c.strings(p.BlockKind, p.CallID, p.ToolName, p.ExchangeID, p.Kind, p.Text, p.NextCursor, p.Parent.CaptureRunID, p.Parent.ManualCaptureID, f.EnvironmentID, f.EnvironmentDigest, f.ClientEndpointID, f.ProtocolPlanID, f.RouteID, string(p.Mode)), uint64(unsafe.Sizeof(ContentPage{}))); err != nil {
		return err
	}
	if err := c.evidence(p.ProtocolEvidence); err != nil {
		return err
	}
	if err := protocolcore.ValidateProtocolEvidence(p.ProtocolEvidence); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidEvidence, err)
	}
	if err := validateVisible(ctx, c, p.Messages, p.Blocks, p.Mode, p.ExchangeID, l, true, canonicalBlockParentDepth); err != nil {
		return err
	}
	return countPageWire(ctx, min(l.CanonicalBytes, MaxPageBytes), p)
}
