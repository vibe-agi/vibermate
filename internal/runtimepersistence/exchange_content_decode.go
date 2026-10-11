package runtimepersistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"hash"
	"io"
	"math"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
)

type storedDecodeLimits struct{ CanonicalBytes, RetainedBytes, RetainedStructureBytes, WorkspaceBytes uint64 }

// Logical Source currencies count each decoded field once, independently of
// the parser's capacity/workspace reservations below. Zero structure denotes
// a snapshot taken before the first authenticated pass has measured a block.
type storedBlockLogicalCost struct{ RetainedBytes, StructureBytes uint64 }

// Absolute overlapping snapshot, NOT an additive delta. The caller accounts
// physical rows separately and keeps separate charges for earlier outputs
// retained across subsequent calls. Failure never transfers provisional output.
type storedDecodeCost struct {
	RetainedBytes, RetainedStructureBytes, WorkspaceBytes uint64
	LogicalCost                                           storedBlockLogicalCost
}
type storedDecodeInput struct {
	Reader io.Reader
	Size   uint64
	Digest [sha256.Size]byte
	Slots  int
}
type storedDecodeOpen func(context.Context) (storedDecodeInput, error)
type storedBlockFacts struct {
	Shape                                    exchangecontent.BlockShape
	CanonicalBytes, TextBytes, ArgumentBytes uint64
	LogicalCost                              storedBlockLogicalCost
	MetadataBytes                            uint64
	RawUTF8, RawObject                       bool
	ArgumentDepth                            uint16
	PrettyUpper                              uint64
	FiniteNumbers                            bool
}
type storedBodyRange struct {
	Facts       storedBlockFacts
	Offset, End uint64
	Arguments   bool
	Bytes       []byte
}

// Concrete storage plus conservative fixed dynamic allowances: two copies of
// every bounded semantic string, 2*512 bytes for hash implementations, 32*16
// key bytes, 1024 for transient syntax errors/closures, and 4096 for allocation
// size-class rounding. This is an allocation reservation, not an RSS estimate.
// Input/scratch/inline scanner arrays are included in the measured decoder.
// Heap scanner growth and returned storage are charged separately. Loader-owned
// physical/compressed rows, decoder workspace and SQL state are caller-owned.
const storedDecodeSemanticBytes = 32 + 32 + 512 + 256 + 256 + 3*512 + 71

var storedDecodeFixedWorkspace = uint64(reflect.TypeFor[storedBlockDecoder]().Size()+
	2*reflect.TypeFor[storedBlockPass]().Size()+
	2*reflect.TypeFor[storedPassFacts]().Size()+
	reflect.TypeFor[storedDecodeResult]().Size()+
	reflect.TypeFor[exchangecontent.Block]().Size()+
	reflect.TypeFor[exchangecontent.AgentContext]().Size()) +
	2*uint64(reflect.TypeFor[storedRawPresentation]().Size()) +
	2*storedDecodeSemanticBytes + 2*512 + 32*16 + 1024 + 4096

const (
	decodedKind = iota
	decodedAvailability
	decodedText
	decodedOriginalSize
	decodedCallID
	decodedToolName
	decodedToolNamespace
	decodedArguments
	decodedToolError
	decodedProviderSource
	decodedProviderKind
	decodedFingerprint
	decodedAgent
	decodedAgentName
	decodedAuthor
	decodedRecipient
	decodedFields
)

var storedBlockFieldNames = [...]string{"kind", "availability", "text", "originalSize", "callId", "toolName", "toolNamespace", "arguments", "toolError", "providerSource", "providerKind", "fingerprint", "agent"}

// A decoder is one sequential operation's reusable workspace. It is not safe
// for concurrent use. Discard it after the owning operation drains; no pool.
type storedBlockDecoder struct {
	limits  storedDecodeLimits
	reserve func(storedDecodeCost) error
	cost    storedDecodeCost
	scan    storedJSONScanner
	input   [4096]byte
	scratch [512]byte
}

func newStoredBlockDecoder(limits storedDecodeLimits, reserve func(storedDecodeCost) error) (*storedBlockDecoder, error) {
	if limits.CanonicalBytes == 0 || limits.CanonicalBytes > math.MaxInt64 || limits.RetainedBytes == 0 || limits.RetainedBytes > math.MaxInt64 || limits.RetainedStructureBytes == 0 || limits.WorkspaceBytes < storedDecodeFixedWorkspace || reserve == nil {
		return nil, exchangecontent.ErrInvalidEvidence
	}
	cost := storedDecodeCost{WorkspaceBytes: storedDecodeFixedWorkspace}
	if err := reserve(cost); err != nil {
		return nil, err
	}
	d := &storedBlockDecoder{limits: limits, reserve: reserve, cost: cost}
	d.scan.grow = func(old, next int) error {
		// The inline array is already charged; heap replacement overlaps both stores.
		if old == len(d.scan.inline) {
			old = 0
		}
		cost := d.cost
		cost.WorkspaceBytes = storedDecodeFixedWorkspace + uint64(old+next)*uint64(strconv.IntSize/8)
		return d.admit(cost)
	}
	return d, nil
}

