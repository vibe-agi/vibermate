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
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func utf8Record(t *testing.T) Record {
	t.Helper()
	request, response := evidenceFixture(t)
	r, err := NewRecord("utf8", frozenFixture(), environment.DefaultContentRecordingPolicy(), time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC), request, &response)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSourceRejectsUnpersistableOrdinaryUTF8(t *testing.T) {
	bad := string([]byte{0xff})
	for _, test := range []struct {
		name   string
		change func(*Record)
	}{
		{"text", func(r *Record) { r.Request.Messages[0].Blocks[0].Text = bad }},
		{"provider source", func(r *Record) { r.Request.Messages[0].Blocks[0].ProviderSource = bad }},
		{"provider kind", func(r *Record) { r.Request.Messages[0].Blocks[0].ProviderKind = bad }},
		{"frozen environment", func(r *Record) { r.Frozen.EnvironmentID = bad }},
		{"frozen endpoint", func(r *Record) { r.Frozen.ClientEndpointID = bad }},
		{"frozen plan", func(r *Record) { r.Frozen.ProtocolPlanID = bad }},
		{"frozen route", func(r *Record) { r.Frozen.RouteID = bad }},
		{"request requested model", func(r *Record) { r.Request.RequestedModel = bad }},
		{"request effective model", func(r *Record) { r.Request.EffectiveModel = bad }},
		{"response requested model", func(r *Record) { r.Response.RequestedModel = bad }},
		{"response effective model", func(r *Record) { r.Response.EffectiveModel = bad }},
		{"response reported model", func(r *Record) { r.Response.ReportedModel = bad }},
		{"usage source", func(r *Record) { r.Response.Usage.InputUncached.Source = bad }},
		{"exchange identity", func(r *Record) { r.ExchangeID = bad }},
		{"parent", func(r *Record) { r.Parent.CaptureRunID = bad }},
		{"message role", func(r *Record) { r.Request.Messages[0].Role = bad }},
		{"agent", func(r *Record) { r.Request.Messages[0].Agent = &AgentContext{AgentName: bad} }},
		{"block agent", func(r *Record) { r.Request.Messages[0].Blocks[0].Agent = &AgentContext{AgentName: bad} }},
		{"tool", func(r *Record) { r.Request.Tools = []ToolDefinition{{Name: bad}} }},
		{"evidence", func(r *Record) {
			r.Request.ProtocolEvidence = []protocolcore.ProtocolEvidenceValue{{Name: "native", Value: bad}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := utf8Record(t)
			test.change(&r)
			_, err := SourceFromRecordWithin(sourceFixtureLimits(), r)
			if !errors.Is(err, ErrInvalidEvidence) {
				t.Fatalf("Source accepted ordinary string with changed stored identity: %v", err)
			}
		})
	}
	for _, mode := range []environment.ContentRecordingMode{environment.ContentRecordingFull, environment.ContentRecordingMetadataOnly} {
		t.Run(string(mode), func(t *testing.T) {
			r := utf8Record(t)
			r.Mode = mode
			if mode == environment.ContentRecordingMetadataOnly {
				r.Request.Messages[0].Blocks = []Block{{Kind: "provider_extension", Availability: AvailabilityOmitted, ProviderSource: "native", ProviderKind: "opaque", Fingerprint: "sha256:" + strings.Repeat("a", 64)}}
				r.Response = nil
			}
			r.Request.Messages[0].Blocks[0].ProviderKind = bad
			if _, err := SourceFromRecordWithin(sourceFixtureLimits(), r); !errors.Is(err, ErrInvalidEvidence) {
				t.Fatal("recording mode admitted invalid retained metadata")
			}
		})
	}
}

func TestSourceUTF8RawAndValidStringControls(t *testing.T) {
	for _, raw := range []json.RawMessage{json.RawMessage([]byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}), json.RawMessage(`{"x":"\ufffd"}`), json.RawMessage(`{"x":"\ud800"}`)} {
		t.Run(fmt.Sprintf("raw-%x", raw), func(t *testing.T) {
			r := utf8Record(t)
			r.Request.Messages[0].Blocks = []Block{{Kind: "tool_call", Availability: AvailabilityRecorded, CallID: "call", ToolName: "read", Arguments: append([]byte(nil), raw...), OriginalSize: len(raw)}}
			s, err := SourceFromRecordWithin(sourceFixtureLimits(), r)
			if err != nil {
				t.Fatal(err)
			}
			want, _ := json.Marshal(r)
			var got bytes.Buffer
			if err := writeSourceCanonical(context.Background(), &got, s); err != nil || !bytes.Equal(want, got.Bytes()) {
				t.Fatal("raw bytes normalized")
			}
			cost, err := s.Measure(context.Background())
			if err != nil || cost.CanonicalBytes != uint64(len(want)) {
				t.Fatal("raw cost changed")
			}
			decoded, err := DecodeCanonicalJSON(want)
			if err != nil || !bytes.Equal(decoded.Request.Messages[0].Blocks[0].Arguments, raw) {
				t.Fatal("raw spellings changed on readback")
			}
			err = s.Walk(context.Background(), func(p Part, _ int, m MessageSource) error {
				if p != RequestPart {
					return nil
				}
				return m.WalkBlocks(context.Background(), func(b Block) error {
					if !bytes.Equal(b.Arguments, raw) {
						t.Fatal("raw callback changed")
					}
					b.Arguments[0] = '['
					return nil
				})
			})
			if err != nil || !bytes.Equal(r.Request.Messages[0].Blocks[0].Arguments, raw) {
				t.Fatal("raw ownership changed")
			}
		})
	}
	for _, text := range []string{"�", "😀", "<&>�😀"} {
		r := utf8Record(t)
		r.Request.Messages[0].Blocks[0].Text = text
		r.Request.RequestedModel = text
		r.Response.Usage.Output.Source = text
		s, err := SourceFromRecordWithin(sourceFixtureLimits(), r)
		if err != nil {
			t.Fatal(err)
		}
		want, _ := json.Marshal(r)
		var got bytes.Buffer
		if err := writeSourceCanonical(context.Background(), &got, s); err != nil || !bytes.Equal(want, got.Bytes()) {
			t.Fatal("valid UTF8 encoding changed")
		}
		a, err := s.Measure(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		tight := sourceFixtureLimits()
		tight.CanonicalBytes = a.CanonicalBytes
		tight.RetainedBytes = a.RetainedBytes
		tight.StructureBytes = a.StructureBytes
		s2, err := SourceFromRecordWithin(tight, r)
		if err != nil {
			t.Fatal("valid costs increased")
		}
		b, err := s2.Measure(context.Background())
		if err != nil || a != b {
			t.Fatal("valid cost meanings changed")
		}
	}
	bad := Block{Kind: "text", Availability: AvailabilityRecorded, Text: string([]byte{0xff}), OriginalSize: 1}
	want, _ := json.Marshal(bad)
	var got bytes.Buffer
	if err := WriteCanonicalBlock(&got, bad); err != nil || !bytes.Equal(want, got.Bytes()) {
		t.Fatal("generic writer stopped replacing invalid ordinary UTF8")
	}
}

func TestSourceUTF8MetadataOnlyDoesNotInspectPrivatePayload(t *testing.T) {
	request, _ := evidenceFixture(t)
	raw := []byte{'{', '"', 't', 'h', 'i', 'n', 'k', 'i', 'n', 'g', '"', ':', '"', 0xff, '"', ',', '"', 's', 'i', 'g', 'n', 'a', 't', 'u', 'r', 'e', '"', ':', '"', 's', 'i', 'g', '"', '}'}
	extension, err := protocolcore.NewProviderExtension(protocolcore.ProviderExtensionSourceAnthropicMessages, protocolcore.ProviderExtensionThinking, "path", [][]byte{raw})
	if err != nil {
		t.Fatal(err)
	}
	block, err := protocolcore.NewProviderExtensionBlock(extension)
	if err != nil {
		t.Fatal(err)
	}
	request.Messages = []protocolcore.Message{{Role: protocolcore.RoleAssistant, Blocks: []protocolcore.ContentBlock{block}}}
	s, err := NewSourceWithin(sourceFixtureLimits(), "private", frozenFixture(), environment.ContentRecordingPolicy{Mode: environment.ContentRecordingMetadataOnly, RetentionDays: 1}, time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC), request, nil)
	if err != nil {
		t.Fatal("omitted private bytes rejected")
	}
	err = s.Walk(context.Background(), func(_ Part, _ int, m MessageSource) error {
		return m.WalkBlocks(context.Background(), func(b Block) error {
			if b.Text != "" || b.Availability != AvailabilityOmitted || b.Fingerprint != "sha256:a543997d84f12798350c09bdef2cdb171bf41ed3e4a5f808af2feb0c56263009" {
				t.Fatal("omitted private facts changed")
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Measure(cancelled); err != context.Canceled {
		t.Fatal("context error wrapped")
	}
	sentinel := errors.New("caller")
	if err := s.Walk(context.Background(), func(Part, int, MessageSource) error { return sentinel }); err != sentinel {
		t.Fatal("callback error wrapped")
	}
}

func TestSourceUTF8AdmissionBoundedAllocation(t *testing.T) {
	r := utf8Record(t)
	r.Request.Messages[0].Blocks[0].Text = strings.Repeat("x", 6<<20) + string([]byte{0xff})
	runtime.GC()
	var a, b runtime.MemStats
	runtime.ReadMemStats(&a)
	if _, err := SourceFromRecordWithin(sourceFixtureLimits(), r); !errors.Is(err, ErrInvalidEvidence) {
		t.Fatal("large invalid retained text accepted")
	}
	runtime.ReadMemStats(&b)
	t.Logf("large UTF8 refusal allocation=%d", b.TotalAlloc-a.TotalAlloc)
	if b.TotalAlloc-a.TotalAlloc > 1<<20 {
		t.Fatal("ordinary UTF8 admission copied input-sized body")
	}
}
