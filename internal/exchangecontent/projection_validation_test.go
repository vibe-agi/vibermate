package exchangecontent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func withinFixture(t *testing.T, count int) Record {
	t.Helper()
	r := utf8Record(t)
	r.Request.Messages = make([]Message, count)
	for i := range r.Request.Messages {
		r.Request.Messages[i] = Message{Role: "user", Blocks: []Block{{Kind: "text", Availability: AvailabilityRecorded, Text: "tail", OriginalSize: 4}}}
	}
	r.Presentation = RequestPresentation{Mode: RequestPresentationCheckpoint}
	return r
}

func withinProjection(r Record) Projection {
	return Projection{ExchangeID: r.ExchangeID, Parent: r.Parent, Frozen: r.Frozen, Mode: r.Mode, RecordedAt: r.RecordedAt, ExpiresAt: r.ExpiresAt, Request: r.Request, Response: r.Response, Presentation: r.Presentation, View: RequestViewFull, TotalMessageCount: len(r.Request.Messages)}
}

func TestWithinLongViews(t *testing.T) {
	ctx, l := context.Background(), sourceFixtureLimits()
	for _, view := range []RequestView{RequestViewFull, RequestViewIncremental} {
		for _, replay := range []bool{false, true} {
			r := withinFixture(t, 4111)
			r.Presentation = RequestPresentation{Mode: RequestPresentationIncremental, InheritedMessageCount: 4110}
			if replay {
				r.Presentation = RequestPresentation{Mode: RequestPresentationSameTranscript, InheritedMessageCount: 4111}
			}
			p, err := ProjectWithin(ctx, l, r, view)
			if err != nil {
				t.Errorf("%s replay=%v: %v", view, replay, err)
				continue
			}
			want := 4111
			if view == RequestViewIncremental {
				want = 1
				if replay {
					want = 0
				}
			}
			if len(p.Request.Messages) != want || p.TotalMessageCount != 4111 {
				t.Fatal("view equation changed")
			}
			if err := p.ValidateWithin(ctx, l); err != nil {
				t.Fatal(err)
			}
		}
	}
	p := withinProjection(withinFixture(t, 1))
	p.TotalMessageCount = 4111
	p.Page = &ProjectionPage{RequestOffset: 4110}
	p.Request.Messages = []Message{{Role: "unknown", Blocks: []Block{{Kind: "deferred", Availability: AvailabilityRecorded, Deferred: &DeferredContent{ExchangeID: p.ExchangeID, Cursor: "c", EstimatedBytes: MaxEncodedBytes + 1}}}}}
	if err := p.ValidateWithin(ctx, l); err != nil {
		t.Errorf("large deferred projection: %v", err)
	}
	page := ContentPage{ExchangeID: p.ExchangeID, Parent: p.Parent, Frozen: p.Frozen, Mode: p.Mode, Kind: "request", Messages: p.Request.Messages, Offset: 4110, Total: 4111}
	if err := page.ValidateWithin(ctx, l); err != nil {
		t.Errorf("large deferred page: %v", err)
	}
	page.Kind = "text"
	page.Messages = nil
	page.Text = "tail"
	page.Offset = MaxEncodedBytes
	page.Total = MaxEncodedBytes + 4
	if err := page.ValidateWithin(ctx, l); err != nil {
		t.Errorf("large logical body: %v", err)
	}
}

