package nativemcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/environment"
)

// A new event advances the same stream's replay position just as a retained
// event does. Publishing an older retained event afterwards regresses it.
func TestNativeStreamNewEventRejectsRetainedOrderRegression(t *testing.T) {
	f := newNativeFixtureMode(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	event := func(id, progress string) string {
		return "id: " + id + "\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":9,\"progress\":" + progress + "}}\n\n"
	}
	e1, e2, e3 := event("e1", "1"), event("e2", "2"), event("e3", "3")
	f.transport.handler = func(r *http.Request, _ []byte) (*http.Response, error) {
		if r.Method == "GET" {
			return wireResponse(e3+e2, true), nil
		}
		return wireResponse(e1+e2, true), nil
	}
	f.serve(t, `{"jsonrpc":"2.0","id":40,"method":"ping","params":{"_meta":{"progressToken":9}}}`, "synthetic-session")
	get := f.captured(t, "GET", "", "synthetic-session")
	get.request.Header.Set("Last-Event-Id", "e1")
	w := httptest.NewRecorder()
	f.executor.Serve(context.Background(), get, w)
	if !strings.Contains(w.Body.String(), e3) || strings.Contains(w.Body.String(), e2) {
		t.Fatalf("new event did not fence older replay: %q", w.Body.String())
	}
}

func TestNativeFreshGETRejectsResponsesAndUnrelatedProgress(t *testing.T) {
	for _, message := range []string{
		`{"jsonrpc":"2.0","id":40,"result":{}}`,
		`{"jsonrpc":"2.0","id":40,"error":{"code":-1,"message":"synthetic"}}`,
		`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":9,"progress":1}}`,
	} {
		t.Run(message, func(t *testing.T) {
			f := newNativeFixtureMode(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
			f.handshake(t)
			f.transport.handler = func(*http.Request, []byte) (*http.Response, error) {
				return wireResponse("data: "+message+"\n\n", true), nil
			}
			get := f.captured(t, "GET", "", "synthetic-session")
			w := httptest.NewRecorder()
			got := f.executor.Serve(context.Background(), get, w)
			if got.Outcome == OutcomeCompleted || strings.Contains(w.Body.String(), message) {
				t.Fatalf("fresh GET published unrelated response/progress: %+v %q", got, w.Body.String())
			}
		})
	}
}

func TestNativeStreamEventIDsRejectConflictsAndOtherStreams(t *testing.T) {
	for _, edge := range []string{"other_stream", "changed_bytes"} {
		t.Run(edge, func(t *testing.T) {
			f := newNativeFixtureMode(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
			f.handshake(t)
			a := "id: a\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":9,\"progress\":1}}\n\n"
			b := strings.Replace(a, "id: a", "id: b", 1)
			wire := a
			f.transport.handler = func(*http.Request, []byte) (*http.Response, error) { return wireResponse(wire, true), nil }
			f.serve(t, `{"jsonrpc":"2.0","id":40,"method":"ping","params":{"_meta":{"progressToken":9}}}`, "synthetic-session")
			wire = b
			f.serve(t, `{"jsonrpc":"2.0","id":41,"method":"ping","params":{"_meta":{"progressToken":9}}}`, "synthetic-session")
			if edge == "changed_bytes" {
				wire = strings.Replace(a, `"progress":1`, `"progress":2`, 1)
			}
			get := f.captured(t, "GET", "", "synthetic-session")
			get.request.Header.Set("Last-Event-Id", "a")
			w := httptest.NewRecorder()
			got := f.executor.Serve(context.Background(), get, w)
			if got.Outcome == OutcomeCompleted || strings.Contains(w.Body.String(), wire) {
				t.Fatalf("conflicting/cross-stream bytes published: %+v %q", got, w.Body.String())
			}
		})
	}
}

func TestNativeRetainedCallbackReplayDoesNotRegisterOrSendTwice(t *testing.T) {
	f := newNativeFixtureMode(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	e1 := "id: e1\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":9,\"progress\":1}}\n\n"
	e2 := "id: e2\ndata: {\"jsonrpc\":\"2.0\",\"id\":\"callback\",\"method\":\"ping\"}\n\n"
	wire := e1 + e2
	f.transport.handler = func(*http.Request, []byte) (*http.Response, error) { return wireResponse(wire, true), nil }
	f.serve(t, `{"jsonrpc":"2.0","id":40,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"},"_meta":{"progressToken":9}}}`, "synthetic-session")
	// At/before-cursor bytes are suppressed; the later retained callback is
	// delivered exactly as received but must not register a second witness.
	wire = e1 + e2
	get := f.captured(t, "GET", "", "synthetic-session")
	get.request.Header.Set("Last-Event-Id", "e1")
	w := httptest.NewRecorder()
	f.executor.Serve(context.Background(), get, w)
	if w.Body.String() != e2 {
		t.Fatalf("retained suffix=%q", w.Body.String())
	}
	f.transport.handler = func(*http.Request, []byte) (*http.Response, error) {
		return &http.Response{StatusCode: 202, Header: http.Header{}, Body: http.NoBody}, nil
	}
	before := f.transport.sends()
	got, _ := f.serve(t, `{"jsonrpc":"2.0","id":"callback","result":{}}`, "synthetic-session")
	if got.Outcome != OutcomeCompleted || f.transport.sends() != before+1 {
		t.Fatalf("single observed callback failed: %+v", got)
	}
	f.serve(t, `{"jsonrpc":"2.0","id":"callback","result":{}}`, "synthetic-session")
	if f.transport.sends() != before+1 {
		t.Fatal("retained callback replay supplied a second send")
	}
}
