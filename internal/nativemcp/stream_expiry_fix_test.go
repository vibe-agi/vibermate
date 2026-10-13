package nativemcp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
)

func TestNativeEffectiveStreamExpiryReleasesModuleWithoutRequest(t *testing.T) {
	for _, edge := range []string{"store", "session", "executor"} {
		t.Run(edge, func(t *testing.T) {
			limits := DefaultLimits()
			limits.ConcurrentRPCsPerSession = 1
			ceiling := time.Duration(0)
			switch edge {
			case "store":
				ceiling = 300 * time.Millisecond
			case "session":
				limits.SessionIdle = 300 * time.Millisecond
			case "executor":
				limits.CallLifetime = 300 * time.Millisecond
			}
			f := newNativeFixtureStoreLifetime(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, ceiling, limits)
			f.handshake(t)
			e := f.executor.(*executor)
			e.dependencies.ContentProfiles = syntheticProfiles{argumentPaths: []string{"/proof"}}
			e.mu.Lock()
			var s *session
			for _, candidate := range e.scopes[f.capture.Key()].sessions {
				s = candidate
			}
			e.mu.Unlock()
			s.mu.Lock()
			retainedBefore := s.retained
			s.mu.Unlock()
			f.transport.handler = func(_ *http.Request, body []byte) (*http.Response, error) {
				if strings.Contains(string(body), `"tools/call"`) {
					return wireResponse("id: expire\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":9,\"progress\":1}}\n\n", true), nil
				}
				return wireResponse(`{"jsonrpc":"2.0","id":44,"result":{}}`, false), nil
			}
			call := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a","proof":"synthetic"},"_meta":{"progressToken":9}}}`, "synthetic-session")
			f.executor.Serve(context.Background(), call, httptest.NewRecorder())
			s.mu.Lock()
			charged := s.retained > retainedBefore && len(s.tools) == 1
			s.mu.Unlock()
			if !charged {
				t.Fatal("test did not retain actual stream and witness")
			}
			deadline := time.Now().Add(2 * time.Second)
			for {
				s.mu.Lock()
				released := len(s.active) == 0 && len(s.streams) == 0 && len(s.eventIndex) == 0 && len(s.tools) == 0 && s.retained == retainedBefore
				s.mu.Unlock()
				op, err := f.store.NativeToolRepository().Get(context.Background(), call.id)
				if err != nil {
					t.Fatal(err)
				}
				if released && op.State == "uncertain" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("effective expiry left module state: released=%v durable=%s", released, op.State)
				}
				time.Sleep(5 * time.Millisecond)
			}
			scope, err := f.store.NativeToolRepository().GetScope(context.Background(), f.capture)
			if err != nil || !scope.ReplayBarrier {
				t.Fatalf("expiry barrier %+v %v", scope, err)
			}
			if edge != "session" {
				next, _ := f.serve(t, `{"jsonrpc":"2.0","id":44,"method":"ping"}`, "synthetic-session")
				if next.Outcome != OutcomeCompleted {
					t.Fatalf("released slot blocks next RPC %+v", next)
				}
			}
			before := f.transport.sends()
			get := f.captured(t, "GET", "", "synthetic-session")
			get.request.Header.Set("Last-Event-Id", "expire")
			got := f.executor.Serve(context.Background(), get, httptest.NewRecorder())
			if got.Outcome != OutcomeRejected || f.transport.sends() != before {
				t.Fatal("expired recovery sent")
			}
		})
	}
}

type expiryBlockedBody struct {
	*strings.Reader
	entered, closed chan struct{}
	enter, close    sync.Once
}

func (b *expiryBlockedBody) Read(p []byte) (int, error) {
	if b.Reader.Len() > 0 {
		return b.Reader.Read(p)
	}
	b.enter.Do(func() { close(b.entered) })
	<-b.closed
	return 0, io.ErrClosedPipe
}
func (b *expiryBlockedBody) Close() error { b.close.Do(func() { close(b.closed) }); return nil }

