package nativemcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

// A real returned Start is held outside the Store, then actual Verify records
// the first exclusive claim. No gate result or durable authority is replaced.
func holdUnsentLifecycle(t *testing.T, f *nativeFixture, request CapturedRequest, beforeStart ...func()) (*heldFreshGETStart, func() Terminal) {
	t.Helper()
	e := f.executor.(*executor)
	held := &heldFreshGETStart{Repository: e.dependencies.Store, target: request.id, started: make(chan nativetool.StartReceipt, 1), release: make(chan struct{}), verified: make(chan error, 1), claimedAtVerify: make(chan bool, 1)}
	held.claim = func() bool { e.mu.Lock(); defer e.mu.Unlock(); return e.claimed[request.id] != nil }
	e.dependencies.Store = held
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	var once sync.Once
	release := func() { once.Do(func() { close(held.release) }) }
	done := make(chan struct{})
	var terminal Terminal
	go func() { terminal = e.Serve(ctx, request, httptest.NewRecorder()); close(done) }()
	t.Cleanup(func() {
		release()
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("held lifecycle did not drain")
		}
	})
	for _, action := range beforeStart {
		action()
	}
	select {
	case receipt := <-held.started:
		if receipt.OperationID() != request.id || receipt.AttemptID() == "" || held.claim() {
			t.Fatal("missing own newly committed unclaimed receipt")
		}
	case <-done:
		op, err := f.store.NativeToolRepository().Get(context.Background(), request.id)
		t.Fatalf("target ended before successful Start: terminal=%s/%s operation=%s/%s error=%v", terminal.Outcome, terminal.Reason, op.State, op.Reason, err)
	case <-ctx.Done():
		t.Fatal("target never returned actual successful Start")
	}
	return held, func() Terminal {
		release()
		select {
		case <-done:
		case <-ctx.Done():
			t.Fatal("target refusal did not finish")
		}
		if !<-held.claimedAtVerify {
			t.Fatal("actual Verify did not follow first claim")
		}
		return terminal
	}
}

func TestUnsentPingCatalogRefusalKeepsSessionLive(t *testing.T) {
	testReadyUnsentLifecycle(t, "ping")
}
func TestUnsentListChangedDoesNotPublishCatalog(t *testing.T) {
	testReadyUnsentLifecycle(t, "list")
}
func TestUnsentDeleteDoesNotCloseSession(t *testing.T) {
	testReadyUnsentLifecycle(t, "delete")
}
func TestUnsentCancellationDoesNotCancelTarget(t *testing.T) {
	testReadyUnsentLifecycle(t, "cancel")
}
func TestUnsentRecoveryGETClosesOnlyChildAttempt(t *testing.T) {
	testReadyUnsentLifecycle(t, "recovery")
}
func TestUnsentReadyKindsPreserveSessionClosureBeforeVerify(t *testing.T) {
	for _, kind := range []string{"ping", "list", "delete", "cancel", "recovery"} {
		t.Run(kind, func(t *testing.T) { testReadyUnsentLifecycle(t, kind, true) })
	}
}

