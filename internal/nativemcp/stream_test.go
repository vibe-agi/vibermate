package nativemcp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
)

func TestNativeStreamDetachedRecoveryUsesOnePOST(t *testing.T) {
	limits := DefaultLimits()
	limits.ConcurrentRPCsPerSession = 1
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, limits)
	f.handshake(t)
	progress := "id: e1\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":\"p\",\"progress\":1}}\n\n"
	result := "id: e2\ndata: {\"jsonrpc\":\"2.0\",\"id\":3,\"result\":{\"content\":[]}}\n\n"
	posts, gets := 0, 0
	f.transport.handler = func(r *http.Request, _ []byte) (*http.Response, error) {
		wire := progress
		if r.Method == "GET" {
			gets++
			if r.Header.Get("Last-Event-Id") != "e1" {
				t.Error("wrong recovery cursor")
			}
			wire = result
		} else {
			posts++
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}, nil
	}
	call := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"},"_meta":{"progressToken":"p"}}}`, "synthetic-session")
	done := make(chan Terminal, 1)
	go func() { done <- f.executor.Serve(context.Background(), call, httptest.NewRecorder()) }()
	f.approve(t, call.id)
	<-done
	op, err := f.store.NativeToolRepository().Get(context.Background(), call.id)
	if err != nil || op.State != "started" {
		t.Fatalf("disconnect lost logical RPC: %+v %v", op, err)
	}
	resume := f.captured(t, "GET", "", "synthetic-session")
	resume.request.Header.Set("Last-Event-Id", "e1")
	w := httptest.NewRecorder()
	got := f.executor.Serve(context.Background(), resume, w)
	if got.Outcome != OutcomeCompleted || w.Body.String() != result {
		t.Fatalf("recovery %+v wire=%q", got, w.Body.String())
	}
	op, err = f.store.NativeToolRepository().Get(context.Background(), call.id)
	if err != nil || op.State != "completed" {
		t.Fatalf("late RPC %+v %v", op, err)
	}
	if posts != 1 || gets != 1 {
		t.Fatalf("POST=%d GET=%d", posts, gets)
	}
	var once, audits int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM native_tool_once WHERE operation_id=?`, call.id).Scan(&once); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM runtime_egress_attempts WHERE parent_id IN(?,?)`, call.id, resume.id).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if once != 1 || audits != 2 {
		t.Fatalf("once=%d audits=%d", once, audits)
	}
}

type terminalDeliveryFailure struct {
	*httptest.ResponseRecorder
	writes int
}

func (w *terminalDeliveryFailure) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == 2 {
		return 0, io.ErrClosedPipe
	}
	return w.ResponseRecorder.Write(p)
}

