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

// Every closed deferred shell occupies at least this many wire bytes. The
// independent page wire ceiling therefore bounds even nested marker lists;
// unknown message wrappers are additionally bounded by the 24-message window.
const readControlMinShellWire = len(`{"deferred":{"exchangeId":"a","cursor":"c","estimatedBytes":0},"kind":"deferred","availability":"recorded","originalSize":0}`)
const readControlMaxShells = MaxPageBytes / readControlMinShellWire
const readControlPayloadLimit = MaxPageBytes + 3*MaxPageCursorBytes
const readControlStructureLimit = uint64(readControlMaxShells)*(uint64(unsafe.Sizeof(Block{}))+uint64(unsafe.Sizeof(DeferredContent{}))) + PageMessageLimit*uint64(unsafe.Sizeof(Message{})) + uint64(unsafe.Sizeof(Projection{})) + uint64(unsafe.Sizeof(ProjectionPage{})) + uint64(unsafe.Sizeof(ContentPage{})) + uint64(unsafe.Sizeof(readControlCost{}))

// Control is not hidden content credit. Only strictly validated generated
// shells/wrappers enter this ledger. Real visible data keeps Source's exact
// L/S policy. One fixed writer is reused to measure exact control wire bytes;
// its concrete workspace is included above, independently of Source Scratch.
type readControlCost struct {
	payload, structure uint64
	shells, unknown    int
	wire               wireCounter
	writer             canonicalWriter
}

func newReadControlCost(ctx context.Context) *readControlCost {
	c := &readControlCost{structure: uint64(unsafe.Sizeof(readControlCost{})), wire: wireCounter{ctx: ctx, limit: MaxPageBytes}}
	c.writer.sink = &c.wire
	return c
}
func (c *readControlCost) add(payload, structure uint64) error {
	if payload > readControlPayloadLimit-c.payload || structure > readControlStructureLimit-c.structure {
		return fmt.Errorf("%w: page control budget exceeded", ErrInvalidEvidence)
	}
	c.payload += payload
	c.structure += structure
	return nil
}
func (c *readControlCost) shell(b Block) error {
	if c.shells >= readControlMaxShells {
		return ErrInvalidEvidence
	}
	c.shells++
	d := b.Deferred
	if err := c.add(uint64(len(b.Kind)+len(b.Availability)+len(d.ExchangeID)+len(d.Cursor)), uint64(unsafe.Sizeof(b))+uint64(unsafe.Sizeof(*d))); err != nil {
		return err
	}
	c.writer.block(b)
	c.writer.flush()
	return c.writer.err
}
func (c *readControlCost) unknownMessage() error {
	if c.unknown >= PageMessageLimit {
		return ErrInvalidEvidence
	}
	c.unknown++
	if err := c.add(uint64(len("unknown")), uint64(unsafe.Sizeof(Message{}))); err != nil {
		return err
	}
	c.writer.text(`{"role":"unknown","blocks":[]}`)
	c.writer.flush()
	return c.writer.err
}
func (c *readControlCost) projection(page *ProjectionPage) error {
	return c.add(uint64(len(page.RequestNextCursor)+len(page.RequestEvidenceNextCursor)+len(page.ResponseEvidenceNextCursor)), uint64(unsafe.Sizeof(Projection{}))-uint64(unsafe.Sizeof(Record{}))+uint64(unsafe.Sizeof(*page)))
}
func (c *readControlCost) page(p ContentPage) error {
	if err := c.add(uint64(len(p.Kind)+len(p.NextCursor)), uint64(unsafe.Sizeof(p))); err != nil {
		return err
	}
	w := &c.writer
	// Exact generated container bytes from writePageWire. Exchange identity,
	// real block/tool metadata, evidence and body bytes are not credited here.
	w.text("{")
	w.text(",")
	w.field("kind", p.Kind)
	w.text(`,"messages":`)
	if p.Messages == nil {
		w.text("null")
	} else {
		w.text("[")
		for i := 1; i < len(p.Messages); i++ {
			w.text(",")
		}
		w.text("]")
	}
	w.text(`,"blocks":`)
	if p.Blocks == nil {
		w.text("null")
	} else {
		w.text("[")
		for i := 1; i < len(p.Blocks); i++ {
			w.text(",")
		}
		w.text("]")
	}
	w.text(`,"offset":`)
	w.integer(int64(p.Offset))
	w.text(`,"total":`)
	w.integer(int64(p.Total))
	w.optional("nextCursor", p.NextCursor)
	w.text("}")
	w.flush()
	return w.err
}
func (c *readControlCost) wireLimit(logical uint64) uint64 {
	// Saturating at the existing page cap avoids addition overflow even for
	// a policy at MaxInt64. Only exactly measured control bytes add credit.
	if logical >= MaxPageBytes {
		return MaxPageBytes
	}
	return logical + min(c.wire.n, uint64(MaxPageBytes)-logical)
}

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
	var control *readControlCost
	if p.Page != nil {
		control = newReadControlCost(ctx)
		if err := control.projection(p.Page); err != nil {
			return err
		}
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
	if err := validateVisible(ctx, c, p.Request.Messages, p.Request.System, p.Mode, p.ExchangeID, l, control, canonicalBlockParentDepth); err != nil {
		return err
	}
	if p.Response != nil {
		if err := validateVisible(ctx, c, nil, p.Response.Blocks, p.Mode, p.ExchangeID, l, control, canonicalBlockParentDepth); err != nil {
			return err
		}
	}
	limit := l.CanonicalBytes
	if p.Page != nil {
		limit = control.wireLimit(limit)
	}
	return countProjectionWire(ctx, limit, p)
}