func testReadyUnsentLifecycle(t *testing.T, kind string, terminalFirst ...bool) {
	t.Helper()
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	f.transport.handler = func(_ *http.Request, body []byte) (*http.Response, error) {
		r, err := parseRequest(body, DefaultLimits())
		if err != nil {
			return nil, err
		}
		result := `{}`
		if r.kind == nativetool.RequestInitialized {
			return &http.Response{StatusCode: 202, Header: http.Header{}, Body: http.NoBody}, nil
		}
		if r.kind == nativetool.RequestInitialize {
			response := wireResponse(`{"jsonrpc":"2.0","id":`+string(r.id)+`,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"synthetic","version":"1"}}}`, false)
			response.Header.Set("Mcp-Session-Id", "sibling-session")
			return response, nil
		}
		if r.kind == nativetool.RequestToolsList {
			result = `{"tools":[{"name":"read_file","inputSchema":{"type":"object"}}]}`
		}
		if kind == "list" && string(r.id) == "40" {
			return wireResponse("data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\",\"params\":{}}\n\ndata: {\"jsonrpc\":\"2.0\",\"id\":40,\"result\":{}}\n\n", true), nil
		}
		if r.kind == nativetool.RequestToolsCall {
			return wireResponse("id: e1\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":\"p\",\"progress\":1}}\n\n", true), nil
		}
		return wireResponse(`{"jsonrpc":"2.0","id":`+string(r.id)+`,"result":`+result+`}`, false), nil
	}
	if result, _ := f.serve(t, `{"jsonrpc":"2.0","id":10,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, ""); result.Outcome != OutcomeCompleted {
		t.Fatal(result)
	}
	if result, _ := f.serve(t, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, "sibling-session"); result.Outcome != OutcomeCompleted {
		t.Fatal(result)
	}
	var targetSession, sibling *session
	for _, s := range f.executor.(*executor).scopes[f.capture.Key()].sessions {
		if s.wire == "synthetic-session" {
			targetSession = s
		} else {
			sibling = s
		}
	}
	siblingBefore := freshGETRow(t, f, `SELECT * FROM native_tool_sessions WHERE session_id=?`, sibling.id)
	var original CapturedRequest
	var originalAudit, originalOnce []any
	var originalStream *streamRecord
	if kind == "cancel" || kind == "recovery" {
		original = f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"},"_meta":{"progressToken":"p"}}}`, "synthetic-session")
		done := make(chan Terminal, 1)
		go func() { done <- f.executor.Serve(context.Background(), original, httptest.NewRecorder()) }()
		f.approve(t, original.id)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("original call did not detach")
		}
		op, err := f.store.NativeToolRepository().Get(context.Background(), original.id)
		if err != nil || op.State != "started" {
			t.Fatal("original logical RPC did not remain live")
		}
		originalAudit = freshGETRow(t, f, `SELECT * FROM native_tool_deliveries WHERE operation_id=?`, original.id)
		originalOnce = freshGETRow(t, f, `SELECT * FROM native_tool_once WHERE operation_id=?`, original.id)
		originalStream = targetSession.streams[original.id]
		if originalStream == nil || originalStream.protocolCanceled {
			t.Fatal("actual original stream missing")
		}
	}
	method, body := "POST", `{"jsonrpc":"2.0","id":30,"method":"ping"}`
	switch kind {
	case "list":
		body = `{"jsonrpc":"2.0","id":30,"method":"tools/list"}`
	case "delete":
		method, body = "DELETE", ""
	case "cancel":
		body = `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":3}}`
	case "recovery":
		method, body = "GET", ""
	}
	request := f.captured(t, method, body, "synthetic-session")
	if kind == "recovery" {
		request.request.Header.Set("Last-Event-Id", "e1")
	}
	held, finish := holdUnsentLifecycle(t, f, request)
	before := readFreshGETFacts(t, f, request.id)
	if before.OperationState != "started" || before.AuditOutcome != "" {
		t.Fatal("held operation was not still open")
	}
	sends := f.transport.sends()
	trigger := `{"jsonrpc":"2.0","id":40,"method":"tools/list"}`
	if kind == "list" {
		trigger = `{"jsonrpc":"2.0","id":40,"method":"ping"}`
	}
	if result, _ := f.serve(t, trigger, "synthetic-session"); result.Outcome != OutcomeCompleted {
		t.Fatal(result)
	}
	after := readFreshGETFacts(t, f, request.id)
	wantGeneration := before.SessionGeneration + 1
	if kind == "list" {
		wantGeneration = 0 // Actual InvalidateCatalog clears the published generation.
	}
	if after.SessionRevision != before.SessionRevision+1 || after.SessionGeneration != wantGeneration || !reflect.DeepEqual(before.Binding, after.Binding) || !reflect.DeepEqual(before.Once, after.Once) || !reflect.DeepEqual(before.Audit, after.Audit) {
		t.Fatal("catalog trigger changed target or did not advance current authority")
	}
	closedFirst := len(terminalFirst) > 0 && terminalFirst[0]
	raceFinish := len(terminalFirst) > 1 && terminalFirst[1]
	var raced *unsentHistoryReadRace
	var historical freshGETFacts
	var historicalScope nativetool.Scope
	if closedFirst {
		closeTarget := func() {
			if err := f.executor.(*executor).closeSession(context.Background(), targetSession); err != nil {
				t.Error(err)
				return
			}
			historical = readFreshGETFacts(t, f, request.id)
			var err error
			historicalScope, err = f.store.NativeToolRepository().GetScope(context.Background(), f.capture)
			if err != nil {
				t.Error(err)
			}
		}
		if raceFinish {
			raced = &unsentHistoryReadRace{Repository: held.Repository, target: request.id, afterOpen: closeTarget}
			held.Repository = raced
		} else {
			held.beforeVerify = closeTarget
		}
	}
	terminal := finish()
	// An actual module CloseSession is a new ordinary physical proof event;
	// observed recovery attempts still require their exact receipt boundary.
	requireNativeClaimAfterDrain(t, f.executor.(*executor), request.id, closedFirst && kind == "recovery")
	verify := <-held.verified
	facts := readFreshGETFacts(t, f, request.id)
	scope, err := f.store.NativeToolRepository().GetScope(context.Background(), f.capture)
	refused := errors.Is(verify, nativetool.ErrConflict)
	if closedFirst && !raceFinish {
		// Exact session close cancels this consumer as well as revoking SQL.
		refused = errors.Is(verify, context.Canceled) || errors.Is(verify, nativetool.ErrDenied)
	}
	if err != nil || !refused || f.transport.sends() != sends+1 {
		t.Fatalf("real catalog/first-claim refusal not established: verify=%v sends=%d", verify, f.transport.sends()-sends)
	}
	t.Logf("firstClaim=true Verify=%v targetEntries=0 state=%s byte_counts_known=%d scope=%s barrier=%t", verify, facts.OperationState, facts.AuditKnown, scope.State, scope.ReplayBarrier)
	if closedFirst {
		if raceFinish && (raced.reads.Load() != 2 || raced.finishes.Load() != 1) {
			t.Fatalf("Finish conflict did not reread exact terminal: reads=%d", raced.reads.Load())
		}
		if historical.OperationState != "uncertain" || historical.AuditKnown != 0 || terminal.Outcome != OutcomeUncertain || terminal.Reason != ReasonExecutionUncertain || !reflect.DeepEqual(historical, facts) || !reflect.DeepEqual(historicalScope, scope) || !reflect.DeepEqual(siblingBefore, freshGETRow(t, f, `SELECT * FROM native_tool_sessions WHERE session_id=?`, sibling.id)) {
			t.Fatal("preexisting exact session closure was replaced or unrelated scope/sibling revoked")
		}
		return
	}
	if terminal.Outcome != OutcomeFailed || facts.OperationState != "failed" || facts.AuditKnown != 1 || !facts.BytesIn.Valid || !facts.BytesOut.Valid || facts.BytesIn.Int64 != 0 || facts.BytesOut.Int64 != 0 || scope.State != "active" || scope.ReplayBarrier {
		t.Fatal("positively unsent request invented uncertainty or revoked the live session")
	}
	if kind == "cancel" || kind == "recovery" {
		op, err := f.store.NativeToolRepository().Get(context.Background(), original.id)
		if err != nil || op.State != "started" || originalStream.protocolCanceled || originalStream.context.Err() != nil || !reflect.DeepEqual(originalAudit, freshGETRow(t, f, `SELECT * FROM native_tool_deliveries WHERE operation_id=?`, original.id)) || !reflect.DeepEqual(originalOnce, freshGETRow(t, f, `SELECT * FROM native_tool_once WHERE operation_id=?`, original.id)) {
			t.Fatal("unsent cancellation/recovery affected original RPC")
		}
		if originalStream.attached || len(originalStream.events) != 1 || originalStream.events[0].id != "e1" {
			t.Fatal("unsent child changed original cursor/attachment")
		}
	}
	if kind == "list" {
		if targetSession.catalogBusy || len(targetSession.pages) != 0 || targetSession.catalog != nil {
			t.Fatal("unsent list retained collection or published stale catalog")
		}
		if result, _ := f.serve(t, `{"jsonrpc":"2.0","id":42,"method":"tools/list"}`, "synthetic-session"); result.Outcome != OutcomeCompleted {
			t.Fatal("fresh collection could not publish")
		}
		sends++
	}
	if result, _ := f.serve(t, `{"jsonrpc":"2.0","id":41,"method":"ping"}`, "synthetic-session"); result.Outcome != OutcomeCompleted || f.transport.sends() != sends+2 {
		t.Fatal("new current-plan ping could not proceed")
	}
	if !reflect.DeepEqual(siblingBefore, freshGETRow(t, f, `SELECT * FROM native_tool_sessions WHERE session_id=?`, sibling.id)) {
		t.Fatal("unrelated sibling session changed")
	}
}
