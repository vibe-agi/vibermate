package exchangecontent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/openairesponses"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

// This baseline catches the real wire/retained-canonical capacity mismatch.
func TestSourceEscapedResponseBaseline(t *testing.T) {
	request, _ := evidenceFixture(t)
	codec, err := openairesponses.New(openairesponses.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range []string{"x", "&"} {
		wire := []byte(`{"id":"resp-fixture","status":"completed","model":"model","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"` + strings.Repeat(ch, 6<<20) + `"}]}]}`)
		wire = append(wire, []byte(strings.Repeat(" ", 61))...)
		t.Logf("literal %q wire bytes=%d", ch, len(wire))
		response, _, err := codec.DecodeProviderResponse(request, wire)
		if err != nil {
			t.Fatal(err)
		}
		_, err = NewRecord("escaped", frozenFixture(), environment.DefaultContentRecordingPolicy(), time.Date(2026, 8, 8, 1, 2, 3, 0, time.UTC), request, &response)
		if ch == "x" && err != nil {
			t.Fatal(err)
		}
		if ch == "&" && err == nil {
			t.Fatal("legacy convenience unexpectedly accepted escaped response")
		}
		s, err := NewSourceWithin(sourceFixtureLimits(), "escaped", frozenFixture(), environment.DefaultContentRecordingPolicy(), time.Date(2026, 8, 8, 1, 2, 3, 0, time.UTC), request, &response)
		if err != nil {
			t.Fatalf("complete source: %v", err)
		}
		var tail string
		err = s.Walk(context.Background(), func(p Part, _ int, m MessageSource) error {
			if p == ResponsePart {
				return m.WalkBlocks(context.Background(), func(b Block) error { tail = b.Text; return nil })
			}
			return nil
		})
		if err != nil || tail != strings.Repeat(ch, 6<<20) {
			t.Fatalf("complete tail changed: %v", err)
		}
		cost, err := s.Measure(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if ch == "&" && cost.MaxLogicalBlockBytes <= MaxEncodedBytes {
			t.Fatalf("escaped logical block cost: %+v", cost)
		}
	}
}

// Local finite policy for component experiments; these are not release defaults.
func sourceFixtureLimits() SourceLimits {
	return SourceLimits{Semantic: protocolcore.ResourceLimits{Request: protocolcore.ResourceCost{PayloadBytes: 64 << 20, StructureBytes: 64 << 20}, Response: protocolcore.ResourceCost{PayloadBytes: 64 << 20, StructureBytes: 64 << 20}}, Scratch: protocolcore.ResourceCost{PayloadBytes: 512 << 20, StructureBytes: 128 << 20}, CanonicalBytes: 128 << 20, RetainedBytes: 128 << 20, StructureBytes: 64 << 20}
}
func TestSourceCompleteLongHistory(t *testing.T) {
	for _, count := range []int{4097, 4111, 16384} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			r, _ := evidenceFixture(t)
			r.System = nil
			r.Messages = make([]protocolcore.Message, count)
			text := "tail"
			if count == 4111 {
				text = strings.Repeat("x", 1024)
			}
			for i := range r.Messages {
				r.Messages[i] = protocolcore.Message{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{{Kind: protocolcore.BlockText, Text: text}}}
			}
			s, err := NewSourceWithin(sourceFixtureLimits(), "long", frozenFixture(), environment.DefaultContentRecordingPolicy(), time.Date(2026, 8, 8, 1, 2, 3, 0, time.UTC), r, nil)
			if err != nil {
				t.Fatal(err)
			}
			seen := 0
			err = s.Walk(context.Background(), func(p Part, i int, m MessageSource) error {
				if p != RequestPart || i != seen {
					t.Fatal("order")
				}
				seen++
				return m.WalkBlocks(context.Background(), func(b Block) error {
					if b.Text != text {
						t.Fatal("tail omitted")
					}
					return nil
				})
			})
			if err != nil || seen != count {
				t.Fatalf("walk %d/%d %v", seen, count, err)
			}
			r.Messages[count-1].Role = "invalid"
			if _, err = NewSourceWithin(sourceFixtureLimits(), "long", frozenFixture(), environment.DefaultContentRecordingPolicy(), time.Date(2026, 8, 8, 1, 2, 3, 0, time.UTC), r, nil); err == nil {
				t.Fatal("invalid last message admitted")
			}
		})
	}
}