func validateVisible(ctx context.Context, c *retainedCost, messages []Message, blocks []Block, mode environment.ContentRecordingMode, id string, l SourceLimits, control *readControlCost, envelope int) error {
	checkBlocks := func(bs []Block, depth int) error {
		for _, b := range bs {
			if err := ctx.Err(); err != nil {
				return err
			}
			if b.Deferred != nil {
				if control == nil || validateDeferredShell(b, id, l.CanonicalBytes) != nil {
					return ErrInvalidEvidence
				}
				if err := control.shell(b); err != nil {
					return err
				}
			} else {
				if err := c.block(ctx, b); err != nil {
					return err
				}
				if err := b.Validate(mode); err != nil {
					return err
				}
				if argumentDepthOverflow(b.Arguments, depth) != 0 {
					return fmt.Errorf("%w: canonical arguments exceed parent depth", ErrInvalidEvidence)
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
		if control != nil && m.Role == "unknown" {
			if m.Agent != nil || len(m.Blocks) != 1 || m.Blocks[0].Deferred == nil {
				return ErrInvalidEvidence
			}
			if err := control.unknownMessage(); err != nil {
				return err
			}
		} else if err := validateMessageHeader(m); err != nil {
			return err
		} else if err := c.message(MessageHeader{Role: m.Role, Agent: m.Agent}); err != nil {
			return err
		}
		if len(m.Blocks) == 0 {
			return ErrInvalidEvidence
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
	control := newReadControlCost(ctx)
	if err := control.page(p); err != nil {
		return err
	}
	f := p.Frozen
	if err := c.add(c.strings(p.BlockKind, p.CallID, p.ToolName, p.ExchangeID, p.Text, p.Parent.CaptureRunID, p.Parent.ManualCaptureID, f.EnvironmentID, f.EnvironmentDigest, f.ClientEndpointID, f.ProtocolPlanID, f.RouteID, string(p.Mode)), 0); err != nil {
		return err
	}
	if err := c.evidence(p.ProtocolEvidence); err != nil {
		return err
	}
	if err := protocolcore.ValidateProtocolEvidence(p.ProtocolEvidence); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidEvidence, err)
	}
	if err := validateVisible(ctx, c, p.Messages, p.Blocks, p.Mode, p.ExchangeID, l, control, canonicalBlockParentDepth); err != nil {
		return err
	}
	return countPageWire(ctx, control.wireLimit(l.CanonicalBytes), p)
}
