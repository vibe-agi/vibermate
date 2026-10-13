package nativemcp

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"fmt"
)

func TestNativeManyShortSSELinesDoNotAllocatePerLine(t *testing.T) {
	wire := []byte(strings.Repeat(":\n", (8<<20)-1) + "\n")
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	event, err := parseSSEEvent(wire, DefaultLimits())
	runtime.ReadMemStats(&after)
	if err != nil || len(event.data) != 0 {
		t.Fatalf("legal comment frame %v", err)
	}
	if after.TotalAlloc-before.TotalAlloc > 1<<20 {
		t.Fatalf("per-line allocation exceeded budget: %d", after.TotalAlloc-before.TotalAlloc)
	}
}

func TestNativeSSEJoinedDataPreservesEmptyFields(t *testing.T) {
	event, err := parseSSEEvent([]byte("data:\ndata: {}\ndata:\n\n"), DefaultLimits())
	if err != nil || string(event.data) != "\n{}\n" {
		t.Fatalf("SSE data normalization %q %v", event.data, err)
	}
}

func TestNativeTypedContentDoesNotCloneLargeRawBlocks(t *testing.T) {
	raw := []byte(`{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"` + strings.Repeat("x", 3<<20) + `"}]}}`)
	result, err := parseResponse(raw, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	err = validateMethodResult(nativetool.RequestToolsCall, result, DefaultLimits())
	runtime.ReadMemStats(&after)
	if err != nil || after.TotalAlloc-before.TotalAlloc > uint64(jsonAllocation(raw)) {
		t.Fatalf("content clones unaccounted %v allocations%d admitted%d", err, after.TotalAlloc-before.TotalAlloc, jsonAllocation(raw))
	}
}

type allocationHoldingWriter struct {
	*httptest.ResponseRecorder
	entered, release chan struct{}
}

func (w *allocationHoldingWriter) Write(p []byte) (int, error) {
	close(w.entered)
	<-w.release
	return w.ResponseRecorder.Write(p)
}

func TestNativeAggregateResponseAllocationAdmission(t *testing.T) {
	limits := DefaultLimits()
	limits.BufferBytes = 8 << 20
	limits.ResultBytes = 4 << 20
	limits.EventBytes = 4 << 20
	f := newNativeFixtureMode(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, limits)
	f.handshake(t)
	e := f.executor.(*executor)
	f.transport.handler = func(_ *http.Request, body []byte) (*http.Response, error) {
		r, _ := parseRequest(body, limits)
		wire := `{"jsonrpc":"2.0","id":` + string(r.id) + `,"result":{"content":[{"type":"text","text":"` + strings.Repeat("x", 3<<20) + `"}]}}`
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(wire))}, nil
	}
	w := &allocationHoldingWriter{httptest.NewRecorder(), make(chan struct{}), make(chan struct{})}
	results := make(chan Terminal, 1)
	c := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"}}}`, "synthetic-session")
	go func() { results <- e.Serve(context.Background(), c, w) }()
	<-w.entered
	terminal, _ := f.serve(t, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"b"}}}`, "synthetic-session")
	e.budget.mu.Lock()
	used := e.budget.used
	e.budget.mu.Unlock()
	close(w.release)
	<-results
	if terminal.Outcome == OutcomeCompleted || used > limits.BufferBytes {
		t.Fatalf("aggregate allocation admitted %+v used%d", terminal, used)
	}
}
func TestNativeManySmallJSONKeysAdmittedBeforeMaps(t *testing.T) {
	var raw strings.Builder
	raw.WriteByte('{')
	for index := 0; index < 300000; index++ {
		if index > 0 {
			raw.WriteByte(',')
		}
		fmt.Fprintf(&raw, "\"k%d\":0", index)
	}
	raw.WriteByte('}')
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := decodeObject([]byte(raw.String()), DefaultLimits().ResultBytes, 64)
	runtime.ReadMemStats(&after)
	if err != errBudget {
		t.Fatalf("unaccounted member shape admitted %v", err)
	}
	if after.TotalAlloc-before.TotalAlloc > 8<<20 {
		t.Fatalf("maps allocated before rejection %d", after.TotalAlloc-before.TotalAlloc)
	}
}

func TestNativeManySmallArrayElementsCountBeforeDecode(t *testing.T) {
	raw := []byte(`{"items":[` + strings.Repeat(`"a",`, 600000) + `"a"]}`)
	if jsonAllocation(raw) <= DefaultLimits().BufferBytes {
		t.Fatal("array element allocations omitted")
	}
	if _, err := decodeObject(raw, DefaultLimits().ResultBytes, 64); err != errBudget {
		t.Fatalf("unadmitted array decoded %v", err)
	}
}