func (d *storedBlockDecoder) admit(cost storedDecodeCost) error {
	if cost.RetainedBytes > d.limits.RetainedBytes || cost.RetainedStructureBytes > d.limits.RetainedStructureBytes || cost.WorkspaceBytes > d.limits.WorkspaceBytes {
		return exchangecontent.ErrInvalidEvidence
	}
	if err := d.reserve(cost); err != nil {
		return err
	}
	d.cost = cost
	return nil
}

func (d *storedBlockDecoder) full(ctx context.Context, open storedDecodeOpen, mode environment.ContentRecordingMode) (exchangecontent.Block, error) {
	result, err := d.decode(ctx, open, mode, storedDecodeRequest{full: true})
	if err != nil {
		return exchangecontent.Block{}, err
	}
	b := result.block()
	if err := b.RetainedShape().Validate(mode); err != nil {
		return exchangecontent.Block{}, err
	}
	return b, nil
}

func (result *storedDecodeResult) block() exchangecontent.Block {
	b := exchangecontent.Block{Kind: result.strings[decodedKind].String(), Availability: exchangecontent.Availability(result.strings[decodedAvailability].String()), Text: result.strings[decodedText].String(), OriginalSize: result.facts.Shape.OriginalSize, CallID: result.strings[decodedCallID].String(), ToolName: result.strings[decodedToolName].String(), ToolNamespace: result.strings[decodedToolNamespace].String(), Arguments: result.arguments, ToolError: result.facts.Shape.ToolError, ProviderSource: result.strings[decodedProviderSource].String(), ProviderKind: result.strings[decodedProviderKind].String(), Fingerprint: result.strings[decodedFingerprint].String()}
	if result.facts.Shape.HasAgent {
		b.Agent = &exchangecontent.AgentContext{AgentName: result.strings[decodedAgentName].String(), Author: result.strings[decodedAuthor].String(), Recipient: result.strings[decodedRecipient].String()}
	}
	return b
}

type storedDetailPage struct {
	Body      storedBodyRange
	Metadata  *exchangecontent.BlockPageMetadata
	Canonical bool
}

func (d *storedBlockDecoder) detail(ctx context.Context, open storedDecodeOpen, mode environment.ContentRecordingMode, kind string, offset uint64) (storedDetailPage, error) {
	if offset > exchangecontent.MaxCanonicalBlockBytes {
		return storedDetailPage{}, exchangecontent.ErrInvalidEvidence
	}
	r, err := d.decode(ctx, open, mode, storedDecodeRequest{selected: true, offset: offset, end: offset + exchangecontent.PageBodyBytes, pageKind: kind})
	if err != nil {
		return storedDetailPage{}, err
	}
	page := storedDetailPage{Canonical: r.canonical, Body: storedBodyRange{Facts: r.facts, Offset: offset, End: r.end, Arguments: r.selectedField == decodedArguments, Bytes: r.body}}
	if r.metadata {
		m := &exchangecontent.BlockPageMetadata{Kind: r.strings[decodedKind].String(), Availability: exchangecontent.Availability(r.strings[decodedAvailability].String()), OriginalSize: r.facts.Shape.OriginalSize, CallID: r.strings[decodedCallID].String(), ToolName: r.strings[decodedToolName].String(), ToolNamespace: r.strings[decodedToolNamespace].String(), ToolError: r.facts.Shape.ToolError, ProviderSource: r.strings[decodedProviderSource].String(), ProviderKind: r.strings[decodedProviderKind].String(), Fingerprint: r.strings[decodedFingerprint].String(), TextBytes: r.facts.TextBytes, ArgumentBytes: r.facts.ArgumentBytes}
		if r.facts.Shape.HasAgent {
			m.Agent = &exchangecontent.AgentContext{AgentName: r.strings[decodedAgentName].String(), Author: r.strings[decodedAuthor].String(), Recipient: r.strings[decodedRecipient].String()}
		}
		page.Metadata = m
	}
	return page, nil
}

type storedCanonicalRangeReader struct {
	io.Reader
	offset, end, position uint64
	dst                   []byte
}

func (r *storedCanonicalRangeReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	start, end := max(r.position, r.offset), min(r.position+uint64(n), r.end)
	if start < end {
		copy(r.dst[start-r.offset:end-r.offset], p[start-r.position:end-r.position])
	}
	r.position += uint64(n)
	return n, err
}
func (d *storedBlockDecoder) count(ctx context.Context, open storedDecodeOpen, mode environment.ContentRecordingMode) (storedBlockFacts, error) {
	result, err := d.decode(ctx, open, mode, storedDecodeRequest{})
	if err != nil {
		return storedBlockFacts{}, err
	}
	return result.facts, nil
}
func (d *storedBlockDecoder) selectBody(ctx context.Context, open storedDecodeOpen, mode environment.ContentRecordingMode, offset, maximum uint64) (storedBodyRange, error) {
	if maximum == 0 || offset > math.MaxInt64 || maximum > math.MaxInt64-offset {
		return storedBodyRange{}, exchangecontent.ErrInvalidEvidence
	}
	result, err := d.decode(ctx, open, mode, storedDecodeRequest{selected: true, offset: offset, end: offset + maximum})
	if err != nil {
		return storedBodyRange{}, err
	}
	return storedBodyRange{Facts: result.facts, Offset: offset, End: result.end, Arguments: result.facts.ArgumentBytes != 0, Bytes: result.body}, nil
}

