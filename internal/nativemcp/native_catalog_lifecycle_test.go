package nativemcp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
)

func TestNativeCatalogListChangedCancelsWaitingBeforeRefresh(t *testing.T) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	old := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"}}}`, "synthetic-session")
	results := make(chan Terminal, 1)
	go func() { results <- f.executor.Serve(context.Background(), old, httptest.NewRecorder()) }()
	deadline := time.Now().Add(time.Second)
	found := false
	for time.Now().Before(deadline) {
		op, err := f.store.NativeToolRepository().Get(context.Background(), old.id)
		if err == nil && op.ApprovalID != nil {
			found = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !found {
		t.Fatal("waiting question absent")
	}
	f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\",\"params\":{}}\n\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":44,\"result\":{}}\n\n"))}, nil
	}
	terminal, _ := f.serve(t, `{"jsonrpc":"2.0","id":44,"method":"ping"}`, "synthetic-session")
	if terminal.Outcome != OutcomeCompleted {
		t.Fatalf("known ping effect became uncertain %+v", terminal)
	}
	terminal = <-results
	if terminal.Outcome == OutcomeCompleted || f.transport.sends() != 4 {
		t.Fatalf("stale waiting call sent %+v sends%d", terminal, f.transport.sends())
	}
	op, err := f.store.NativeToolRepository().Get(context.Background(), old.id)
	if err != nil || op.State != "canceled" {
		t.Fatalf("actual old question %+v %v", op, err)
	}
	f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
		response := `{"jsonrpc":"2.0","id":5,"result":{"tools":[{"name":"read_file","description":"Synthetic public tool","inputSchema":{"type":"object","properties":{"path":{"type":"string"}}}}]}}`
		if strings.Contains(string(body), `"method":"tools/call"`) {
			response = `{"jsonrpc":"2.0","id":6,"result":{"content":[]}}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(response))}, nil
	}
	terminal, _ = f.serve(t, `{"jsonrpc":"2.0","id":5,"method":"tools/list"}`, "synthetic-session")
	if terminal.Outcome != OutcomeCompleted {
		t.Fatalf("complete refresh %+v", terminal)
	}
	fresh := f.captured(t, "POST", `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"}}}`, "synthetic-session")
	go func() { results <- f.executor.Serve(context.Background(), fresh, httptest.NewRecorder()) }()
	f.approve(t, fresh.id)
	terminal = <-results
	if terminal.Outcome != OutcomeCompleted || f.transport.sends() != 6 {
		t.Fatalf("new observed catalog decision %+v sends%d", terminal, f.transport.sends())
	}
}