func TestNativeStreamTerminalDeliveryFailureReplaysLocally(t *testing.T) {
	f := newNativeFixtureMode(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	progress := "id: p1\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":\"p\",\"progress\":1}}\n\n"
	result := "id: p2\ndata: {\"jsonrpc\":\"2.0\",\"id\":3,\"result\":{\"content\":[]}}\n\n"
	f.transport.handler = func(*http.Request, []byte) (*http.Response, error) { return wireResponse(progress+result, true), nil }
	call := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"},"_meta":{"progressToken":"p"}}}`, "synthetic-session")
	w := &terminalDeliveryFailure{ResponseRecorder: httptest.NewRecorder()}
	f.executor.Serve(context.Background(), call, w)
	op, err := f.store.NativeToolRepository().Get(context.Background(), call.id)
	if err != nil || op.State != "completed" {
		t.Fatalf("known terminal lost: %+v %v", op, err)
	}
	before := f.transport.sends()
	var operations int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM native_tool_operations`).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	get := f.captured(t, "GET", "", "synthetic-session")
	get.request.Header.Set("Last-Event-Id", "p1")
	replay := httptest.NewRecorder()
	got := f.executor.Serve(context.Background(), get, replay)
	var after int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM native_tool_operations`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if got.Outcome != OutcomeCompleted || replay.Body.String() != result || f.transport.sends() != before || operations != after {
		t.Fatalf("nonlocal terminal replay %+v body=%q sends=%d/%d rows=%d/%d", got, replay.Body.String(), before, f.transport.sends(), operations, after)
	}
}

func TestNativeStreamDetachedCancellationAndExpiryFenceRecovery(t *testing.T) {
	for _, edge := range []string{"cancel", "expiry", "revoke", "owner", "unknown", "partial"} {
		t.Run(edge, func(t *testing.T) {
			limits := DefaultLimits()
			limits.ConcurrentRPCsPerSession = 1
			if edge == "expiry" {
				limits.CallLifetime = 100 * time.Millisecond
			}
			f := newNativeFixtureMode(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, limits)
			f.handshake(t)
			wire := "id: c1\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":\"p\",\"progress\":1}}\n\n"
			if edge == "partial" {
				wire += "data: {"
			}
			f.transport.handler = func(r *http.Request, _ []byte) (*http.Response, error) {
				if r.Method == "POST" && r.ContentLength < 100 {
					return &http.Response{StatusCode: 202, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
				}
				return wireResponse(wire, true), nil
			}
			call := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"},"_meta":{"progressToken":"p"}}}`, "synthetic-session")
			f.executor.Serve(context.Background(), call, httptest.NewRecorder())
			switch edge {
			case "cancel":
				got, _ := f.serve(t, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":3}}`, "synthetic-session")
				if got.Outcome != OutcomeCompleted {
					t.Fatalf("cancel %+v", got)
				}
			case "revoke":
				if err := f.executor.Revoke(context.Background(), f.capture, RevocationCaptureStopped); err != nil {
					t.Fatal(err)
				}
			case "owner":
				e := f.executor.(*executor)
				e.mu.Lock()
				e.scopes[f.capture.Key()].cancel()
				e.mu.Unlock()
			}
			if edge != "unknown" {
				deadline := time.Now().Add(time.Second)
				for {
					op, err := f.store.NativeToolRepository().Get(context.Background(), call.id)
					if err != nil {
						t.Fatal(err)
					}
					if op.State == "uncertain" {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("unresolved after %s: %+v", edge, op)
					}
					time.Sleep(time.Millisecond)
				}
			}
			before := f.transport.sends()
			get := f.captured(t, "GET", "", "synthetic-session")
			cursor := "c1"
			if edge == "unknown" {
				cursor = "unknown"
			}
			get.request.Header.Set("Last-Event-Id", cursor)
			got := f.executor.Serve(context.Background(), get, httptest.NewRecorder())
			if got.Outcome == OutcomeCompleted || f.transport.sends() != before {
				t.Fatalf("late recovery %+v sends=%d/%d", got, before, f.transport.sends())
			}
			if edge == "unknown" {
				op, err := f.store.NativeToolRepository().Get(context.Background(), call.id)
				if err != nil && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				if op.State != "started" {
					t.Fatal("unknown cursor retired unrelated stream")
				}
			}
		})
	}
}

func TestNativeStreamRecoveryExclusiveAttachmentKeepsLogicalLimit(t *testing.T) {
	limits := DefaultLimits()
	limits.ConcurrentRPCsPerSession = 1
	f := newNativeFixtureMode(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, limits)
	f.handshake(t)
	entered, release := make(chan struct{}), make(chan struct{})
	f.transport.handler = func(r *http.Request, _ []byte) (*http.Response, error) {
		if r.Method == "GET" {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
			return wireResponse("id: end\ndata: {\"jsonrpc\":\"2.0\",\"id\":40,\"result\":{}}\n\n", true), nil
		}
		return wireResponse("id: begin\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":9,\"progress\":1}}\n\n", true), nil
	}
	f.serve(t, `{"jsonrpc":"2.0","id":40,"method":"ping","params":{"_meta":{"progressToken":9}}}`, "synthetic-session")
	before := f.transport.sends()
	other, _ := f.serve(t, `{"jsonrpc":"2.0","id":41,"method":"ping"}`, "synthetic-session")
	if other.Outcome != OutcomeRejected || f.transport.sends() != before {
		t.Fatal("unrelated RPC borrowed detached slot")
	}
	get := f.captured(t, "GET", "", "synthetic-session")
	get.request.Header.Set("Last-Event-Id", "begin")
	done := make(chan Terminal, 1)
	go func() { done <- f.executor.Serve(context.Background(), get, httptest.NewRecorder()) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("recovery GET never attached")
	}
	second := f.captured(t, "GET", "", "synthetic-session")
	second.request.Header.Set("Last-Event-Id", "begin")
	got := f.executor.Serve(context.Background(), second, httptest.NewRecorder())
	close(release)
	first := <-done
	if got.Outcome != OutcomeRejected || first.Outcome != OutcomeCompleted || f.transport.sends() != before+1 {
		t.Fatalf("exclusive attachment first=%+v second=%+v sends=%d", first, got, f.transport.sends())
	}
}

func TestNativeStreamRecoveryNetworkBreakPreservesOriginal(t *testing.T) {
	f := newNativeFixtureMode(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	gets := 0
	f.transport.handler = func(r *http.Request, _ []byte) (*http.Response, error) {
		if r.Method == "GET" {
			gets++
			if gets == 1 {
				return nil, io.ErrUnexpectedEOF
			}
			return wireResponse("id: after\ndata: {\"jsonrpc\":\"2.0\",\"id\":40,\"result\":{}}\n\n", true), nil
		}
		return wireResponse("id: before\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":9,\"progress\":1}}\n\n", true), nil
	}
	call := f.captured(t, "POST", `{"jsonrpc":"2.0","id":40,"method":"ping","params":{"_meta":{"progressToken":9}}}`, "synthetic-session")
	f.executor.Serve(context.Background(), call, httptest.NewRecorder())
	for i := 0; i < 2; i++ {
		get := f.captured(t, "GET", "", "synthetic-session")
		get.request.Header.Set("Last-Event-Id", "before")
		got := f.executor.Serve(context.Background(), get, httptest.NewRecorder())
		op, err := f.store.NativeToolRepository().Get(context.Background(), call.id)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 && op.State != "started" {
			t.Fatalf("GET transport break retired original: %+v", op)
		}
		if i == 1 && (got.Outcome != OutcomeCompleted || op.State != "completed") {
			t.Fatalf("second GET %+v op=%+v", got, op)
		}
	}
	if gets != 2 {
		t.Fatalf("GET count=%d", gets)
	}
}

type cancelAfterEventWriter struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
}

func (w *cancelAfterEventWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseRecorder.Write(p)
	w.cancel()
	return n, err
}

func TestNativeStreamHandlerCancellationOnlyDetaches(t *testing.T) {
	f := newNativeFixtureMode(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	f.transport.handler = func(*http.Request, []byte) (*http.Response, error) {
		return wireResponse("id: saved\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":9,\"progress\":1}}\n\n", true), nil
	}
	call := f.captured(t, "POST", `{"jsonrpc":"2.0","id":40,"method":"ping","params":{"_meta":{"progressToken":9}}}`, "synthetic-session")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.executor.Serve(ctx, call, &cancelAfterEventWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel})
	op, err := f.store.NativeToolRepository().Get(context.Background(), call.id)
	if err != nil || op.State != "started" {
		t.Fatalf("handler cancellation became MCP cancellation: %+v %v", op, err)
	}
	scope, err := f.store.NativeToolRepository().GetScope(context.Background(), f.capture)
	if err != nil || scope.State != "active" || scope.ReplayBarrier {
		t.Fatalf("handler revoked scope: %+v %v", scope, err)
	}
}

type cancelPartialBody struct {
	*strings.Reader
	cancel context.CancelFunc
}

func (b *cancelPartialBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if b.Reader.Len() == 0 {
		b.cancel()
	}
	return n, err
}
func (b *cancelPartialBody) Close() error { return nil }

func TestNativeStreamHandlerCancellationPartialFrameRetires(t *testing.T) {
	f := newNativeFixtureMode(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frame := "id: saved\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":9,\"progress\":1}}\n\ndata: {"
	f.transport.handler = func(*http.Request, []byte) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: &cancelPartialBody{Reader: strings.NewReader(frame), cancel: cancel}}, nil
	}
	call := f.captured(t, "POST", `{"jsonrpc":"2.0","id":40,"method":"ping","params":{"_meta":{"progressToken":9}}}`, "synthetic-session")
	f.executor.Serve(ctx, call, httptest.NewRecorder())
	op, err := f.store.NativeToolRepository().Get(context.Background(), call.id)
	if err != nil || op.State != "uncertain" {
		t.Fatalf("partial cancellation recoverable: %+v %v", op, err)
	}
	before := f.transport.sends()
	get := f.captured(t, "GET", "", "synthetic-session")
	get.request.Header.Set("Last-Event-Id", "saved")
	got := f.executor.Serve(context.Background(), get, httptest.NewRecorder())
	if got.Outcome != OutcomeRejected || f.transport.sends() != before {
		t.Fatal("partial cursor sent")
	}
}

func TestNativeExpiredAttachedSSEOnlyKeepsUnindexedTerminal(t *testing.T) {
	for _, kind := range []string{"terminal", "cursor", "callback"} {
		t.Run(kind, func(t *testing.T) {
			limits := DefaultLimits()
			limits.SessionIdle = time.Second
			f := newNativeFixtureMode(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, limits)
			f.handshake(t)
			e := f.executor.(*executor)
			now := time.Now().UTC()
			e.dependencies.Clock = func() time.Time { return now }
			wire := "data: {\"jsonrpc\":\"2.0\",\"id\":44,\"result\":{}}\n\n"
			if kind == "cursor" {
				wire = "id: expired\n" + wire
			}
			if kind == "callback" {
				wire = "data: {\"jsonrpc\":\"2.0\",\"id\":\"expired-callback\",\"method\":\"ping\"}\n\n"
			}
			f.transport.handler = func(*http.Request, []byte) (*http.Response, error) {
				now = now.Add(2 * time.Second)
				return wireResponse(wire, true), nil
			}
			terminal, w := f.serve(t, `{"jsonrpc":"2.0","id":44,"method":"ping"}`, "synthetic-session")
			if kind == "terminal" {
				if terminal.Outcome != OutcomeCompleted || w.Body.String() != wire {
					t.Fatalf("known attached terminal %+v %q", terminal, w.Body.String())
				}
			} else if terminal.Outcome == OutcomeCompleted || strings.Contains(w.Body.String(), "expired-callback") {
				t.Fatalf("expired observation accepted %+v %q", terminal, w.Body.String())
			}
			e.mu.Lock()
			owner := e.scopes[f.capture.Key()]
			if owner != nil {
				for _, s := range owner.sessions {
					s.mu.Lock()
					if len(s.eventIndex) != 0 || len(s.parents) != 0 {
						t.Error("expired observation retained authority")
					}
					s.mu.Unlock()
				}
			}
			e.mu.Unlock()
		})
	}
}

func TestNativeRecoveredResultIsRetainedUnderOriginalID(t *testing.T) {
	f := newNativeFixtureMode(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingFull, RetentionDays: 1})
	f.handshake(t)
	f.executor.(*executor).dependencies.ContentProfiles = syntheticProfiles{}
	f.transport.handler = func(r *http.Request, _ []byte) (*http.Response, error) {
		wire := "id: r1\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":\"p\",\"progress\":1}}\n\n"
		if r.Method == "GET" {
			wire = "id: r2\ndata: {\"jsonrpc\":\"2.0\",\"id\":3,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"retained result\"}]}}\n\n"
		}
		return wireResponse(wire, true), nil
	}
	call := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"},"_meta":{"progressToken":"p"}}}`, "synthetic-session")
	f.executor.Serve(context.Background(), call, httptest.NewRecorder())
	get := f.captured(t, "GET", "", "synthetic-session")
	get.request.Header.Set("Last-Event-Id", "r1")
	got := f.executor.Serve(context.Background(), get, httptest.NewRecorder())
	if got.Outcome != OutcomeCompleted {
		t.Fatal(got)
	}
	op, err := f.store.NativeToolRepository().Get(context.Background(), call.id)
	if err != nil || op.State != "completed" || op.Result == nil {
		t.Fatalf("original retention %+v %v", op, err)
	}
	child, err := f.store.NativeToolRepository().Get(context.Background(), get.id)
	if err != nil || child.Result != nil {
		t.Fatalf("GET stole result %+v %v", child, err)
	}
}

func TestNativeStreamExecutorReusesDeliveredTerminalCapacity(t *testing.T) {
	limits := DefaultLimits()
	limits.Pending = 2
	f := newNativeFixtureMode(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, limits)
	f.handshake(t)
	f.transport.handler = func(_ *http.Request, body []byte) (*http.Response, error) {
		r, err := parseRequest(body, limits)
		if err != nil {
			return nil, err
		}
		return wireResponse("id: event-"+string(r.id)+"\ndata: {\"jsonrpc\":\"2.0\",\"id\":"+string(r.id)+",\"result\":{}}\n\n", true), nil
	}
	for _, id := range []string{"40", "41", "42", "43", "44"} {
		terminal, _ := f.serve(t, `{"jsonrpc":"2.0","id":`+id+`,"method":"ping"}`, "synthetic-session")
		if terminal.Outcome != OutcomeCompleted {
			t.Fatalf("terminal cache became lifetime quota at %s: %+v", id, terminal)
		}
	}
}

type streamDeliveryWriter struct {
	*httptest.ResponseRecorder
	mode            string
	writes, flushes int
}

func (w *streamDeliveryWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == 2 {
		if w.mode == "write" {
			return 0, io.ErrClosedPipe
		}
		if w.mode == "short" {
			return len(p) - 1, nil
		}
	}
	return w.ResponseRecorder.Write(p)
}
func (w *streamDeliveryWriter) FlushError() error {
	w.flushes++
	if w.flushes == 2 {
		if w.mode == "flush" {
			return io.ErrClosedPipe
		}
		if w.mode == "unknown" {
			return http.ErrNotSupported
		}
	}
	w.ResponseRecorder.Flush()
	return nil
}

func TestNativeStreamFailedDeliveryPinsCacheUntilSuccessfulReplay(t *testing.T) {
	for _, mode := range []string{"write", "short", "flush", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			limits := DefaultLimits()
			limits.Pending = 2
			f := newNativeFixtureMode(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, limits)
			f.handshake(t)
			progress := "id: pin1\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":9,\"progress\":1}}\n\n"
			result := "id: pin2\ndata: {\"jsonrpc\":\"2.0\",\"id\":40,\"result\":{}}\n\n"
			f.transport.handler = func(*http.Request, []byte) (*http.Response, error) { return wireResponse(progress+result, true), nil }
			call := f.captured(t, "POST", `{"jsonrpc":"2.0","id":40,"method":"ping","params":{"_meta":{"progressToken":9}}}`, "synthetic-session")
			f.executor.Serve(context.Background(), call, &streamDeliveryWriter{ResponseRecorder: httptest.NewRecorder(), mode: mode})
			before := f.transport.sends()
			blocked, _ := f.serve(t, `{"jsonrpc":"2.0","id":41,"method":"ping"}`, "synthetic-session")
			if blocked.Outcome != OutcomeRejected || f.transport.sends() != before {
				t.Fatalf("pinned cache granted new send: %+v sends=%d/%d", blocked, before, f.transport.sends())
			}
			get := f.captured(t, "GET", "", "synthetic-session")
			get.request.Header.Set("Last-Event-Id", "pin1")
			w := httptest.NewRecorder()
			replay := f.executor.Serve(context.Background(), get, w)
			if replay.Outcome != OutcomeCompleted || w.Body.String() != result || f.transport.sends() != before {
				t.Fatalf("failed terminal cache lost %+v %q", replay, w.Body.String())
			}
			f.transport.handler = func(*http.Request, []byte) (*http.Response, error) {
				return wireResponse("id: new\ndata: {\"jsonrpc\":\"2.0\",\"id\":42,\"result\":{}}\n\n", true), nil
			}
			next, _ := f.serve(t, `{"jsonrpc":"2.0","id":42,"method":"ping"}`, "synthetic-session")
			if next.Outcome != OutcomeCompleted {
				t.Fatalf("successful replay stayed pinned: %+v", next)
			}
			get = f.captured(t, "GET", "", "synthetic-session")
			get.request.Header.Set("Last-Event-Id", "pin1")
			before = f.transport.sends()
			old := f.executor.Serve(context.Background(), get, httptest.NewRecorder())
			if old.Outcome != OutcomeRejected || f.transport.sends() != before {
				t.Fatal("reclaimed cursor created outbound")
			}
			op, err := f.store.NativeToolRepository().Get(context.Background(), call.id)
			if err != nil || op.State != "completed" {
				t.Fatalf("cache reclaim changed known result %+v %v", op, err)
			}
		})
	}
}
