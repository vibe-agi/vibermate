package nativemcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

func TestUnsentPingReplyRetiresExactObservation(t *testing.T) {
	ExerciseUnsentReply(t, "ping", nil)
}
func TestUnsentPingReplyPreservesPriorRetirement(t *testing.T) {
	ExerciseUnsentReply(t, "ping", nil, true)
}
func TestUnsentPingReplyPreservesRetirementWinningFinishRace(t *testing.T) {
	ExerciseUnsentReply(t, "ping", nil, true, true)
}

// Test-only bridge follows the existing control harness to avoid an import
// cycle: form/URL use the real authenticated desktop router, not Store bypass.
func ExerciseUnsentReply(t *testing.T, kind string, approve func(Executor, *runtimepersistence.Store, manualcapture.Controller, captureidentity.Reference, nativetool.Approval), terminalFirst ...bool) {
	t.Helper()
	limits := DefaultLimits()
	limits.CallLifetime = time.Minute
	mode := environment.ToolPolicyReview
	if kind == "ping" {
		mode = environment.ToolPolicyObserve
	}
	f := newNativeFixtureStoreLifetime(t, mode, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, time.Minute, limits)
	e := f.executor.(*executor)
	clock := &unsentAtomicClock{}
	clock.set(time.Now().UTC())
	// Real ToolDecision/control use real time before the explicit expiry step;
	// Start must not backdate consumption before the authenticated decision.
	e.dependencies.Clock = func() time.Time {
		now := time.Now().UTC()
		if advanced := clock.now(); advanced.After(now) {
			return advanced
		}
		return now
	}
	f.handshake(t)
	callback := `{"jsonrpc":"2.0","id":17,"method":"ping"}`
	reply := `{"jsonrpc":"2.0","id":17,"result":{}}`
	switch kind {
	case "form":
		callback = `{"jsonrpc":"2.0","id":17,"method":"elicitation/create","params":{"message":"synthetic private form","requestedSchema":{"type":"object","properties":{"answer":{"type":"string"}}}}}`
		reply = `{"jsonrpc":"2.0","id":17,"result":{"action":"decline"}}`
	case "url":
		callback = `{"jsonrpc":"2.0","id":17,"method":"elicitation/create","params":{"mode":"url","message":"synthetic private URL","url":"https://example.invalid/consent","elicitationId":"unsent-url"}}`
		reply = `{"jsonrpc":"2.0","id":17,"result":{"action":"accept"}}`
	}
	var getCount atomic.Int32
	f.transport.handler = func(r *http.Request, _ []byte) (*http.Response, error) {
		if r.Method == "GET" {
			wire := callback
			if getCount.Add(1) == 2 {
				wire = strings.Replace(wire, `"id":17`, `"id":18`, 1)
				wire = strings.Replace(wire, `"unsent-url"`, `"sibling-url"`, 1)
			}
			return wireResponse("data: "+wire+"\n\n", true), nil
		}
		return &http.Response{StatusCode: 202, Header: http.Header{}, Body: http.NoBody}, nil
	}
	parent := f.captured(t, "GET", "", "synthetic-session")
	if result := e.Serve(context.Background(), parent, httptest.NewRecorder()); result.Outcome != OutcomeCompleted {
		t.Fatal(result)
	}
	parentAudit := freshGETRow(t, f, `SELECT * FROM runtime_egress_attempts WHERE parent_id=?`, parent.id)
	var s *session
	e.mu.Lock()
	for _, candidate := range e.scopes[f.capture.Key()].sessions {
		s = candidate
	}
	e.mu.Unlock()
	s.mu.Lock()
	observed, stream := s.parents["17"], s.streams[parent.id]
	s.mu.Unlock()
	if observed == nil || stream == nil || !stream.expires.Before(s.timeLimit) {
		t.Fatal("actual retained observation expiry absent")
	}
	manuals, err := manualcapture.NewManager(context.Background(), manualcapture.DefaultOptions(f.store.ManualCaptureRepository()))
	if err != nil {
		t.Fatal(err)
	}
	defer manuals.Shutdown(context.Background())
	child := f.captured(t, "POST", reply, "synthetic-session")
	held, finish := holdUnsentLifecycle(t, f, child, func() {
		if kind != "ping" {
			if approve == nil {
				t.Fatal("authenticated approval callback required")
			}
			approve(e, f.store, manuals, f.capture, waitNativeQuestion(t, f, child.id))
		}
	})
	before := readFreshGETFacts(t, f, child.id)
	var approval []any
	if kind != "ping" {
		op, err := f.store.NativeToolRepository().Get(context.Background(), child.id)
		if err != nil || op.ApprovalID == nil {
			t.Fatal("authenticated reply lacked approval")
		}
		a, err := f.store.NativeToolRepository().GetApproval(context.Background(), *op.ApprovalID)
		if err != nil || a.ConsumedAt == nil {
			t.Fatal("actual reply Start did not consume Allow")
		}
		approval = freshGETRow(t, f, `SELECT * FROM native_tool_approvals WHERE operation_id=?`, child.id)
	}
	if before.OperationState != "started" || before.AuditOutcome != "" {
		t.Fatal("held child was already terminal")
	}
	// A later genuine observation must survive exact target cleanup. Its
	// independent StreamAuthority remains live beyond the target's expiry.
	clock.set(stream.created.Add(time.Second))
	sibling := f.captured(t, "GET", "", "synthetic-session")
	siblingID := sibling.id
	if result := e.Serve(context.Background(), sibling, httptest.NewRecorder()); result.Outcome != OutcomeCompleted {
		t.Fatal(result)
	}
	siblingAudit := freshGETRow(t, f, `SELECT * FROM runtime_egress_attempts WHERE parent_id=?`, siblingID)
	s.mu.Lock()
	siblingParent, siblingStream := s.parents["18"], s.streams[siblingID]
	s.mu.Unlock()
	if siblingParent == nil || siblingStream == nil || !siblingStream.expires.After(stream.expires) {
		t.Fatal("independent later callback absent")
	}
	clock.set(stream.expires.Add(time.Millisecond))
	scope, err := f.store.NativeToolRepository().GetScope(context.Background(), f.capture)
	if err != nil || !clock.now().Before(scope.ExpiresAt) || !clock.now().Before(s.timeLimit) {
		t.Fatal("parent-only expiry also expired owner/session")
	}
	sends := f.transport.sends()
	retiredFirst := len(terminalFirst) > 0 && terminalFirst[0]
	raceFinish := len(terminalFirst) > 1 && terminalFirst[1]
	var raced *unsentHistoryReadRace
	var historical freshGETFacts
	var historicalScope nativetool.Scope
	if retiredFirst {
		retireTarget := func() {
			if err := f.store.NativeToolRepository().RetireObservedCallback(context.Background(), observed.authority, clock.now()); err != nil {
				t.Error(err)
				return
			}
			historical = readFreshGETFacts(t, f, child.id)
			historicalScope, _ = f.store.NativeToolRepository().GetScope(context.Background(), f.capture)
		}
		if raceFinish {
			raced = &unsentHistoryReadRace{Repository: held.Repository, target: child.id, afterOpen: retireTarget}
			held.Repository = raced
		} else {
			held.beforeVerify = retireTarget
		}
	}
	terminal := finish()
	requireNativeClaimAfterDrain(t, e, child.id, retiredFirst)
	verify := <-held.verified
	facts := readFreshGETFacts(t, f, child.id)
	scope, err = f.store.NativeToolRepository().GetScope(context.Background(), f.capture)
	refused := errors.Is(verify, nativetool.ErrDenied) || retiredFirst && errors.Is(verify, nativetool.ErrReplay)
	if err != nil || !refused || f.transport.sends() != sends {
		t.Fatalf("actual parent expiry refusal absent: %v", verify)
	}
	t.Logf("%s firstClaim=true Verify=%v entries=0 child=%s known=%d scope=%s barrier=%t", kind, verify, facts.OperationState, facts.AuditKnown, scope.State, scope.ReplayBarrier)
	if retiredFirst {
		if raceFinish && (raced.reads.Load() < 2 || raced.finishes.Load() != 1) {
			t.Fatal("callback conflict did not preserve without Finish retry")
		}
		if historical.OperationState != "uncertain" || terminal.Outcome != OutcomeUncertain || terminal.Reason != ReasonExecutionUncertain || !reflect.DeepEqual(historical, facts) || !reflect.DeepEqual(historicalScope, scope) {
			t.Fatal("exact callback retirement was replaced or unrelated owner revoked")
		}
	} else if terminal.Outcome != OutcomeFailed || facts.OperationState != "failed" || facts.AuditKnown != 1 || !facts.BytesIn.Valid || !facts.BytesOut.Valid || facts.BytesIn.Int64 != 0 || facts.BytesOut.Int64 != 0 || scope.State != "active" || scope.ReplayBarrier {
		t.Fatal("expired never-sent reply invented unknown effects")
	}
	if !reflect.DeepEqual(parentAudit, freshGETRow(t, f, `SELECT * FROM runtime_egress_attempts WHERE parent_id=?`, parent.id)) || !reflect.DeepEqual(before.Once, facts.Once) || kind != "ping" && !reflect.DeepEqual(approval, freshGETRow(t, f, `SELECT * FROM native_tool_approvals WHERE operation_id=?`, child.id)) {
		t.Fatal("failed reply changed parent audit/once/consumed approval")
	}
	if !reflect.DeepEqual(siblingAudit, freshGETRow(t, f, `SELECT * FROM runtime_egress_attempts WHERE parent_id=?`, siblingID)) {
		t.Fatal("exact cleanup changed independent callback audit")
	}
	s.mu.Lock()
	wantElicitations := 0
	if kind == "url" {
		wantElicitations = 1
	}
	valid := s.parents["17"] == nil && s.parents["18"] == siblingParent && !siblingParent.consumed && siblingStream.context.Err() == nil && clock.now().Before(siblingStream.expires) && len(s.elicitations) == wantElicitations
	s.mu.Unlock()
	if !valid {
		t.Fatal("failed reply retained its observation or invented URL completion obligation")
	}
	if result, _ := f.serve(t, reply, "synthetic-session"); result.Outcome == OutcomeCompleted || f.transport.sends() != sends {
		t.Fatal("failed observed reply was sent again")
	}
}
