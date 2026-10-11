package runtimepersistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
)

func parserFixture(t testing.TB, data []byte) (*storedBlockDecoder, storedDecodeOpen) {
	t.Helper()
	d, err := newStoredBlockDecoder(storedDecodeLimits{1 << 30, 1 << 29, 1 << 20, 1 << 20}, func(storedDecodeCost) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	open := func(context.Context) (storedDecodeInput, error) {
		slots := 1
		if len(data) > 32<<20 {
			slots = (len(data)-1)/((32<<20)-56) + 1
		}
		return storedDecodeInput{bytes.NewReader(data), uint64(len(data)), sha256.Sum256(data), slots}, nil
	}
	return d, open
}

// A full decoder must report Source's logical bytes before opening pass two,
// including provider strings omitted from count/selection output. Its capacity
// reservation deliberately remains a different currency.
func TestStoredCanonicalParserLogicalAdmission(t *testing.T) {
	for _, mode := range []environment.ContentRecordingMode{environment.ContentRecordingFull, environment.ContentRecordingMetadataOnly} {
		for _, limitDelta := range []int{0, -1} {
			t.Run(string(mode)+"/"+strconvBool(limitDelta == 0), func(t *testing.T) {
				b := exchangecontent.Block{Kind: "reasoning", Availability: exchangecontent.AvailabilityRecorded, Text: "body", ProviderSource: strings.Repeat("s", 8193), ProviderKind: "k", Agent: &exchangecontent.AgentContext{AgentName: "a", Author: "b", Recipient: "c"}}
				if mode == environment.ContentRecordingMetadataOnly {
					b.Text = ""
					b.Availability = exchangecontent.AvailabilityOmitted
				}
				data, err := json.Marshal(b)
				if err != nil {
					t.Fatal(err)
				}
				want := storedBlockLogicalCost{uint64(9 + len(b.Availability) + len(b.Text) + 8193 + 1 + 3), uint64(reflect.TypeFor[exchangecontent.Block]().Size() + reflect.TypeFor[exchangecontent.AgentContext]().Size())}
				allowed := want.RetainedBytes
				if limitDelta < 0 {
					allowed--
				}
				opens, measured := 0, 0
				d, err := newStoredBlockDecoder(storedDecodeLimits{1 << 20, 1 << 20, 1 << 20, 1 << 20}, func(c storedDecodeCost) error {
					if c.LogicalCost.StructureBytes == 0 {
						return nil
					}
					measured++
					if c.LogicalCost != want {
						t.Fatalf("logical cost %+v, want %+v", c.LogicalCost, want)
					}
					if opens != 1 {
						t.Fatalf("logical admission after output open: %d", opens)
					}
					if c.LogicalCost.RetainedBytes > allowed {
						return exchangecontent.ErrInvalidEvidence
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				open := func(context.Context) (storedDecodeInput, error) {
					opens++
					return storedDecodeInput{bytes.NewReader(data), uint64(len(data)), sha256.Sum256(data), 1}, nil
				}
				got, err := d.full(context.Background(), open, mode)
				if limitDelta < 0 {
					if !errors.Is(err, exchangecontent.ErrInvalidEvidence) || opens != 1 || !reflect.DeepEqual(got, exchangecontent.Block{}) {
						t.Fatalf("logical rejection must precede pass two: opens=%d empty=%v err=%v", opens, reflect.DeepEqual(got, exchangecontent.Block{}), err)
					}
				} else if err != nil || !reflect.DeepEqual(got, b) || measured == 0 || opens != 2 {
					t.Fatalf("exact budget: opens=%d admissions=%d equal=%v err=%v", opens, measured, reflect.DeepEqual(got, b), err)
				}
			})
		}
	}
}

func TestStoredCanonicalParserLogicalCountRawAndReuse(t *testing.T) {
	b := exchangecontent.Block{Kind: "tool_call", Availability: exchangecontent.AvailabilityRecorded, CallID: "id", ToolName: "fn", Arguments: json.RawMessage(`{"a":[1,"x"]}`)}
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	want := storedBlockLogicalCost{uint64(9 + 8 + 2 + 2 + 13), uint64(reflect.TypeFor[exchangecontent.Block]().Size())}
	d, open := parserFixture(t, data)
	for i := 0; i < 2; i++ {
		facts, err := d.count(context.Background(), open, environment.ContentRecordingFull)
		if err != nil || facts.LogicalCost != want {
			t.Fatalf("occurrence %d: facts=%+v want=%+v err=%v", i, facts, want, err)
		}
	}
}

func TestStoredCanonicalParserLegacyLanguage(t *testing.T) {
	ordinary := []string{`"plain"`, `"\"\\\b\f\n\r\t\u0000\u001f\u003c\u003e\u0026\u2028\u2029😀�"`, `"\u0061"`, `"\/"`, `"\u000a"`, `"\u003C"`, `"\ud800"`, `"\ud800\udc00"`, `"<"`, `"\ufffd"`, "\"\xff\"", `""`}
	raw := []string{`null`, `true`, `-0`, `1e+02`, `1e9999999999999999999999`, `{"z":1,"a":"\u0061","a":"\/\u003C\ud800"}`, `[null,"\ud800\udc00"]`, "\"\xff\"", `"\ufffd"`, `"<"`, `"\u003c"`, `[ 1 ]`, `" "`, `[1,]`, `{"a":}`, `1e+`}
	for _, isRaw := range []bool{false, true} {
		values := ordinary
		if isRaw {
			values = raw
		}
		for _, value := range values {
			t.Run(value+strconvBool(isRaw), func(t *testing.T) {
				data := []byte(`{"kind":"text","availability":"recorded","text":` + value + `,"originalSize":1}`)
				if isRaw {
					data = []byte(`{"kind":"tool_call","availability":"recorded","originalSize":1,"callId":"c","toolName":"f","arguments":` + value + `}`)
				}
				legacy, legacyErr := decodeStoredBlock(data)
				if legacyErr == nil {
					legacyErr = legacy.Validate(environment.ContentRecordingFull)
				}
				d, open := parserFixture(t, data)
				got, err := d.full(context.Background(), open, environment.ContentRecordingFull)
				if (err == nil) != (legacyErr == nil) {
					t.Fatalf("acceptance got=%v legacy=%v", err, legacyErr)
				}
				if err == nil && !reflect.DeepEqual(got, legacy) {
					t.Fatalf("different value: %+v / %+v", got, legacy)
				}
				if err != nil && !errors.Is(err, exchangecontent.ErrInvalidEvidence) {
					t.Fatalf("wrong invalid classification: %v", err)
				}
			})
		}
	}
}
func strconvBool(b bool) string {
	if b {
		return "/raw"
	}
	return "/ordinary"
}

func TestStoredCanonicalParserClosedEnvelope(t *testing.T) {
	base := `{"kind":"text","availability":"recorded","originalSize":0}`
	cases := []string{base + " ", base + "{}", strings.Replace(base, `"kind":`, `"Kind":`, 1), strings.Replace(base, `"kind":"text",`, ``, 1), strings.Replace(base, `"kind":"text",`, `"kind":"text","kind":"text",`, 1), strings.Replace(base, `"availability":"recorded",`, `"availability":"recorded","text":"",`, 1), strings.Replace(base, `"originalSize":0`, `"originalSize":-0`, 1), strings.Replace(base, `"originalSize":0`, `"originalSize":0.0`, 1), strings.Replace(base, `"originalSize":0`, `"originalSize":00`, 1), strings.Replace(base, `"originalSize":0`, `"originalSize":9223372036854775808`, 1), strings.Replace(base, `"originalSize":0`, `"originalSize":0,"toolError":false`, 1), strings.Replace(base, `"originalSize":0`, `"originalSize":0,"agent":null`, 1), strings.Replace(base, `"originalSize":0`, `"originalSize":0,"agent":{"author":"a","agentName":"n","recipient":"r"}`, 1), strings.Replace(base, `"originalSize":0`, `"originalSize":0,"deferred":{}`, 1), `null`, `{}`}
	for _, data := range cases {
		d, open := parserFixture(t, []byte(data))
		got, err := d.full(context.Background(), open, environment.ContentRecordingFull)
		if err == nil || !reflect.DeepEqual(got, exchangecontent.Block{}) {
			t.Fatalf("accepted %q: %+v %v", data, got, err)
		}
	}
}

func TestStoredCanonicalParserShapes(t *testing.T) {
	for _, mode := range []environment.ContentRecordingMode{environment.ContentRecordingFull, environment.ContentRecordingMetadataOnly} {
		for _, kind := range []string{"text", "refusal", "reasoning", "tool_call", "tool_result", "provider_extension"} {
			t.Run(string(mode)+"/"+kind, func(t *testing.T) {
				b := exchangecontent.Block{Kind: kind, Availability: exchangecontent.AvailabilityRecorded, OriginalSize: 3, Text: "abc", Agent: &exchangecontent.AgentContext{AgentName: "agent", Author: "a", Recipient: "b"}}
				switch kind {
				case "tool_call":
					b.CallID = "call"
					b.ToolName = "fn"
					b.ToolNamespace = "ns"
					b.Arguments = json.RawMessage(`null`)
				case "tool_result":
					b.CallID = "call"
					b.ToolError = true
				case "reasoning":
					b.ProviderSource = "src"
					b.ProviderKind = "kind"
				case "provider_extension":
					b.ProviderSource = "src"
					b.ProviderKind = "kind"
					b.Fingerprint = "sha256:" + strings.Repeat("a", 64)
					b.Availability = exchangecontent.AvailabilityOmitted
					b.Text = ""
				}
				if mode == environment.ContentRecordingMetadataOnly {
					b.Availability = exchangecontent.AvailabilityOmitted
					b.Text = ""
					b.Arguments = nil
				}
				if err := b.Validate(mode); err != nil {
					t.Fatalf("fixture %v", err)
				}
				data, err := json.Marshal(b)
				if err != nil {
					t.Fatal(err)
				}
				d, open := parserFixture(t, data)
				got, err := d.full(context.Background(), open, mode)
				if err != nil || !reflect.DeepEqual(got, b) {
					t.Fatalf("full %+v %v", got, err)
				}
				facts, err := d.count(context.Background(), open, mode)
				if err != nil || facts.Shape != b.RetainedShape() {
					t.Fatalf("count %+v %v", facts, err)
				}
				if b.Text != "" || len(b.Arguments) > 0 {
					part, err := d.selectBody(context.Background(), open, mode, 0, 32)
					want := b.Text
					if len(b.Arguments) > 0 {
						want = string(b.Arguments)
					}
					if err != nil || string(part.Bytes) != want {
						t.Fatalf("range %q %v", part.Bytes, err)
					}
				}
			})
		}
	}
}

type parserChunkReader struct {
	data     []byte
	chunk    int
	terminal error
	eof      *bool
	cancel   context.CancelFunc
}

func (r *parserChunkReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		if r.eof != nil {
			*r.eof = true
		}
		if r.terminal != nil {
			return 0, r.terminal
		}
		return 0, io.EOF
	}
	n := min(len(p), r.chunk, len(r.data))
	copy(p, r.data[:n])
	r.data = r.data[n:]
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	if len(r.data) == 0 && r.terminal != nil {
		return n, r.terminal
	}
	return n, nil
}

func TestStoredCanonicalParserChunksAndEOF(t *testing.T) {
	b := exchangecontent.Block{Kind: "tool_call", Availability: exchangecontent.AvailabilityRecorded, OriginalSize: 10, CallID: "c", ToolName: "f", Text: "\x00<😀\n\u2028", Arguments: json.RawMessage(`{"x":"😀\ud800","x":1e+02}`)}
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	for chunk := 1; chunk <= len(data); chunk++ {
		d, _ := parserFixture(t, data)
		opens := 0
		ended := [2]bool{}
		open := func(context.Context) (storedDecodeInput, error) {
			index := opens
			opens++
			return storedDecodeInput{&parserChunkReader{data: data, chunk: chunk, eof: &ended[index]}, uint64(len(data)), sha256.Sum256(data), 1}, nil
		}
		got, err := d.full(context.Background(), open, environment.ContentRecordingFull)
		if err != nil || !reflect.DeepEqual(got, b) || opens != 2 || ended != [2]bool{true, true} {
			t.Fatalf("chunk%d: %v opens=%d EOF=%v", chunk, err, opens, ended)
		}
	}
	sentinel := errors.New("terminal read fault")
	for _, second := range []bool{false, true} {
		d, _ := parserFixture(t, data)
		opens := 0
		open := func(context.Context) (storedDecodeInput, error) {
			opens++
			var end error
			if (opens == 2) == second {
				end = sentinel
			}
			return storedDecodeInput{&parserChunkReader{data: data, chunk: len(data), terminal: end}, uint64(len(data)), sha256.Sum256(data), 1}, nil
		}
		got, err := d.full(context.Background(), open, environment.ContentRecordingFull)
		if err != sentinel || !reflect.DeepEqual(got, exchangecontent.Block{}) {
			t.Fatalf("final bytes+error: %+v %v", got, err)
		}
	}
	for _, second := range []bool{false, true} {
		d, _ := parserFixture(t, data)
		ctx, cancel := context.WithCancel(context.Background())
		opens := 0
		open := func(context.Context) (storedDecodeInput, error) {
			opens++
			var c context.CancelFunc
			if (opens == 2) == second {
				c = cancel
			}
			return storedDecodeInput{&parserChunkReader{data: data, chunk: 1, cancel: c}, uint64(len(data)), sha256.Sum256(data), 1}, nil
		}
		got, err := d.full(ctx, open, environment.ContentRecordingFull)
		cancel()
		if err != context.Canceled || !reflect.DeepEqual(got, exchangecontent.Block{}) {
			t.Fatalf("cancel: %+v %v", got, err)
		}
	}
}

func TestStoredCanonicalParserSecondPassMutation(t *testing.T) {
	base := []byte(`{"kind":"text","availability":"recorded","text":"abcdef","originalSize":6,"providerKind":"tail"}`)
	for _, mutation := range []string{strings.Replace(string(base), "abcdef", "xbcdef", 1), strings.Replace(string(base), "abcdef", "abcdefg", 1), strings.Replace(string(base), "tail", "fail", 1), strings.Replace(string(base), `"kind":"text","availability":"recorded"`, `"availability":"recorded","kind":"text"`, 1), strings.Replace(string(base), `"text"`, `"refusal"`, 1), string(base[:len(base)-1])} {
		for _, which := range []string{"count", "full", "range"} {
			t.Run(which+mutation, func(t *testing.T) {
				d, _ := parserFixture(t, base)
				calls := 0
				open := func(context.Context) (storedDecodeInput, error) {
					calls++
					data := base
					if calls == 2 {
						data = []byte(mutation)
					}
					return storedDecodeInput{bytes.NewReader(data), uint64(len(base)), sha256.Sum256(base), 1}, nil
				}
				var err error
				switch which {
				case "count":
					var got storedBlockFacts
					got, err = d.count(context.Background(), open, environment.ContentRecordingFull)
					if got != (storedBlockFacts{}) {
						t.Fatal("provisional facts")
					}
				case "full":
					var got exchangecontent.Block
					got, err = d.full(context.Background(), open, environment.ContentRecordingFull)
					if !reflect.DeepEqual(got, exchangecontent.Block{}) {
						t.Fatal("provisional full")
					}
				case "range":
					var got storedBodyRange
					got, err = d.selectBody(context.Background(), open, environment.ContentRecordingFull, 0, 2)
					if !reflect.DeepEqual(got, storedBodyRange{}) {
						t.Fatal("provisional range")
					}
				}
				if err == nil || calls != 2 {
					t.Fatalf("mutation err=%v calls=%d", err, calls)
				}
			})
		}
	}
	for _, identity := range []string{"size", "digest", "slots"} {
		d, _ := parserFixture(t, base)
		calls := 0
		open := func(context.Context) (storedDecodeInput, error) {
			calls++
			in := storedDecodeInput{bytes.NewReader(base), uint64(len(base)), sha256.Sum256(base), 1}
			if calls == 2 {
				switch identity {
				case "size":
					in.Size++
				case "digest":
					in.Digest[0] ^= 1
				case "slots":
					in.Slots++
				}
			}
			return in, nil
		}
		if _, err := d.count(context.Background(), open, environment.ContentRecordingFull); err == nil {
			t.Fatalf("accepted changed %s", identity)
		}
	}
}

func TestStoredCanonicalParserReservationsAndAllocations(t *testing.T) {
	large := strings.Repeat("<", 6<<20)
	for _, body := range []string{"short", large} {
		b := exchangecontent.Block{Kind: "text", Availability: exchangecontent.AvailabilityRecorded, Text: body, OriginalSize: len(body), ProviderSource: strings.Repeat("m", 1<<20), Fingerprint: strings.Repeat("f", 1<<20)}
		data, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		d, open := parserFixture(t, data)
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		facts, err := d.count(context.Background(), open, environment.ContentRecordingFull)
		runtime.ReadMemStats(&after)
		if err != nil || facts.TextBytes != uint64(len(body)) {
			t.Fatalf("count: %+v %v", facts, err)
		}
		allocated := after.TotalAlloc - before.TotalAlloc
		t.Logf("body=%d canonical=%d count allocated=%d", len(body), len(data), allocated)
		if allocated > 128<<10 {
			t.Fatalf("skipped bodies allocated %d", allocated)
		}
		part, err := d.selectBody(context.Background(), open, environment.ContentRecordingFull, 0, 3)
		if err != nil || string(part.Bytes) != body[:3] {
			t.Fatalf("range %q %v", part.Bytes, err)
		}
	}
	data := []byte(`{"kind":"text","availability":"recorded","text":"abcdef","originalSize":6}`)
	denied := errors.New("selected reservation refused")
	calls := 0
	opens := 0
	d, err := newStoredBlockDecoder(storedDecodeLimits{1 << 20, 1 << 20, 1 << 20, 1 << 20}, func(cost storedDecodeCost) error {
		calls++
		if cost.RetainedBytes > 0 {
			return denied
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	open := func(context.Context) (storedDecodeInput, error) {
		opens++
		return storedDecodeInput{bytes.NewReader(data), uint64(len(data)), sha256.Sum256(data), 1}, nil
	}
	part, err := d.selectBody(context.Background(), open, environment.ContentRecordingFull, 0, 3)
	if err != denied || opens != 1 || part.Bytes != nil || calls < 2 {
		t.Fatalf("reservation: %+v %v opens%d calls%d", part, err, opens, calls)
	}
}

func TestStoredCanonicalParserRawDepthAndRanges(t *testing.T) {
	for _, depth := range []int{9995, 9997, 9999, 10000} {
		raw := strings.Repeat("[", depth) + "0" + strings.Repeat("]", depth)
		b := exchangecontent.Block{Kind: "tool_call", Availability: exchangecontent.AvailabilityRecorded, CallID: "c", ToolName: "f", Arguments: json.RawMessage(raw)}
		data, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		_, legacyErr := decodeStoredBlock(data)
		d, open := parserFixture(t, data)
		facts, err := d.count(context.Background(), open, environment.ContentRecordingFull)
		if err != nil || depth < 10000 && legacyErr != nil {
			t.Fatalf("depth%d parser%v legacy%v", depth, err, legacyErr)
		}
		if facts.ArgumentDepth != uint16(depth) {
			t.Fatalf("authenticated raw depth=%d want%d", facts.ArgumentDepth, depth)
		}
	}
	for _, raw := range []string{`{"big":"` + strings.Repeat("a", 5<<20) + `"}`, "\"\xff\"", `"😀z"`} {
		b := exchangecontent.Block{Kind: "tool_call", Availability: exchangecontent.AvailabilityRecorded, CallID: "c", ToolName: "f", Arguments: json.RawMessage(raw)}
		data, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		d, open := parserFixture(t, data)
		got, err := d.full(context.Background(), open, environment.ContentRecordingFull)
		if err != nil || string(got.Arguments) != raw {
			t.Fatalf("raw full len%d: %v", len(raw), err)
		}
		_, err = d.selectBody(context.Background(), open, environment.ContentRecordingFull, 0, 5)
		if raw == "\"\xff\"" {
			if err == nil {
				t.Fatal("invalidUTF8 raw page accepted")
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
}

func FuzzStoredCanonicalParser(f *testing.F) {
	for _, s := range []string{`{"kind":"text","availability":"recorded","text":"abc","originalSize":3}`, `{"kind":"tool_call","availability":"recorded","originalSize":0,"callId":"c","toolName":"f","arguments":{"a":"\ud800"}}`, `{"kind":"text","availability":"recorded","text":"\u003C","originalSize":1}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64<<10 {
			t.Skip()
		}
		legacy, err := decodeStoredBlock(data)
		want := err == nil && legacy.Validate(environment.ContentRecordingFull) == nil
		d, open := parserFixture(t, data)
		got, err := d.full(context.Background(), open, environment.ContentRecordingFull)
		if (err == nil) != want {
			t.Fatalf("acceptance legacy=%v parser=%v input=%q", want, err, data)
		}
		if want && !reflect.DeepEqual(got, legacy) {
			t.Fatal("decoded mismatch")
		}
	})
}

func TestStoredCanonicalParserFiniteLimitsAndWorkspace(t *testing.T) {
	b := exchangecontent.Block{Kind: "tool_call", Availability: exchangecontent.AvailabilityRecorded, CallID: strings.Repeat("c", 512), ToolName: strings.Repeat("n", 256), ToolNamespace: strings.Repeat("s", 256), Arguments: json.RawMessage(`null`), Fingerprint: "sha256:" + strings.Repeat("a", 64), Agent: &exchangecontent.AgentContext{AgentName: strings.Repeat("a", 512), Author: strings.Repeat("b", 512), Recipient: strings.Repeat("c", 512)}}
	if err := b.Validate(environment.ContentRecordingFull); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	_, open := parserFixture(t, data)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	var snapshot storedDecodeCost
	d, err := newStoredBlockDecoder(storedDecodeLimits{1 << 20, 1 << 20, 1 << 20, storedDecodeFixedWorkspace}, func(c storedDecodeCost) error { snapshot = c; return nil })
	if err != nil {
		t.Fatal(err)
	}
	facts, err := d.count(context.Background(), open, environment.ContentRecordingFull)
	runtime.ReadMemStats(&after)
	if err != nil || facts.Shape != b.RetainedShape() {
		t.Fatalf("metadata count: %v", err)
	}
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("fixed workspace=%d maximal metadata cold allocations=%d retained=%+v", storedDecodeFixedWorkspace, allocated, snapshot)
	if allocated > storedDecodeFixedWorkspace+snapshot.RetainedBytes+snapshot.RetainedStructureBytes {
		t.Fatalf("fixed workspace undercounts observed allocation %d", allocated)
	}
	wantStrings := uint64(len(b.Kind) + len(b.Availability) + len(b.CallID) + len(b.ToolName) + len(b.ToolNamespace) + len(b.Agent.AgentName) + len(b.Agent.Author) + len(b.Agent.Recipient))
	if snapshot.RetainedBytes != wantStrings || snapshot.RetainedStructureBytes != uint64(reflect.TypeFor[storedBlockFacts]().Size()) {
		t.Fatalf("count cost %+v", snapshot)
	}
	for _, which := range []string{"canonical", "retained", "structure", "workspace"} {
		limits := storedDecodeLimits{1 << 20, 1 << 20, 1 << 20, 1 << 20}
		switch which {
		case "canonical":
			limits.CanonicalBytes = uint64(len(data) - 1)
		case "retained":
			limits.RetainedBytes = wantStrings - 1
		case "structure":
			limits.RetainedStructureBytes = uint64(reflect.TypeFor[storedBlockFacts]().Size()) - 1
		case "workspace":
			limits.WorkspaceBytes = storedDecodeFixedWorkspace - 1
		}
		candidate, err := newStoredBlockDecoder(limits, func(storedDecodeCost) error { return nil })
		if err == nil {
			_, err = candidate.count(context.Background(), open, environment.ContentRecordingFull)
		}
		if !errors.Is(err, exchangecontent.ErrInvalidEvidence) {
			t.Fatalf("%s own limit not enforced: %v", which, err)
		}
	}
	// Sequential use resets the absolute output snapshot without invalidating
	// old returned outputs. The outer owner must keep their separate reservations.
	full, err := d.full(context.Background(), open, environment.ContentRecordingFull)
	if err != nil {
		t.Fatal(err)
	}
	saved := append([]byte(nil), full.Arguments...)
	data[len(data)/2] ^= 1
	if !bytes.Equal(full.Arguments, saved) {
		t.Fatal("returned raw aliases input")
	}
	data[len(data)/2] ^= 1
	if _, err := d.count(context.Background(), open, environment.ContentRecordingFull); err != nil {
		t.Fatal(err)
	}
	full.Arguments[0] = 'X'
	if bytes.Contains(data, []byte(`Xull`)) {
		t.Fatal("input aliases returned raw")
	}
}

func TestStoredCanonicalParserRangeBoundaries(t *testing.T) {
	data := []byte(`{"kind":"text","availability":"recorded","text":"a😀z","originalSize":6}`)
	for _, tc := range []struct {
		offset, size uint64
		want         string
		bad          bool
	}{{0, 4, "a", false}, {1, 4, "😀", false}, {1, 3, "", true}, {2, 4, "", true}, {5, 99, "z", false}, {6, 1, "", true}} {
		d, open := parserFixture(t, data)
		got, err := d.selectBody(context.Background(), open, environment.ContentRecordingFull, tc.offset, tc.size)
		if (err != nil) != tc.bad || string(got.Bytes) != tc.want {
			t.Fatalf("%+v got%q err%v", tc, got.Bytes, err)
		}
	}
	// A selected prefix never excuses malformed/invalidUTF8 content in the tail.
	raw := []byte(`{"kind":"tool_call","availability":"recorded","originalSize":0,"callId":"c","toolName":"f","arguments":"abcdef` + string([]byte{0xff}) + `"}`)
	d, open := parserFixture(t, raw)
	if _, err := d.count(context.Background(), open, environment.ContentRecordingFull); err != nil {
		t.Fatal(err)
	}
	if _, err := d.selectBody(context.Background(), open, environment.ContentRecordingFull, 0, 2); err == nil {
		t.Fatal("invalidUTF8 skipped tail accepted as page")
	}
	calls := 0
	open = func(context.Context) (storedDecodeInput, error) {
		calls++
		body := raw
		if calls == 2 {
			body = bytes.Replace(raw, []byte("abcdef"), []byte("abcdeg"), 1)
		}
		return storedDecodeInput{bytes.NewReader(body), uint64(len(raw)), sha256.Sum256(raw), 1}, nil
	}
	if _, err := d.count(context.Background(), open, environment.ContentRecordingFull); err == nil {
		t.Fatal("mutated raw skipped tail")
	}
}

// Frames are assembled independently from standard-library canonical bytes,
// rather than using the production framing writer as its own parser oracle.
func parserFramedFixture(t *testing.T, data []byte, falseLogical bool) (storedDecodeOpen, func()) {
	t.Helper()
	sum := sha256.Sum256(data)
	claimed := sum
	if falseLogical {
		claimed[0] ^= 1
	}
	count := (len(data)-1)/((32<<20)-56) + 1
	rows := make(map[string][]byte)
	manifest := ""
	for index := 0; index < count; index++ {
		start := index * ((32 << 20) - 56)
		end := min(len(data), start+((32<<20)-56))
		row := make([]byte, 56+end-start)
		copy(row, "VMECB\x00\x01\x00")
		copy(row[8:40], claimed[:])
		binary.BigEndian.PutUint64(row[40:48], uint64(len(data)))
		binary.BigEndian.PutUint32(row[48:52], uint32(index))
		binary.BigEndian.PutUint32(row[52:56], uint32(count))
		copy(row[56:], data[start:end])
		physical := sha256.Sum256(row)
		digest := hex.EncodeToString(physical[:])
		rows[digest] = row
		manifest += digest
	}
	loads := 0
	open := func(ctx context.Context) (storedDecodeInput, error) {
		r, err := newStoredLogicalBlockReader(ctx, manifest, 0, 1<<30, func(_ context.Context, digest string) (int, string, []byte, error) {
			loads++
			row := rows[digest]
			return len(row), chunkCodecIdentity, row, nil
		})
		if err != nil {
			return storedDecodeInput{}, err
		}
		return storedDecodeInput{r, r.Size, r.Digest, r.Slots}, nil
	}
	return open, func() {
		if loads < count {
			t.Fatalf("tail not loaded: %d/%d", loads, count)
		}
	}
}

func TestStoredCanonicalParserAuthenticatedFrames(t *testing.T) {
	for _, unit := range []string{`\u003c`, "😀", `\ud800`} {
		t.Run(unit, func(t *testing.T) {
			raw := unit == `\ud800`
			prefix := `{"kind":"text","availability":"recorded","text":"`
			suffix := `","originalSize":0}`
			if raw {
				prefix = `{"kind":"tool_call","availability":"recorded","originalSize":0,"callId":"c","toolName":"f","arguments":"`
				suffix = `"}`
			}
			// Place two bytes of the multibyte/escape unit in the first physical body.
			padding := (32 << 20) - 56 - len(prefix) - 2
			data := []byte(prefix + strings.Repeat("x", padding) + unit + strings.Repeat("z", 100) + suffix)
			legacy, err := decodeStoredBlock(data)
			if err != nil || legacy.Validate(environment.ContentRecordingFull) != nil {
				t.Fatalf("fixture %v", err)
			}
			d, _ := parserFixture(t, data)
			open, tail := parserFramedFixture(t, data, false)
			got, err := d.selectBody(context.Background(), open, environment.ContentRecordingFull, uint64(padding-1), 16)
			tail()
			decoded := unit
			if unit == `\u003c` {
				decoded = "<"
			}
			want := "x" + decoded + strings.Repeat("z", 16-len(decoded)-1)
			// Raw offsets include the opening JSON quote; ordinary offsets are decoded.
			if raw {
				want = "xx" + unit + strings.Repeat("z", 16-len(unit)-2)
			}
			if err != nil || string(got.Bytes) != want {
				t.Fatalf("split range got%q want%q err%v", got.Bytes, want, err)
			}
			bad, tail := parserFramedFixture(t, data, true)
			got, err = d.selectBody(context.Background(), bad, environment.ContentRecordingFull, 0, 1)
			tail()
			if !errors.Is(err, exchangecontent.ErrInvalidEvidence) || got.Bytes != nil {
				t.Fatalf("authenticated EOF failure: %+v %v", got, err)
			}
		})
	}
}

func TestStoredCanonicalParserTruncatedEvidence(t *testing.T) {
	data := []byte(`{"kind":"tool_call","availability":"recorded","originalSize":0,"callId":"c","toolName":"f","arguments":{"x":[1e+02,"\ud800"]}}`)
	for end := 1; end < len(data); end++ {
		d, open := parserFixture(t, data[:end])
		got, err := d.count(context.Background(), open, environment.ContentRecordingFull)
		if !errors.Is(err, exchangecontent.ErrInvalidEvidence) || got != (storedBlockFacts{}) {
			t.Fatalf("truncated at%d: %+v %v", end, got, err)
		}
	}
}

func TestStoredCanonicalParserReusesDeepWorkspace(t *testing.T) {
	b := exchangecontent.Block{Kind: "tool_call", Availability: exchangecontent.AvailabilityRecorded, CallID: "c", ToolName: "f", Arguments: json.RawMessage(strings.Repeat("[", 9999) + "0" + strings.Repeat("]", 9999))}
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	d, open := parserFixture(t, data)
	if _, err := d.count(context.Background(), open, environment.ContentRecordingFull); err != nil {
		t.Fatal(err)
	}
	tiny := []byte(`{"kind":"tool_call","availability":"recorded","originalSize":0,"callId":"c","toolName":"f","arguments":null}`)
	_, open = parserFixture(t, tiny)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range 2000 {
		if _, err := d.count(context.Background(), open, environment.ContentRecordingFull); err != nil {
			t.Fatal(err)
		}
	}
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("2000 complete two-pass tiny blocks after depth9999: %d allocated bytes", allocated)
	if allocated > 8<<20 {
		t.Fatalf("per-block stack allocation: %d", allocated)
	}
}

func TestStoredCanonicalParserLargeMetadataFull(t *testing.T) {
	b := exchangecontent.Block{Kind: "text", Availability: exchangecontent.AvailabilityRecorded, ProviderSource: strings.Repeat("s", 1<<20), ProviderKind: strings.Repeat("k", 1<<20), Fingerprint: strings.Repeat("f", 1<<20)}
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	d, open := parserFixture(t, data)
	got, err := d.full(context.Background(), open, environment.ContentRecordingFull)
	if err != nil || !reflect.DeepEqual(got, b) {
		t.Fatalf("large metadata roundtrip: %v", err)
	}
}

func TestStoredCanonicalParserDeniesBeforeLargeCopies(t *testing.T) {
	data := []byte(`{"kind":"text","availability":"recorded","text":"` + strings.Repeat("a", 6<<20) + `","originalSize":6291456}`)
	for _, full := range []bool{false, true} {
		denied := errors.New("large reservation denied")
		opens := 0
		d, err := newStoredBlockDecoder(storedDecodeLimits{1 << 30, 1 << 29, 1 << 20, 1 << 20}, func(cost storedDecodeCost) error {
			if cost.RetainedBytes > 0 {
				return denied
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		open := func(context.Context) (storedDecodeInput, error) {
			opens++
			return storedDecodeInput{bytes.NewReader(data), uint64(len(data)), sum, 1}, nil
		}
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		if full {
			_, err = d.full(context.Background(), open, environment.ContentRecordingFull)
		} else {
			_, err = d.selectBody(context.Background(), open, environment.ContentRecordingFull, 0, 6<<20)
		}
		runtime.ReadMemStats(&after)
		allocated := after.TotalAlloc - before.TotalAlloc
		if err != denied || opens != 1 || allocated > 64<<10 {
			t.Fatalf("full%v err%v opens%d allocated%d", full, err, opens, allocated)
		}
		t.Logf("full=%v denied 6MiB output before allocation: %d bytes", full, allocated)
	}
}

func TestStoredCanonicalParserFullOwnershipAndAllocation(t *testing.T) {
	first := exchangecontent.Block{Kind: "tool_call", Availability: exchangecontent.AvailabilityRecorded, Text: strings.Repeat("a", 6<<20), CallID: "c", ToolName: "f", Arguments: json.RawMessage(`{"v":1}`), ProviderKind: strings.Repeat("p", 1<<20)}
	firstData, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	d, open := parserFixture(t, firstData)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	gotFirst, err := d.full(context.Background(), open, environment.ContentRecordingFull)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("full 7MiB ordinary fields allocated=%d", allocated)
	if allocated > 8<<20 {
		t.Fatalf("whole-field conversion copied retained strings: %d", allocated)
	}
	second := first
	second.Text = strings.Repeat("b", 6<<20)
	second.ProviderKind = strings.Repeat("q", 1<<20)
	second.Arguments = json.RawMessage(`{"v":2}`)
	secondData, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	_, open = parserFixture(t, secondData)
	gotSecond, err := d.full(context.Background(), open, environment.ContentRecordingFull)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotFirst, first) || !reflect.DeepEqual(gotSecond, second) {
		t.Fatal("sequential reads changed prior result")
	}
	gotSecond.Arguments[5] = '9'
	if string(gotFirst.Arguments) != `{"v":1}` || !bytes.Contains(secondData, []byte(`{"v":2}`)) {
		t.Fatal("raw results alias each other/input")
	}
	calls := 0
	open = func(context.Context) (storedDecodeInput, error) {
		calls++
		data := secondData
		if calls == 2 {
			data = firstData
		}
		return storedDecodeInput{bytes.NewReader(data), uint64(len(secondData)), sha256.Sum256(secondData), 1}, nil
	}
	failed, err := d.full(context.Background(), open, environment.ContentRecordingFull)
	if err == nil || !reflect.DeepEqual(failed, exchangecontent.Block{}) || !reflect.DeepEqual(gotFirst, first) || gotSecond.Text != second.Text || gotSecond.ProviderKind != second.ProviderKind {
		t.Fatal("failed pass altered live result or published provisional block")
	}
}

func FuzzStoredCanonicalValues(f *testing.F) {
	for _, s := range []string{"plain", "<>&\n😀\u2028", string([]byte{0xff}), `{"x":"\ud800","x":1e+02}`, `[ null ]`, `"\u003C"`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, value []byte) {
		if len(value) > 16<<10 {
			t.Skip()
		}
		check := func(data []byte) {
			legacy, err := decodeStoredBlock(data)
			want := err == nil && legacy.Validate(environment.ContentRecordingFull) == nil
			d, open := parserFixture(t, data)
			got, err := d.full(context.Background(), open, environment.ContentRecordingFull)
			if (err == nil) != want {
				t.Fatalf("legacy=%v parser=%v data=%q", want, err, data)
			}
			if want && !reflect.DeepEqual(got, legacy) {
				t.Fatal("value mismatch")
			}
		}
		b := exchangecontent.Block{Kind: "text", Availability: exchangecontent.AvailabilityRecorded, Text: string(value), OriginalSize: len(value)}
		data, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		check(data)
		// Independent raw oracle: direct spelling acceptance and actual RawMessage
		// marshal compaction/HTML escaping. It must preserve all other spellings.
		raw := append([]byte(`{"kind":"tool_call","availability":"recorded","originalSize":0,"callId":"c","toolName":"f","arguments":`), value...)
		raw = append(raw, '}')
		check(raw)
		if json.Valid(value) {
			b = exchangecontent.Block{Kind: "tool_call", Availability: exchangecontent.AvailabilityRecorded, CallID: "c", ToolName: "f", Arguments: json.RawMessage(value)}
			data, err = json.Marshal(b)
			if err != nil {
				t.Fatal(err)
			}
			check(data)
		}
	})
}

func TestStoredCanonicalParserValidModes(t *testing.T) {
	data := []byte(`{"kind":"text","availability":"recorded","text":"a\u003c😀z","originalSize":7}`)
	want := exchangecontent.Block{Kind: "text", Availability: exchangecontent.AvailabilityRecorded, Text: "a<😀z", OriginalSize: 7}
	legacy, err := decodeStoredBlock(data)
	if err != nil || !reflect.DeepEqual(legacy, want) {
		t.Fatalf("invalid fixture: %+v %v", legacy, err)
	}
	if err := want.Validate(environment.ContentRecordingFull); err != nil {
		t.Fatal(err)
	}
	d, open := parserFixture(t, data)
	full, err := d.full(context.Background(), open, environment.ContentRecordingFull)
	if err != nil || !reflect.DeepEqual(full, want) {
		t.Fatalf("full=%+v err=%v", full, err)
	}
	facts, err := d.count(context.Background(), open, environment.ContentRecordingFull)
	if err != nil || facts.TextBytes != 7 || facts.CanonicalBytes != uint64(len(data)) {
		t.Fatalf("facts=%+v err=%v", facts, err)
	}
	part, err := d.selectBody(context.Background(), open, environment.ContentRecordingFull, 1, 5)
	if err != nil || string(part.Bytes) != "<😀" || part.End != 6 {
		t.Fatalf("range=%+v err=%v", part, err)
	}
	encoded, err := json.Marshal(full)
	if err != nil || !bytes.Equal(encoded, data) {
		t.Fatalf("canonical=%q err=%v", encoded, err)
	}
}
