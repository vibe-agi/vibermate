package nativemcp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/environment"
)

func TestNativeSSECompletedTerminalAndGETNeverInventSecondRPC(t *testing.T) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	event := "data: {\"jsonrpc\":\"2.0\",\"id\":44,\"result\":{}}\n\n"
	f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(event + event))}, nil
	}
	terminal, w := f.serve(t, `{"jsonrpc":"2.0","id":44,"method":"ping"}`, "synthetic-session")
	if terminal.Outcome != OutcomeUncertain || w.Code != 200 || strings.Count(w.Body.String(), `"result":{}`) != 1 || strings.Contains(w.Body.String(), `"code":-32004`) {
		t.Fatalf("duplicated completed RPC terminal %+v %s", terminal, w.Body.String())
	}
	f = newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	known := "id: known\ndata: {\"jsonrpc\":\"2.0\",\"id\":44,\"result\":{}}\n\n"
	f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(known))}, nil
	}
	terminal, _ = f.serve(t, `{"jsonrpc":"2.0","id":44,"method":"ping"}`, "synthetic-session")
	if terminal.Outcome != OutcomeCompleted {
		t.Fatal(terminal)
	}
	f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(known + strings.Replace(known, "known", "unknown", 1)))}, nil
	}
	c := f.captured(t, "GET", "", "synthetic-session")
	c.request.Header.Set("Last-Event-Id", "known")
	w = httptest.NewRecorder()
	before := f.transport.sends()
	terminal = f.executor.Serve(context.Background(), c, w)
	if terminal.Outcome != OutcomeRejected || f.transport.sends() != before || strings.Contains(w.Body.String(), "unknown") {
		t.Fatalf("GET invented RPC terminal %+v %s", terminal, w.Body.String())
	}
}
func TestNativeSSEFailureFrameHonorsEventBudget(t *testing.T) {
	w := httptest.NewRecorder()
	tracked := &nativeWriter{ResponseWriter: w, maximum: 8, correlation: []byte("3")}
	tracked.Header().Set("Content-Type", "text/event-stream")
	tracked.WriteHeader(200)
	if !tracked.failAfterHeaders() || w.Body.Len() != 0 {
		t.Fatal("failure exceeded immutable event budget")
	}
	tracked.maximum = 1024
	if !tracked.failAfterHeaders() || w.Body.Len() != 0 {
		t.Fatal("failure attempted twice")
	}
}
