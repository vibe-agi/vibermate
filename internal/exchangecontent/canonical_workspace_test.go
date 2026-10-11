package exchangecontent

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"runtime"
	"strings"
	"testing"
	"unsafe"
)

func workspaceMessage(message Message) MessageSource {
	return MessageSource{source: &Source{limits: sourceFixtureLimits(), record: &Record{Request: Request{Messages: []Message{message}}}}, part: RequestPart}
}

func TestCanonicalWorkspaceSequentialParityAndSinkIndependence(t *testing.T) {
	var encoder CanonicalEncoder
	for _, text := range []string{"", "<&>\"\\\b\f\n\r\t\x00", "😀\u2028\u2029", string([]byte{0xff, 0xfe})} {
		block := Block{Kind: "tool_call", Availability: AvailabilityRecorded, Text: text, OriginalSize: len(text), CallID: "call", ToolName: "f", Arguments: json.RawMessage(` { "z" : 1e+02 , "a":"<&>\u0026\u2028" } `), Agent: &AgentContext{AgentName: "<&>"}}
		message := Message{Role: "assistant", Blocks: []Block{block, {Kind: "text", Availability: AvailabilityRecorded, Text: "TAIL"}}, Agent: &AgentContext{Author: "author", Recipient: "recipient"}}
		wantBlock, err := json.Marshal(block)
		if err != nil {
			t.Fatal(err)
		}
		wantMessage, err := json.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		var first, second bytes.Buffer
		if err := encoder.WriteBlock(&first, block); err != nil {
			t.Fatal(err)
		}
		retained := append([]byte(nil), first.Bytes()...)
		if err := encoder.WriteMessage(&second, workspaceMessage(message)); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first.Bytes(), retained) || !bytes.Equal(retained, wantBlock) || !bytes.Equal(second.Bytes(), wantMessage) {
			t.Fatal("sequential canonical/JSON parity or retained sink independence changed")
		}
		assertCanonicalWorkspaceReleased(t, &encoder)
	}
	// Raw leaves keep their independent10000 depth, without a parent scanner.
	deep := strings.Repeat("[", 10000) + "0" + strings.Repeat("]", 10000)
	block := Block{Kind: "tool_call", Availability: AvailabilityRecorded, Arguments: json.RawMessage(deep)}
	var out bytes.Buffer
	if err := encoder.WriteBlock(&out, block); err != nil {
		t.Fatal(err)
	}
	want := `{"kind":"tool_call","availability":"recorded","originalSize":0,"arguments":` + deep + `}`
	if out.String() != want {
		t.Fatal("independent deep raw leaf changed")
	}
	assertCanonicalWorkspaceReleased(t, &encoder)
}

func assertCanonicalWorkspaceReleased(t *testing.T, encoder *CanonicalEncoder) {
	t.Helper()
	w := &encoder.writer
	if w.sink != nil || w.used != 0 || w.err != nil || w.unescapedHTML {
		t.Fatal("workspace retained sink/error/encoding state")
	}
	for _, b := range w.scratch {
		if b != 0 {
			t.Fatal("workspace retained old canonical bytes")
		}
	}
}

func TestCanonicalWorkspaceFailureThenReuse(t *testing.T) {
	var encoder CanonicalEncoder
	valid := Block{Kind: "text", Availability: AvailabilityRecorded, Text: "GOOD"}
	invalid := Block{Kind: "tool_call", Arguments: json.RawMessage(`{"late":`)}
	var out bytes.Buffer
	if err := encoder.WriteBlock(&out, invalid); err == nil || out.Len() != 0 {
		t.Fatal("invalid block exposed a prefix")
	}
	if err := encoder.WriteMessage(&out, workspaceMessage(Message{Role: "user", Blocks: []Block{valid, invalid}})); err == nil || out.Len() != 0 {
		t.Fatal("late invalid message block exposed a prefix")
	}
	if !errors.Is(encoder.WriteMessage(nil, MessageSource{}), ErrInvalidEvidence) || !errors.Is(encoder.WriteBlock(nil, invalid), ErrInvalidEvidence) {
		t.Fatal("nil sink precedence changed")
	}
	assertCanonicalWorkspaceReleased(t, &encoder)
	sentinel := errors.New("workspace sink failed")
	for _, mode := range []string{"error", "partialnil", "zero", "fullerr", "negative", "oversized"} {
		for _, operation := range []string{"block", "message"} {
			fault := faultSink{offset: 5, mode: mode, err: sentinel}
			var err error
			if operation == "message" {
				err = encoder.WriteMessage(&fault, workspaceMessage(Message{Role: "user", Blocks: []Block{valid}}))
			} else {
				err = encoder.WriteBlock(&fault, valid)
			}
			if err == nil || fault.calls != 1 {
				t.Fatalf("writer failure lost: %s %v", mode, err)
			}
			if (mode == "error" || mode == "fullerr") && !errors.Is(err, sentinel) {
				t.Fatal("sink error identity lost")
			}
			if (mode == "partialnil" || mode == "zero") && !errors.Is(err, io.ErrShortWrite) {
				t.Fatal("short write identity lost")
			}
			assertCanonicalWorkspaceReleased(t, &encoder)
			out.Reset()
			if err := encoder.WriteMessage(&out, workspaceMessage(Message{Role: "user", Blocks: []Block{valid}})); err != nil {
				t.Fatal(err)
			}
			if out.String() != `{"role":"user","blocks":[{"kind":"text","availability":"recorded","text":"GOOD","originalSize":0}]}` {
				t.Fatal("failed write poisoned next message")
			}
		}
	}
}

type reentrantCanonicalSink struct {
	encoder *CanonicalEncoder
	out     bytes.Buffer
	nested  error
}

func (s *reentrantCanonicalSink) Write(p []byte) (int, error) {
	s.nested = s.encoder.WriteBlock(io.Discard, Block{Kind: "text", Availability: AvailabilityRecorded, Text: "nested"})
	return s.out.Write(p)
}

func TestCanonicalWorkspaceRefusesReentrancyAndBoundsRepeatedAllocation(t *testing.T) {
	var encoder CanonicalEncoder
	block := Block{Kind: "text", Availability: AvailabilityRecorded, Text: "TAIL"}
	sink := reentrantCanonicalSink{encoder: &encoder}
	if err := encoder.WriteBlock(&sink, block); err != nil || !errors.Is(sink.nested, ErrInvalidEvidence) {
		t.Fatalf("reentrant mutation: outer=%v nested=%v", err, sink.nested)
	}
	if sink.out.String() != `{"kind":"text","availability":"recorded","text":"TAIL","originalSize":0}` {
		t.Fatal("reentrant callback changed outer bytes")
	}
	assertCanonicalWorkspaceReleased(t, &encoder)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := 0; i < 1024; i++ {
		if err := encoder.WriteBlock(io.Discard, block); err != nil {
			t.Fatal(err)
		}
	}
	runtime.ReadMemStats(&after)
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated >= 256<<10 {
		t.Fatalf("repeated fixed scratch allocated: %d B", allocated)
	}
	t.Logf("operation-owned encoder=%d B; canonicalWriter=%d B; fixed scratch=%d B", unsafe.Sizeof(encoder), unsafe.Sizeof(encoder.writer), len(encoder.writer.scratch))
	assertCanonicalWorkspaceReleased(t, &encoder)
}