func TestWithinBudgetBoundaries(t *testing.T) {
	ctx := context.Background()
	r := withinFixture(t, 2)
	p := withinProjection(r)
	s, err := SourceFromRecordWithin(sourceFixtureLimits(), r)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Measure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, currency := range []string{"canonical", "retained", "structure"} {
		for _, delta := range []int64{-1, 0, 1} {
			l := sourceFixtureLimits()
			switch currency {
			case "canonical":
				l.CanonicalBytes = uint64(int64(c.CanonicalBytes) + delta)
			case "retained":
				l.RetainedBytes = uint64(int64(c.RetainedBytes) + delta)
			case "structure":
				l.StructureBytes = uint64(int64(c.StructureBytes) + delta)
			}
			err := p.ValidateWithin(ctx, l)
			if (err != nil) != (delta < 0) {
				t.Errorf("%s delta%d: %v", currency, delta, err)
			}
			_, err = ProjectWithin(ctx, l, r, RequestViewFull)
			if (err != nil) != (delta < 0) {
				t.Errorf("project %s delta%d: %v", currency, delta, err)
			}
		}
	}
	// Partial cost must cover the supplied suffix, never its hidden history.
	p.TotalMessageCount = 100001
	p.Presentation = RequestPresentation{Mode: RequestPresentationIncremental, InheritedMessageCount: 99999}
	p.View = RequestViewIncremental
	l := sourceFixtureLimits()
	l.RetainedBytes = c.RetainedBytes
	l.StructureBytes = c.StructureBytes
	l.CanonicalBytes = c.CanonicalBytes
	if err := p.ValidateWithin(ctx, l); err != nil {
		t.Fatal(err)
	}
	p.TotalMessageCount = 100002
	if err := p.ValidateWithin(ctx, l); err == nil {
		t.Fatal("storage identity maximum ignored")
	}
}

func TestWithinTypedSemantics(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Projection)
	}{
		{"identity", func(p *Projection) { p.ExchangeID = "" }}, {"parent", func(p *Projection) { p.Parent = ParentRef{CaptureRunID: "a", ManualCaptureID: "b"} }},
		{"frozen", func(p *Projection) { p.Frozen.EnvironmentRevision = 0 }}, {"time", func(p *Projection) { p.ExpiresAt = p.RecordedAt }},
		{"time encoding", func(p *Projection) { p.ExpiresAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) }},
		{"tool", func(p *Projection) { p.Request.Tools = []ToolDefinition{{Name: ""}} }}, {"agent", func(p *Projection) { p.Request.Messages[0].Agent = &AgentContext{Author: "\n"} }},
		{"role", func(p *Projection) { p.Request.Messages[0].Role = "unknown" }}, {"availability", func(p *Projection) { p.Request.Messages[0].Blocks[0].Availability = AvailabilityOmitted }},
		{"usage", func(p *Projection) { p.Response.Usage.Output.Tokens = -1 }}, {"presentation", func(p *Projection) { p.Presentation.InheritedMessageCount = 1 }},
		{"ordinary UTF8", func(p *Projection) { p.Request.EffectiveModel = string([]byte{255}) }},
		{"cursor", func(p *Projection) { p.Page = &ProjectionPage{RequestNextCursor: strings.Repeat("x", 2049)} }},
		{"overflow", func(p *Projection) { p.Page = &ProjectionPage{RequestOffset: math.MaxInt} }},
		{"deferred fact", func(p *Projection) {
			p.Page = &ProjectionPage{}
			p.Request.Messages[0].Blocks[0] = Block{Kind: "deferred", Availability: AvailabilityRecorded, ToolError: true, Deferred: &DeferredContent{ExchangeID: p.ExchangeID, Cursor: "c"}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := withinProjection(withinFixture(t, 2))
			tc.edit(&p)
			if err := p.ValidateWithin(context.Background(), sourceFixtureLimits()); !errors.Is(err, ErrInvalidEvidence) {
				t.Fatalf("accepted invalid typed evidence: %v", err)
			}
		})
	}
	p := withinProjection(withinFixture(t, 2))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.ValidateWithin(ctx, sourceFixtureLimits()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	if _, err := ProjectWithin(ctx, sourceFixtureLimits(), withinFixture(t, 2), RequestViewFull); !errors.Is(err, context.Canceled) {
		t.Fatal("project cancellation lost")
	}
}

