package exchangecontent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"reflect"
	"strconv"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

// canonicalWriter retains only fixed scratch. Once a sink fails no later write
// is attempted, including after partial writes with a nil error.
type canonicalWriter struct {
	sink          io.Writer
	scratch       [4096]byte
	used          int
	err           error
	unescapedHTML bool
}

func (w *canonicalWriter) flush() {
	if w.err != nil || w.used == 0 {
		return
	}
	n, err := w.sink.Write(w.scratch[:w.used])
	if n < 0 || n > w.used {
		w.err = fmt.Errorf("invalid writer count %d", n)
		return
	}
	if err != nil {
		w.err = err
		return
	}
	if n != w.used {
		w.err = io.ErrShortWrite
		return
	}
	w.used = 0
}
func (w *canonicalWriter) text(s string) {
	for len(s) > 0 && w.err == nil {
		if w.used == len(w.scratch) {
			w.flush()
		}
		n := copy(w.scratch[w.used:], s)
		w.used += n
		s = s[n:]
	}
}
func (w *canonicalWriter) bytes(b []byte) {
	for len(b) > 0 && w.err == nil {
		if w.used == len(w.scratch) {
			w.flush()
		}
		n := copy(w.scratch[w.used:], b)
		w.used += n
		b = b[n:]
	}
}

const hexDigits = "0123456789abcdef"

