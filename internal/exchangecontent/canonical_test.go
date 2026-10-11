package exchangecontent

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/environment"
)

func TestCanonicalBlockLiteral(t *testing.T) {
	block := Block{Kind: "text", Availability: AvailabilityRecorded, Text: "<&>\n😀\u2028", OriginalSize: 12}
	const want = `{"kind":"text","availability":"recorded","text":"\u003c\u0026\u003e\n😀\u2028","originalSize":12}`
	var got bytes.Buffer
	if err := WriteCanonicalBlock(&got, block); err != nil {
		t.Fatal(err)
	}
	if got.String() != want {
		t.Fatalf("canonical bytes: %q", got.String())
	}
	legacy, err := json.Marshal(block)
	if err != nil || string(legacy) != want {
		t.Fatalf("legacy oracle = %s, %v", legacy, err)
	}
}

func TestCanonicalLegacyMatrix(t *testing.T) {
	request, response := evidenceFixture(t)
	r, err := NewRecord("matrix", frozenFixture(), environment.DefaultContentRecordingPolicy(), time.Date(2026, 1, 2, 3, 4, 5, 123000000, time.FixedZone("offset", -7*3600)), request, &response)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"", "<&>\"\\\b\f\n\r\t\x00", "😀\u2028\u2029", string([]byte{0xff, 0xfe})} {
		for _, raw := range []json.RawMessage{nil, {}, json.RawMessage(`null`), json.RawMessage(` { "z" : 1e+02 , "a":"<&>\u0026\u2028" } `)} {
			v := r.Clone()
			v.RecordedAt = time.Date(2026, 1, 2, 3, 4, 5, 123000000, time.FixedZone("offset", -7*3600))
			v.ExpiresAt = v.RecordedAt.AddDate(0, 0, 1)
			v.Request.System = nil
			v.Request.Messages[0].Blocks = append(v.Request.Messages[0].Blocks, Block{Kind: "tool_call", Availability: AvailabilityRecorded, Text: text, Arguments: raw, OriginalSize: len(text)})
			v.Request.Messages[0].Agent = &AgentContext{}
			v.Response.Blocks = nil
			v.Response.ProtocolEvidence = nil
			v.Response.Usage = Usage{InputUncached: UsageValue{Known: true, Tokens: 0, Source: "<&>"}, Output: UsageValue{Known: true, Tokens: 2, Source: "source"}}
			before, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			var got bytes.Buffer
			if err := WriteCanonicalJSON(&got, v); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got.Bytes(), before) {
				t.Fatalf("legacy matrix changed\nwant %s\ngot %s", before, got.Bytes())
			}
			after, _ := json.Marshal(v)
			if !bytes.Equal(before, after) {
				t.Fatal("writer changed caller")
			}
			counter := countWriter{}
			h := sha256.New()
			if err := WriteCanonicalJSON(io.MultiWriter(&counter, h), v); err != nil {
				t.Fatal(err)
			}
			if counter.n != uint64(len(before)) || !bytes.Equal(h.Sum(nil), hashBytes(before)) {
				t.Fatal("count/hash differs")
			}
		}
	}
}
func hashBytes(b []byte) []byte { h := sha256.Sum256(b); return h[:] }

type faultSink struct {
	offset        int
	written       []byte
	mode          string
	calls, failed int
	err           error
}