func TestNativeEffectiveExpiryStopsAttachedParserAndRacesClose(t *testing.T) {
	for _, edge := range []string{"expiry", "close-race"} {
		t.Run(edge, func(t *testing.T) {
			limits := DefaultLimits()
			limits.ConcurrentRPCsPerSession = 1
			f := newNativeFixtureStoreLifetime(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, 300*time.Millisecond, limits)
			f.handshake(t)
			e := f.executor.(*executor)
			e.mu.Lock()
			var s *session
			for _, candidate := range e.scopes[f.capture.Key()].sessions {
				s = candidate
			}
			e.mu.Unlock()
			s.mu.Lock()
			baseline := s.retained
			s.mu.Unlock()
			body := &expiryBlockedBody{Reader: strings.NewReader("id: attached\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":9,\"progress\":1}}\n\n"), entered: make(chan struct{}), closed: make(chan struct{})}
			f.transport.handler = func(*http.Request, []byte) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: body}, nil
			}
			call := f.captured(t, "POST", `{"jsonrpc":"2.0","id":40,"method":"ping","params":{"_meta":{"progressToken":9}}}`, "synthetic-session")
			done := make(chan Terminal, 1)
			go func() { done <- e.Serve(context.Background(), call, httptest.NewRecorder()) }()
			select {
			case <-body.entered:
			case <-time.After(time.Second):
				t.Fatal("parser not attached")
			}
			var closeDone chan error
			if edge == "close-race" {
				s.mu.Lock()
				for _, stream := range s.streams {
					stream.expiryTimer.Reset(0)
				}
				s.mu.Unlock()
				closeDone = make(chan error, 1)
				go func() { closeDone <- e.Close(context.Background()) }()
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("effective expiry left attached read alive")
			}
			if closeDone != nil {
				if err := <-closeDone; err != nil {
					t.Fatal(err)
				}
			}
			deadline := time.Now().Add(time.Second)
			for {
				s.mu.Lock()
				released := len(s.active) == 0 && len(s.streams) == 0 && len(s.eventIndex) == 0 && (s.retained == baseline || s.closed && s.retained == 0)
				s.mu.Unlock()
				if released {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("retirement race leaked charges")
				}
				time.Sleep(time.Millisecond)
			}
			op, err := f.store.NativeToolRepository().Get(context.Background(), call.id)
			if err != nil || op.State != "uncertain" {
				t.Fatalf("attached expiry state=%s err=%v", op.State, err)
			}
		})
	}
}

func TestNativeEffectiveDeadlineSurvivesReconnect(t *testing.T) {
	f := newNativeFixtureStoreLifetime(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, 500*time.Millisecond)
	f.handshake(t)
	e := f.executor.(*executor)
	f.transport.handler = func(r *http.Request, _ []byte) (*http.Response, error) {
		id := "origin"
		if r.Method == "GET" {
			id = "resumed"
		}
		return wireResponse("id: "+id+"\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":9,\"progress\":1}}\n\n", true), nil
	}
	call := f.captured(t, "POST", `{"jsonrpc":"2.0","id":40,"method":"ping","params":{"_meta":{"progressToken":9}}}`, "synthetic-session")
	e.Serve(context.Background(), call, httptest.NewRecorder())
	e.mu.Lock()
	var s *session
	for _, candidate := range e.scopes[f.capture.Key()].sessions {
		s = candidate
	}
	e.mu.Unlock()
	s.mu.Lock()
	stream := s.streams[call.id]
	originalDeadline := stream.expires
	originalContext := stream.context
	s.mu.Unlock()
	get := f.captured(t, "GET", "", "synthetic-session")
	get.request.Header.Set("Last-Event-Id", "origin")
	e.Serve(context.Background(), get, httptest.NewRecorder())
	s.mu.Lock()
	same := stream.context == originalContext && stream.expires.Equal(originalDeadline)
	s.mu.Unlock()
	if !same {
		t.Fatal("reconnect replaced context or extended deadline")
	}
	select {
	case <-originalContext.Done():
	case <-time.After(time.Until(originalDeadline) + time.Second):
		t.Fatal("original absolute deadline lost")
	}
}