func (w *canonicalWriter) string(s string) {
	w.text(`"`)
	start := 0
	for i := 0; i < len(s); {
		b := s[i]
		if b >= 0x20 && b < utf8.RuneSelf && b != '"' && b != '\\' && (w.unescapedHTML || (b != '<' && b != '>' && b != '&')) {
			i++
			continue
		}
		if b < utf8.RuneSelf {
			w.text(s[start:i])
			switch b {
			case '"', '\\':
				w.text("\\")
				w.text(s[i : i+1])
			case '\b':
				w.text(`\b`)
			case '\f':
				w.text(`\f`)
			case '\n':
				w.text(`\n`)
			case '\r':
				w.text(`\r`)
			case '\t':
				w.text(`\t`)
			default:
				w.text(`\u00`)
				w.text(hexDigits[b>>4 : b>>4+1])
				w.text(hexDigits[b&15 : b&15+1])
			}
			i++
			start = i
			continue
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && n == 1 {
			w.text(s[start:i])
			w.text(`\ufffd`)
			i++
			start = i
			continue
		}
		if r == 0x2028 || r == 0x2029 {
			w.text(s[start:i])
			if r == 0x2028 {
				w.text(`\u2028`)
			} else {
				w.text(`\u2029`)
			}
			i += n
			start = i
			continue
		}
		i += n
	}
	w.text(s[start:])
	w.text(`"`)
}
func (w *canonicalWriter) integer(n int64) { var b [20]byte; w.bytes(strconv.AppendInt(b[:0], n, 10)) }
func (w *canonicalWriter) unsigned(n uint64) {
	var b [20]byte
	w.bytes(strconv.AppendUint(b[:0], n, 10))
}
func (w *canonicalWriter) boolean(v bool) {
	if v {
		w.text("true")
	} else {
		w.text("false")
	}
}
func (w *canonicalWriter) field(name, value string) { w.text(`"` + name + `":`); w.string(value) }
func (w *canonicalWriter) optional(name, value string) {
	if value != "" {
		w.text(",")
		w.field(name, value)
	}
}
func (w *canonicalWriter) agent(a *AgentContext) {
	w.text("{")
	comma := false
	for _, f := range [3][2]string{{"agentName", a.AgentName}, {"author", a.Author}, {"recipient", a.Recipient}} {
		if f[1] != "" {
			if comma {
				w.text(",")
			}
			w.field(f[0], f[1])
			comma = true
		}
	}
	w.text("}")
}

// raw compacts a prevalidated JSON leaf without decoding/reordering its values.
func (w *canonicalWriter) raw(b []byte) {
	iter := canonicalRawIterator{input: b}
	for iter.offset < len(b) {
		start := iter.offset
		replacement, omit := iter.next()
		if omit {
			continue
		}
		if replacement != "" {
			w.text(replacement)
		} else {
			w.bytes(b[start:iter.offset])
		}
	}
}

// This is the existing raw compaction/escaping walk, not a JSON grammar or a
// decoded-value normalization. The writer and footprint counter share it so
// duplicate keys, number spelling, raw invalid UTF8 and escapes stay unchanged.
type canonicalRawIterator struct {
	input             []byte
	offset            int
	inString, escaped bool
}

func (s *canonicalRawIterator) next() (replacement string, omit bool) {
	i := s.offset
	c := s.input[i]
	s.offset++
	if !s.inString && (c == ' ' || c == '\n' || c == '\t' || c == '\r') {
		return "", true
	}
	switch c {
	case '<':
		replacement = `\u003c`
	case '>':
		replacement = `\u003e`
	case '&':
		replacement = `\u0026`
	case 0xe2:
		if i+2 < len(s.input) && s.input[i+1] == 0x80 && (s.input[i+2] == 0xa8 || s.input[i+2] == 0xa9) {
			if s.input[i+2] == 0xa8 {
				replacement = `\u2028`
			} else {
				replacement = `\u2029`
			}
			s.offset += 2
		}
	}
	if s.inString {
		if s.escaped {
			s.escaped = false
		} else if c == '\\' {
			s.escaped = true
		} else if c == '"' {
			s.inString = false
		}
	} else if c == '"' {
		s.inString = true
	}
	return replacement, false
}

func canonicalRawBytes(ctx context.Context, raw []byte) (uint64, error) {
	if ctx == nil {
		return 0, ErrInvalidEvidence
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	iter := canonicalRawIterator{input: raw}
	var total uint64
	sinceCheck := 0
	for iter.offset < len(raw) {
		start := iter.offset
		replacement, omit := iter.next()
		width := iter.offset - start
		sinceCheck += width
		if sinceCheck >= 4096 {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			sinceCheck = 0
		}
		if omit {
			continue
		}
		n := uint64(width)
		if replacement != "" {
			n = uint64(len(replacement))
		}
		if n > math.MaxInt64-total {
			return 0, ErrInvalidEvidence
		}
		total += n
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return total, nil
}
func preflightBlock(b Block) error {
	if len(b.Arguments) > 0 && !json.Valid(b.Arguments) {
		// Unmarshal checks syntax before looking at its destination. This branch
		// is invalid-only: no raw body is decoded, cloned or compacted. Legacy
		// appendCompact does not count scanner bytes, so Offset is deliberately
		// zero for MarshalJSON syntax errors, not an input byte position.
		err := json.Unmarshal(b.Arguments, nil)
		if syntax, ok := err.(*json.SyntaxError); ok {
			syntax.Offset = 0
		}
		return &json.MarshalerError{Type: reflect.TypeOf(json.RawMessage(nil)), Err: err}
	}
	return nil
}
func (w *canonicalWriter) block(b Block) {
	w.text("{")
	if b.Deferred != nil {
		w.text(`"deferred":{`)
		w.field("exchangeId", b.Deferred.ExchangeID)
		w.text(",")
		w.field("cursor", b.Deferred.Cursor)
		w.text(`,"estimatedBytes":`)
		w.integer(int64(b.Deferred.EstimatedBytes))
		w.text("},")
	}
	w.field("kind", b.Kind)
	w.text(",")
	w.field("availability", string(b.Availability))
	w.optional("text", b.Text)
	w.text(`,"originalSize":`)
	w.integer(int64(b.OriginalSize))
	w.optional("callId", b.CallID)
	w.optional("toolName", b.ToolName)
	w.optional("toolNamespace", b.ToolNamespace)
	if len(b.Arguments) > 0 {
		w.text(`,"arguments":`)
		w.raw(b.Arguments)
	}
	if b.ToolError {
		w.text(`,"toolError":true`)
	}
	w.optional("providerSource", b.ProviderSource)
	w.optional("providerKind", b.ProviderKind)
	w.optional("fingerprint", b.Fingerprint)
	if b.Agent != nil {
		w.text(`,"agent":`)
		w.agent(b.Agent)
	}
	w.text("}")
}

func WriteCanonicalBlock(sink io.Writer, block Block) error {
	if sink == nil {
		return fmt.Errorf("%w: nil writer", ErrInvalidEvidence)
	}
	if err := preflightBlock(block); err != nil {
		return err
	}
	w := canonicalWriter{sink: sink}
	w.block(block)
	w.flush()
	return w.err
}

func preflightRecord(r Record) error {
	if _, err := r.RecordedAt.MarshalJSON(); err != nil {
		return &json.MarshalerError{Type: reflect.TypeOf(r.RecordedAt), Err: err}
	}
	if _, err := r.ExpiresAt.MarshalJSON(); err != nil {
		return &json.MarshalerError{Type: reflect.TypeOf(r.ExpiresAt), Err: err}
	}
	for _, b := range r.Request.System {
		if err := preflightBlock(b); err != nil {
			return &json.MarshalerError{Type: reflect.TypeOf(r.Request), Err: err}
		}
	}
	for _, m := range r.Request.Messages {
		for _, b := range m.Blocks {
			if err := preflightBlock(b); err != nil {
				return &json.MarshalerError{Type: reflect.TypeOf(r.Request), Err: err}
			}
		}
	}
	// Request.MarshalJSON finishes encoding every inner leaf before the parent
	// marshaler rescans its output. Preserve that ordering: a late malformed
	// leaf outranks an earlier parent-depth overflow in this same request.
	for _, b := range r.Request.System {
		if c := argumentDepthOverflow(b.Arguments, canonicalBlockParentDepth); c != 0 {
			return parentDepthError(reflect.TypeOf(r.Request), c)
		}
	}
	for _, m := range r.Request.Messages {
		for _, b := range m.Blocks {
			if c := argumentDepthOverflow(b.Arguments, canonicalMessageParentDepth); c != 0 {
				return parentDepthError(reflect.TypeOf(r.Request), c)
			}
		}
	}
	if r.Response != nil {
		for _, b := range r.Response.Blocks {
			if err := preflightBlock(b); err != nil {
				return &json.MarshalerError{Type: reflect.TypeOf(r.Response), Err: err}
			}
		}
		for _, v := range usageValues(r.Response.Usage) {
			if err := v.validateEncoding(); err != nil {
				return &json.MarshalerError{Type: reflect.TypeOf(r.Response), Err: &json.MarshalerError{Type: reflect.TypeOf(v), Err: err}}
			}
		}
		for _, b := range r.Response.Blocks {
			if c := argumentDepthOverflow(b.Arguments, canonicalBlockParentDepth); c != 0 {
				return parentDepthError(reflect.TypeOf(r.Response), c)
			}
		}
	}
	return nil
}

// These envelopes derive from the existing custom-marshaler scan, not a new
// protocol capacity: Request/messages[]/Message/blocks[]/Block is five;
// Request/system[]/Block and Response/blocks[]/Block are three containers.
const (
	canonicalMaxNesting         = 10000
	canonicalMessageParentDepth = 5
	canonicalBlockParentDepth   = 3
)

// Grammar was already validated by json.Valid/Block.Validate. Only lexical
// container depth is counted here, with no nesting stack or decoded strings.
func argumentDepthOverflow(raw []byte, envelope int) byte {
	limit := canonicalMaxNesting - envelope
	depth := 0
	quoted, escaped := false, false
	for _, c := range raw {
		if quoted {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				quoted = false
			}
			continue
		}
		switch c {
		case '"':
			quoted = true
		case '[', '{':
			if depth == limit {
				return c
			}
			depth++
		case ']', '}':
			depth--
		}
	}
	return 0
}
func parentDepthError(parent reflect.Type, opening byte) error {
	// SyntaxError's message is private. A fixed invalid-only probe obtains the
	// pinned scanner's exact error, including the actual offending character.
	// Each call owns its error; exposed mutable Offset cannot alter later calls.
	probe := make([]byte, canonicalMaxNesting+1)
	for i := range probe {
		probe[i] = '['
	}
	probe[canonicalMaxNesting] = opening
	err := json.Unmarshal(probe, nil)
	if syntax, ok := err.(*json.SyntaxError); ok {
		syntax.Offset = 0
	}
	return &json.MarshalerError{Type: parent, Err: err}
}
func usageValues(u Usage) [5]UsageValue {
	return [5]UsageValue{u.InputUncached, u.CacheWrite, u.CacheRead, u.Output, u.Reasoning}
}
func (w *canonicalWriter) blocks(bs []Block, normalize bool) {
	if bs == nil && !normalize {
		w.text("null")
		return
	}
	w.text("[")
	for i, b := range bs {
		if i > 0 {
			w.text(",")
		}
		w.block(b)
	}
	w.text("]")
}
func (w *canonicalWriter) message(m Message, normalize bool) {
	w.text("{")
	w.field("role", m.Role)
	w.text(`,"blocks":`)
	w.blocks(m.Blocks, normalize)
	if m.Agent != nil {
		w.text(`,"agent":`)
		w.agent(m.Agent)
	}
	w.text("}")
}
func (w *canonicalWriter) evidence(es []protocolcore.ProtocolEvidenceValue) {
	w.text("[")
	for i, e := range es {
		if i > 0 {
			w.text(",")
		}
		w.text("{")
		w.field("name", e.Name)
		w.text(",")
		w.field("value", e.Value)
		w.text("}")
	}
	w.text("]")
}
func (w *canonicalWriter) requestStart(r RequestMetadata) {
	w.text("{")
	w.field("requestedModel", r.RequestedModel)
	w.text(",")
	w.field("effectiveModel", r.EffectiveModel)
	w.text(`,"maxOutputTokens":`)
	w.integer(int64(r.MaxOutputTokens))
	w.text(`,"stream":`)
	w.boolean(r.Stream)
}
func (w *canonicalWriter) requestEnd(r RequestMetadata) {
	w.text(`,"tools":[`)
	for i, t := range r.Tools {
		if i > 0 {
			w.text(",")
		}
		w.text("{")
		w.field("name", t.Name)
		w.optional("namespace", t.Namespace)
		w.text("}")
	}
	w.text(`],"protocolEvidence":`)
	w.evidence(r.ProtocolEvidence)
	w.text("}")
}
func (w *canonicalWriter) responseStart(r ResponseMetadata) {
	w.text("{")
	w.field("id", r.ID)
	w.text(",")
	w.field("requestedModel", r.RequestedModel)
	w.text(",")
	w.field("effectiveModel", r.EffectiveModel)
	w.text(",")
	w.field("reportedModel", r.ReportedModel)
	w.text(",")
	w.field("stopReason", r.StopReason)
	w.text(`,"blocks":`)
}
func (w *canonicalWriter) responseEnd(r ResponseMetadata) {
	w.text(`,"usage":{`)
	names := [5]string{"inputUncached", "cacheWrite", "cacheRead", "output", "reasoning"}
	for i, v := range usageValues(r.Usage) {
		if i > 0 {
			w.text(",")
		}
		w.text(`"` + names[i] + `":`)
		if !v.Known {
			w.text(`{"known":false}`)
		} else {
			w.text(`{"known":true,"tokens":`)
			w.integer(v.Tokens)
			w.text(",")
			w.field("source", v.Source)
			w.text("}")
		}
	}
	w.text(`},"protocolEvidence":`)
	w.evidence(r.ProtocolEvidence)
	w.text("}")
}
func (w *canonicalWriter) recordStart(r RecordMetadata) {
	w.text("{")
	w.field("exchangeId", r.ExchangeID)
	w.text(`,"parent":{`)
	comma := false
	for _, f := range [2][2]string{{"captureRunId", r.Parent.CaptureRunID}, {"manualCaptureId", r.Parent.ManualCaptureID}} {
		if f[1] != "" {
			if comma {
				w.text(",")
			}
			w.field(f[0], f[1])
			comma = true
		}
	}
	w.text(`},"frozen":{`)
	f := r.Frozen
	w.field("environmentId", f.EnvironmentID)
	w.text(`,"environmentRevision":`)
	w.unsigned(f.EnvironmentRevision)
	w.text(",")
	w.field("environmentDigest", f.EnvironmentDigest)
	w.text(",")
	w.field("clientEndpointId", f.ClientEndpointID)
	w.text(`,"clientEndpointRevision":`)
	w.unsigned(f.ClientEndpointRevision)
	w.text(",")
	w.field("protocolPlanId", f.ProtocolPlanID)
	w.text(`,"protocolPlanRevision":`)
	w.unsigned(f.ProtocolPlanRevision)
	w.text(",")
	w.field("routeId", f.RouteID)
	w.text(`,"routeRevision":`)
	w.unsigned(f.RouteRevision)
	w.text("},")
	w.field("mode", string(r.Mode))
	w.text(`,"recordedAt":`)
	b, _ := r.RecordedAt.MarshalJSON()
	w.bytes(b)
	w.text(`,"expiresAt":`)
	b, _ = r.ExpiresAt.MarshalJSON()
	w.bytes(b)
	w.text(`,"request":`)
}

// WriteCanonicalJSON is an encoding boundary, not semantic admission. All
// fallible leaf encodings are preflighted before the first sink write.
func WriteCanonicalJSON(sink io.Writer, r Record) error {
	if sink == nil {
		return fmt.Errorf("%w: nil writer", ErrInvalidEvidence)
	}
	if err := preflightRecord(r); err != nil {
		return err
	}
	w := canonicalWriter{sink: sink}
	meta := metadataFromRecord(r)
	w.recordStart(meta)
	w.requestStart(meta.Request)
	w.text(`,"system":`)
	w.blocks(r.Request.System, true)
	w.text(`,"messages":[`)
	for i, m := range r.Request.Messages {
		if i > 0 {
			w.text(",")
		}
		w.message(m, true)
	}
	w.text("]")
	w.requestEnd(meta.Request)
	if r.Response != nil {
		w.text(`,"response":`)
		w.responseStart(*meta.Response)
		w.blocks(r.Response.Blocks, true)
		w.responseEnd(*meta.Response)
	}
	w.text("}")
	w.flush()
	return w.err
}

// wireCounter stops emission at the selected finite bound without retaining
// output. Context is checked on every bounded writer flush, including bodies.
type wireCounter struct {
	ctx      context.Context
	limit, n uint64
}

func (c *wireCounter) Write(b []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	if uint64(len(b)) > c.limit-c.n {
		return 0, fmt.Errorf("%w: encoded view exceeds its bound", ErrInvalidEvidence)
	}
	c.n += uint64(len(b))
	return len(b), nil
}

func countProjectionWire(ctx context.Context, limit uint64, p Projection) error {
	c := wireCounter{ctx: ctx, limit: limit}
	w := canonicalWriter{sink: &c}
	m := projectionMetadata(p)
	w.recordStart(m)
	w.requestStart(m.Request)
	w.text(`,"system":`)
	w.blocks(p.Request.System, true)
	w.text(`,"messages":[`)
	for i, v := range p.Request.Messages {
		if i > 0 {
			w.text(",")
		}
		w.message(v, true)
	}
	w.text("]")
	w.requestEnd(m.Request)
	if p.Response != nil {
		w.text(`,"response":`)
		w.responseStart(*m.Response)
		w.blocks(p.Response.Blocks, true)
		w.responseEnd(*m.Response)
	}
	w.text("}")
	w.flush()
	return w.err
}

// ContentPage has no collection-normalizing MarshalJSON. Its nil arrays and
// omitempty fields therefore use the page's actual wire layout, not Record's.
func writePageWire(w *canonicalWriter, p ContentPage) {
	w.text("{")
	comma := false
	for _, f := range [3][2]string{{"blockKind", p.BlockKind}, {"callId", p.CallID}, {"toolName", p.ToolName}} {
		if f[1] != "" {
			if comma {
				w.text(",")
			}
			w.field(f[0], f[1])
			comma = true
		}
	}
	if comma {
		w.text(",")
	}
	w.field("exchangeId", p.ExchangeID)
	w.text(",")
	w.field("kind", p.Kind)
	w.text(`,"messages":`)
	if p.Messages == nil {
		w.text("null")
	} else {
		w.text("[")
		for i, m := range p.Messages {
			if i > 0 {
				w.text(",")
			}
			w.message(m, false)
		}
		w.text("]")
	}
	w.text(`,"blocks":`)
	w.blocks(p.Blocks, false)
	if len(p.ProtocolEvidence) > 0 {
		w.text(`,"protocolEvidence":`)
		w.evidence(p.ProtocolEvidence)
	}
	w.optional("text", p.Text)
	w.text(`,"offset":`)
	w.integer(int64(p.Offset))
	w.text(`,"total":`)
	w.integer(int64(p.Total))
	w.optional("nextCursor", p.NextCursor)
	w.text("}")
}
func countPageWire(ctx context.Context, limit uint64, p ContentPage) error {
	c := wireCounter{ctx: ctx, limit: limit}
	w := canonicalWriter{sink: &c}
	writePageWire(&w, p)
	w.flush()
	return w.err
}

func (w *canonicalWriter) sourceMessage(ctx context.Context, m MessageSource) error {
	h := m.Header()
	w.text("{")
	w.field("role", h.Role)
	w.text(`,"blocks":[`)
	i := 0
	if err := m.WalkBlocks(ctx, func(b Block) error {
		if i > 0 {
			w.text(",")
		}
		w.block(b)
		i++
		return w.err
	}); err != nil {
		return err
	}
	w.text("]")
	if h.Agent != nil {
		w.text(`,"agent":`)
		w.agent(h.Agent)
	}
	w.text("}")
	return w.err
}
func WriteCanonicalMessage(sink io.Writer, m MessageSource) error {
	if sink == nil || !m.valid() {
		return ErrInvalidEvidence
	}
	ctx := context.Background()
	if err := m.WalkBlocks(ctx, preflightBlock); err != nil {
		return err
	}
	w := canonicalWriter{sink: sink}
	if err := w.sourceMessage(ctx, m); err != nil {
		return err
	}
	w.flush()
	return w.err
}
func writeSourceCanonical(ctx context.Context, sink io.Writer, s *Source) error {
	w := canonicalWriter{sink: sink}
	meta := s.metadata
	w.recordStart(meta)
	w.requestStart(meta.Request)
	w.text(`,"system":[`)
	i := 0
	if err := s.message(SystemPart, 0).WalkBlocks(ctx, func(b Block) error {
		if i > 0 {
			w.text(",")
		}
		w.block(b)
		i++
		return w.err
	}); err != nil {
		return err
	}
	w.text(`],"messages":[`)
	n := len(s.request.Messages)
	if s.record != nil {
		n = len(s.record.Request.Messages)
	}
	for i := 0; i < n; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if i > 0 {
			w.text(",")
		}
		if err := w.sourceMessage(ctx, s.message(RequestPart, i)); err != nil {
			return err
		}
	}
	w.text("]")
	w.requestEnd(meta.Request)
	if meta.Response != nil {
		w.text(`,"response":`)
		w.responseStart(*meta.Response)
		w.text("[")
		i = 0
		if err := s.message(ResponsePart, 0).WalkBlocks(ctx, func(b Block) error {
			if i > 0 {
				w.text(",")
			}
			w.block(b)
			i++
			return w.err
		}); err != nil {
			return err
		}
		w.text("]")
		w.responseEnd(*meta.Response)
	}
	w.text("}")
	w.flush()
	return w.err
}
