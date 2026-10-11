package desktopcontrol

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"unsafe"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
)

// This is a per-operation ownership bound, independent of semantic admission
// and model execution slots. The operation borrows the DTO through both passes;
// only one ordinary leaf and fixed carrier scratch are encoded at a time.
func detailOutputLimit(l exchangecontent.SourceLimits) (uint64, error) {
	a := l.StructureBytes / uint64(unsafe.Sizeof(exchangecontent.AgentContext{}))
	b := l.StructureBytes / uint64(unsafe.Sizeof(exchangecontent.Block{}))
	n := uint64(84632065)
	for _, term := range [][2]uint64{{l.CanonicalBytes, 1}, {a, 15626}, {b, 11062}, {256, 1}} {
		if term[0] > (math.MaxInt64-n)/term[1] {
			return 0, errors.New("detail output bound overflows")
		}
		n += term[0] * term[1]
	}
	return n, nil
}

type detailJSONPlan struct {
	ctx    context.Context
	detail ExchangeDetail
	bytes  uint64
}

func prepareExchangeDetailJSON(ctx context.Context, l exchangecontent.SourceLimits, d ExchangeDetail) (detailJSONPlan, error) {
	if ctx == nil {
		return detailJSONPlan{}, errors.New("missing detail context")
	}
	maximum, err := detailOutputLimit(l)
	if err != nil {
		return detailJSONPlan{}, err
	}
	if d.ClientIdentity != nil {
		if err := d.ClientIdentity.Validate(); err != nil {
			return detailJSONPlan{}, err
		}
	}
	if len(d.ProcessingTrace.PluginRunIDs) != 0 || len(d.ProcessingTrace.Attempts) > 500 {
		return detailJSONPlan{}, errors.New("invalid detail trace")
	}
	if err := validateDetailOrdinary(d); err != nil {
		return detailJSONPlan{}, err
	}
	counter := detailSink{ctx: ctx, remaining: maximum}
	if err := writeExchangeDetailJSON(&counter, d); err != nil {
		return detailJSONPlan{}, err
	}
	return detailJSONPlan{ctx: ctx, detail: d, bytes: maximum - counter.remaining}, nil
}

func validateDetailOrdinary(d ExchangeDetail) error {
	check := func(maximum int, values ...string) bool {
		for _, v := range values {
			if len(v) > maximum {
				return false
			}
		}
		return true
	}
	if !check(512, d.ID, d.Status, d.ProcessingTrace.Result, d.ProcessingTrace.EgressProxyID) || !check(128, d.Environment.ID, d.Environment.ClientEndpointID, d.Environment.ProtocolPlanID, d.Environment.RouteID, d.Environment.AccountID) || len(d.Environment.Digest) > 64 {
		return errors.New("invalid ordinary detail bounds")
	}
	if !check(512, d.ParentRefs.CaptureRunID, d.ParentRefs.ManualCaptureID, d.ParentRefs.ConnectionID, d.ParentRefs.ExchangeID, string(d.Content.State), d.Content.Mode) {
		return errors.New("invalid detail shell bounds")
	}
	if p := d.Content.Page; p != nil && !check(2048, p.RequestNextCursor, p.RequestEvidenceNextCursor, p.ResponseEvidenceNextCursor) {
		return errors.New("invalid detail cursor bounds")
	}
	if p := d.Content.RequestProjection; p != nil && !check(512, string(p.View), string(p.Relationship)) {
		return errors.New("invalid projection shell bounds")
	}
	if d.Diagnosis != nil && (!check(128, d.Diagnosis.ProviderErrorCode, d.Diagnosis.ProviderField, d.Diagnosis.ClientField) || len(d.Diagnosis.ClientPath) > 256) {
		return errors.New("invalid diagnostic bounds")
	}
	for _, v := range d.ProcessingTrace.Attempts {
		if !boundedAttemptView(v) {
			return errors.New("invalid attempt detail bounds")
		}
	}
	if g := d.Content.AgentConversation; g != nil {
		if len(g.Scope) > 16 {
			return errors.New("invalid graph scope")
		}
		for _, a := range g.Agents {
			if !check(512, a.Name) {
				return errors.New("invalid graph agent")
			}
		}
		for _, r := range g.Relationships {
			if !check(512, r.Source, r.Target) || r.Kind != "message" {
				return errors.New("invalid graph relationship")
			}
		}
		for _, a := range g.Actions {
			if !check(512, a.CallID, a.SourceAgent, a.ResultAgent) || len(a.Name) > 256 || len(a.Status) > 9 {
				return errors.New("invalid graph action")
			}
		}
	}
	return nil
}
func boundedAttemptView(v egressaudit.View) bool {
	for _, s := range []string{v.ID, v.ConnectionID, v.Parent.ID, v.Parent.ExchangeID, v.CallerID, v.Decision.PolicyID, v.Decision.RuleID, v.Decision.ProxyID, v.Decision.AccountID, v.ErrorClass} {
		if len(s) > 512 {
			return false
		}
	}
	if len(v.TargetOrigin) > 1024 {
		return false
	}
	for _, s := range []string{string(v.Purpose), string(v.PayloadClass), string(v.Parent.Kind), string(v.Caller), string(v.Decision.Authority), string(v.Outcome)} {
		if len(s) > 32 {
			return false
		}
	}
	return true
}

