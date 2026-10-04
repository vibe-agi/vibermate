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

func TestNativeDeletePreservesSiblingReceiptAndQuestion(t *testing.T) {
	testNativeSiblingPreservation(t, true)
}

func TestNativeCatalogInvalidationPreservesSiblingReceiptAndQuestion(t *testing.T) {
	testNativeSiblingPreservation(t, false)
}

func testNativeSiblingPreservation(t *testing.T, deleteSession bool) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
		response := ""
		headers := http.Header{"Content-Type": []string{"application/json"}}
		switch {
		case strings.Contains(string(body), `"method":"initialize"`):
			headers.Set("Mcp-Session-Id", "sibling-session")
			response = `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"synthetic","version":"1"}}}`
		case strings.Contains(string(body), `"method":"tools/list"`):
			response = `{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"read_file","description":"Synthetic public tool","inputSchema":{"type":"object","properties":{"path":{"type":"string"}}}}]}}`
		default:
			return &http.Response{StatusCode: 202, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		return &http.Response{StatusCode: 200, Header: headers, Body: io.NopCloser(strings.NewReader(response))}, nil
	}
	for _, body := range []string{`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`} {
		wire := "sibling-session"
		if strings.Contains(body, `"method":"initialize"`) {
			wire = ""
		}
		terminal, _ := f.serve(t, body, wire)
		if terminal.Outcome != OutcomeCompleted {
			t.Fatalf("sibling handshake %+v", terminal)
		}
	}
	receipt, outgoing, plan := prepareRealReceipt(t, f, "sibling-session")
	sibling := f.captured(t, "POST", `{"jsonrpc":"2.0","id":34,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"}}}`, "sibling-session")
	results := make(chan Terminal, 1)
	go func() { results <- f.executor.Serve(context.Background(), sibling, httptest.NewRecorder()) }()
	deadline := time.Now().Add(time.Second)
	found := false
	for time.Now().Before(deadline) {
		op, err := f.store.NativeToolRepository().Get(context.Background(), sibling.id)
		if err == nil && op.ApprovalID != nil {
			found = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !found {
		t.Fatal("sibling question absent")
	}
	f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
		if r.Method == http.MethodDelete {
			return &http.Response{StatusCode: 202, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		if strings.Contains(string(body), `"method":"ping"`) {
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\",\"params\":{}}\n\ndata: {\"jsonrpc\":\"2.0\",\"id\":44,\"result\":{}}\n\n"))}, nil
		}
		id := "33"
		if strings.Contains(string(body), `"id":34`) {
			id = "34"
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":` + id + `,"result":{"content":[]}}`))}, nil
	}
	var terminal Terminal
	if deleteSession {
		terminal = f.executor.Serve(context.Background(), f.captured(t, "DELETE", "", "synthetic-session"), httptest.NewRecorder())
	} else {
		terminal, _ = f.serve(t, `{"jsonrpc":"2.0","id":44,"method":"ping"}`, "synthetic-session")
	}
	if terminal.Outcome != OutcomeCompleted {
		t.Fatal(terminal)
	}
	if err := f.store.NativeToolRepository().VerifyStartReceipt(context.Background(), receipt, time.Now().UTC()); err != nil {
		t.Fatalf("sibling receipt revoked %v", err)
	}
	op, err := f.store.NativeToolRepository().Get(context.Background(), sibling.id)
	if err != nil || op.ApprovalID == nil || op.State != "awaiting_decision" {
		t.Fatalf("sibling question affected %+v %v", op, err)
	}
	f.approve(t, sibling.id)
	terminal = <-results
	if terminal.Outcome != OutcomeCompleted {
		t.Fatal(terminal)
	}
	response, err := f.executor.(*executor).sendCommitted(context.Background(), outgoing, plan, receipt)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
}
