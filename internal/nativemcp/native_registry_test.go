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

func TestNativeSensitiveRegistryRetiresAfterRealTerminalWithoutLifetimeQuota(t *testing.T) {
	f := newNativeFixtureMode(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	replied := make(chan struct{}, 1)
	f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
		if strings.Contains(string(body), `"method":"tools/call"`) {
			reader, writer := io.Pipe()
			go func() {
				defer writer.Close()
				for index := 0; index < 1025; index++ {
					callback := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"openai/form","params":{"message":"Synthetic bounded form","requestedSchema":{"type":"object","properties":{}}}}`, 10000+index)
					if _, err := io.WriteString(writer, "data: "+callback+"\n\n"); err != nil {
						return
					}
					<-replied
				}
				_, _ = io.WriteString(writer, "data: {\"jsonrpc\":\"2.0\",\"id\":3,\"result\":{\"content\":[]}}\n\n")
			}()
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: reader}, nil
		}
		replied <- struct{}{}
		return &http.Response{StatusCode: 202, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	parent := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"}}}`, "synthetic-session")
	done := make(chan Terminal, 1)
	go func() { done <- f.executor.Serve(context.Background(), parent, httptest.NewRecorder()) }()
	for index := 0; index < 1025; index++ {
		identity := fmt.Sprint(10000 + index)
		deadline := time.Now().Add(time.Second)
		found := false
		for time.Now().Before(deadline) {
			e := f.executor.(*executor)
			e.mu.Lock()
			owner := e.scopes[f.capture.Key()]
			for _, s := range owner.sessions {
				s.mu.Lock()
				found = s.parents[identity] != nil
				s.mu.Unlock()
			}
			e.mu.Unlock()
			if found {
				break
			}
			time.Sleep(time.Millisecond)
		}
		if !found {
			t.Fatalf("callback%d source missing", index+1)
		}
		terminal, _ := f.serve(t, `{"jsonrpc":"2.0","id":`+identity+`,"result":{"action":"decline"}}`, "synthetic-session")
		if terminal.Outcome != OutcomeCompleted {
			t.Fatalf("sequential callback%d %+v", index+1, terminal)
		}
	}
	terminal := <-done
	if terminal.Outcome != OutcomeCompleted || f.transport.sends() != 1029 {
		t.Fatalf("lifetime callback quota %+v sends%d", terminal, f.transport.sends())
	}
	e := f.executor.(*executor)
	e.parentsMu.Lock()
	pending := e.parentPending
	e.parentsMu.Unlock()
	if pending != 0 {
		t.Fatalf("terminal callback resources remained %d", pending)
	}
	var rows int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM native_tool_operations WHERE rpc_kind='server_reply' AND state='completed'`).Scan(&rows); err != nil || rows != 1025 {
		t.Fatalf("durable callback terminals%d %v", rows, err)
	}
	before := f.transport.sends()
	terminal, _ = f.serve(t, `{"jsonrpc":"2.0","id":10000,"result":{"action":"decline"}}`, "synthetic-session")
	if terminal.Outcome == OutcomeCompleted || f.transport.sends() != before {
		t.Fatal("retired callback replayed")
	}
}
func TestNativeSensitiveRegistryLiveBoundNeverEvictsPending(t *testing.T) {
	f := newNativeFixtureMode(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
		var wire strings.Builder
		for index := 0; index < 1025; index++ {
			fmt.Fprintf(&wire, "data: {\"jsonrpc\":\"2.0\",\"id\":%d,\"method\":\"openai/form\",\"params\":{\"message\":\"Synthetic live form\",\"requestedSchema\":{\"type\":\"object\",\"properties\":{}}}}\n\n", 20000+index)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire.String()))}, nil
	}
	parent := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"}}}`, "synthetic-session")
	w := httptest.NewRecorder()
	terminal := f.executor.Serve(context.Background(), parent, w)
	if terminal.Outcome != OutcomeUncertain || strings.Count(w.Body.String(), `"method":"openai/form"`) != 1024 {
		t.Fatalf("live callbacks quota %+v accepted%d", terminal, strings.Count(w.Body.String(), `"method":"openai/form"`))
	}
	if w.Code != 200 || strings.Count(w.Body.String(), `"code":-32004`) != 1 || !strings.HasSuffix(w.Body.String(), "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":3,\"error\":{\"code\":-32004,\"message\":\"execution_uncertain\"}}\n\n") {
		t.Fatalf("invalid post-header SSE terminal framing status%d", w.Code)
	}
	op, err := f.store.NativeToolRepository().Get(context.Background(), parent.id)
	if err != nil || op.State != "uncertain" || op.BytesOut != nil {
		t.Fatalf("overflow parent %+v %v", op, err)
	}
}
