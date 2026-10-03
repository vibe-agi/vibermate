package nativemcp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
)

type waitingBody struct {
	closed chan struct{}
	once   sync.Once
}

func (b *waitingBody) Read([]byte) (int, error) { <-b.closed; return 0, io.ErrClosedPipe }
func (b *waitingBody) Close() error             { b.once.Do(func() { close(b.closed) }); return nil }
func TestNativeInputCallHeaderAndStreamDeadlinesCloseOwnedReads(t *testing.T) {
	for _, edge := range []string{"input_call", "headers", "stream"} {
		t.Run(edge, func(t *testing.T) {
			limits := DefaultLimits()
			switch edge {
			case "input_call":
				limits.CallLifetime = 100 * time.Millisecond
			case "headers":
				limits.Headers = 20 * time.Millisecond
			case "stream":
				limits.StreamIdle = 20 * time.Millisecond
			}
			f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, limits)
			f.handshake(t)
			if edge == "input_call" {
				request := f.captured(t, "POST", `{"jsonrpc":"2.0","id":44,"method":"ping"}`, "synthetic-session")
				request.request.Body = &waitingBody{closed: make(chan struct{})}
				start := time.Now()
				terminal := f.executor.Serve(context.Background(), request, httptest.NewRecorder())
				if terminal.Outcome == OutcomeCompleted || time.Since(start) > time.Second || f.transport.sends() != 3 {
					t.Fatalf("unbounded borrowed input %+v", terminal)
				}
				return
			}
			f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
				if edge == "headers" {
					<-r.Context().Done()
					return nil, r.Context().Err()
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: &waitingBody{closed: make(chan struct{})}}, nil
			}
			request := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"}}}`, "synthetic-session")
			results := make(chan Terminal, 1)
			go func() { results <- f.executor.Serve(context.Background(), request, httptest.NewRecorder()) }()
			f.approve(t, request.id)
			select {
			case terminal := <-results:
				if terminal.Outcome != OutcomeUncertain {
					t.Fatalf("deadline after acceptance %+v", terminal)
				}
			case <-time.After(time.Second):
				t.Fatal("transport deadline did not close owned read")
			}
		})
	}
}
