package exchangecontent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
)

func depthRaw(depth int, kind string) json.RawMessage {
	var b strings.Builder
	for i := 0; i < depth; i++ {
		if kind == "object" || (kind == "mixed" && i%2 == 1) {
			b.WriteString(`{"k":`)
		} else {
			b.WriteByte('[')
		}
	}
	b.WriteString(`"braces {[}] and \" quote"`)
	for i := depth - 1; i >= 0; i-- {
		if kind == "object" || (kind == "mixed" && i%2 == 1) {
			b.WriteByte('}')
		} else {
			b.WriteByte(']')
		}
	}
	return json.RawMessage(b.String())
}
func depthRecord(placement string, raw json.RawMessage) Record {
	block := Block{Kind: "tool_call", Availability: AvailabilityRecorded, CallID: "call", ToolName: "read", OriginalSize: len(raw), Arguments: raw}
	at := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	r := Record{ExchangeID: "depth", Frozen: frozenFixture(), Mode: environment.ContentRecordingFull, RecordedAt: at, ExpiresAt: at.Add(time.Hour), Request: Request{RequestedModel: "m", EffectiveModel: "m", Messages: []Message{{Role: "assistant", Blocks: []Block{block}}}}}
	if placement == "system" {
		r.Request.System = []Block{block}
		r.Request.Messages[0].Blocks = []Block{{Kind: "text", Availability: AvailabilityRecorded, Text: "question", OriginalSize: 8}}
	}
	if placement == "response" {
		r.Request.Messages[0].Blocks = []Block{{Kind: "text", Availability: AvailabilityRecorded, Text: "question", OriginalSize: 8}}
		r.Response = &Response{ID: "response", RequestedModel: "m", EffectiveModel: "m", ReportedModel: "m", StopReason: "tool_use", Blocks: []Block{block}}
	}
	return r
}
func depthErrorEqual(t *testing.T, got, want error) {
	t.Helper()
	if got == nil || want == nil || got.Error() != want.Error() {
		t.Fatalf("legacy error differs\nwant %v\ngot %v", want, got)
	}
	for {
		a, aok := want.(*json.MarshalerError)
		b, bok := got.(*json.MarshalerError)
		if aok != bok || (aok && a.Type != b.Type) {
			t.Fatalf("error category/type differs %T/%T", want, got)
		}
		if !aok {
			var x, y *json.SyntaxError
			if errors.As(want, &x) && (!errors.As(got, &y) || x.Offset != y.Offset) {
				t.Fatal("syntax offset/category changed")
			}
			return
		}
		want = a.Err
		got = b.Err
	}
}
func TestCanonicalDepthBoundaries(t *testing.T) {
	for _, placement := range []string{"request", "system", "response"} {
		limit := 9997
		if placement == "request" {
			limit = 9995
		}
		for _, kind := range []string{"array", "object", "mixed"} {
			for _, depth := range []int{1, limit - 1, limit, limit + 1, 10000, 10001} {
				t.Run(fmt.Sprintf("%s/%s/%d", placement, kind, depth), func(t *testing.T) {
					raw := depthRaw(depth, kind)
					original := append([]byte(nil), raw...)
					r := depthRecord(placement, raw)
					want, legacyErr := json.Marshal(r)
					if (legacyErr == nil) != (depth <= limit) {
						t.Fatal("legacy threshold prediction differs")
					}
					var got bytes.Buffer
					err := WriteCanonicalJSON(&got, r)
					if legacyErr != nil {
						depthErrorEqual(t, err, legacyErr)
						if got.Len() != 0 {
							t.Fatal("preflight exposed bytes")
						}
					} else {
						if err != nil || !bytes.Equal(want, got.Bytes()) {
							t.Fatalf("valid legacy boundary changed: %v", err)
						}
						recovered, err := DecodeCanonicalJSON(want)
						if err != nil {
							t.Fatalf("legacy encoder output unreadable: %v", err)
						}
						roundtrip, _ := json.Marshal(recovered)
						if !bytes.Equal(want, roundtrip) {
							t.Fatal("owned decode bytes changed")
						}
					}
					s, sourceErr := SourceFromRecordWithin(sourceFixtureLimits(), r)
					if depth <= 10000 {
						if sourceErr != nil {
							t.Fatalf("valid boundary rejected by source: %v", sourceErr)
						}
						var sourceBytes bytes.Buffer
						if err := writeSourceCanonical(context.Background(), &sourceBytes, s); err != nil {
							t.Fatal(err)
						}
						cost, err := s.Measure(context.Background())
						if err != nil || cost.CanonicalBytes != uint64(sourceBytes.Len()) {
							t.Fatal("source depth/bytes differs")
						}
					} else if !errors.Is(sourceErr, ErrInvalidEvidence) {
						t.Fatalf("unrepresentable Source admitted: %v", sourceErr)
					}
					if !bytes.Equal(original, raw) {
						t.Fatal("raw input mutated")
					}
					block := Block{Arguments: raw}
					standalone, blockErr := json.Marshal(block)
					got.Reset()
					err = WriteCanonicalBlock(&got, block)
					if depth <= 10000 {
						if blockErr != nil || err != nil || !bytes.Equal(standalone, got.Bytes()) {
							t.Fatal("standalone Block domain shrank")
						}
					} else {
						depthErrorEqual(t, err, blockErr)
					}
				})
			}
		}
	}
}
func TestCanonicalDepthErrorPrecedence(t *testing.T) {
	deep := depthRaw(9996, "array")
	bad := json.RawMessage(`{"late":`)
	cases := []Record{depthRecord("request", deep), depthRecord("system", depthRaw(9998, "object")), depthRecord("response", depthRaw(9998, "mixed")), depthRecord("request", deep), depthRecord("request", depthRaw(10001, "array")), depthRecord("request", deep)}
	cases[0].Request.Messages[0].Blocks = append(cases[0].Request.Messages[0].Blocks, Block{Arguments: bad})
	cases[1].Request.Messages[0].Blocks = append(cases[1].Request.Messages[0].Blocks, Block{Arguments: bad})
	cases[2].Response.Usage.Output = UsageValue{Tokens: 1}
	cases[3].Response = &Response{Blocks: []Block{{Arguments: bad}}}
	cases[4].Request.Messages[0].Blocks = append(cases[4].Request.Messages[0].Blocks, Block{Arguments: bad})
	cases[5].RecordedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, r := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			_, want := json.Marshal(r)
			var got bytes.Buffer
			err := WriteCanonicalJSON(&got, r)
			depthErrorEqual(t, err, want)
			if got.Len() != 0 {
				t.Fatal("combined invalid preflight exposed prefix")
			}
		})
	}
}
func TestCanonicalDepthDecodeClosedEnvelope(t *testing.T) {
	r := depthRecord("request", depthRaw(9995, "mixed"))
	want, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range [][]byte{want, append([]byte(nil), want...)} {
		decoded, err := DecodeCanonicalJSON(input)
		if err != nil {
			t.Fatal(err)
		}
		original := append([]byte(nil), r.Request.Messages[0].Blocks[0].Arguments...)
		start := bytes.Index(input, original)
		if start < 0 {
			t.Fatal("missing raw region")
		}
		input[start] = '!'
		if !bytes.Equal(decoded.Request.Messages[0].Blocks[0].Arguments, original) {
			t.Fatal("decoder aliases input")
		}
		decoded.Request.Messages[0].Blocks[0].Arguments[1] = '!'
		if input[start+1] != original[1] {
			t.Fatal("input aliases decoded arguments")
		}
	}
	valid, _ := json.Marshal(r)
	for _, input := range [][]byte{append(append([]byte(nil), valid...), []byte(` {}`)...), append([]byte(" "), valid...), []byte(`null`), []byte(`[]`), []byte(`{"unknown":true}`), bytes.Replace(valid, []byte(`"exchangeId":"depth"`), []byte(`"exchangeId":"depth","exchangeId":"depth"`), 1), bytes.Replace(valid, []byte(`"requestedModel":"m"`), []byte(`"requestedModel":"m","unknown":true`), 1), valid[:len(valid)-1]} {
		if _, err := DecodeCanonicalJSON(input); !errors.Is(err, ErrInvalidEvidence) {
			t.Fatalf("invalid envelope accepted: %v", err)
		}
	}
	if _, err := DecodeCanonicalJSON(make([]byte, MaxEncodedBytes+1)); !errors.Is(err, ErrInvalidEvidence) {
		t.Fatal("legacy size bound changed")
	}
}
func TestCanonicalDepthBoundedScratch(t *testing.T) {
	r := depthRecord("request", depthRaw(9996, "array"))
	// A parent marshaler would encode this late large string before rescanning
	// and finding the earlier depth error. Fixed preflight scratch must not.
	r.Request.Messages[0].Blocks = append(r.Request.Messages[0].Blocks, Block{Kind: "text", Availability: AvailabilityRecorded, Text: strings.Repeat("&", 6<<20)})
	runtime.GC()
	var a, b runtime.MemStats
	runtime.ReadMemStats(&a)
	if WriteCanonicalJSON(io.Discard, r) == nil {
		t.Fatal("depth overflow emitted")
	}
	runtime.ReadMemStats(&b)
	t.Logf("bounded depth error allocation=%d", b.TotalAlloc-a.TotalAlloc)
	// Two bounded pinned scanner operations (raw validation and the fixed
	// invalid probe), including geometric stack growth to10001, fit1MiB.
	if b.TotalAlloc-a.TotalAlloc > 1<<20 {
		t.Fatal("depth error allocated whole parent")
	}
	var syntax *json.SyntaxError
	first := WriteCanonicalJSON(io.Discard, r)
	if !errors.As(first, &syntax) {
		t.Fatal("missing fresh SyntaxError")
	}
	syntax.Offset = 123
	second := WriteCanonicalJSON(io.Discard, r)
	if !errors.As(second, &syntax) || syntax.Offset != 0 {
		t.Fatal("exposed error mutation contaminated later error")
	}
	// Increase raw bytes without increasing depth. json.Valid and the lexical
	// check must scan, not copy/compact, the string inside these same containers.
	r.Request.Messages[0].Blocks[0].Arguments = json.RawMessage(strings.Repeat("[", 9996) + `"` + strings.Repeat("&", 6<<20) + `"` + strings.Repeat("]", 9996))
	runtime.GC()
	runtime.ReadMemStats(&a)
	if WriteCanonicalJSON(io.Discard, r) == nil {
		t.Fatal("large raw depth overflow emitted")
	}
	runtime.ReadMemStats(&b)
	t.Logf("large raw same-depth allocation=%d", b.TotalAlloc-a.TotalAlloc)
	if b.TotalAlloc-a.TotalAlloc > 1<<20 {
		t.Fatal("depth preflight copied growing raw input")
	}
}