func TestWithinOwnershipAndLegacyBytes(t *testing.T) {
	r := withinFixture(t, 2)
	r.Request.Messages[1].Agent = &AgentContext{AgentName: "agent"}
	r.Request.Messages[1].Blocks = []Block{{Kind: "tool_call", Availability: AvailabilityRecorded, CallID: "call", ToolName: "read", Arguments: json.RawMessage(`{ "x": "\ud800" }`)}}
	r.Presentation = RequestPresentation{Mode: RequestPresentationIncremental, InheritedMessageCount: 1}
	for _, view := range []RequestView{RequestViewFull, RequestViewIncremental} {
		old, err := Project(r, view)
		if err != nil {
			t.Fatal(err)
		}
		l := sourceFixtureLimits()
		got, err := ProjectWithin(context.Background(), l, r, view)
		if err != nil {
			t.Fatal(err)
		}
		a, _ := json.Marshal(old)
		b, _ := json.Marshal(got)
		if !bytes.Equal(a, b) {
			t.Fatal("legacy wire bytes changed")
		}
		got.Request.Messages[len(got.Request.Messages)-1].Blocks[0].Arguments[0] = '['
		got.Request.Messages[len(got.Request.Messages)-1].Agent.AgentName = "changed"
		l.RetainedBytes = 1
		again, err := ProjectWithin(context.Background(), sourceFixtureLimits(), r, view)
		if err != nil {
			t.Fatal(err)
		}
		b, _ = json.Marshal(again)
		if !bytes.Equal(a, b) {
			t.Fatal("caller mutation changed input/later result")
		}
	}
}

func TestWithinLargeLogicalBlockAndPreclone(t *testing.T) {
	r := withinFixture(t, 1)
	r.Response = nil
	r.Request.Messages[0].Blocks[0].Text = strings.Repeat("&", 6<<20)
	r.Request.Messages[0].Blocks[0].OriginalSize = 6 << 20
	if r.Validate() == nil {
		t.Fatal("legacy encoded limit changed")
	}
	p, err := ProjectWithin(context.Background(), sourceFixtureLimits(), r, RequestViewFull)
	if err != nil || p.Request.Messages[0].Blocks[0].Text != r.Request.Messages[0].Blocks[0].Text {
		t.Fatalf("large logical body lost: %v", err)
	}
	r.Request.Messages[0].Blocks = []Block{{Kind: "tool_call", Availability: AvailabilityRecorded, CallID: "call", ToolName: "read", Arguments: json.RawMessage(`{"x":"` + strings.Repeat("x", 8<<20) + `"}`)}}
	l := sourceFixtureLimits()
	l.RetainedBytes = 1
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err = ProjectWithin(context.Background(), l, r, RequestViewFull)
	runtime.ReadMemStats(&after)
	if !errors.Is(err, ErrInvalidEvidence) || after.TotalAlloc-before.TotalAlloc > 512<<10 {
		t.Fatalf("low cost rejection copied a large leaf: err=%v allocated=%d", err, after.TotalAlloc-before.TotalAlloc)
	}
}