// Graph ownership is admitted before any derived maps/slices grow. Field
// strings borrow the already-owned projection; only relationship keys copy.
// The surrounding HTTP operation retains both until synchronous writing ends.
func reserveDetailGraph(ctx context.Context, l exchangecontent.SourceLimits, p exchangecontent.Projection) error {
	var agents, blocks uint64
	for _, m := range p.Request.Messages {
		if err := ctx.Err(); err != nil {
			return err
		}
		if m.Agent != nil {
			agents++
		}
		for _, b := range m.Blocks {
			blocks++
			if b.Agent != nil {
				agents++
			}
		}
	}
	if p.Response != nil {
		for _, b := range p.Response.Blocks {
			blocks++
			if b.Agent != nil {
				agents++
			}
		}
	}
	maxAgents := l.StructureBytes / uint64(unsafe.Sizeof(exchangecontent.AgentContext{}))
	maxBlocks := l.StructureBytes / uint64(unsafe.Sizeof(exchangecontent.Block{}))
	if agents > maxAgents || blocks > maxBlocks {
		return errors.New("derived graph exceeds admitted source structure")
	}
	// Three capacities cover append/map growth with old/new storage. The
	// integer checks also establish a constructor-time finite per-operation
	// control envelope, separate from the model BodyAdmission pool.
	var total uint64
	for _, term := range [][2]uint64{{agents, 3*(3*uint64(unsafe.Sizeof(AgentConversationAgent{}))+uint64(unsafe.Sizeof(AgentConversationRelationship{}))+4*uint64(unsafe.Sizeof(""))) + 1025}, {blocks, 3 * (uint64(unsafe.Sizeof(AgentConversationAction{})) + uint64(unsafe.Sizeof("")) + uint64(unsafe.Sizeof(int(0))))}, {1025, 1}} {
		if term[0] > (math.MaxInt64-total)/term[1] {
			return errors.New("derived graph ownership overflows")
		}
		total += term[0] * term[1]
	}
	return ctx.Err()
}
func (p detailJSONPlan) WriteTo(w io.Writer) (int64, error) {
	sink := detailSink{ctx: p.ctx, remaining: p.bytes, writer: w}
	if err := writeExchangeDetailJSON(&sink, p.detail); err != nil {
		return int64(p.bytes - sink.remaining), err
	}
	if sink.remaining != 0 {
		return int64(p.bytes - sink.remaining), io.ErrUnexpectedEOF
	}
	return int64(p.bytes), nil
}

type detailSink struct {
	ctx       context.Context
	remaining uint64
	writer    io.Writer
}

func (s *detailSink) Write(b []byte) (int, error) {
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	if uint64(len(b)) > s.remaining {
		return 0, errors.New("detail output exceeds preflight")
	}
	n := len(b)
	var err error
	if s.writer != nil {
		n, err = s.writer.Write(b)
	}
	if n < 0 || n > len(b) {
		return 0, errors.New("invalid detail writer count")
	}
	s.remaining -= uint64(n)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	return n, err
}

// detailWriter is deliberately closed to these DTOs. Ordinary collections are
// encoded entry-by-entry; retained raw carriers go directly to leaf writers.
type detailWriter struct {
	w   io.Writer
	err error
}

