package nativemcp

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

type unsentAtomicClock struct{ millis atomic.Int64 }

func (c *unsentAtomicClock) now() time.Time  { return time.UnixMilli(c.millis.Load()).UTC() }
func (c *unsentAtomicClock) set(t time.Time) { c.millis.Store(t.UnixMilli()) }

func TestUnsentInitializeClosesOnlyProvisionalSession(t *testing.T) {
	testUnsentInitialization(t, false)
}
func TestUnsentInitializedDoesNotReadySession(t *testing.T) {
	testUnsentInitialization(t, true)
}
func TestUnsentInitializationPreservesPriorSessionClosure(t *testing.T) {
	for _, initialized := range []bool{false, true} {
		t.Run(map[bool]string{false: "initialize", true: "initialized"}[initialized], func(t *testing.T) { testUnsentInitialization(t, initialized, true) })
	}
}
func TestUnsentInitializationPreservesClosureWinningFinishRace(t *testing.T) {
	for _, initialized := range []bool{false, true} {
		t.Run(map[bool]string{false: "initialize", true: "initialized"}[initialized], func(t *testing.T) { testUnsentInitialization(t, initialized, true, true) })
	}
}
func testUnsentInitialization(t *testing.T, initialized bool, terminalFirst ...bool) {
	t.Helper()
	limits := DefaultLimits()
	limits.SessionIdle = time.Minute
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, limits)
	e := f.executor.(*executor)
	clock := &unsentAtomicClock{}
	base := time.Now().UTC().Truncate(time.Millisecond)
	clock.set(base)
	e.dependencies.Clock = clock.now // Immutable dependency before any Serve.
	f.transport.handler = func(_ *http.Request, body []byte) (*http.Response, error) {
		r, err := parseRequest(body, limits)
		if err != nil {
			return nil, err
		}
		if r.kind == nativetool.RequestInitialized {
			return &http.Response{StatusCode: 202, Header: http.Header{}, Body: http.NoBody}, nil
		}
		result := `{}`
		if r.kind == nativetool.RequestInitialize {
			result = `{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"synthetic","version":"1"}}`
		}
		response := wireResponse(`{"jsonrpc":"2.0","id":`+string(r.id)+`,"result":`+result+`}`, false)
		if r.kind == nativetool.RequestInitialize {
			wire := "original-session"
			if string(r.id) == "11" {
				wire = "sibling-session"
			}
			response.Header.Set("Mcp-Session-Id", wire)
		}
		return response, nil
	}
	body, wire := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, ""
	if initialized {
		if result, _ := f.serve(t, body, ""); result.Outcome != OutcomeCompleted {
			t.Fatal(result)
		}
		body, wire = `{"jsonrpc":"2.0","method":"notifications/initialized"}`, "original-session"
	}
	request := f.captured(t, "POST", body, wire)
	held, finish := holdUnsentLifecycle(t, f, request)
	before := readFreshGETFacts(t, f, request.id)
	var target *session
	e.mu.Lock()
	for _, s := range e.scopes[f.capture.Key()].sessions {
		target = s
	}
	e.mu.Unlock()
	if target == nil || target.ready || before.SessionState != "initializing" || before.OperationState != "started" || before.AuditOutcome != "" {
		t.Fatal("target is not an open provisional session")
	}
	clock.set(base.Add(time.Second))
	if result, _ := f.serve(t, `{"jsonrpc":"2.0","id":11,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, ""); result.Outcome != OutcomeCompleted {
		t.Fatal(result)
	}
	if result, _ := f.serve(t, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, "sibling-session"); result.Outcome != OutcomeCompleted {
		t.Fatal(result)
	}
	var sibling *session
	e.mu.Lock()
	for _, s := range e.scopes[f.capture.Key()].sessions {
		if s != target {
			sibling = s
		}
	}
	e.mu.Unlock()
	var targetExpiry, siblingExpiry int64
	if sibling == nil || f.db.QueryRow(`SELECT expires_at_unix_ms FROM native_tool_sessions WHERE session_id=?`, target.id).Scan(&targetExpiry) != nil || f.db.QueryRow(`SELECT expires_at_unix_ms FROM native_tool_sessions WHERE session_id=?`, sibling.id).Scan(&siblingExpiry) != nil {
		t.Fatal("real session expiries absent")
	}
	scope, err := f.store.NativeToolRepository().GetScope(context.Background(), f.capture)
	if err != nil || targetExpiry != base.Add(time.Minute).UnixMilli() || siblingExpiry <= targetExpiry || scope.ExpiresAt.UnixMilli() < siblingExpiry {
		t.Fatal("sibling registration did not extend only shared lifetime")
	}
	siblingBefore := freshGETRow(t, f, `SELECT * FROM native_tool_sessions WHERE session_id=?`, sibling.id)
	sends := f.transport.sends()
	clock.set(time.UnixMilli(targetExpiry + 1))
	if !clock.now().Before(scope.ExpiresAt) || !clock.now().Before(time.UnixMilli(siblingExpiry)) {
		t.Fatal("clock also expired scope/sibling")
	}
	closedFirst := len(terminalFirst) > 0 && terminalFirst[0]
	raceFinish := len(terminalFirst) > 1 && terminalFirst[1]
	var raced *unsentHistoryReadRace
	var historical freshGETFacts
	var historicalScope nativetool.Scope
	if closedFirst {
		closeTarget := func() {
			if err := e.closeSession(context.Background(), target); err != nil {
				t.Error(err)
				return
			}
			historical = readFreshGETFacts(t, f, request.id)
			historicalScope, _ = f.store.NativeToolRepository().GetScope(context.Background(), f.capture)
		}
		if raceFinish {
			raced = &unsentHistoryReadRace{Repository: held.Repository, target: request.id, afterOpen: closeTarget}
			held.Repository = raced
		} else {
			held.beforeVerify = closeTarget
		}
	}
	terminal := finish()
	requireNativeClaimAfterDrain(t, e, request.id, closedFirst)
	verify := <-held.verified
	facts := readFreshGETFacts(t, f, request.id)
	scope, err = f.store.NativeToolRepository().GetScope(context.Background(), f.capture)
	refused := errors.Is(verify, nativetool.ErrDenied) || closedFirst && errors.Is(verify, context.Canceled)
	if err != nil || !refused || f.transport.sends() != sends {
		t.Fatalf("session-only expiry did not refuse actual first claim: verify=%v", verify)
	}
	t.Logf("firstClaim=true Verify=%v targetEntries=0 op=%s known=%d session=%s scope=%s siblingExpiry=%d targetExpiry=%d", verify, facts.OperationState, facts.AuditKnown, facts.SessionState, scope.State, siblingExpiry, targetExpiry)
	if closedFirst {
		if raceFinish && (raced.reads.Load() != 2 || raced.finishes.Load() != 1) {
			t.Fatal("Finish conflict did not preserve without retry")
		}
		if historical.OperationState != "uncertain" || terminal.Outcome != OutcomeUncertain || terminal.Reason != ReasonExecutionUncertain || !reflect.DeepEqual(historical, facts) || !reflect.DeepEqual(historicalScope, scope) || !reflect.DeepEqual(siblingBefore, freshGETRow(t, f, `SELECT * FROM native_tool_sessions WHERE session_id=?`, sibling.id)) {
			t.Fatal("prior provisional-session closure was replaced or valid sibling revoked")
		}
		return
	}
	if terminal.Outcome != OutcomeFailed || facts.OperationState != "failed" || facts.AuditKnown != 1 || !facts.BytesIn.Valid || !facts.BytesOut.Valid || facts.BytesIn.Int64 != 0 || facts.BytesOut.Int64 != 0 || facts.SessionState != "revoked" || target.ready || scope.State != "active" || scope.ReplayBarrier || !reflect.DeepEqual(siblingBefore, freshGETRow(t, f, `SELECT * FROM native_tool_sessions WHERE session_id=?`, sibling.id)) {
		t.Fatal("unsent initialization did not close only its provisional session with known-zero failure")
	}
	e.mu.Lock()
	_, retained := e.scopes[f.capture.Key()].sessions[target.id]
	e.mu.Unlock()
	if retained {
		t.Fatal("provisional session remained reachable")
	}
	if result, _ := f.serve(t, `{"jsonrpc":"2.0","id":12,"method":"ping"}`, "sibling-session"); result.Outcome != OutcomeCompleted || f.transport.sends() != sends+1 {
		t.Fatal("valid sibling could not proceed")
	}
}