func TestWithinPageWireAndCurrencyBoundaries(t *testing.T) {
	r := withinFixture(t, 1)
	base := ContentPage{ExchangeID: r.ExchangeID, Parent: r.Parent, Frozen: r.Frozen, Mode: r.Mode, Kind: "text", Text: "<&>😀", Total: 7}
	for _, kind := range []string{"text", "arguments", "request", "message", "protocol"} {
		p := base
		p.Kind = kind
		switch kind {
		case "request":
			p.Text = ""
			p.Messages = r.Request.Messages
			p.Total = 1
		case "message":
			p.Text = ""
			p.Blocks = r.Request.Messages[0].Blocks
			p.Total = 1
		case "protocol":
			p.Text = ""
			p.ProtocolEvidence = []protocolcore.ProtocolEvidenceValue{{Name: "native", Value: "<&>"}}
			p.Total = 1
		}
		p.BlockKind = "text"
		p.CallID = "call"
		p.ToolName = "read"
		p.NextCursor = "cursor"
		want, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		var got bytes.Buffer
		w := canonicalWriter{sink: &got}
		writePageWire(&w, p)
		w.flush()
		if w.err != nil || !bytes.Equal(got.Bytes(), want) {
			t.Fatalf("%s wire differs: %s / %s", kind, got.Bytes(), want)
		}
		for _, delta := range []int64{-1, 0, 1} {
			l := sourceFixtureLimits()
			l.CanonicalBytes = uint64(int64(len(want)) + delta)
			err := countPageWire(context.Background(), l.CanonicalBytes, p)
			if (err != nil) != (delta < 0) {
				t.Fatalf("page %s wire delta%d: %v", kind, delta, err)
			}
		}
	}
	// The generated page kind/container/cursor have their own finite ledger;
	// real visible fields retain the Source currency.
	f := base.Frozen
	payload := len(base.ExchangeID) + len(base.Text) + len(string(base.Mode)) + len(f.EnvironmentID) + len(f.EnvironmentDigest) + len(f.ClientEndpointID) + len(f.ProtocolPlanID) + len(f.RouteID)
	for _, delta := range []int64{-1, 0, 1} {
		l := sourceFixtureLimits()
		l.RetainedBytes = uint64(int64(payload) + delta)
		if err := base.ValidateWithin(context.Background(), l); (err != nil) != (delta < 0) {
			t.Fatalf("real page payload delta%d: %v", delta, err)
		}
	}
	for _, delta := range []int64{-1, 0, 1} {
		p := base
		p.Kind = "message"
		p.Text = ""
		p.Total = 1
		p.Blocks = []Block{{Kind: "text", Availability: AvailabilityRecorded, Text: "data"}}
		l := sourceFixtureLimits()
		l.StructureBytes = uint64(int64(unsafe.Sizeof(Block{})) + delta)
		if err := p.ValidateWithin(context.Background(), l); (err != nil) != (delta < 0) {
			t.Fatalf("real block structure delta%d: %v", delta, err)
		}
	}
	for _, size := range []int{PageBodyBytes - 1, PageBodyBytes, PageBodyBytes + 1} {
		p := base
		p.Text = strings.Repeat("x", size)
		p.Total = size
		if err := p.ValidateWithin(context.Background(), sourceFixtureLimits()); (err != nil) != (size > PageBodyBytes) {
			t.Fatalf("body boundary%d: %v", size, err)
		}
	}
	// 24 inline blocks can exceed the independent 1MiB wire ceiling.
	p := base
	p.Kind = "message"
	p.Text = ""
	p.Total = 24
	p.Blocks = make([]Block, 24)
	for i := range p.Blocks {
		p.Blocks[i] = Block{Kind: "text", Availability: AvailabilityRecorded, Text: strings.Repeat("x", 50<<10)}
	}
	if err := p.ValidateWithin(context.Background(), sourceFixtureLimits()); !errors.Is(err, ErrInvalidEvidence) {
		t.Fatal("page wire ceiling ignored")
	}
}

func TestWithinGeneratedControlBudgetAndCanonicalCredit(t *testing.T) {
	r := withinFixture(t, 1)
	p := ContentPage{ExchangeID: "e", Parent: r.Parent, Frozen: r.Frozen, Mode: r.Mode, Kind: "text", Text: "x", Total: 1, NextCursor: strings.Repeat("\"<&", 100)}
	// The only non-control wire in this body page is independently literal.
	const dataWire = len(`"exchangeId":"e",` + `"text":"x"`)
	for _, delta := range []int{-1, 0, 1} {
		l := sourceFixtureLimits()
		l.CanonicalBytes = uint64(dataWire + delta)
		if err := p.ValidateWithin(context.Background(), l); (err != nil) != (delta < 0) {
			t.Fatalf("actual data canonical delta%d: %v", delta, err)
		}
	}
	p.NextCursor = strings.Repeat("c", MaxPageCursorBytes+1)
	if err := p.ValidateWithin(context.Background(), sourceFixtureLimits()); !errors.Is(err, ErrInvalidEvidence) {
		t.Fatal("cursor limit widened")
	}
	c := newReadControlCost(context.Background())
	if err := c.add(readControlPayloadLimit, 0); err != nil {
		t.Fatal(err)
	}
	if err := c.add(1, 0); !errors.Is(err, ErrInvalidEvidence) {
		t.Fatal("control payload overflow")
	}
	c = newReadControlCost(context.Background())
	if err := c.add(0, math.MaxUint64); !errors.Is(err, ErrInvalidEvidence) {
		t.Fatal("control structure overflow")
	}
	if c.wireLimit(math.MaxInt64) != MaxPageBytes {
		t.Fatal("wire limit overflow or cap widening")
	}
	marker := Block{Kind: "deferred", Availability: AvailabilityRecorded, Deferred: &DeferredContent{ExchangeID: r.ExchangeID, Cursor: "c", EstimatedBytes: 1}}
	projection := withinProjection(r)
	projection.Page = &ProjectionPage{}
	projection.Request.Messages = []Message{{Role: "unknown", Blocks: []Block{marker}}}
	for _, mutate := range []func(*Block){func(b *Block) { b.Text = "real payload" }, func(b *Block) { b.Arguments = []byte(`null`) }, func(b *Block) { b.Agent = &AgentContext{AgentName: "a"} }, func(b *Block) { b.ProviderSource = "real" }} {
		bad := projection
		bad.Request.Messages = []Message{{Role: "unknown", Blocks: []Block{marker}}}
		mutate(&bad.Request.Messages[0].Blocks[0])
		if err := bad.ValidateWithin(context.Background(), sourceFixtureLimits()); !errors.Is(err, ErrInvalidEvidence) {
			t.Fatal("forged shell escaped real-data admission")
		}
	}
}