func TestCanonicalDepthSourceAdmission(t *testing.T) {
	for _, p := range []string{"request", "system", "response"} {
		t.Run(p, func(t *testing.T) {
			for _, n := range []int{1, 9995, 9996, 9997, 9998, 10000, 10001} {
				r := depthRecord(p, depthRaw(n, "mixed"))
				if _, err := SourceFromRecordWithin(sourceFixtureLimits(), r); (err == nil) != (n <= 10000) {
					t.Fatalf("Source independent raw depth %d: %v", n, err)
				}
			}
		})
	}
}

func TestIndependentLeafExactSourceBudgets(t *testing.T) {
	for _, position := range []string{"request", "system", "response"} {
		r := depthRecord(position, depthRaw(10000, "mixed"))
		limits := sourceFixtureLimits()
		source, err := SourceFromRecordWithin(limits, r)
		if err != nil {
			t.Fatal(err)
		}
		cost, err := source.Measure(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		limits.CanonicalBytes = cost.CanonicalBytes
		limits.RetainedBytes = cost.RetainedBytes
		limits.StructureBytes = cost.StructureBytes
		limits.Scratch.PayloadBytes = 2*uint64(len(depthRaw(10000, "mixed"))) + 4096
		limits.Scratch.StructureBytes = uint64(reflect.TypeFor[Block]().Size()+reflect.TypeFor[AgentContext]().Size()) + 4096
		if _, err := SourceFromRecordWithin(limits, r); err != nil {
			t.Fatal(err)
		}
		for _, currency := range []string{"canonical", "retained", "structure", "scratchPayload", "scratchStructure"} {
			small := limits
			switch currency {
			case "canonical":
				small.CanonicalBytes--
			case "retained":
				small.RetainedBytes--
			case "structure":
				small.StructureBytes--
			case "scratchPayload":
				small.Scratch.PayloadBytes--
			case "scratchStructure":
				small.Scratch.StructureBytes--
			}
			if _, err := SourceFromRecordWithin(small, r); !errors.Is(err, ErrInvalidEvidence) {
				t.Fatalf("%s %s minus1: %v", position, currency, err)
			}
		}
	}
}
func FuzzCanonicalDepth(f *testing.F) {
	for _, n := range []uint16{0, 1, 9994, 9995, 9996, 9997, 9998, 10000, 10001} {
		f.Add(n, byte(0), byte(0))
	}
	f.Fuzz(func(t *testing.T, n uint16, p, k byte) {
		depth := int(n)
		if depth > 10001 {
			t.Skip()
		}
		r := depthRecord([]string{"request", "system", "response"}[int(p)%3], depthRaw(depth, []string{"array", "object", "mixed"}[int(k)%3]))
		want, oldErr := json.Marshal(r)
		var got bytes.Buffer
		err := WriteCanonicalJSON(&got, r)
		if oldErr != nil {
			depthErrorEqual(t, err, oldErr)
			if got.Len() != 0 {
				t.Fatal("depth preflight prefix")
			}
			return
		}
		if err != nil || !bytes.Equal(want, got.Bytes()) {
			t.Fatal("depth bytes changed")
		}
		if _, err := DecodeCanonicalJSON(want); err != nil {
			t.Fatal(err)
		}
	})
}
