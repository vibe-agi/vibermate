package nativemcp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
)

func TestNativeRetiredSessionRegistryAndMapsAreReleased(t *testing.T) {
	f := newNativeFixtureMode(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	e := f.executor.(*executor)
	for cycle := 0; cycle < 12; cycle++ {
		f.transport.handler = nil
		f.handshake(t)
		var old *session
		e.mu.Lock()
		owner := e.scopes[f.capture.Key()]
		for _, s := range owner.sessions {
			if !s.closed {
				old = s
			}
		}
		e.mu.Unlock()
		f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
			if r.Method == http.MethodDelete {
				return &http.Response{StatusCode: 202, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
			}
			var wire strings.Builder
			for i := 0; i < 64; i++ {
				fmt.Fprintf(&wire, "id: event-%d\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":9,\"progress\":%d,\"message\":%q}}\n\n", i, i, strings.Repeat("x", 1024))
			}
			wire.WriteString("id: terminal\ndata: {\"jsonrpc\":\"2.0\",\"id\":44,\"result\":{}}\n\n")
			return wireResponse(wire.String(), true), nil
		}
		terminal, _ := f.serve(t, `{"jsonrpc":"2.0","id":44,"method":"ping","params":{"_meta":{"progressToken":9}}}`, "synthetic-session")
		if terminal.Outcome != OutcomeCompleted {
			t.Fatal(terminal)
		}
		if len(old.eventIndex) != 65 {
			t.Fatalf("real event allocation absent %d", len(old.eventIndex))
		}
		terminal = e.Serve(context.Background(), f.captured(t, "DELETE", "", "synthetic-session"), httptest.NewRecorder())
		if terminal.Outcome != OutcomeCompleted {
			t.Fatal(terminal)
		}
		e.mu.Lock()
		size := len(owner.sessions)
		e.mu.Unlock()
		if size != 0 || old.eventIndex != nil || old.parents != nil || old.tools != nil || old.active != nil || old.callbacks != nil || old.elicitations != nil || old.hasher != nil || old.retained != 0 {
			t.Fatalf("retired allocations reachable cycle%d registry%d eventsNil%v activeNil%v retained%d", cycle, size, old.eventIndex == nil, old.active == nil, old.retained)
		}
		before := f.transport.sends()
		terminal, _ = f.serve(t, `{"jsonrpc":"2.0","id":44,"method":"ping"}`, "synthetic-session")
		if terminal.Outcome == OutcomeCompleted || f.transport.sends() != before {
			t.Fatal("retired session replayed")
		}
		var once int
		if err := f.db.QueryRow(`SELECT COUNT(*) FROM native_tool_once`).Scan(&once); err != nil || once != (cycle+1)*5 {
			t.Fatalf("SQL once lost %d %v", once, err)
		}
	}
}

func TestNativeExpiredSessionUnlinksRegistry(t *testing.T) {
	limits := DefaultLimits()
	limits.SessionIdle = time.Minute
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, limits)
	e := f.executor.(*executor)
	now := e.dependencies.Clock()
	e.dependencies.Clock = func() time.Time { return now }
	for cycle := 0; cycle < 6; cycle++ {
		f.handshake(t)
		e.mu.Lock()
		owner := e.scopes[f.capture.Key()]
		var old *session
		for _, s := range owner.sessions {
			old = s
		}
		e.mu.Unlock()
		now = old.timeLimit.Add(time.Millisecond)
		terminal, _ := f.serve(t, `{"jsonrpc":"2.0","id":44,"method":"ping"}`, "synthetic-session")
		if terminal.Outcome == OutcomeCompleted {
			t.Fatal("expired sent")
		}
		e.mu.Lock()
		size := len(owner.sessions)
		e.mu.Unlock()
		if size != 0 || old.active != nil || old.eventIndex != nil {
			t.Fatalf("expired registry retained %d", size)
		}
	}
}

type heldRetirementBody struct{ entered, closed, release chan struct{} }

func (b *heldRetirementBody) Read([]byte) (int, error) {
	close(b.entered)
	<-b.release
	return 0, io.EOF
}
func (b *heldRetirementBody) Close() error {
	select {
	case <-b.closed:
	default:
		close(b.closed)
	}
	return nil
}

func TestNativeRetirementChargesActiveConsumersUntilDrain(t *testing.T) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	e := f.executor.(*executor)
	var old *session
	e.mu.Lock()
	owner := e.scopes[f.capture.Key()]
	for _, s := range owner.sessions {
		old = s
	}
	e.mu.Unlock()
	body := &heldRetirementBody{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	f.transport.handler = func(r *http.Request, _ []byte) (*http.Response, error) {
		if r.Method == http.MethodGet {
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: body}, nil
		}
		return &http.Response{StatusCode: 202, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	results := make(chan Terminal, 1)
	go func() {
		results <- e.Serve(context.Background(), f.captured(t, "GET", "", "synthetic-session"), httptest.NewRecorder())
	}()
	<-body.entered
	terminal := e.Serve(context.Background(), f.captured(t, "DELETE", "", "synthetic-session"), httptest.NewRecorder())
	if terminal.Outcome != OutcomeCompleted {
		t.Fatal(terminal)
	}
	e.mu.Lock()
	size := len(owner.sessions)
	e.mu.Unlock()
	old.mu.Lock()
	held := old.retained
	handles := len(old.active)
	old.mu.Unlock()
	if size != 0 || held == 0 || handles != 1 {
		t.Fatalf("active allocations uncharged/unlinked registry%d retained%d handles%d", size, held, handles)
	}
	close(body.release)
	<-results
	old.mu.Lock()
	defer old.mu.Unlock()
	if old.retained != 0 || old.active != nil || old.eventIndex != nil || old.hasher != nil {
		t.Fatal("drained allocations retained")
	}
}

func TestNativeRetiredSessionReceiptNeverSends(t *testing.T) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	receipt, request, plan := prepareRealReceipt(t, f)
	terminal := f.executor.Serve(context.Background(), f.captured(t, "DELETE", "", "synthetic-session"), httptest.NewRecorder())
	if terminal.Outcome != OutcomeCompleted {
		t.Fatal(terminal)
	}
	before := f.transport.sends()
	sent := f.executor.(*executor).sendCommitted(context.Background(), request, plan, receipt)
	response, err := sent.response, sent.err
	if response != nil {
		response.Body.Close()
	}
	if err == nil || f.transport.sends() != before {
		t.Fatal("retired receipt sent")
	}
	terminal, _ = f.serve(t, `{"jsonrpc":"2.0","id":99,"result":{"action":"decline"}}`, "synthetic-session")
	if terminal.Outcome == OutcomeCompleted || f.transport.sends() != before {
		t.Fatal("retired session reply sent")
	}
}