func (f *faultSink) Write(p []byte) (int, error) {
	f.calls++
	if f.failed > 0 {
		panic("write after failure")
	}
	n := min(len(p), max(0, f.offset-len(f.written)))
	if n == len(p) && f.mode != "fullerr" {
		f.written = append(f.written, p...)
		return n, nil
	}
	f.failed++
	switch f.mode {
	case "negative":
		return -1, nil
	case "oversized":
		return len(p) + 1, nil
	case "zero":
		return 0, nil
	case "fullerr":
		f.written = append(f.written, p...)
		return len(p), f.err
	case "partialnil":
		f.written = append(f.written, p[:n]...)
		return n, nil
	default:
		f.written = append(f.written, p[:n]...)
		return n, f.err
	}
}
func TestCanonicalFaultPrefixes(t *testing.T) {
	b := Block{Kind: "text", Availability: AvailabilityRecorded, Text: "<&>"}
	want, _ := json.Marshal(b)
	sentinel := errors.New("sink failed")
	for _, mode := range []string{"error", "partialnil", "zero", "fullerr", "negative", "oversized"} {
		for offset := 0; offset < len(want); offset++ {
			f := faultSink{offset: offset, mode: mode, err: sentinel}
			err := WriteCanonicalBlock(&f, b)
			if err == nil || f.calls != 1 {
				t.Fatalf("%s/%d: %v calls %d", mode, offset, err, f.calls)
			}
			n := offset
			if mode == "zero" || mode == "negative" || mode == "oversized" {
				n = 0
			}
			if mode == "fullerr" {
				n = len(want)
			}
			if !bytes.Equal(f.written, want[:n]) {
				t.Fatalf("prefix %s/%d", mode, offset)
			}
			if (mode == "error" || mode == "fullerr") && !errors.Is(err, sentinel) {
				t.Fatal("real sink error lost")
			}
			if (mode == "partialnil" || mode == "zero") && !errors.Is(err, io.ErrShortWrite) {
				t.Fatal("short write lost")
			}
		}
	}
	large := b
	large.Text = strings.Repeat("&", 2000)
	f := faultSink{offset: 5000, mode: "error", err: sentinel}
	if err := WriteCanonicalBlock(&f, large); !errors.Is(err, sentinel) || len(f.written) != 5000 || f.calls != 2 {
		t.Fatalf("late sink failure: %v %+v", err, f)
	}
}
func TestCanonicalPreflightLateInvalidLeaves(t *testing.T) {
	request, response := evidenceFixture(t)
	r, err := NewRecord("invalid", frozenFixture(), environment.DefaultContentRecordingPolicy(), time.Now(), request, &response)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Record){func(r *Record) {
		r.Response.Blocks = append(r.Response.Blocks, Block{Arguments: json.RawMessage(`{"late":`)})
	}, func(r *Record) { r.Response.Usage.Reasoning = UsageValue{Tokens: 1} }, func(r *Record) { r.ExpiresAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) }} {
		v := r.Clone()
		change(&v)
		var b bytes.Buffer
		if WriteCanonicalJSON(&b, v) == nil || b.Len() != 0 {
			t.Fatal("invalid leaf exposed a prefix")
		}
	}
}

