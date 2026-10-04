package nativemcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

func TestNativeRPCConcurrentLimitReleasesAndHasNoLifetimeQuota(t *testing.T) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	e := f.executor.(*executor)
	e.mu.Lock()
	var s *session
	for _, candidate := range e.scopes[f.capture.Key()].sessions {
		s = candidate
	}
	e.mu.Unlock()
	var releases []func()
	for index := 0; index < 16; index++ {
		release, err := e.acquireRPC(s, nativetool.ID(fmt.Sprintf("%032x", 20000+index)), rpc{id: []byte(fmt.Sprint(index)), kind: nativetool.RequestPing})
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	if _, err := e.acquireRPC(s, "ffffffffffffffffffffffffffffffff", rpc{id: []byte("17"), kind: nativetool.RequestPing}); err == nil {
		t.Fatal("17 concurrent RPCs admitted")
	}
	releases[0]()
	release, err := e.acquireRPC(s, "ffffffffffffffffffffffffffffffff", rpc{id: []byte("17"), kind: nativetool.RequestPing})
	if err != nil {
		t.Fatal("completed slot not reusable")
	}
	release()
	for _, release := range releases[1:] {
		release()
	}
	f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
		var object map[string]json.RawMessage
		_ = json.Unmarshal(body, &object)
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":` + string(object["id"]) + `,"result":{}}`))}, nil
	}
	for index := 0; index < 18; index++ {
		terminal, _ := f.serve(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"ping"}`, 100+index), "synthetic-session")
		if terminal.Outcome != OutcomeCompleted {
			t.Fatalf("lifetime RPC%d %+v", index+1, terminal)
		}
	}
	for _, size := range []int{512, 513} {
		body := `{"jsonrpc":"2.0","id":"` + strings.Repeat("x", size) + `","method":"ping"}`
		before := f.transport.sends()
		terminal, _ := f.serve(t, body, "synthetic-session")
		if (terminal.Outcome == OutcomeCompleted) != (size == 512) || f.transport.sends() != before+func() int {
			if size == 512 {
				return 1
			}
			return 0
		}() {
			t.Fatalf("actual ID boundary%d %+v sends%d", size, terminal, f.transport.sends())
		}
	}
}
func TestNativeGetRedeliversOnlyObservedSameSessionEvents(t *testing.T) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	event := "event: message\nid: observed-ping\ndata: {\"jsonrpc\":\"2.0\",\"id\":44,\"result\":{}}\n\n"
	f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(event))}, nil
	}
	terminal, _ := f.serve(t, `{"jsonrpc":"2.0","id":44,"method":"ping"}`, "synthetic-session")
	if terminal.Outcome != OutcomeCompleted {
		t.Fatal(terminal)
	}
	request := f.captured(t, "GET", "", "synthetic-session")
	request.request.Header.Set("Last-Event-Id", "observed-ping")
	before := f.transport.sends()
	terminal = f.executor.Serve(context.Background(), request, httptest.NewRecorder())
	if terminal.Outcome != OutcomeRejected || f.transport.sends() != before {
		t.Fatal(terminal)
	}
	event = strings.Replace(event, "observed-ping", "unobserved", 1)
	request = f.captured(t, "GET", "", "synthetic-session")
	request.request.Header.Set("Last-Event-Id", "observed-ping")
	terminal = f.executor.Serve(context.Background(), request, httptest.NewRecorder())
	if terminal.Outcome != OutcomeRejected || f.transport.sends() != before {
		t.Fatalf("unobserved GET event %+v", terminal)
	}
	scope, err := f.store.NativeToolRepository().GetScope(context.Background(), f.capture)
	if err != nil || scope.ReplayBarrier {
		t.Fatalf("resume uncertainty barrier %+v %v", scope, err)
	}
}
func TestNativeSessionDeadlineAndConstructorCeilings(t *testing.T) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	e := f.executor.(*executor)
	e.mu.Lock()
	var s *session
	for _, candidate := range e.scopes[f.capture.Key()].sessions {
		s = candidate
	}
	e.mu.Unlock()
	s.mu.Lock()
	boundary := s.timeLimit
	s.mu.Unlock()
	e.dependencies.Clock = func() time.Time { return boundary }
	before := f.transport.sends()
	terminal, _ := f.serve(t, `{"jsonrpc":"2.0","id":88,"method":"ping"}`, "synthetic-session")
	if terminal.Outcome == OutcomeCompleted || f.transport.sends() != before {
		t.Fatal("expired session sent")
	}
	for _, mutate := range []func(*Limits){func(l *Limits) { l.RequestBytes++ }, func(l *Limits) { l.ResultBytes++ }, func(l *Limits) { l.EventBytes++ }, func(l *Limits) { l.CatalogPageBytes++ }, func(l *Limits) { l.CatalogBytes++ }, func(l *Limits) { l.BufferBytes++ }, func(l *Limits) { l.Depth++ }, func(l *Limits) { l.OpaqueIDBytes++ }, func(l *Limits) { l.CatalogTools++ }, func(l *Limits) { l.CatalogPages++ }, func(l *Limits) { l.SessionsPerCapture++ }, func(l *Limits) { l.ConcurrentRPCsPerSession++ }, func(l *Limits) { l.Pending++ }, func(l *Limits) { l.SessionIdle++ }, func(l *Limits) { l.Headers++ }, func(l *Limits) { l.StreamIdle++ }, func(l *Limits) { l.CallLifetime++ }, func(l *Limits) { l.RequestBytes = 0 }} {
		limits := DefaultLimits()
		mutate(&limits)
		if _, err := New(e.dependencies, limits); err == nil {
			t.Fatal("above-ceiling bounds admitted")
		}
	}
}