func TestSourceRetainedScratchBeforeLeafCopy(t *testing.T) {
	request, response := evidenceFixture(t)
	r, err := NewRecord("scratch", frozenFixture(), environment.DefaultContentRecordingPolicy(), time.Now(), request, &response)
	if err != nil {
		t.Fatal(err)
	}
	r.Request.Messages[0].Blocks = []Block{{Kind: "tool_call", Availability: AvailabilityRecorded, CallID: "c", ToolName: "f", Arguments: json.RawMessage(`{"x":"` + strings.Repeat("x", 1<<20) + `"}`)}}
	limits := sourceFixtureLimits()
	limits.Scratch.PayloadBytes = 8192
	if _, err := SourceFromRecordWithin(limits, r); err == nil {
		t.Fatal("already retained raw leaf copied without scratch reservation")
	}
}

func TestSourceOwnershipOptionsAndCancellation(t *testing.T) {
	r, v := evidenceFixture(t)
	schema, err := protocolcore.NewJSONObject([]byte(`{}`), 100)
	if err != nil {
		t.Fatal(err)
	}
	r.Tools = []protocolcore.ToolDefinition{{Name: "f", InputSchema: schema}}
	r.ToolNamespaces = []protocolcore.ToolNamespace{{Name: "ns", Description: "namespace", Tools: []protocolcore.ToolDefinition{{Name: "read_file", InputSchema: schema}}}}
	v.Blocks[0].ToolCall.Namespace = "ns"
	at := time.Date(2026, 1, 31, 23, 0, 0, 0, time.FixedZone("local", 8*3600))
	for _, test := range []struct {
		name   string
		opts   []RecordOption
		parent ParentRef
		bad    bool
	}{{name: "omitted"}, {name: "empty", opts: []RecordOption{WithParentRef(ParentRef{})}}, {name: "valid", opts: []RecordOption{WithParentRef(ParentRef{CaptureRunID: "run"})}, parent: ParentRef{CaptureRunID: "run"}}, {name: "last wins", opts: []RecordOption{WithParentRef(ParentRef{CaptureRunID: "first"}), WithParentRef(ParentRef{ManualCaptureID: "last"})}, parent: ParentRef{ManualCaptureID: "last"}}, {name: "ambiguous", opts: []RecordOption{WithParentRef(ParentRef{CaptureRunID: "a", ManualCaptureID: "b"})}, bad: true}, {name: "invalid first", opts: []RecordOption{WithParentRef(ParentRef{CaptureRunID: "a", ManualCaptureID: "b"}), WithParentRef(ParentRef{CaptureRunID: "run"})}, bad: true}, {name: "zero", opts: []RecordOption{{}}, bad: true}} {
		t.Run(test.name, func(t *testing.T) {
			source, err := NewSourceWithin(sourceFixtureLimits(), "parent", frozenFixture(), environment.DefaultContentRecordingPolicy(), at, r, &v, test.opts...)
			record, oldErr := NewRecord("parent", frozenFixture(), environment.DefaultContentRecordingPolicy(), at, r, &v, test.opts...)
			if (err != nil) != test.bad || (oldErr != nil) != test.bad {
				t.Fatalf("source %v record %v", err, oldErr)
			}
			if test.bad {
				return
			}
			if source.Metadata().Parent != test.parent || record.Parent != test.parent {
				t.Fatal("parent changed")
			}
			retained, err := SourceFromRecordWithin(sourceFixtureLimits(), record)
			if err != nil {
				t.Fatal(err)
			}
			a, err := source.Measure(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			b, err := retained.Measure(context.Background())
			if err != nil || a != b {
				t.Fatalf("logical cost currency differs %+v/%+v %v", a, b, err)
			}
			var got bytes.Buffer
			if err := writeSourceCanonical(context.Background(), &got, source); err != nil {
				t.Fatal(err)
			}
			want, _ := json.Marshal(record)
			if !bytes.Equal(want, got.Bytes()) {
				t.Fatal("source projection changed legacy record")
			}
			if source.Metadata().RecordedAt.Location() != time.UTC || !source.Metadata().ExpiresAt.Equal(record.ExpiresAt) {
				t.Fatal("time freeze changed")
			}
		})
	}
	source, err := NewSourceWithin(sourceFixtureLimits(), "owned", frozenFixture(), environment.DefaultContentRecordingPolicy(), at, r, &v)
	if err != nil {
		t.Fatal(err)
	}
	metadata := source.Metadata()
	metadata.Request.Tools[0].Name = "mutated"
	metadata.Request.ProtocolEvidence = append(metadata.Request.ProtocolEvidence, protocolcore.ProtocolEvidenceValue{Name: "new", Value: "new"})
	metadata.Response.ID = "mutated"
	if source.Metadata().Request.Tools[0].Name == "mutated" || source.Metadata().Response.ID == "mutated" {
		t.Fatal("metadata alias")
	}
	sentinel := errors.New("stop")
	calls := 0
	if err := source.Walk(context.Background(), func(Part, int, MessageSource) error { calls++; return sentinel }); err != sentinel || calls != 1 {
		t.Fatal("walk continued after error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := source.Walk(ctx, func(Part, int, MessageSource) error { t.Fatal("late callback"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if source.Walk(nil, func(Part, int, MessageSource) error { return nil }) == nil || source.Walk(context.Background(), nil) == nil || (*Source)(nil).Walk(context.Background(), func(Part, int, MessageSource) error { return nil }) == nil {
		t.Fatal("invalid traversal accepted")
	}
	record, err := NewRecord("retained", frozenFixture(), environment.DefaultContentRecordingPolicy(), at, r, &v)
	if err != nil {
		t.Fatal(err)
	}
	record.Request.Messages[0].Agent = &AgentContext{AgentName: "agent"}
	record.Request.Messages[0].Blocks[0].Agent = &AgentContext{AgentName: "block"}
	record.Request.Messages[0].Blocks = append(record.Request.Messages[0].Blocks, Block{Kind: "tool_call", Availability: AvailabilityRecorded, CallID: "call", ToolName: "f", Arguments: json.RawMessage(`{"x":1}`)})
	retained, err := SourceFromRecordWithin(sourceFixtureLimits(), record)
	if err != nil {
		t.Fatal(err)
	}
	_ = retained.Walk(context.Background(), func(p Part, _ int, m MessageSource) error {
		if p != RequestPart {
			return nil
		}
		h := m.Header()
		h.Agent.AgentName = "changed"
		return m.WalkBlocks(context.Background(), func(b Block) error {
			if b.Agent != nil {
				b.Agent.AgentName = "changed"
			}
			if len(b.Arguments) > 0 {
				b.Arguments[0] = '['
			}
			return nil
		})
	})
	if record.Request.Messages[0].Agent.AgentName != "agent" || record.Request.Messages[0].Blocks[0].Agent.AgentName != "block" || record.Request.Messages[0].Blocks[1].Arguments[0] != '{' {
		t.Fatal("borrowed record changed through callback")
	}
}

func TestSourceRetainedLongCostAndBounds(t *testing.T) {
	r, v := evidenceFixture(t)
	record, err := NewRecord("retained-long", frozenFixture(), environment.DefaultContentRecordingPolicy(), time.Now(), r, &v)
	if err != nil {
		t.Fatal(err)
	}
	record.Request.System = []Block{{Kind: "text", Availability: AvailabilityRecorded, Text: "system", OriginalSize: 6}}
	record.Request.Messages = make([]Message, 4111)
	for i := range record.Request.Messages {
		record.Request.Messages[i] = Message{Role: "user", Blocks: []Block{{Kind: "text", Availability: AvailabilityRecorded, Text: strings.Repeat("x", 1024), OriginalSize: 1024}}}
	}
	record.Request.Messages[4110].Blocks[0].Text = "sentinel"
	record.Request.Messages[4110].Blocks = append(record.Request.Messages[4110].Blocks, Block{Kind: "text", Availability: AvailabilityRecorded, Text: strings.Repeat("&", 6<<20), OriginalSize: 6 << 20}, Block{Kind: "text", Availability: AvailabilityRecorded, Text: strings.Repeat("&", 6<<20), OriginalSize: 6 << 20})
	record.Request.Messages[4110].Agent = &AgentContext{AgentName: strings.Repeat("&", 512), Author: strings.Repeat("&", 512), Recipient: strings.Repeat("&", 512)}
	limits := sourceFixtureLimits()
	s, err := SourceFromRecordWithin(limits, record)
	if err != nil {
		t.Fatal(err)
	}
	cost, err := s.Measure(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(record)
	if cost.CanonicalBytes != uint64(len(want)) || cost.TranscriptNodes != 4112 || cost.MaxPhysicalSlots != 5 || cost.MaxAgentBytes >= 4096 {
		t.Fatalf("cost %+v", cost)
	}
	limits.CanonicalBytes = 1
	if _, err := s.Measure(context.Background()); err != nil {
		t.Fatal("source limits alias caller")
	}
	if record.Validate() == nil {
		t.Fatal("old record cap/count changed")
	}
	if _, err := CanonicalJSON(record); err == nil {
		t.Fatal("byte convenience accepted oversized")
	}
	for _, bound := range []string{"canonical", "retained", "structure"} {
		tight := sourceFixtureLimits()
		switch bound {
		case "canonical":
			tight.CanonicalBytes = cost.CanonicalBytes - 1
		case "retained":
			tight.RetainedBytes = cost.RetainedBytes - 1
		case "structure":
			tight.StructureBytes = cost.StructureBytes - 1
		}
		if _, err := SourceFromRecordWithin(tight, record); err == nil {
			t.Fatalf("%s bound ignored", bound)
		}
	}
	record.Request.Messages[4110].Blocks = append(record.Request.Messages[4110].Blocks, Block{Deferred: &DeferredContent{ExchangeID: "x", Cursor: "c"}})
	if _, err := SourceFromRecordWithin(sourceFixtureLimits(), record); err == nil {
		t.Fatal("deferred complete evidence admitted")
	}
}

func TestSourceNoSecondEntireRequestProjection(t *testing.T) {
	request, _ := evidenceFixture(t)
	request.System = nil
	request.Tools = nil
	request.Messages = make([]protocolcore.Message, 16384)
	for i := range request.Messages {
		request.Messages[i] = protocolcore.Message{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{{Kind: protocolcore.BlockText, Text: "x"}}}
	}
	runtime.GC()
	var a, b runtime.MemStats
	runtime.ReadMemStats(&a)
	s, err := NewSourceWithin(sourceFixtureLimits(), "allocation", frozenFixture(), environment.ContentRecordingPolicy{Mode: environment.ContentRecordingMetadataOnly, RetentionDays: 1}, time.Now(), request, nil)
	if err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&b)
	t.Logf("16384 borrowed messages allocated=%d", b.TotalAlloc-a.TotalAlloc)
	if b.TotalAlloc-a.TotalAlloc > 32<<20 {
		t.Fatal("source materialized another request")
	}
	runtime.GC()
	runtime.ReadMemStats(&b)
	if b.HeapAlloc > a.HeapAlloc+(1<<20) {
		t.Fatal("source retained an entire projected request")
	}
	runtime.KeepAlive(request)
	runtime.KeepAlive(s)
	if _, err := s.Measure(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSourceExtensionsAndRecordingModes(t *testing.T) {
	r, v := evidenceFixture(t)
	r.System = []protocolcore.ContentBlock{{Kind: protocolcore.BlockText, Text: "system"}}
	r.Messages[0].Agent = &protocolcore.AgentMessageContext{AgentName: "message"}
	r.ProtocolEvidence = []protocolcore.ProtocolEvidenceValue{{Name: "native", Value: "present"}}
	inputs := []struct {
		source    protocolcore.ProviderExtensionSource
		kind      protocolcore.ProviderExtensionKind
		fragments []string
	}{{protocolcore.ProviderExtensionSourceAnthropicMessages, protocolcore.ProviderExtensionThinking, []string{`{"thinking":"private /Users/alice/path","signature":"opaque-signature"}`}}, {protocolcore.ProviderExtensionSourceAnthropicMessages, protocolcore.ProviderExtensionThinking, []string{`{"thinking":"readable","signature":""}`}}, {protocolcore.ProviderExtensionSourceAnthropicMessages, protocolcore.ProviderExtensionThinking, []string{`{"thinking":"split","signature":"si"}`, `{"thinking":" text","signature":"g"}`}}, {protocolcore.ProviderExtensionSourceOpenAIResponses, protocolcore.ProviderExtensionReasoningEncryptedContent, []string{`"encrypted"`}}, {protocolcore.ProviderExtensionSourceOpenAIResponses, protocolcore.ProviderExtensionInputImage, []string{`{"image_url":"data:private"}`}}, {protocolcore.ProviderExtensionSourceOpenAIResponses, protocolcore.ProviderExtensionReasoningSummary, []string{`{"text":"duplicate"}`}}, {protocolcore.ProviderExtensionSourceOpenAIResponses, protocolcore.ProviderExtensionReasoningContent, []string{`"duplicate"`}}}
	for index, input := range inputs {
		var fragments [][]byte
		for _, fragment := range input.fragments {
			fragments = append(fragments, []byte(fragment))
		}
		ext, err := protocolcore.NewProviderExtension(input.source, input.kind, fmt.Sprintf("path%d", index), fragments)
		if err != nil {
			t.Fatal(err)
		}
		block, err := protocolcore.NewProviderExtensionBlock(ext)
		if err != nil {
			t.Fatal(err)
		}
		block.Agent = &protocolcore.AgentMessageContext{AgentName: "block"}
		role := protocolcore.RoleAssistant
		if input.kind == protocolcore.ProviderExtensionInputImage {
			role = protocolcore.RoleUser
		}
		r.Messages = append(r.Messages, protocolcore.Message{Role: role, Blocks: []protocolcore.ContentBlock{{Kind: protocolcore.BlockText, Text: "before"}, block, {Kind: protocolcore.BlockText, Text: "after"}}})
		v.ProviderExtensions = append(v.ProviderExtensions, ext)
	}
	for _, mode := range []environment.ContentRecordingMode{environment.ContentRecordingFull, environment.ContentRecordingMetadataOnly} {
		t.Run(string(mode), func(t *testing.T) {
			p := environment.ContentRecordingPolicy{Mode: mode, RetentionDays: 2}
			record, err := NewRecord("extensions", frozenFixture(), p, time.Date(2026, 2, 28, 1, 2, 3, 0, time.UTC), r, &v)
			if err != nil {
				t.Fatal(err)
			}
			s, err := NewSourceWithin(sourceFixtureLimits(), "extensions", frozenFixture(), p, time.Date(2026, 2, 28, 1, 2, 3, 0, time.UTC), r, &v)
			if err != nil {
				t.Fatal(err)
			}
			var got bytes.Buffer
			if err := writeSourceCanonical(context.Background(), &got, s); err != nil {
				t.Fatal(err)
			}
			want, _ := json.Marshal(record)
			if !bytes.Equal(want, got.Bytes()) {
				t.Fatalf("legacy extension bytes differ\nwant %s\ngot %s", want, got.Bytes())
			}
			if !bytes.Contains(got.Bytes(), []byte("sha256:ea9960e6a05a735aa10469bcee49059e3cb0fba43273c20e360ce3e4dfd94916")) || !bytes.Contains(got.Bytes(), []byte("sha256:a543997d84f12798350c09bdef2cdb171bf41ed3e4a5f808af2feb0c56263009")) {
				t.Fatal("opaque signature identity changed")
			}
			if mode == environment.ContentRecordingMetadataOnly && (bytes.Contains(got.Bytes(), []byte("private")) || bytes.Contains(got.Bytes(), []byte("duplicate"))) {
				t.Fatal("metadata-only retained plaintext")
			}
			parts := []Part{}
			err = s.Walk(context.Background(), func(p Part, index int, m MessageSource) error {
				parts = append(parts, p)
				h := m.Header()
				var blocks []Block
				err := m.WalkBlocks(context.Background(), func(b Block) error {
					blocks = append(blocks, b)
					if b.Agent != nil && b.Agent.AgentName != "block" {
						t.Fatal("extension agent changed")
					}
					return nil
				})
				if err != nil {
					return err
				}
				if h.BlockCount != len(blocks) {
					t.Fatal("expanded block count changed")
				}
				if p == RequestPart {
					var encoded bytes.Buffer
					if err := WriteCanonicalMessage(&encoded, m); err != nil {
						return err
					}
					expected, _ := json.Marshal(record.Request.Messages[index])
					if !bytes.Equal(expected, encoded.Bytes()) {
						t.Fatal("message bytes changed")
					}
				}
				return nil
			})
			if err != nil || len(parts) != len(r.Messages)+2 || parts[0] != SystemPart || parts[1] != RequestPart || parts[len(parts)-1] != ResponsePart {
				t.Fatalf("parts %v err %v", parts, err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			blocks := 0
			err = s.Walk(ctx, func(p Part, index int, m MessageSource) error {
				calls++
				if p == RequestPart && index == 1 {
					return m.WalkBlocks(ctx, func(Block) error { blocks++; cancel(); return nil })
				}
				return nil
			})
			if !errors.Is(err, context.Canceled) || calls != 3 || blocks != 1 {
				t.Fatalf("late cancellation callbacks=%d blocks=%d error=%v", calls, blocks, err)
			}
		})
	}
	if _, err := NewSourceWithin(sourceFixtureLimits(), "off", frozenFixture(), environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, time.Now(), r, &v); err == nil {
		t.Fatal("Off source created")
	}
}

func TestSourceMetadataOnlyDoesNotMaterializePrivatePayload(t *testing.T) {
	r, _ := evidenceFixture(t)
	raw := []byte(`{"thinking":"` + strings.Repeat("x", 6<<20) + `","signature":"sig"}`)
	ext, err := protocolcore.NewProviderExtension(protocolcore.ProviderExtensionSourceAnthropicMessages, protocolcore.ProviderExtensionThinking, "path", [][]byte{raw})
	if err != nil {
		t.Fatal(err)
	}
	block, err := protocolcore.NewProviderExtensionBlock(ext)
	if err != nil {
		t.Fatal(err)
	}
	args, err := protocolcore.NewJSONObject([]byte(`{"private":"`+strings.Repeat("x", 3<<20)+`"}`), protocolcore.MaxToolJSONBytes)
	if err != nil {
		t.Fatal(err)
	}
	key, err := protocolcore.NewCallKey("test", "call")
	if err != nil {
		t.Fatal(err)
	}
	r.Messages[0].Blocks = []protocolcore.ContentBlock{block, {Kind: protocolcore.BlockToolCall, ToolCall: protocolcore.ToolCall{Key: key, Name: "f", Arguments: args}}}
	r.Messages[0].Role = protocolcore.RoleAssistant
	runtime.GC()
	var a, b runtime.MemStats
	runtime.ReadMemStats(&a)
	s, err := NewSourceWithin(sourceFixtureLimits(), "private", frozenFixture(), environment.ContentRecordingPolicy{Mode: environment.ContentRecordingMetadataOnly, RetentionDays: 1}, time.Now(), r, nil)
	if err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&b)
	t.Logf("metadata-only 6MiB thinking/3MiB arguments allocation=%d", b.TotalAlloc-a.TotalAlloc)
	if b.TotalAlloc-a.TotalAlloc > 1<<20 {
		t.Fatal("private plaintext materialized to discard")
	}
	seen := 0
	err = s.Walk(context.Background(), func(_ Part, _ int, m MessageSource) error {
		return m.WalkBlocks(context.Background(), func(b Block) error {
			seen++
			if b.Text != "" || len(b.Arguments) != 0 {
				t.Fatal("omitted body retained")
			}
			if b.Kind == "tool_call" && b.OriginalSize != args.ByteLen() {
				t.Fatal("argument original size changed")
			}
			return nil
		})
	})
	if err != nil || seen != 2 {
		t.Fatal(err)
	}
	copy := args.Bytes()
	copy[0] = '['
	if args.ByteLen() != 3<<20+14 || args.Bytes()[0] != '{' {
		t.Fatal("ByteLen/Bytes ownership changed")
	}
}

func TestSourceEmptyTerminalAndLimits(t *testing.T) {
	r, v := evidenceFixture(t)
	v.Blocks = nil
	v.ProviderExtensions = nil
	v.StopReason = protocolcore.StopReasonEndTurn
	s, err := NewSourceWithin(sourceFixtureLimits(), "empty", frozenFixture(), environment.DefaultContentRecordingPolicy(), time.Now(), r, &v)
	if err != nil {
		t.Fatal(err)
	}
	if s.Metadata().Response == nil || !s.Metadata().Response.EmptyOutput {
		t.Fatal("empty terminal metadata lost")
	}
	nodes := 0
	if err := s.Walk(context.Background(), func(p Part, _ int, _ MessageSource) error {
		if p == ResponsePart {
			t.Fatal("invented empty assistant")
		}
		nodes++
		return nil
	}); err != nil || nodes != 1 {
		t.Fatal(err)
	}
	for _, mutate := range []func(*SourceLimits){func(l *SourceLimits) { l.CanonicalBytes = 0 }, func(l *SourceLimits) { l.RetainedBytes = ^uint64(0) }, func(l *SourceLimits) { l.StructureBytes = 0 }, func(l *SourceLimits) { l.Scratch.PayloadBytes = 0 }, func(l *SourceLimits) { l.Semantic.Request.StructureBytes = 0 }} {
		l := sourceFixtureLimits()
		mutate(&l)
		if l.Validate() == nil {
			t.Fatal("zero/overflow limit admitted")
		}
	}
	l := sourceFixtureLimits()
	l.Semantic.Request.PayloadBytes = 1
	if _, err := NewSourceWithin(l, "tiny", frozenFixture(), environment.DefaultContentRecordingPolicy(), time.Now(), r, &v); err == nil {
		t.Fatal("semantic budget bypassed")
	}
	l = sourceFixtureLimits()
	l.Scratch.PayloadBytes = 1
	if _, err := NewSourceWithin(l, "tiny", frozenFixture(), environment.DefaultContentRecordingPolicy(), time.Now(), r, &v); err == nil {
		t.Fatal("scratch budget bypassed")
	}
}

func FuzzSourceThinkingMatchesLegacy(f *testing.F) {
	for _, raw := range []string{`{"thinking":"text","signature":"sig"}`, `{"thinking":"<&>\u2028","SIGNATURE":"sig","signature":null}`, `{"thinking":"text","signature":12,"signature":"sig"}`, `{"thinking":"text","nested":{"signature":"wrong"},"signature":"s\u0069g"}`, `null`, `[1,2]`, `{"thinking":12,"signature":"sig"}`, `{"ſignature":"sig"}`, `{"signature":"first","signature":"last"}`, `{"signature":`} {
		f.Add([]byte(raw), true)
		f.Add([]byte(raw), false)
	}
	f.Fuzz(func(t *testing.T, raw []byte, full bool) {
		if len(raw) > 4096 {
			t.Skip()
		}
		copy := append([]byte(nil), raw...)
		ext, err := protocolcore.NewProviderExtension(protocolcore.ProviderExtensionSourceAnthropicMessages, protocolcore.ProviderExtensionThinking, "path", [][]byte{raw})
		if err != nil {
			if !bytes.Equal(copy, raw) {
				t.Fatal("invalid fragment mutated")
			}
			return
		}
		block, err := protocolcore.NewProviderExtensionBlock(ext)
		if err != nil {
			t.Fatal(err)
		}
		r := protocolcore.Request{RequestedModel: "m", EffectiveModel: "m", Messages: []protocolcore.Message{{Role: protocolcore.RoleAssistant, Blocks: []protocolcore.ContentBlock{block}}}}
		mode := environment.ContentRecordingMetadataOnly
		if full {
			mode = environment.ContentRecordingFull
		}
		p := environment.ContentRecordingPolicy{Mode: mode, RetentionDays: 1}
		at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		record, oldErr := NewRecord("fuzz", frozenFixture(), p, at, r, nil)
		s, newErr := NewSourceWithin(sourceFixtureLimits(), "fuzz", frozenFixture(), p, at, r, nil)
		if (oldErr != nil) != (newErr != nil) {
			t.Fatalf("constructor errors differ: %v %v", oldErr, newErr)
		}
		if oldErr != nil {
			return
		}
		want, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		var got bytes.Buffer
		if err := writeSourceCanonical(context.Background(), &got, s); err != nil || !bytes.Equal(want, got.Bytes()) {
			t.Fatalf("thinking projection changed raw=%q\nwant=%s\ngot=%s error=%v", raw, want, got.Bytes(), err)
		}
		if !bytes.Equal(copy, raw) {
			t.Fatal("fragment mutated")
		}
	})
}

func TestSourceExpandedBlockCountRoundTrip(t *testing.T) {
	ext, err := protocolcore.NewProviderExtension(protocolcore.ProviderExtensionSourceAnthropicMessages, protocolcore.ProviderExtensionThinking, "path", [][]byte{[]byte(`{"thinking":"r","signature":"sig"}`)})
	if err != nil {
		t.Fatal(err)
	}
	r := protocolcore.Request{RequestedModel: "m", EffectiveModel: "m", Messages: []protocolcore.Message{{Role: protocolcore.RoleAssistant}}}
	for i := 0; i < 4096; i++ {
		block := protocolcore.ContentBlock{Kind: protocolcore.BlockText}
		if i < 256 {
			block = protocolcore.ContentBlock{Kind: protocolcore.BlockProviderExtension, ProviderExtension: ext}
		}
		r.Messages[0].Blocks = append(r.Messages[0].Blocks, block)
	}
	v := protocolcore.Response{ID: "response", RequestedModel: "m", EffectiveModel: "m", ReportedModel: "m", StopReason: protocolcore.StopReasonEndTurn, Blocks: make([]protocolcore.ContentBlock, 4096), ProviderExtensions: []protocolcore.ProviderExtension{ext}}
	for i := range v.Blocks {
		v.Blocks[i].Kind = protocolcore.BlockText
	}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s, err := NewSourceWithin(sourceFixtureLimits(), "expanded", frozenFixture(), environment.DefaultContentRecordingPolicy(), at, r, &v)
	if err != nil {
		t.Fatal(err)
	}
	meta := s.Metadata()
	record := Record{ExchangeID: meta.ExchangeID, Parent: meta.Parent, Frozen: meta.Frozen, Mode: meta.Mode, RecordedAt: meta.RecordedAt, ExpiresAt: meta.ExpiresAt, Request: Request{RequestedModel: meta.Request.RequestedModel, EffectiveModel: meta.Request.EffectiveModel}, Response: &Response{ID: meta.Response.ID, RequestedModel: meta.Response.RequestedModel, EffectiveModel: meta.Response.EffectiveModel, ReportedModel: meta.Response.ReportedModel, StopReason: meta.Response.StopReason, Usage: meta.Response.Usage}}
	err = s.Walk(context.Background(), func(p Part, _ int, m MessageSource) error {
		header := m.Header()
		message := Message{Role: header.Role, Agent: header.Agent}
		if err := m.WalkBlocks(context.Background(), func(b Block) error { message.Blocks = append(message.Blocks, b); return nil }); err != nil {
			return err
		}
		if p == RequestPart {
			record.Request.Messages = append(record.Request.Messages, message)
		} else if p == ResponsePart {
			record.Response.Blocks = message.Blocks
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Request.Messages[0].Blocks) != 4352 || len(record.Response.Blocks) != 4098 {
		t.Fatal("extension facts lost")
	}
	recovered, err := SourceFromRecordWithin(sourceFixtureLimits(), record)
	if err != nil {
		t.Fatalf("complete projected evidence cannot be reopened: %v", err)
	}
	a, err := s.Measure(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b, err := recovered.Measure(context.Background())
	if err != nil || a != b || b.MaxPhysicalSlots != 4352 {
		t.Fatalf("projected cost changed %+v/%+v %v", a, b, err)
	}
	want, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	if err := writeSourceCanonical(context.Background(), &got, recovered); err != nil || !bytes.Equal(want, got.Bytes()) || b.CanonicalBytes != uint64(len(want)) {
		t.Fatal("expanded retained canonical bytes changed")
	}
	tight := sourceFixtureLimits()
	tight.StructureBytes = b.StructureBytes - 1
	if _, err := SourceFromRecordWithin(tight, record); err == nil {
		t.Fatal("expanded physical-slot structures bypassed finite policy")
	}
	if record.Validate() == nil {
		t.Fatal("legacy projected block cap changed")
	}
	responseOnly := record
	responseOnly.Request.Messages = []Message{{Role: "assistant", Blocks: []Block{{Kind: "text", Availability: AvailabilityRecorded}}}}
	if responseOnly.Validate() == nil {
		t.Fatal("legacy response projected block cap changed")
	}
}