func TestCanonicalLegacyEncodingErrors(t *testing.T) {
	bad := Block{Arguments: json.RawMessage(`{"late":`)}
	base := Record{RecordedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), ExpiresAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)}
	tests := []struct {
		name  string
		value any
		write func(io.Writer) error
	}{{name: "block raw", value: bad, write: func(w io.Writer) error { return WriteCanonicalBlock(w, bad) }}}
	for _, test := range []struct {
		name   string
		change func(*Record)
	}{{"request raw", func(r *Record) { r.Request.Messages = []Message{{Blocks: []Block{bad}}} }}, {"response raw", func(r *Record) { r.Response = &Response{Blocks: []Block{bad}} }}, {"unknown usage", func(r *Record) { r.Response = &Response{Usage: Usage{Reasoning: UsageValue{Tokens: 1}}} }}, {"known usage", func(r *Record) { r.Response = &Response{Usage: Usage{Output: UsageValue{Known: true}}} }}, {"time year", func(r *Record) { r.ExpiresAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) }}} {
		r := base
		test.change(&r)
		tests = append(tests, struct {
			name  string
			value any
			write func(io.Writer) error
		}{test.name, r, func(w io.Writer) error { return WriteCanonicalJSON(w, r) }})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, want := json.Marshal(test.value)
			var got bytes.Buffer
			err := test.write(&got)
			if want == nil || err == nil || got.Len() != 0 {
				t.Fatalf("encoding error/prefix %v %v %d", want, err, got.Len())
			}
			if err.Error() != want.Error() {
				t.Fatalf("encoding error changed\nwant %v\ngot %v", want, err)
			}
			old, current := want, err
			for old != nil {
				a, aok := old.(*json.MarshalerError)
				b, bok := current.(*json.MarshalerError)
				if aok != bok || (aok && a.Type != b.Type) {
					t.Fatalf("marshaler error chain differs %T/%T", old, current)
				}
				if aok {
					old = a.Err
					current = b.Err
					continue
				}
				var os, ns *json.SyntaxError
				if errors.As(old, &os) && (!errors.As(current, &ns) || os.Offset != ns.Offset) {
					t.Fatalf("syntax error differs %v/%v", old, current)
				}
				break
			}
		})
	}
	raw := Block{Arguments: json.RawMessage(`"` + strings.Repeat("x", 1<<20))}
	runtime.GC()
	var a, b runtime.MemStats
	runtime.ReadMemStats(&a)
	if WriteCanonicalBlock(io.Discard, raw) == nil {
		t.Fatal("large invalid raw accepted")
	}
	runtime.ReadMemStats(&b)
	if b.TotalAlloc-a.TotalAlloc > 256<<10 {
		t.Fatal("invalid raw preflight allocated body")
	}
}
func TestCanonicalLargeEscapedLeafBoundedAllocation(t *testing.T) {
	b := Block{Kind: "text", Availability: AvailabilityRecorded, Text: strings.Repeat("&", 6<<20), OriginalSize: 6 << 20}
	want, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	expected := hashBytes(want)
	size := len(want)
	want = nil
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	cw := countWriter{}
	h := sha256.New()
	if err := WriteCanonicalBlock(io.MultiWriter(&cw, h), b); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	delta := after.TotalAlloc - before.TotalAlloc
	t.Logf("escaped leaf bytes=%d write allocation=%d", cw.n, delta)
	if cw.n != uint64(size) || !bytes.Equal(h.Sum(nil), expected) {
		t.Fatal("large leaf bytes changed")
	}
	if delta > 1<<20 {
		t.Fatalf("bounded writer allocated %d", delta)
	}
}

func TestCanonicalLargeUsageSourceBoundedAllocation(t *testing.T) {
	r := Record{RecordedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), ExpiresAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), Response: &Response{Usage: Usage{Output: UsageValue{Known: true, Source: strings.Repeat("&", 1<<20)}}}}
	runtime.GC()
	var a, b runtime.MemStats
	runtime.ReadMemStats(&a)
	if err := WriteCanonicalJSON(io.Discard, r); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&b)
	if b.TotalAlloc-a.TotalAlloc > 1<<20 {
		t.Fatalf("usage preflight allocated complete escaped source: %d", b.TotalAlloc-a.TotalAlloc)
	}
}