func TestWithinPageTypedFailures(t *testing.T) {
	r := withinFixture(t, 1)
	base := ContentPage{ExchangeID: r.ExchangeID, Parent: r.Parent, Frozen: r.Frozen, Mode: r.Mode, Kind: "request", Messages: r.Request.Messages, Total: 1}
	for _, tc := range []struct {
		name string
		edit func(*ContentPage)
	}{
		{"identity", func(p *ContentPage) { p.ExchangeID = "" }}, {"parent", func(p *ContentPage) { p.Parent = ParentRef{CaptureRunID: "a", ManualCaptureID: "b"} }}, {"frozen", func(p *ContentPage) { p.Frozen.EnvironmentRevision = 0 }},
		{"mode", func(p *ContentPage) { p.Mode = environment.ContentRecordingOff }}, {"offset overflow", func(p *ContentPage) { p.Offset = math.MaxInt; p.Total = math.MaxInt }}, {"negative", func(p *ContentPage) { p.Offset = -1 }},
		{"cursor", func(p *ContentPage) { p.NextCursor = string([]byte{255}) }}, {"window", func(p *ContentPage) { p.Messages = make([]Message, 25); p.Total = 25 }}, {"role", func(p *ContentPage) { p.Messages = []Message{{Role: "wrong", Blocks: r.Request.Messages[0].Blocks}} }},
		{"agent", func(p *ContentPage) {
			p.Messages = []Message{{Role: "user", Agent: &AgentContext{Author: "\n"}, Blocks: r.Request.Messages[0].Blocks}}
		}}, {"protocol extent", func(p *ContentPage) {
			p.Kind = "protocol"
			p.Messages = nil
			p.Total = protocolcore.MaxProtocolEvidenceValues + 1
		}},
		{"body extent", func(p *ContentPage) { p.Kind = "text"; p.Messages = nil; p.Total = math.MaxInt }}, {"ordinary", func(p *ContentPage) { p.BlockKind = string([]byte{255}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			tc.edit(&p)
			if err := p.ValidateWithin(context.Background(), sourceFixtureLimits()); !errors.Is(err, ErrInvalidEvidence) {
				t.Fatalf("invalid page admitted: %v", err)
			}
		})
	}
	for _, field := range []string{"kind", "availability", "size", "text", "raw", "tool", "provider", "agent", "estimate", "cursor", "exchange"} {
		p := base
		d := &DeferredContent{ExchangeID: p.ExchangeID, Cursor: "c", EstimatedBytes: 10}
		b := Block{Kind: "deferred", Availability: AvailabilityRecorded, Deferred: d}
		switch field {
		case "kind":
			b.Kind = "text"
		case "availability":
			b.Availability = AvailabilityOmitted
		case "size":
			b.OriginalSize = 1
		case "text":
			b.Text = "x"
		case "raw":
			b.Arguments = json.RawMessage(`{}`)
		case "tool":
			b.ToolError = true
		case "provider":
			b.ProviderSource = "native"
		case "agent":
			b.Agent = &AgentContext{AgentName: "a"}
		case "estimate":
			d.EstimatedBytes = -1
		case "cursor":
			d.Cursor = ""
		case "exchange":
			d.ExchangeID = "other"
		}
		p.Messages = []Message{{Role: "unknown", Blocks: []Block{b}}}
		if err := p.ValidateWithin(context.Background(), sourceFixtureLimits()); err == nil {
			t.Fatalf("non-shell %s accepted", field)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := base.ValidateWithin(ctx, sourceFixtureLimits()); !errors.Is(err, context.Canceled) {
		t.Fatal("page cancellation lost")
	}
}

func TestWithinRawDomainDepthAndPresentation(t *testing.T) {
	for _, raw := range []json.RawMessage{json.RawMessage(`{"x":"\ud800"}`), json.RawMessage([]byte{'"', 255, '"'}), json.RawMessage(" { \"x\" : 1 } ")} {
		r := withinFixture(t, 1)
		r.Request.Messages[0].Blocks = []Block{{Kind: "tool_call", Availability: AvailabilityRecorded, CallID: "c", ToolName: "read", Arguments: raw}}
		p, err := ProjectWithin(context.Background(), sourceFixtureLimits(), r, RequestViewFull)
		if err != nil || !bytes.Equal(p.Request.Messages[0].Blocks[0].Arguments, raw) {
			t.Fatalf("raw domain/ownership changed: %v", err)
		}
	}
	for _, depth := range []int{canonicalMaxNesting - canonicalMessageParentDepth, canonicalMaxNesting - canonicalMessageParentDepth + 1} {
		r := withinFixture(t, 1)
		r.Request.Messages[0].Blocks = []Block{{Kind: "tool_call", Availability: AvailabilityRecorded, CallID: "c", ToolName: "read", Arguments: json.RawMessage(strings.Repeat("[", depth) + "0" + strings.Repeat("]", depth))}}
		if err := withinProjection(r).ValidateWithin(context.Background(), sourceFixtureLimits()); (err != nil) != (depth > canonicalMaxNesting-canonicalMessageParentDepth) {
			t.Fatalf("raw depth%d: %v", depth, err)
		}
	}
	r := withinFixture(t, 1)
	r.Response.Blocks = []Block{{Kind: BlockKindReasoning, Availability: AvailabilityRecorded, Text: "same", ProviderSource: "native", ProviderKind: string(protocolcore.ProviderExtensionReasoningSummary)}, {Kind: BlockKindReasoning, Availability: AvailabilityRecorded, Text: "same", ProviderSource: "native", ProviderKind: string(protocolcore.ProviderExtensionReasoningContent)}}
	old, _ := Project(r, RequestViewFull)
	p, err := ProjectWithin(context.Background(), sourceFixtureLimits(), r, RequestViewFull)
	a, _ := json.Marshal(old)
	b, _ := json.Marshal(p)
	if err != nil || !bytes.Equal(a, b) || len(r.Response.Blocks) != 2 {
		t.Fatal("presentation fold changed retained facts")
	}
	if withinProjection(withinFixture(t, 4111)).Validate() == nil {
		t.Fatal("legacy count adapter activated candidate policy")
	}
}

func TestWithinPresentationWorkspaceParity(t *testing.T) {
	r := withinFixture(t, 1)
	text := strings.Repeat("x", 256<<10) + "\x00tail"
	r.Response.Blocks = make([]Block, 64)
	for i := range r.Response.Blocks {
		kind := protocolcore.ProviderExtensionReasoningSummary
		if i%3 == 1 {
			kind = protocolcore.ProviderExtensionReasoningContent
		}
		if i%3 == 2 {
			kind = protocolcore.ProviderExtensionThinking
		}
		r.Response.Blocks[i] = Block{Kind: BlockKindReasoning, Availability: AvailabilityRecorded, Text: text, ProviderSource: "native", ProviderKind: string(kind), OriginalSize: len(text), Agent: &AgentContext{AgentName: "a", Author: "b", Recipient: "c"}}
	}
	l := sourceFixtureLimits()
	l.Scratch.PayloadBytes = 4096
	l.Scratch.StructureBytes = 4096 + uint64(unsafe.Sizeof(Block{})) + uint64(unsafe.Sizeof(AgentContext{}))
	s, err := SourceFromRecordWithin(l, r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Measure(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := mergeDuplicateReadableReasoning(cloneBlocks(r.Response.Blocks))
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	p, err := ProjectWithin(context.Background(), l, r, RequestViewFull)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 1<<20 {
		t.Fatalf("presentation copied borrowed key bodies: allocated=%d", allocated)
	}
	w, err := presentationWorkspaceFor(r.Response)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("candidate allocation=%d; candidates=%d map workspace=%d key=%d index=%d; fixed projection=%d page=%d cursor maximum=%d", after.TotalAlloc-before.TotalAlloc, w.candidates, w.bytes, unsafe.Sizeof(presentationKey{}), unsafe.Sizeof(int(0)), unsafe.Sizeof(Projection{})-unsafe.Sizeof(Record{}), unsafe.Sizeof(ProjectionPage{}), 3*MaxPageCursorBytes)
	a, _ := json.Marshal(want)
	b, _ := json.Marshal(p.Response.Blocks)
	if !bytes.Equal(a, b) || len(r.Response.Blocks) != 64 {
		t.Fatal("fold changed tuple/priority/same-kind/order/NUL semantics")
	}
}

func TestSourceSharedCostOccurrenceParity(t *testing.T) {
	r := withinFixture(t, 2)
	r.Response = nil
	r.Request.System = nil
	r.Request.Tools = nil
	r.Request.ProtocolEvidence = nil
	a := &AgentContext{AgentName: "a", Author: "b", Recipient: "c"}
	b := Block{Kind: "text", Availability: AvailabilityRecorded, Text: "tail", OriginalSize: 4, Agent: a}
	for i := range r.Request.Messages {
		r.Request.Messages[i] = Message{Role: "user", Agent: a, Blocks: []Block{b}}
	}
	f := r.Frozen
	metaBytes := len(r.ExchangeID) + len(r.Parent.CaptureRunID) + len(r.Parent.ManualCaptureID) + len(f.EnvironmentID) + len(f.EnvironmentDigest) + len(f.ClientEndpointID) + len(f.ProtocolPlanID) + len(f.RouteID) + len(string(r.Mode)) + len(r.Request.RequestedModel) + len(r.Request.EffectiveModel)
	// Each repeated message holds role(4), block kind(4), availability(8),
	// text(4), message agent(3), and block agent(3): 26 bytes per occurrence.
	wire, _ := json.Marshal(r)
	blockWire, _ := json.Marshal(b)
	agentWire, _ := json.Marshal(a)
	want := RecordCost{RetainedBytes: uint64(metaBytes + 2*26), StructureBytes: uint64(unsafe.Sizeof(Record{})) + 2*(uint64(unsafe.Sizeof(Message{}))+uint64(unsafe.Sizeof(Block{}))+2*uint64(unsafe.Sizeof(AgentContext{}))), CanonicalBytes: uint64(len(wire)), TranscriptNodes: 2, MaxLogicalBlockBytes: uint64(len(blockWire)), MaxPhysicalSlots: 1, MaxAgentBytes: uint64(len(agentWire))}
	s, err := SourceFromRecordWithin(sourceFixtureLimits(), r)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Measure(context.Background())
	if err != nil || got != want {
		t.Fatalf("Source per-occurrence cost changed: got=%+v want=%+v err=%v", got, want, err)
	}
	for _, delta := range []int64{-1, 0, 1} {
		l := sourceFixtureLimits()
		l.RetainedBytes = uint64(int64(want.RetainedBytes) + delta)
		l.StructureBytes = uint64(int64(want.StructureBytes) + delta)
		if err := withinProjection(r).ValidateWithin(context.Background(), l); (err != nil) != (delta < 0) {
			t.Fatalf("independent exact cost delta%d: %v", delta, err)
		}
	}
}

func TestWithinEncodingScratchBeforeGrowth(t *testing.T) {
	r := withinFixture(t, 1)
	p := withinProjection(r)
	page := ContentPage{ExchangeID: r.ExchangeID, Parent: r.Parent, Frozen: r.Frozen, Mode: r.Mode, Kind: "request", Messages: r.Request.Messages, Total: 1}
	for _, currency := range []string{"payload", "structure"} {
		for _, delta := range []int64{-1, 0, 1} {
			l := sourceFixtureLimits()
			if currency == "payload" {
				l.Scratch.PayloadBytes = uint64(4096 + delta)
			} else {
				l.Scratch.StructureBytes = uint64(int64(unsafe.Sizeof(canonicalWriter{})) - int64(unsafe.Sizeof(canonicalWriter{}.scratch)) + int64(unsafe.Sizeof(wireCounter{})) + delta)
			}
			if err := p.ValidateWithin(context.Background(), l); (err != nil) != (delta < 0) {
				t.Errorf("projection scratch %s delta%d: %v", currency, delta, err)
			}
			if err := page.ValidateWithin(context.Background(), l); (err != nil) != (delta < 0) {
				t.Errorf("page scratch %s delta%d: %v", currency, delta, err)
			}
		}
	}
}

func TestWithinMinimalSourceAdmittedScratch(t *testing.T) {
	request := protocolcore.Request{RequestedModel: "m", EffectiveModel: "m", Messages: []protocolcore.Message{{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{{Kind: protocolcore.BlockText, Text: "private"}}}}}
	l := sourceFixtureLimits()
	l.Scratch.PayloadBytes = 4096
	l.Scratch.StructureBytes = uint64(unsafe.Sizeof(Block{}))
	policy := environment.ContentRecordingPolicy{Mode: environment.ContentRecordingMetadataOnly, RetentionDays: 1}
	at := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	s, err := NewSourceWithin(l, "minimal-scratch", frozenFixture(), policy, at, request, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Measure(context.Background()); err != nil {
		t.Fatal(err)
	}
	r, err := NewRecord("minimal-scratch", frozenFixture(), policy, at, request, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Presentation = RequestPresentation{Mode: RequestPresentationCheckpoint}
	if err := withinProjection(r).ValidateWithin(context.Background(), l); err != nil {
		t.Errorf("Source-admitted full view narrowed: %v", err)
	}
	page := ContentPage{ExchangeID: r.ExchangeID, Parent: r.Parent, Frozen: r.Frozen, Mode: r.Mode, Kind: "request", Messages: r.Request.Messages, Total: 1}
	if err := page.ValidateWithin(context.Background(), l); err != nil {
		t.Errorf("Source-admitted page narrowed: %v", err)
	}
	if _, err := ProjectWithin(context.Background(), l, r, RequestViewFull); err != nil {
		t.Errorf("Source-admitted projection narrowed: %v", err)
	}
}

func TestWithinCompletePreflightRejectsHiddenInvalidTail(t *testing.T) {
	for _, invalid := range []string{"last role", "hidden raw"} {
		r := withinFixture(t, 4111)
		r.Presentation = RequestPresentation{Mode: RequestPresentationIncremental, InheritedMessageCount: 4110}
		if invalid == "last role" {
			r.Request.Messages[4110].Role = "unsupported"
		} else {
			r.Request.Messages[4109].Blocks = []Block{{Kind: "tool_call", Availability: AvailabilityRecorded, CallID: "c", ToolName: "read", Arguments: json.RawMessage(`{`)}}
		}
		if _, err := ProjectWithin(context.Background(), sourceFixtureLimits(), r, RequestViewIncremental); !errors.Is(err, ErrInvalidEvidence) {
			t.Fatalf("complete preflight skipped %s: %v", invalid, err)
		}
	}
}
