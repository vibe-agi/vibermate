package exchangecontent

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"time"
	"unicode/utf8"
	"unsafe"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

type Part uint8

const (
	SystemPart Part = iota
	RequestPart
	ResponsePart
)

type MessageHeader struct {
	Role       string
	Agent      *AgentContext
	BlockCount int
}
type RecordMetadata struct {
	ExchangeID            string
	Parent                ParentRef
	Frozen                FrozenRef
	Mode                  environment.ContentRecordingMode
	RecordedAt, ExpiresAt time.Time
	Request               RequestMetadata
	Response              *ResponseMetadata
}
type RequestMetadata struct {
	RequestedModel, EffectiveModel string
	MaxOutputTokens                int
	Stream                         bool
	Tools                          []ToolDefinition
	ProtocolEvidence               []protocolcore.ProtocolEvidenceValue
}
type ResponseMetadata struct {
	ID, RequestedModel, EffectiveModel, ReportedModel, StopReason string
	Usage                                                         Usage
	ProtocolEvidence                                              []protocolcore.ProtocolEvidenceValue
	EmptyOutput                                                   bool
}

func metadataFromRecord(r Record) RecordMetadata {
	m := RecordMetadata{ExchangeID: r.ExchangeID, Parent: r.Parent, Frozen: r.Frozen, Mode: r.Mode, RecordedAt: r.RecordedAt, ExpiresAt: r.ExpiresAt, Request: RequestMetadata{RequestedModel: r.Request.RequestedModel, EffectiveModel: r.Request.EffectiveModel, MaxOutputTokens: r.Request.MaxOutputTokens, Stream: r.Request.Stream, Tools: r.Request.Tools, ProtocolEvidence: r.Request.ProtocolEvidence}}
	if r.Response != nil {
		v := r.Response
		m.Response = &ResponseMetadata{ID: v.ID, RequestedModel: v.RequestedModel, EffectiveModel: v.EffectiveModel, ReportedModel: v.ReportedModel, StopReason: v.StopReason, Usage: v.Usage, ProtocolEvidence: v.ProtocolEvidence, EmptyOutput: len(v.Blocks) == 0}
	}
	return m
}

type SourceLimits struct {
	Semantic                                      protocolcore.ResourceLimits
	Scratch                                       protocolcore.ResourceCost
	CanonicalBytes, RetainedBytes, StructureBytes uint64
}

// RecordCost counts projected logical evidence per occurrence. Semantic input
// and transient Scratch are separately bounded and are not added to this cost.
// MaxPhysicalSlots is the greatest sum of per-block slots in one message (or
// System). TranscriptNodes excludes System and excludes an empty terminal.
type RecordCost struct{ CanonicalBytes, RetainedBytes, StructureBytes, TranscriptNodes, MaxLogicalBlockBytes, MaxPhysicalSlots, MaxAgentBytes uint64 }

// Source borrows its input. The caller must keep it immutable until all
// synchronous consumption ends; it creates no goroutines or retained callbacks.
type Source struct {
	limits   SourceLimits
	metadata RecordMetadata
	request  protocolcore.Request
	response *protocolcore.Response
	record   *Record
}
type MessageSource struct {
	source *Source
	part   Part
	index  int
}