type storedDecodeRequest struct {
	full, selected      bool
	offset, end         uint64
	pageKind            string
	metadata, canonical bool
	inline              bool
	inlineBudget        uint64
	inlineEnvelope      uint16
}
type storedDecodeResult struct {
	facts storedBlockFacts
	// The result remains at one address; non-zero builders are never copied.
	// After pass 2 they are read with String only, never written/reset/reused.
	strings             [decodedFields]strings.Builder
	lengths             [decodedFields]uint64
	arguments, body     []byte
	end                 uint64
	selectedField       int
	metadata, canonical bool
	deferred            bool
	inlineDebit         uint64
}
type storedPassFacts struct {
	facts                        storedBlockFacts
	lengths                      [decodedFields]uint64
	textEnd, rawEnd              uint64
	textStart, rawStart, rawUTF8 bool
	bodyWire                     uint64
}

func (d *storedBlockDecoder) decode(ctx context.Context, open storedDecodeOpen, mode environment.ContentRecordingMode, request storedDecodeRequest) (*storedDecodeResult, error) {
	var zero *storedDecodeResult
	if ctx == nil || open == nil {
		return zero, exchangecontent.ErrInvalidEvidence
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	cost := storedDecodeCost{WorkspaceBytes: storedDecodeFixedWorkspace}
	if cap(d.scan.parseState) > len(d.scan.inline) {
		cost.WorkspaceBytes += uint64(cap(d.scan.parseState)) * uint64(strconv.IntSize/8)
	}
	if err := d.admit(cost); err != nil {
		return zero, err
	}
	input, err := open(ctx)
	if err != nil {
		return zero, err
	}
	if !d.validInput(input) {
		return zero, exchangecontent.ErrInvalidEvidence
	}
	first, err := d.pass(ctx, input, mode, request, nil)
	// Drop the opener's reader/physical row before allocating/reopening pass 2.
	input.Reader = nil
	if err != nil {
		return zero, err
	}
	result := &storedDecodeResult{facts: first.facts, lengths: first.lengths}
	if request.inline {
		result.inlineDebit = first.facts.inlineDebit()
		request.full = first.facts.inlineEligible(request.inlineEnvelope) && result.inlineDebit <= request.inlineBudget
		result.deferred = !request.full
	}
	result.selectedField = decodedText
	if first.facts.ArgumentBytes != 0 {
		result.selectedField = decodedArguments
	}
	if request.pageKind != "" {
		if request.pageKind == "detail" {
			if first.facts.TextBytes > 0 {
				result.selectedField = decodedText
			}
			request.canonical = first.facts.TextBytes == 0 && first.facts.ArgumentBytes == 0 || !first.facts.RawUTF8
		}
		if request.pageKind == "text" {
			result.selectedField = decodedText
		}
		if request.pageKind == "arguments" {
			result.selectedField = decodedArguments
		}
		if request.pageKind == "block_bytes" {
			request.canonical = true
		}
		request.metadata = !request.canonical && first.facts.MetadataBytes <= exchangecontent.PageContentBytes
		result.metadata, result.canonical = request.metadata, request.canonical
		if request.canonical {
			result.selectedField = -1
		}
	}
	cost = d.cost
	cost.LogicalCost = first.facts.LogicalCost
	cost.RetainedStructureBytes = uint64(reflect.TypeFor[storedBlockFacts]().Size())
	if request.full {
		cost.RetainedStructureBytes = uint64(reflect.TypeFor[exchangecontent.Block]().Size())
		for field, n := range first.lengths {
			// Grow is called only on an empty builder. Reserve 2*n as a
			// conservative bound on its rounded backing capacity (tiny size
			// classes are covered by fixed workspace). String makes no copy.
			multiplier := uint64(2)
			if field == decodedArguments {
				multiplier = 1
			}
			if n > uint64(math.MaxInt) || n > (d.limits.RetainedBytes-cost.RetainedBytes)/multiplier {
				return zero, exchangecontent.ErrInvalidEvidence
			}
			cost.RetainedBytes += n * multiplier
		}
		if first.facts.Shape.HasAgent {
			cost.RetainedStructureBytes += uint64(reflect.TypeFor[exchangecontent.AgentContext]().Size())
		}
	} else {
		s := first.facts.Shape
		cost.RetainedBytes = uint64(len(s.Kind) + len(s.Availability) + len(s.CallID) + len(s.ToolName) + len(s.ToolNamespace) + len(s.Agent.AgentName) + len(s.Agent.Author) + len(s.Agent.Recipient))
		if request.selected {
			cost.RetainedStructureBytes = uint64(reflect.TypeFor[storedBodyRange]().Size())
			total, end, start, valid := first.facts.TextBytes, first.textEnd, first.textStart, true
			if result.selectedField == decodedArguments {
				total, end, start, valid = first.facts.ArgumentBytes, first.rawEnd, first.rawStart, first.rawUTF8
			}
			if request.canonical {
				total, end, start, valid = input.Size, min(input.Size, request.end), true, true
			}
			if !valid || !start || request.offset >= total || end <= request.offset {
				return zero, exchangecontent.ErrInvalidEvidence
			}
			result.end = end
			n := end - request.offset
			if n > uint64(math.MaxInt) || cost.RetainedBytes > d.limits.RetainedBytes || n > d.limits.RetainedBytes-cost.RetainedBytes {
				return zero, exchangecontent.ErrInvalidEvidence
			}
			multiplier := uint64(1)
			if request.canonical {
				multiplier = 6
			}
			if n > (d.limits.RetainedBytes-cost.RetainedBytes)/multiplier {
				return zero, exchangecontent.ErrInvalidEvidence
			}
			cost.RetainedBytes += n * multiplier
		}
	}
	if request.metadata {
		cost.RetainedStructureBytes += uint64(reflect.TypeFor[exchangecontent.BlockPageMetadata]().Size() + reflect.TypeFor[exchangecontent.AgentContext]().Size())
		for field, n := range first.lengths {
			if field == decodedText || field == decodedArguments {
				continue
			}
			if n > (d.limits.RetainedBytes-cost.RetainedBytes)/2 {
				return zero, exchangecontent.ErrInvalidEvidence
			}
			cost.RetainedBytes += 2 * n
		}
	}
	if err := d.admit(cost); err != nil {
		return zero, err
	}
	if request.full || request.metadata {
		for field, n := range first.lengths {
			if request.metadata && (field == decodedText || field == decodedArguments) {
				continue
			}
			if n != 0 {
				if field == decodedArguments {
					result.arguments = make([]byte, int(n))
				} else {
					result.strings[field].Grow(int(n))
				}
			}
		}
	}
	if request.selected {
		result.body = make([]byte, int(result.end-request.offset))
	}
	secondInput, err := open(ctx)
	if err != nil {
		return zero, err
	}
	if !d.validInput(secondInput) || input.Size != secondInput.Size || input.Digest != secondInput.Digest || input.Slots != secondInput.Slots {
		return zero, exchangecontent.ErrInvalidEvidence
	}
	if request.canonical {
		secondInput.Reader = &storedCanonicalRangeReader{Reader: secondInput.Reader, offset: request.offset, end: result.end, dst: result.body}
	}
	second, err := d.pass(ctx, secondInput, mode, request, result)
	if err != nil {
		return zero, err
	}
	if first != second {
		return zero, exchangecontent.ErrInvalidEvidence
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	result.facts = second.facts
	return result, nil
}

func (d *storedBlockDecoder) validInput(input storedDecodeInput) bool {
	slots, err := storedBlockSlotCount(input.Size)
	return input.Reader != nil && input.Size <= d.limits.CanonicalBytes && err == nil && input.Slots == int(slots)
}

type storedBlockPass struct {
	owner    *storedBlockDecoder
	ctx      context.Context
	input    storedDecodeInput
	hash     hash.Hash
	pos, end int
	read     uint64
	pending  error
	request  storedDecodeRequest
	result   *storedDecodeResult
	summary  storedPassFacts
	strings  [decodedFields]string
}

func (d *storedBlockDecoder) pass(ctx context.Context, input storedDecodeInput, mode environment.ContentRecordingMode, request storedDecodeRequest, result *storedDecodeResult) (storedPassFacts, error) {
	p := storedBlockPass{owner: d, ctx: ctx, input: input, hash: sha256.New(), request: request, result: result}
	p.summary.rawUTF8 = true
	if err := p.block(); err != nil {
		if err == io.EOF {
			return storedPassFacts{}, exchangecontent.ErrInvalidEvidence
		}
		return storedPassFacts{}, err
	}
	// Even a read that supplies the final bytes can carry a non-EOF error. Peek
	// drains pending errors and forces A1's terminal authentication read.
	if _, err := p.peek(); err != io.EOF {
		if err == nil {
			err = exchangecontent.ErrInvalidEvidence
		}
		return storedPassFacts{}, err
	}
	if p.read != input.Size || !bytes.Equal(p.hash.Sum(nil), input.Digest[:]) {
		return storedPassFacts{}, exchangecontent.ErrInvalidEvidence
	}
	if err := ctx.Err(); err != nil {
		return storedPassFacts{}, err
	}
	s := &p.summary.facts.Shape
	s.Kind = p.strings[decodedKind]
	s.Availability = exchangecontent.Availability(p.strings[decodedAvailability])
	s.CallID = p.strings[decodedCallID]
	s.ToolName = p.strings[decodedToolName]
	s.ToolNamespace = p.strings[decodedToolNamespace]
	s.Agent = exchangecontent.AgentContext{AgentName: p.strings[decodedAgentName], Author: p.strings[decodedAuthor], Recipient: p.strings[decodedRecipient]}
	lengths := p.summary.lengths
	s.HasText = lengths[decodedText] != 0
	s.HasArguments = lengths[decodedArguments] != 0
	s.HasProviderSource = lengths[decodedProviderSource] != 0
	s.HasProviderKind = lengths[decodedProviderKind] != 0
	s.HasFingerprint = lengths[decodedFingerprint] != 0
	fingerprint := p.strings[decodedFingerprint]
	s.FingerprintValid = lengths[decodedFingerprint] == 71 && len(fingerprint) == 71 && fingerprint[:7] == "sha256:"
	if s.FingerprintValid {
		for _, c := range fingerprint[7:] {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				s.FingerprintValid = false
			}
		}
	}
	if err := s.Validate(mode); err != nil {
		return storedPassFacts{}, err
	}
	p.summary.facts.CanonicalBytes = p.read
	p.summary.facts.TextBytes = lengths[decodedText]
	p.summary.facts.ArgumentBytes = lengths[decodedArguments]
	p.summary.facts.RawUTF8 = p.summary.rawUTF8
	p.summary.facts.MetadataBytes = p.read - p.summary.bodyWire + uint64(len(`,"textBytes":`)+len(strconv.FormatUint(lengths[decodedText], 10))+len(`,"argumentBytes":`)+len(strconv.FormatUint(lengths[decodedArguments], 10)))
	logical := storedBlockLogicalCost{StructureBytes: uint64(reflect.TypeFor[exchangecontent.Block]().Size())}
	for _, n := range lengths {
		if n > math.MaxInt64-logical.RetainedBytes {
			return storedPassFacts{}, exchangecontent.ErrInvalidEvidence
		}
		logical.RetainedBytes += n
	}
	if s.HasAgent {
		logical.StructureBytes += uint64(reflect.TypeFor[exchangecontent.AgentContext]().Size())
	}
	p.summary.facts.LogicalCost = logical
	if request.end >= lengths[decodedText] {
		p.summary.textEnd = lengths[decodedText]
	}
	if request.end >= lengths[decodedArguments] {
		p.summary.rawEnd = lengths[decodedArguments]
	}
	return p.summary, nil
}

func (p *storedBlockPass) peek() (byte, error) {
	if p.pos < p.end {
		return p.owner.input[p.pos], nil
	}
	if p.pending != nil {
		return 0, p.pending
	}
	if err := p.ctx.Err(); err != nil {
		return 0, err
	}
	for attempts := 0; attempts < 100; attempts++ {
		n, err := p.input.Reader.Read(p.owner.input[:])
		if n < 0 || n > len(p.owner.input) {
			return 0, exchangecontent.ErrInvalidEvidence
		}
		p.pos = 0
		p.end = n
		p.pending = err
		if n > 0 {
			if uint64(n) > p.input.Size-p.read {
				return 0, exchangecontent.ErrInvalidEvidence
			}
			p.read += uint64(n)
			_, _ = p.hash.Write(p.owner.input[:n])
			return p.owner.input[0], nil
		}
		if err != nil {
			return 0, err
		}
		if err := p.ctx.Err(); err != nil {
			return 0, err
		}
	}
	return 0, io.ErrNoProgress
}
func (p *storedBlockPass) take() (byte, error) {
	c, err := p.peek()
	if err != nil {
		if err == io.EOF {
			err = exchangecontent.ErrInvalidEvidence
		}
		return 0, err
	}
	p.pos++
	return c, nil
}
func (p *storedBlockPass) literal(s string) error {
	for i := range len(s) {
		c, err := p.take()
		if err != nil {
			return err
		}
		if c != s[i] {
			return exchangecontent.ErrInvalidEvidence
		}
	}
	return nil
}

func (p *storedBlockPass) key() (string, error) {
	if err := p.literal("\""); err != nil {
		return "", err
	}
	var key [16]byte
	for n := 0; n < len(key); n++ {
		c, err := p.take()
		if err != nil {
			return "", err
		}
		if c == '"' {
			if err := p.literal(":"); err != nil {
				return "", err
			}
			return string(key[:n]), nil
		}
		key[n] = c
	}
	return "", exchangecontent.ErrInvalidEvidence
}
func (p *storedBlockPass) block() error {
	if err := p.literal("{"); err != nil {
		return err
	}
	rank, seen := -1, uint32(0)
	for {
		key, err := p.key()
		if err != nil {
			return err
		}
		field := -1
		for i, name := range storedBlockFieldNames {
			if key == name {
				field = i
				break
			}
		}
		if field <= rank {
			return exchangecontent.ErrInvalidEvidence
		}
		rank = field
		seen |= 1 << field
		valueStart := p.read - uint64(p.end-p.pos)
		switch field {
		case decodedOriginalSize:
			n, err := p.integer()
			if err != nil {
				return err
			}
			p.summary.facts.Shape.OriginalSize = n
		case decodedToolError:
			if err := p.literal("true"); err != nil {
				return err
			}
			p.summary.facts.Shape.ToolError = true
		case decodedArguments:
			if err := p.raw(); err != nil {
				return err
			}
		case decodedAgent:
			if err := p.agent(); err != nil {
				return err
			}
		default:
			if err := p.stringValue(field); err != nil {
				return err
			}
			if field != decodedKind && field != decodedAvailability && p.summary.lengths[field] == 0 {
				return exchangecontent.ErrInvalidEvidence
			}
		}
		if field == decodedText || field == decodedArguments {
			p.summary.bodyWire += p.read - uint64(p.end-p.pos) - valueStart + uint64(len(key)+4)
		}
		c, err := p.take()
		if err != nil {
			return err
		}
		if c == '}' {
			break
		}
		if c != ',' {
			return exchangecontent.ErrInvalidEvidence
		}
	}
	if seen&(1<<decodedKind|1<<decodedAvailability|1<<decodedOriginalSize) != (1<<decodedKind | 1<<decodedAvailability | 1<<decodedOriginalSize) {
		return exchangecontent.ErrInvalidEvidence
	}
	return nil
}
func (p *storedBlockPass) agent() error {
	if err := p.literal("{"); err != nil {
		return err
	}
	p.summary.facts.Shape.HasAgent = true
	c, err := p.peek()
	if err != nil {
		return err
	}
	if c == '}' {
		p.pos++
		return nil
	}
	rank := decodedAgent
	for {
		key, err := p.key()
		if err != nil {
			return err
		}
		field := -1
		switch key {
		case "agentName":
			field = decodedAgentName
		case "author":
			field = decodedAuthor
		case "recipient":
			field = decodedRecipient
		}
		if field <= rank {
			return exchangecontent.ErrInvalidEvidence
		}
		rank = field
		if err := p.stringValue(field); err != nil {
			return err
		}
		if p.summary.lengths[field] == 0 {
			return exchangecontent.ErrInvalidEvidence
		}
		c, err := p.take()
		if err != nil {
			return err
		}
		if c == '}' {
			return nil
		}
		if c != ',' {
			return exchangecontent.ErrInvalidEvidence
		}
	}
}
func (p *storedBlockPass) integer() (int, error) {
	c, err := p.take()
	if err != nil {
		return 0, err
	}
	if c < '0' || c > '9' {
		return 0, exchangecontent.ErrInvalidEvidence
	}
	n := int(c - '0')
	for {
		c, err = p.peek()
		if err != nil {
			return 0, err
		}
		if c < '0' || c > '9' {
			return n, nil
		}
		if n == 0 || n > (math.MaxInt-int(c-'0'))/10 {
			return 0, exchangecontent.ErrInvalidEvidence
		}
		p.pos++
		n = n*10 + int(c-'0')
	}
}

// Exact recognition of json.Marshal's ordinary-string image. Alternate escape
// spellings, surrogate escapes and invalid UTF8 cannot survive decode/re-marshal
// equality. Only controls without short forms, HTML and U+2028/9 use \u escapes.
func (p *storedBlockPass) stringValue(field int) error {
	if err := p.literal("\""); err != nil {
		return err
	}
	captured := 0
	limit := 0
	switch field {
	case decodedKind, decodedAvailability:
		limit = 32
	case decodedCallID, decodedAgentName, decodedAuthor, decodedRecipient:
		limit = 512
	case decodedToolName, decodedToolNamespace:
		limit = 256
	case decodedFingerprint:
		limit = 71
	}
	for {
		c, err := p.take()
		if err != nil {
			return err
		}
		if c == '"' {
			break
		}
		var unit [4]byte
		n := 1
		unit[0] = c
		if c == '\\' {
			e, err := p.take()
			if err != nil {
				return err
			}
			switch e {
			case '"', '\\':
				unit[0] = e
			case 'b':
				unit[0] = '\b'
			case 'f':
				unit[0] = '\f'
			case 'n':
				unit[0] = '\n'
			case 'r':
				unit[0] = '\r'
			case 't':
				unit[0] = '\t'
			case 'u':
				value := rune(0)
				for range 4 {
					h, err := p.take()
					if err != nil {
						return err
					}
					value *= 16
					switch {
					case h >= '0' && h <= '9':
						value += rune(h - '0')
					case h >= 'a' && h <= 'f':
						value += rune(h - 'a' + 10)
					default:
						return exchangecontent.ErrInvalidEvidence
					}
				}
				if !((value < 0x20 && value != 8 && value != 9 && value != 10 && value != 12 && value != 13) || value == '<' || value == '>' || value == '&' || value == 0x2028 || value == 0x2029) {
					return exchangecontent.ErrInvalidEvidence
				}
				n = utf8.EncodeRune(unit[:], value)
			default:
				return exchangecontent.ErrInvalidEvidence
			}
		} else if c < 0x20 || c == '<' || c == '>' || c == '&' {
			return exchangecontent.ErrInvalidEvidence
		} else if c >= utf8.RuneSelf {
			for !utf8.FullRune(unit[:n]) && n < len(unit) {
				c, err := p.take()
				if err != nil {
					return err
				}
				unit[n] = c
				n++
			}
			r, size := utf8.DecodeRune(unit[:n])
			if (r == utf8.RuneError && size == 1) || size != n || r == 0x2028 || r == 0x2029 {
				return exchangecontent.ErrInvalidEvidence
			}
		}
		if limit > 0 {
			if field != decodedFingerprint && p.summary.lengths[field]+uint64(n) > uint64(limit) {
				return exchangecontent.ErrInvalidEvidence
			}
			if captured < limit {
				captured += copy(p.owner.scratch[captured:limit], unit[:n])
			}
		}
		if err := p.emit(field, unit[:n]); err != nil {
			return err
		}
	}
	if limit > 0 {
		p.strings[field] = string(p.owner.scratch[:captured])
	}
	return nil
}

func (p *storedBlockPass) emit(field int, data []byte) error {
	position := p.summary.lengths[field]
	if uint64(len(data)) > p.owner.limits.CanonicalBytes-position {
		return exchangecontent.ErrInvalidEvidence
	}
	if p.result != nil && (p.request.full || p.request.metadata && field != decodedText && field != decodedArguments) {
		limit := p.result.lengths[field]
		if position > limit || uint64(len(data)) > limit-position {
			return exchangecontent.ErrInvalidEvidence
		}
		if field == decodedArguments {
			copy(p.result.arguments[int(position):], data)
		} else {
			dst := &p.result.strings[field]
			if uint64(dst.Len()) != position || len(data) > dst.Cap()-dst.Len() {
				return exchangecontent.ErrInvalidEvidence
			}
			_, _ = dst.Write(data)
		}
	}
	if p.request.selected && (field == decodedText || field == decodedArguments) {
		if utf8.RuneStart(data[0]) {
			if position == p.request.offset {
				if field == decodedText {
					p.summary.textStart = true
				} else {
					p.summary.rawStart = true
				}
			}
			if position <= p.request.end {
				if field == decodedText {
					p.summary.textEnd = position
				} else {
					p.summary.rawEnd = position
				}
			}
		}
		if p.result != nil && field == p.result.selectedField {
			start, end := max(position, p.request.offset), min(position+uint64(len(data)), p.result.end)
			if start < end {
				dst := p.result.body
				if start-p.request.offset > uint64(len(dst)) || end-start > uint64(len(dst))-(start-p.request.offset) {
					return exchangecontent.ErrInvalidEvidence
				}
				copy(dst[int(start-p.request.offset):], data[int(start-position):int(end-position)])
			}
		}
	}
	p.summary.lengths[field] += uint64(len(data))
	return nil
}

func (p *storedBlockPass) raw() error {
	first, err := p.peek()
	if err != nil {
		return err
	}
	p.summary.facts.RawObject = first == '{'
	s := &p.owner.scan
	s.depthLimit = exchangecontent.MaxArgumentJSONDepth
	s.reset()
	presentation := storedRawPresentation{upper: 1, finite: true}
	inString, escaped := false, false
	var previous [2]byte
	var carry [4]byte
	carryN := 0
	for {
		c, err := p.peek()
		if err != nil {
			return err
		}
		op := s.step(s, c)
		// scanEnd is deliberately delayed until the byte AFTER a scalar; a closed
		// Block delimiter belongs to the caller, not to the raw value's spelling.
		if op == scanEnd && (c == ',' || c == '}') {
			break
		}
		if s.err != nil {
			if _, syntax := s.err.(*storedJSONSyntaxError); syntax {
				return exchangecontent.ErrInvalidEvidence
			}
			return s.err
		}
		if !inString && isSpace(c) {
			return exchangecontent.ErrInvalidEvidence
		}
		if c == '<' || c == '>' || c == '&' || (previous == [2]byte{0xe2, 0x80} && (c == 0xa8 || c == 0xa9)) {
			return exchangecontent.ErrInvalidEvidence
		}
		previous = [2]byte{previous[1], c}
		presentation.byte(c, inString, escaped)
		if inString {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
		} else if c == '"' {
			inString = true
		}
		p.pos++
		s.bytes++
		// UTF8 is a range-presentation fact only, never a RawMessage syntax rule.
		if p.summary.rawUTF8 {
			carry[carryN] = c
			carryN++
			if utf8.FullRune(carry[:carryN]) {
				r, n := utf8.DecodeRune(carry[:carryN])
				if r == utf8.RuneError && n == 1 {
					p.summary.rawUTF8 = false
				}
				carryN = 0
			}
		}
		one := [1]byte{c}
		if err := p.emit(decodedArguments, one[:]); err != nil {
			return err
		}
		if s.endTop {
			break
		}
	}
	if carryN != 0 {
		p.summary.rawUTF8 = false
	}
	if p.summary.lengths[decodedArguments] == 0 {
		return exchangecontent.ErrInvalidEvidence
	}
	presentation.finishNumber()
	p.summary.facts.ArgumentDepth = presentation.peak
	p.summary.facts.PrettyUpper = presentation.upper
	p.summary.facts.FiniteNumbers = presentation.finite
	return nil
}

// These are presentation facts collected alongside the existing grammar scan,
// never a second parser or a retained-depth restriction. Arithmetic saturates
// at the first value that cannot fit the aggregate inline allowance.
const storedInlineOverflow = uint64(exchangecontent.PageContentBytes + 1)

func (f storedBlockFacts) inlineEligible(envelope uint16) bool {
	return f.argumentInlineEligible(envelope) && f.inlineDebit() <= exchangecontent.PageContentBytes
}

func (f storedBlockFacts) argumentInlineEligible(envelope uint16) bool {
	return f.ArgumentBytes == 0 || f.RawUTF8 && f.RawObject && f.FiniteNumbers &&
		int(f.ArgumentDepth)+int(envelope) <= exchangecontent.MaxArgumentJSONDepth && f.PrettyUpper <= exchangecontent.PageContentBytes
}
func (f storedBlockFacts) inlineDebit() uint64 {
	n := max(f.ArgumentBytes, f.PrettyUpper)
	if f.CanonicalBytes < f.ArgumentBytes || n > storedInlineOverflow || f.CanonicalBytes-f.ArgumentBytes > storedInlineOverflow-n {
		return storedInlineOverflow
	}
	return f.CanonicalBytes - f.ArgumentBytes + n
}

type storedRawPresentation struct {
	upper                                                        uint64
	depth, peak                                                  uint16
	unicodeLeft                                                  uint8
	finite, number, decimal, exponent, negativeExponent, nonzero bool
	tokenBytes, magnitude                                        uint64
	integerDigits, leadingZeros, explicitExponent                int64
}

// upper accumulates S+N+L+2*C+K+M+W+1. Openers include both
// punctuation bytes; delimiter whitespace uses the raw depth, including empty
// containers. Every duplicate-key occurrence counts. Strings count decoded
// UTF16 units conservatively without allocating a decoded string or tree.

func (p *storedRawPresentation) add(n uint64) {
	if n > storedInlineOverflow-p.upper {
		p.upper = storedInlineOverflow
	} else {
		p.upper += n
	}
}
func (p *storedRawPresentation) finishNumber() {
	if !p.number {
		return
	}
	if !p.decimal && !p.exponent && p.magnitude <= 9007199254740991 {
		p.add(p.tokenBytes + 2)
	} else {
		// Finite VM/web formatting needs at most 17 significant digits plus
		// punctuation/exponent or its longer plain-decimal spelling (<=32B).
		p.add(32)
	}
	e := p.explicitExponent
	if p.negativeExponent {
		e = -e
	}
	// Bounded counters deliberately defer values whose finite proof overflowed.
	if p.nonzero && (p.explicitExponent >= 1000000 || p.integerDigits >= 1000000 || p.leadingZeros >= 1000000 || e+p.integerDigits-p.leadingZeros-1 > 307) {
		p.finite = false
	}
	p.number = false
	p.decimal = false
	p.exponent = false
	p.negativeExponent = false
	p.nonzero = false
	p.tokenBytes = 0
	p.magnitude = 0
	p.integerDigits = 0
	p.leadingZeros = 0
	p.explicitExponent = 0
}
func (p *storedRawPresentation) byte(c byte, inString, escaped bool) {
	if inString {
		if p.unicodeLeft > 0 {
			p.unicodeLeft--
			return
		}
		if escaped {
			if c == 'u' {
				p.add(6)
				p.unicodeLeft = 4
			} else if c == '"' || c == '\\' || c == '/' {
				p.add(2)
			} else {
				p.add(6)
			}
		} else if c != '"' && c != '\\' {
			if c == '/' {
				p.add(2)
			} else if c < 0x80 {
				p.add(1)
			} else if c >= 0xf0 {
				p.add(12)
			} else if c >= 0xc0 {
				p.add(6)
			}
		}
		return
	}
	if (c >= '0' && c <= '9') || c == '-' || p.number && (c == '.' || c == 'e' || c == 'E' || c == '+') {
		p.number = true
		p.tokenBytes = min(p.tokenBytes+1, storedInlineOverflow)
		switch {
		case c == 'e' || c == 'E':
			p.exponent = true
		case c == '.':
			p.decimal = true
		case c == '-' && p.exponent:
			p.negativeExponent = true
		case c >= '0' && c <= '9':
			digit := int64(c - '0')
			if p.exponent {
				p.explicitExponent = min(1000000, p.explicitExponent*10+digit)
			} else {
				if !p.decimal {
					p.integerDigits = min(1000000, p.integerDigits+1)
				}
				if !p.nonzero && digit == 0 {
					p.leadingZeros = min(1000000, p.leadingZeros+1)
				}
				p.nonzero = p.nonzero || digit != 0
				p.magnitude = min(uint64(9007199254740992), p.magnitude*10+uint64(digit))
			}
		}
		return
	}
	p.finishNumber()
	switch c {
	case '"':
		p.add(2)
	case '{', '[':
		p.depth++
		p.peak = max(p.peak, p.depth)
		p.add(3 + 2*uint64(p.depth))
	case '}', ']':
		p.depth--
		p.add(1 + 2*uint64(p.depth))
	case ',':
		p.add(2 + 2*uint64(p.depth))
	case ':':
		p.add(2)
	default:
		p.add(1) // Each byte of true/false/null.
	}
}