func FuzzCanonicalLegacyBytes(f *testing.F) {
	for _, s := range []string{"plain", "<&>\u2028\u2029", "😀", string([]byte{0xff}), "\"\\\x00"} {
		f.Add(s, []byte(` {"z":1e+02,"a":"<&>"} `), false)
	}
	f.Add("rawnull", []byte(`null`), true)
	f.Add("invalid", []byte(`{"late":`), false)
	f.Fuzz(func(t *testing.T, text string, raw []byte, known bool) {
		if len(text) > 4096 || len(raw) > 4096 {
			t.Skip()
		}
		b := Block{Kind: "tool_call", Availability: AvailabilityRecorded, CallID: "call", ToolName: "f", Text: text, OriginalSize: len(text), Arguments: append([]byte(nil), raw...), Agent: &AgentContext{AgentName: "<&>"}}
		want, oldErr := json.Marshal(b)
		var got bytes.Buffer
		err := WriteCanonicalBlock(&got, b)
		if oldErr != nil {
			if err == nil || got.Len() != 0 {
				t.Fatal("invalid raw JSON was emitted")
			}
			if err.Error() != oldErr.Error() {
				t.Fatalf("raw encoding error changed: %v/%v", oldErr, err)
			}
			return
		}
		if err != nil || !bytes.Equal(want, got.Bytes()) {
			t.Fatalf("block bytes %s/%s %v", want, got.Bytes(), err)
		}
		r := Record{ExchangeID: "e", Frozen: frozenFixture(), Mode: environment.ContentRecordingFull, RecordedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), ExpiresAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), Request: Request{RequestedModel: "m", EffectiveModel: "m", Messages: []Message{{Role: "assistant", Blocks: []Block{b}}}}, Response: &Response{ID: "response", RequestedModel: "m", EffectiveModel: "m", ReportedModel: "m", StopReason: "end_turn", Usage: Usage{Output: UsageValue{Known: known, Source: func() string {
			if known {
				return "provider"
			}
			return ""
		}()}}}}
		want, oldErr = json.Marshal(r)
		got.Reset()
		err = WriteCanonicalJSON(&got, r)
		if oldErr != nil || err != nil || !bytes.Equal(want, got.Bytes()) {
			t.Fatalf("record bytes %v %v", oldErr, err)
		}
		// Generic canonical writers preserve legacy bytes even for ordinary
		// strings that finite persistence cannot authenticate on readback.
		if err := r.Validate(); !utf8.ValidString(text) {
			if !errors.Is(err, ErrInvalidEvidence) {
				t.Fatalf("unpersistable ordinary UTF8 accepted: %v", err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(raw, b.Arguments) {
			t.Fatal(fmt.Sprintf("caller raw changed %q", raw))
		}
	})
}

func TestCanonicalRecordLiteral(t *testing.T) {
	record := Record{ExchangeID: "e", Parent: ParentRef{}, Frozen: FrozenRef{EnvironmentID: "a", EnvironmentRevision: 1, EnvironmentDigest: "d", ClientEndpointID: "b", ClientEndpointRevision: 2, ProtocolPlanID: "c", ProtocolPlanRevision: 3}, Mode: environment.ContentRecordingFull, RecordedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), ExpiresAt: time.Date(2026, 1, 3, 3, 4, 5, 0, time.UTC), Request: Request{RequestedModel: "m", EffectiveModel: "m", Messages: []Message{{Role: "user", Blocks: []Block{{Kind: "text", Availability: AvailabilityRecorded}}}}}}
	const want = `{"exchangeId":"e","parent":{},"frozen":{"environmentId":"a","environmentRevision":1,"environmentDigest":"d","clientEndpointId":"b","clientEndpointRevision":2,"protocolPlanId":"c","protocolPlanRevision":3,"routeId":"","routeRevision":0},"mode":"full","recordedAt":"2026-01-02T03:04:05Z","expiresAt":"2026-01-03T03:04:05Z","request":{"requestedModel":"m","effectiveModel":"m","maxOutputTokens":0,"stream":false,"system":[],"messages":[{"role":"user","blocks":[{"kind":"text","availability":"recorded","originalSize":0}]}],"tools":[],"protocolEvidence":[]}}`
	var got bytes.Buffer
	if err := WriteCanonicalJSON(&got, record); err != nil {
		t.Fatal(err)
	}
	if got.String() != want {
		t.Fatalf("got %s", got.Bytes())
	}
	if fmt.Sprintf("%x", sha256.Sum256(got.Bytes())) != "d78cc0eb7148108b56533eac74d021fd36aa4119ff34eab5f593773cd3b4dff1" {
		t.Fatal("complete literal hash changed")
	}
	legacy, err := json.Marshal(record)
	if err != nil || string(legacy) != want {
		t.Fatalf("legacy %s %v", legacy, err)
	}
}