func (w *detailWriter) text(s string) {
	if w.err == nil {
		_, w.err = io.WriteString(w.w, s)
	}
}
func (w *detailWriter) value(v any) {
	if w.err != nil {
		return
	}
	var b []byte
	b, w.err = json.Marshal(v)
	if w.err == nil {
		n, err := w.w.Write(b)
		w.err = err
		if err == nil && n != len(b) {
			w.err = io.ErrShortWrite
		}
	}
}
func (w *detailWriter) field(name string, v any) { w.value(name); w.text(":"); w.value(v) }
func writeExchangeDetailJSON(sink io.Writer, d ExchangeDetail) error {
	w := detailWriter{w: sink}
	w.text("{")
	w.field("id", d.ID)
	w.text(",")
	w.field("status", d.Status)
	w.text(",")
	w.field("environment", d.Environment)
	w.text(",")
	w.field("parentRefs", d.ParentRefs)
	if d.Diagnosis != nil {
		w.text(",")
		w.field("diagnosis", d.Diagnosis)
	}
	w.text(`,"processingTrace":{`)
	if d.ProcessingTrace.EgressProxyID != "" {
		w.field("egressProxyId", d.ProcessingTrace.EgressProxyID)
		w.text(",")
	}
	w.field("pluginRunIds", d.ProcessingTrace.PluginRunIDs)
	w.text(`,"attempts":`)
	if d.ProcessingTrace.Attempts == nil {
		w.text("null")
	} else {
		w.text("[")
		for i, v := range d.ProcessingTrace.Attempts {
			if i > 0 {
				w.text(",")
			}
			w.value(v)
		}
		w.text("]")
	}
	w.text(",")
	w.field("result", d.ProcessingTrace.Result)
	w.text(`},"content":{`)
	c := d.Content
	if c.Page != nil {
		w.field("page", c.Page)
		w.text(",")
	}
	w.field("state", c.State)
	if c.Mode != "" {
		w.text(",")
		w.field("mode", c.Mode)
	}
	if c.RecordedAt != nil {
		w.text(",")
		w.field("recordedAt", c.RecordedAt)
	}
	if c.ExpiresAt != nil {
		w.text(",")
		w.field("expiresAt", c.ExpiresAt)
	}
	if c.RequestProjection != nil {
		w.text(",")
		w.field("requestProjection", c.RequestProjection)
	}
	if g := c.AgentConversation; g != nil {
		w.text(`,"agentConversation":{`)
		w.field("scope", g.Scope)
		w.text(`,"agents":[`)
		for i, v := range g.Agents {
			if i > 0 {
				w.text(",")
			}
			w.value(v)
		}
		w.text("]")
		w.text(`,"relationships":[`)
		for i, v := range g.Relationships {
			if i > 0 {
				w.text(",")
			}
			w.value(v)
		}
		w.text("]")
		w.text(`,"actions":[`)
		for i, v := range g.Actions {
			if i > 0 {
				w.text(",")
			}
			w.value(v)
		}
		w.text("]}")
	}
	if c.Request != nil {
		w.text(`,"request":`)
		if w.err == nil {
			w.err = exchangecontent.WriteRequestJSON(sink, *c.Request)
		}
	}
	if c.Response != nil {
		w.text(`,"response":`)
		if w.err == nil {
			w.err = exchangecontent.WriteResponseJSON(sink, *c.Response)
		}
	}
	w.text("}")
	if id := d.ClientIdentity; id != nil {
		w.text(`,"clientIdentity":{`)
		w.field("client", id.Client)
		w.text(",")
		w.field("sessionId", id.SessionID)
		w.text(",")
		w.field("sessionResumable", id.SessionResumable)
		for _, f := range []struct{ n, v string }{{"actorId", id.ActorID}, {"actorLabel", id.ActorLabel}, {"actorType", id.ActorType}} {
			if f.v != "" {
				w.text(",")
				w.field(f.n, f.v)
			}
		}
		w.text(",")
		w.field("actorIsSubagent", id.ActorIsSubagent)
		for _, f := range []struct{ n, v string }{{"providerResponseId", id.ProviderResponseID}, {"providerMessageId", id.ProviderMessageID}} {
			if f.v != "" {
				w.text(",")
				w.field(f.n, f.v)
			}
		}
		w.text(",")
		w.field("source", id.Source)
		w.text(",")
		w.field("confidence", id.Confidence)
		w.text(",")
		w.field("observedAt", id.ObservedAt)
		if len(id.ProtocolIDs) > 0 {
			w.text(`,"protocolIds":[`)
			for i, v := range id.ProtocolIDs {
				if i > 0 {
					w.text(",")
				}
				w.value(v)
			}
			w.text("]")
		}
		if len(id.Attributes) > 0 {
			w.text(`,"attributes":[`)
			for i, v := range id.Attributes {
				if i > 0 {
					w.text(",")
				}
				w.value(v)
			}
			w.text("]")
		}
		w.text("}")
	}
	w.text("}\n")
	return w.err
}