func (l SourceLimits) Validate() error {
	if err := l.Semantic.Validate(); err != nil {
		return fmt.Errorf("%w: semantic policy: %w", ErrInvalidEvidence, err)
	}
	for _, n := range []uint64{l.CanonicalBytes, l.RetainedBytes, l.StructureBytes, l.Scratch.PayloadBytes, l.Scratch.StructureBytes, l.Semantic.Request.PayloadBytes, l.Semantic.Request.StructureBytes, l.Semantic.Response.PayloadBytes, l.Semantic.Response.StructureBytes} {
		if n == 0 || n > math.MaxInt64 {
			return fmt.Errorf("%w: source limit must be finite positive and countable", ErrInvalidEvidence)
		}
	}
	return nil
}
func compatibilitySourceLimits() SourceLimits {
	// These adapters preserve legacy count/encoded bounds. Other currencies
	// impose checked representation limits only, not a new production capacity.
	const checkedMaximum = uint64(math.MaxInt64)
	return SourceLimits{Semantic: protocolcore.ResourceLimits{Request: protocolcore.ResourceCost{PayloadBytes: checkedMaximum, StructureBytes: checkedMaximum}, Response: protocolcore.ResourceCost{PayloadBytes: checkedMaximum, StructureBytes: checkedMaximum}}, Scratch: protocolcore.ResourceCost{PayloadBytes: checkedMaximum, StructureBytes: checkedMaximum}, CanonicalBytes: MaxEncodedBytes, RetainedBytes: checkedMaximum, StructureBytes: checkedMaximum}
}
func NewSource(id string, f FrozenRef, p environment.ContentRecordingPolicy, t time.Time, r protocolcore.Request, v *protocolcore.Response, o ...RecordOption) (*Source, error) {
	if err := r.Validate(); err != nil {
		return nil, fmt.Errorf("%w: request: %w", ErrInvalidEvidence, err)
	}
	return NewSourceWithin(compatibilitySourceLimits(), id, f, p, t, r, v, o...)
}
func NewSourceWithin(l SourceLimits, id string, f FrozenRef, p environment.ContentRecordingPolicy, t time.Time, r protocolcore.Request, v *protocolcore.Response, o ...RecordOption) (*Source, error) {
	if err := l.Validate(); err != nil {
		return nil, err
	}
	if !validIdentity(id, MaxExchangeIDBytes) || f.Validate() != nil || p.Validate() != nil || p.Mode == environment.ContentRecordingOff || t.IsZero() {
		return nil, ErrInvalidEvidence
	}
	parent, err := applyRecordOptions(o)
	if err != nil {
		return nil, err
	}
	if err := protocolcore.ValidateRequestWithin(r, l.Semantic); err != nil {
		return nil, fmt.Errorf("%w: request: %w", ErrInvalidEvidence, err)
	}
	if v != nil {
		if err := protocolcore.ValidateResponseWithin(*v, l.Semantic); err != nil {
			return nil, fmt.Errorf("%w: response: %w", ErrInvalidEvidence, err)
		}
	}
	meta := RecordMetadata{ExchangeID: id, Parent: parent, Frozen: f, Mode: p.Mode, RecordedAt: t.UTC(), ExpiresAt: t.UTC().AddDate(0, 0, int(p.RetentionDays)), Request: RequestMetadata{RequestedModel: r.RequestedModel, EffectiveModel: r.EffectiveModel, MaxOutputTokens: r.MaxOutputTokens, Stream: r.Stream, ProtocolEvidence: append([]protocolcore.ProtocolEvidenceValue{}, r.ProtocolEvidence...)}}
	for _, tool := range r.Tools {
		meta.Request.Tools = append(meta.Request.Tools, ToolDefinition{Name: tool.Name})
	}
	for _, ns := range r.ToolNamespaces {
		for _, tool := range ns.Tools {
			meta.Request.Tools = append(meta.Request.Tools, ToolDefinition{Name: tool.Name, Namespace: ns.Name})
		}
	}
	if v != nil {
		meta.Response = &ResponseMetadata{ID: v.ID, RequestedModel: v.RequestedModel, EffectiveModel: v.EffectiveModel, ReportedModel: v.ReportedModel, StopReason: string(v.StopReason), Usage: usageView(v.Usage), ProtocolEvidence: append([]protocolcore.ProtocolEvidenceValue{}, v.ProtocolEvidence...)}
	}
	s := &Source{limits: l, metadata: meta, request: r}
	if v != nil {
		copy := *v
		s.response = &copy
	}
	if meta.Response != nil {
		meta.Response.EmptyOutput = s.message(ResponsePart, 0).Header().BlockCount == 0
	}
	if _, err := s.Measure(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}
func SourceFromRecord(r Record) (*Source, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return SourceFromRecordWithin(compatibilitySourceLimits(), r)
}
func SourceFromRecordWithin(l SourceLimits, r Record) (*Source, error) {
	if err := l.Validate(); err != nil {
		return nil, err
	}
	if !validIdentity(r.ExchangeID, MaxExchangeIDBytes) || r.Parent.Validate() != nil || r.Frozen.Validate() != nil || r.RecordedAt.IsZero() || r.ExpiresAt.IsZero() || !r.ExpiresAt.After(r.RecordedAt) || (r.Mode != environment.ContentRecordingFull && r.Mode != environment.ContentRecordingMetadataOnly) {
		return nil, ErrInvalidEvidence
	}
	if len(r.Request.Messages) == 0 {
		return nil, ErrInvalidEvidence
	}
	if err := r.Request.validateProjectionWithin(r.Mode, false); err != nil {
		return nil, err
	}
	if r.Response != nil {
		if err := r.Response.validateWithin(r.Mode, false); err != nil {
			return nil, err
		}
	}
	s := &Source{limits: l, metadata: metadataFromRecord(r), record: &r}
	s.metadata = copyMetadata(s.metadata)
	if _, err := s.Measure(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}
func copyMetadata(m RecordMetadata) RecordMetadata {
	m.Request.Tools = append([]ToolDefinition{}, m.Request.Tools...)
	m.Request.ProtocolEvidence = append([]protocolcore.ProtocolEvidenceValue{}, m.Request.ProtocolEvidence...)
	if m.Response != nil {
		v := *m.Response
		v.ProtocolEvidence = append([]protocolcore.ProtocolEvidenceValue{}, v.ProtocolEvidence...)
		m.Response = &v
	}
	return m
}
func (s *Source) Metadata() RecordMetadata {
	if s == nil {
		return RecordMetadata{}
	}
	return copyMetadata(s.metadata)
}
func (s *Source) message(p Part, i int) MessageSource {
	return MessageSource{source: s, part: p, index: i}
}
func (s *Source) Walk(ctx context.Context, fn func(Part, int, MessageSource) error) error {
	if s == nil || ctx == nil || fn == nil {
		return ErrInvalidEvidence
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	system, n := len(s.request.System), len(s.request.Messages)
	if s.record != nil {
		system, n = len(s.record.Request.System), len(s.record.Request.Messages)
	}
	emit := func(p Part, i int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return fn(p, i, s.message(p, i))
	}
	if system > 0 {
		if err := emit(SystemPart, 0); err != nil {
			return err
		}
	}
	for i := 0; i < n; i++ {
		if err := emit(RequestPart, i); err != nil {
			return err
		}
	}
	if s.metadata.Response != nil && !s.metadata.Response.EmptyOutput {
		if err := emit(ResponsePart, 0); err != nil {
			return err
		}
	}
	return ctx.Err()
}
func (m MessageSource) valid() bool {
	if m.source == nil {
		return false
	}
	switch m.part {
	case SystemPart:
		return m.index == 0
	case ResponsePart:
		return m.index == 0 && m.source.metadata.Response != nil
	case RequestPart:
		n := len(m.source.request.Messages)
		if m.source.record != nil {
			n = len(m.source.record.Request.Messages)
		}
		return m.index >= 0 && m.index < n
	}
	return false
}
func (m MessageSource) Header() MessageHeader {
	if !m.valid() {
		return MessageHeader{}
	}
	h := MessageHeader{Role: "system"}
	if m.part == ResponsePart {
		h.Role = "assistant"
	}
	if m.source.record != nil {
		switch m.part {
		case SystemPart:
			h.BlockCount = len(m.source.record.Request.System)
		case RequestPart:
			v := m.source.record.Request.Messages[m.index]
			h.Role = v.Role
			h.Agent = cloneAgentContext(v.Agent)
			h.BlockCount = len(v.Blocks)
		case ResponsePart:
			h.BlockCount = len(m.source.record.Response.Blocks)
		}
		return h
	}
	if m.part == RequestPart {
		v := m.source.request.Messages[m.index]
		h.Role = string(v.Role)
		h.Agent = agentContextView(v.Agent)
	}
	_ = m.WalkBlocks(context.Background(), func(Block) error { h.BlockCount++; return nil })
	return h
}
func (m MessageSource) WalkBlocks(ctx context.Context, fn func(Block) error) error {
	if !m.valid() || ctx == nil || fn == nil {
		return ErrInvalidEvidence
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.source
	emit := func(b Block) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return fn(b)
	}
	if s.record != nil {
		var bs []Block
		switch m.part {
		case SystemPart:
			bs = s.record.Request.System
		case RequestPart:
			bs = s.record.Request.Messages[m.index].Blocks
		case ResponsePart:
			bs = s.record.Response.Blocks
		}
		for _, b := range bs {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := s.reserveScratch(2*uint64(len(b.Arguments))+4096, uint64(unsafe.Sizeof(Block{}))+uint64(unsafe.Sizeof(AgentContext{}))+4096); err != nil {
				return err
			}
			b.Arguments = append([]byte(nil), b.Arguments...)
			b.Agent = cloneAgentContext(b.Agent)
			if err := emit(b); err != nil {
				return err
			}
		}
		return ctx.Err()
	}
	var bs []protocolcore.ContentBlock
	switch m.part {
	case SystemPart:
		bs = s.request.System
	case RequestPart:
		bs = s.request.Messages[m.index].Blocks
	case ResponsePart:
		bs = s.response.Blocks
	}
	for _, b := range bs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if b.Kind == protocolcore.BlockProviderExtension {
			if err := s.extensionViews(ctx, b.ProviderExtension, agentContextView(b.Agent), emit); err != nil {
				return err
			}
			continue
		}
		if err := s.reserveBlockScratch(b); err != nil {
			return err
		}
		for _, view := range blockViews([]protocolcore.ContentBlock{b}, s.metadata.Mode == environment.ContentRecordingFull) {
			if err := emit(view); err != nil {
				return err
			}
		}
	}
	if m.part == ResponsePart {
		for _, ext := range s.response.ProviderExtensions {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := s.extensionViews(ctx, ext, nil, emit); err != nil {
				return err
			}
		}
	}
	return ctx.Err()
}

// Scratch allowances are per sequential phase, never cumulative across leaves.
// The sanitizer can grow only header (+11 per >=8 bytes), bearer (+2 per
// >=15), provider key (+6 per >=15), and URL userinfo (+7 per >=11) matches.
// Home replacements shrink. Reserve input plus the replacement byte buffer and
// returned string; no-match text retains the original immutable string.
func sanitizerBound(n uint64) (uint64, error) {
	for _, rule := range [4][2]uint64{{8, 11}, {15, 2}, {15, 6}, {11, 7}} {
		matches := n / rule[0]
		if matches > math.MaxInt64/rule[1] {
			return 0, ErrInvalidEvidence
		}
		delta := matches * rule[1]
		if delta > math.MaxInt64-n {
			return 0, ErrInvalidEvidence
		}
		n += delta
	}
	return n, nil
}
func (s *Source) reserveScratch(payload, structure uint64) error {
	if payload > s.limits.Scratch.PayloadBytes || structure > s.limits.Scratch.StructureBytes {
		return fmt.Errorf("%w: projection scratch exceeded", ErrInvalidEvidence)
	}
	return nil
}
func (s *Source) reserveBlockScratch(b protocolcore.ContentBlock) error {
	if s.metadata.Mode != environment.ContentRecordingFull {
		structure := uint64(unsafe.Sizeof(Block{}))
		if b.Agent != nil {
			structure += uint64(unsafe.Sizeof(AgentContext{}))
		}
		return s.reserveScratch(4096, structure)
	}
	text := b.Text
	if b.Kind == protocolcore.BlockRefusal {
		text = b.Refusal
	}
	if b.Kind == protocolcore.BlockToolResult {
		text = b.ToolResult.Content
	}
	if b.Kind == protocolcore.BlockToolCall && b.ToolCall.EffectiveKind() != protocolcore.ToolKindFunction {
		text = b.ToolCall.Input
	}
	out, err := sanitizerBound(uint64(len(text)))
	if err != nil {
		return err
	}
	payload := uint64(len(text)) + 4*out + 4096
	structure := uint64(unsafe.Sizeof(Block{})) + 4096
	if b.Kind == protocolcore.BlockToolCall && b.ToolCall.EffectiveKind() == protocolcore.ToolKindFunction {
		size := uint64(b.ToolCall.Arguments.ByteLen())
		if err := s.reserveScratch(2*size+4096, 8192); err != nil {
			return err
		}
		raw := b.ToolCall.Arguments.Bytes()
		cost, err := protocolcore.MeasureJSON(raw)
		if err != nil {
			return fmt.Errorf("%w: tool argument resources: %w", ErrInvalidEvidence, err)
		}
		out, err := sanitizerBound(uint64(len(raw)) * 3)
		if err != nil {
			return err
		}
		// MeasureJSON already reserves two owned decoded string occurrences with
		// invalid UTF8 expansion. Add map-redaction copy and worst JSON string HTML
		// escape (6 bytes/byte), encoder grow buffer and returned output overlap.
		payload = cost.PayloadBytes + 4*out + 12*out + 4096
		structure = 2*cost.StructureBytes + 4096
	}
	return s.reserveScratch(payload, structure)
}

// signatureFromReader scans valid JSON with fixed lookahead, retaining only a
// selected signature string. Unknown/plaintext string values are skipped.
func signatureFromReader(input io.Reader) (string, error) {
	r := bufio.NewReaderSize(input, 4096)
	depth := 0
	key, value := false, false
	selected := false
	invalid := false
	signature := ""
	object := false
	for {
		c, err := r.ReadByte()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if c == ' ' || c == '\n' || c == '\r' || c == '\t' {
			continue
		}
		if depth == 0 && c == '{' {
			object = true
		}
		if c == '"' {
			captureKey := object && depth == 1 && key
			captureValue := object && depth == 1 && value && selected
			var token []byte
			if captureKey || captureValue {
				token = append(token, '"')
			}
			escaped := false
			overflow := false
			for {
				b, err := r.ReadByte()
				if err != nil {
					return "", err
				}
				if (captureKey || captureValue) && !overflow {
					token = append(token, b)
					if captureKey && len(token) > 128 {
						token = nil
						overflow = true
					}
				}
				if b == '"' && !escaped {
					break
				}
				if b == '\\' && !escaped {
					escaped = true
				} else {
					escaped = false
				}
			}
			if captureKey {
				var name string
				if !overflow && json.Unmarshal(token, &name) == nil {
					selected = strings.EqualFold(name, "signature")
				} else {
					selected = false
				}
				key = false
			} else if captureValue {
				if json.Unmarshal(token, &signature) != nil {
					invalid = true
				}
				value = false
			}
			continue
		}
		if object && depth == 1 && value {
			if selected && c != 'n' {
				invalid = true
			}
			value = false
		}
		switch c {
		case '{', '[':
			depth++
			if object && depth == 1 {
				key = true
			}
		case '}', ']':
			depth--
		case ':':
			if object && depth == 1 {
				value = true
			}
		case ',':
			if object && depth == 1 {
				key = true
				value = false
				selected = false
			}
		}
	}
	if !object || invalid {
		return "", nil
	}
	return signature, nil
}
func (s *Source) extensionViews(ctx context.Context, ext protocolcore.ProviderExtension, a *AgentContext, emit func(Block) error) error {
	var rawSize int
	err := ext.WalkFragments(func(size int, _ io.Reader) error { rawSize += size; return nil })
	if err != nil {
		return err
	}
	full := s.metadata.Mode == environment.ContentRecordingFull
	// Reading one raw fragment, decoded readable text/opaque signature and the
	// concatenation buffer overlap. Invalid UTF8 strings expand at most 3x;
	// strings.Builder plus sanitizer replacement buffers use the bound above.
	out, err := sanitizerBound(uint64(rawSize) * 3)
	if err != nil {
		return err
	}
	payload := uint64(rawSize)*8 + 4*out + 8192
	if !full {
		payload = uint64(rawSize)*8 + 8192
	}
	if err := s.reserveScratch(payload, uint64(unsafe.Sizeof(Block{}))*2+8192); err != nil {
		return err
	}
	var text strings.Builder
	if full {
		err = ext.WalkFragments(func(size int, r io.Reader) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			b := make([]byte, size)
			if _, err := io.ReadFull(r, b); err != nil {
				return err
			}
			switch ext.Kind() {
			case protocolcore.ProviderExtensionThinking:
				var v struct {
					Thinking string `json:"thinking"`
				}
				if json.Unmarshal(b, &v) == nil {
					text.WriteString(v.Thinking)
				}
			case protocolcore.ProviderExtensionReasoningContent:
				var v string
				if json.Unmarshal(b, &v) == nil {
					text.WriteString(v)
				} else {
					var v struct {
						Text string `json:"text"`
					}
					if json.Unmarshal(b, &v) == nil {
						text.WriteString(v.Text)
					}
				}
			case protocolcore.ProviderExtensionReasoningSummary:
				var v struct {
					Text string `json:"text"`
				}
				if json.Unmarshal(b, &v) == nil {
					text.WriteString(v.Text)
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	hash := sha256.New()
	opaqueSize := 0
	rawOpaque := false
	switch ext.Kind() {
	case protocolcore.ProviderExtensionThinking:
		err = ext.WalkFragments(func(_ int, r io.Reader) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			signature, err := signatureFromReader(r)
			if err != nil {
				return err
			}
			opaqueSize += len(signature)
			_, err = io.WriteString(hash, signature)
			return err
		})
	case protocolcore.ProviderExtensionReasoningEncryptedContent, protocolcore.ProviderExtensionInputImage, protocolcore.ProviderExtensionAgentMessageEncryptedContent, protocolcore.ProviderExtensionRedactedThinking, protocolcore.ProviderExtensionAgentMessageImage, protocolcore.ProviderExtensionAgentMessageFile, protocolcore.ProviderExtensionAgentMessageScreenshot:
		rawOpaque = true
	}
	if err != nil {
		return err
	}
	if opaqueSize == 0 && (rawOpaque || text.Len() == 0 || !full) {
		opaqueSize = rawSize
		var scratch [4096]byte
		err = ext.WalkFragments(func(_ int, r io.Reader) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			_, err := io.CopyBuffer(hash, r, scratch[:])
			return err
		})
		if err != nil {
			return err
		}
	}
	if full && text.Len() > 0 {
		b := Block{Kind: BlockKindReasoning, Availability: AvailabilityRecorded, Text: sanitizeText(text.String()), OriginalSize: text.Len(), ProviderSource: string(ext.Source()), ProviderKind: string(ext.Kind()), Agent: cloneAgentContext(a)}
		if err := emit(b); err != nil {
			return err
		}
	}
	if opaqueSize > 0 {
		b := Block{Kind: string(protocolcore.BlockProviderExtension), Availability: AvailabilityOmitted, OriginalSize: opaqueSize, ProviderSource: string(ext.Source()), ProviderKind: string(ext.Kind()), Fingerprint: fmt.Sprintf("sha256:%x", hash.Sum(nil)), Agent: cloneAgentContext(a)}
		return emit(b)
	}
	return nil
}

type countWriter struct {
	n   uint64
	err error
}

func (w *countWriter) Write(b []byte) (int, error) {
	if uint64(len(b)) > math.MaxUint64-w.n {
		w.err = ErrInvalidEvidence
		return 0, w.err
	}
	w.n += uint64(len(b))
	return len(b), nil
}

// retainedCost is the logical retained-record currency, per occurrence. It is
// independent of physical encoding and parser workspace accounting. Raw JSON
// charges the larger current/durable spelling, covering canonical HTML growth
// without discarding the live input's whitespace. Views reuse these rules.
type retainedCost struct {
	budget      *protocolcore.ResourceBudget
	cost        *RecordCost
	stringError error
}

func newRetainedCost(l SourceLimits) (*retainedCost, error) {
	b, err := protocolcore.NewResourceBudget(protocolcore.ResourceCost{PayloadBytes: l.RetainedBytes, StructureBytes: l.StructureBytes})
	if err != nil {
		return nil, fmt.Errorf("%w: projected record policy: %w", ErrInvalidEvidence, err)
	}
	return &retainedCost{budget: b, cost: &RecordCost{}}, nil
}
func (c *retainedCost) strings(vs ...string) uint64 {
	var n uint64
	for _, v := range vs {
		if !utf8.ValidString(v) {
			c.stringError = fmt.Errorf("%w: retained ordinary string is invalid UTF8", ErrInvalidEvidence)
			return 0
		}
		if uint64(len(v)) > math.MaxInt64-n {
			return uint64(math.MaxInt64) + 1
		}
		n += uint64(len(v))
	}
	return n
}
func (c *retainedCost) add(p, n uint64) error {
	if c.stringError != nil {
		return c.stringError
	}
	if err := c.budget.Reserve(protocolcore.ResourceCost{PayloadBytes: p, StructureBytes: n}); err != nil {
		return fmt.Errorf("%w: projected record budget: %w", ErrInvalidEvidence, err)
	}
	c.cost.RetainedBytes += p
	c.cost.StructureBytes += n
	return nil
}
func (c *retainedCost) agent(a *AgentContext) error {
	if a == nil {
		return nil
	}
	return c.add(c.strings(a.AgentName, a.Author, a.Recipient), uint64(unsafe.Sizeof(*a)))
}
func (c *retainedCost) evidence(es []protocolcore.ProtocolEvidenceValue) error {
	for _, e := range es {
		if err := c.add(c.strings(e.Name, e.Value), uint64(unsafe.Sizeof(e))); err != nil {
			return err
		}
	}
	return nil
}
func (c *retainedCost) metadata(meta RecordMetadata) error {
	f := meta.Frozen
	if err := c.add(c.strings(meta.ExchangeID, meta.Parent.CaptureRunID, meta.Parent.ManualCaptureID, f.EnvironmentID, f.EnvironmentDigest, f.ClientEndpointID, f.ProtocolPlanID, f.RouteID, string(meta.Mode), meta.Request.RequestedModel, meta.Request.EffectiveModel), uint64(unsafe.Sizeof(Record{}))); err != nil {
		return err
	}
	for _, t := range meta.Request.Tools {
		if err := c.add(c.strings(t.Name, t.Namespace), uint64(unsafe.Sizeof(t))); err != nil {
			return err
		}
	}
	if err := c.evidence(meta.Request.ProtocolEvidence); err != nil {
		return err
	}
	if meta.Response != nil {
		v := meta.Response
		if err := c.add(c.strings(v.ID, v.RequestedModel, v.EffectiveModel, v.ReportedModel, v.StopReason), uint64(unsafe.Sizeof(Response{}))); err != nil {
			return err
		}
		for _, u := range usageValues(v.Usage) {
			if err := u.validateEncoding(); err != nil {
				return err
			}
			if err := c.add(c.strings(u.Source), 0); err != nil {
				return err
			}
		}
		if err := c.evidence(v.ProtocolEvidence); err != nil {
			return err
		}
	}
	return nil
}
func (c *retainedCost) message(h MessageHeader) error {
	if err := c.add(c.strings(h.Role), uint64(unsafe.Sizeof(Message{}))); err != nil {
		return err
	}
	return c.agent(h.Agent)
}
func (c *retainedCost) block(ctx context.Context, b Block) error {
	raw, err := canonicalRawBytes(ctx, b.Arguments)
	if err != nil {
		return err
	}
	raw = max(raw, uint64(len(b.Arguments)))
	ordinary := c.strings(b.Kind, string(b.Availability), b.Text, b.CallID, b.ToolName, b.ToolNamespace, b.ProviderSource, b.ProviderKind, b.Fingerprint)
	if ordinary > math.MaxInt64 || raw > math.MaxInt64-ordinary {
		return ErrInvalidEvidence
	}
	if err := c.add(ordinary+raw, uint64(unsafe.Sizeof(Block{}))); err != nil {
		return err
	}
	return c.agent(b.Agent)
}

func (s *Source) Measure(ctx context.Context) (RecordCost, error) {
	var c RecordCost
	if s == nil || ctx == nil {
		return c, ErrInvalidEvidence
	}
	if err := ctx.Err(); err != nil {
		return c, err
	}
	if _, err := s.metadata.RecordedAt.MarshalJSON(); err != nil {
		return c, fmt.Errorf("%w: recorded time: %w", ErrInvalidEvidence, err)
	}
	if _, err := s.metadata.ExpiresAt.MarshalJSON(); err != nil {
		return c, fmt.Errorf("%w: expiry time: %w", ErrInvalidEvidence, err)
	}
	retained, err := newRetainedCost(s.limits)
	if err != nil {
		return c, err
	}
	retained.cost = &c
	agent := func(a *AgentContext) error {
		if a == nil {
			return nil
		}
		cw := countWriter{}
		w := canonicalWriter{sink: &cw}
		w.agent(a)
		w.flush()
		if w.err != nil {
			return w.err
		}
		if cw.n > 4096 {
			cw = countWriter{}
			w = canonicalWriter{sink: &cw, unescapedHTML: true}
			w.agent(a)
			w.flush()
		}
		c.MaxAgentBytes = max(c.MaxAgentBytes, cw.n)
		return w.err
	}
	meta := s.metadata
	if err := retained.metadata(meta); err != nil {
		return c, err
	}
	blockCounter := countWriter{}
	blockEncoder := canonicalWriter{sink: &blockCounter}
	err = s.Walk(ctx, func(p Part, _ int, m MessageSource) error {
		h := m.Header()
		if p != SystemPart {
			c.TranscriptNodes++
		}
		if p == RequestPart {
			if err := retained.message(h); err != nil {
				return err
			}
		}
		if err := agent(h.Agent); err != nil {
			return err
		}
		var slots uint64
		err := m.WalkBlocks(ctx, func(b Block) error {
			if err := b.Validate(meta.Mode); err != nil {
				return err
			}
			envelope := canonicalBlockParentDepth
			if p == RequestPart {
				envelope = canonicalMessageParentDepth
			}
			if argumentDepthOverflow(b.Arguments, envelope) != 0 {
				return fmt.Errorf("%w: canonical arguments exceed parent depth", ErrInvalidEvidence)
			}
			if err := retained.block(ctx, b); err != nil {
				return err
			}
			if err := agent(b.Agent); err != nil {
				return err
			}
			blockCounter.n = 0
			blockEncoder.used = 0
			blockEncoder.block(b)
			blockEncoder.flush()
			if blockEncoder.err != nil {
				return blockEncoder.err
			}
			c.MaxLogicalBlockBytes = max(c.MaxLogicalBlockBytes, blockCounter.n)
			n := uint64(1)
			if blockCounter.n > MaxEncodedBytes {
				const frameBody = MaxEncodedBytes - 56
				n = blockCounter.n / frameBody
				if blockCounter.n%frameBody != 0 {
					n++
				}
			}
			if n > math.MaxUint64-slots {
				return ErrInvalidEvidence
			}
			slots += n
			return nil
		})
		c.MaxPhysicalSlots = max(c.MaxPhysicalSlots, slots)
		return err
	})
	if err != nil {
		return c, err
	}
	cw := countWriter{}
	if err := writeSourceCanonical(ctx, &cw, s); err != nil {
		return c, err
	}
	c.CanonicalBytes = cw.n
	if c.CanonicalBytes > s.limits.CanonicalBytes {
		return c, fmt.Errorf("%w: canonical source bound exceeded", ErrInvalidEvidence)
	}
	return c, nil
}

var _ io.Writer = (*countWriter)(nil)
