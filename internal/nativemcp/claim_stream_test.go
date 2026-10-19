package nativemcp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

// Physical EOF/FinishAttempt must release only the local guard, not the logical
// operation, cursor, consumed approval or retained stream. The real body Close
// remains an independently held consumer until explicitly released.
func TestNativePhysicalDetachCollectsClaimWithLogicalRPCAlive(t *testing.T) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	e := f.executor.(*executor)
	wire := "id: physical-one\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":\"p\",\"progress\":1}}\n\n"
	response := wireResponse(wire, true)
	body := &claimHeldCloseBody{ReadCloser: response.Body, entered: make(chan struct{}), release: make(chan struct{}), returned: make(chan struct{})}
	response.Body = body
	f.transport.handler = func(*http.Request, []byte) (*http.Response, error) { return response, nil }
	call := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"},"_meta":{"progressToken":"p"}}}`, "synthetic-session")
	done := make(chan Terminal, 1)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(body.release) }) }
	go func() { done <- e.Serve(context.Background(), call, httptest.NewRecorder()) }()
	defer func() {
		release()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("detached handler did not join")
		}
		select {
		case <-body.returned:
		case <-time.After(time.Second):
			t.Error("owned response Close did not join")
		}
	}()
	f.approve(t, call.id)
	select {
	case <-body.entered:
	case <-time.After(time.Second):
		t.Fatal("real response cleanup boundary missing")
	}
	facts := readFreshGETFacts(t, f, call.id)
	if facts.OperationState != "started" || facts.AuditOutcome != "failed" || facts.AuditKnown != 0 {
		t.Fatalf("not a real physical-only detach: logical=%s physical=%s known=%d", facts.OperationState, facts.AuditOutcome, facts.AuditKnown)
	}
	e.mu.Lock()
	token := e.claimed[call.id]
	held := token != nil && !token.consumerDone && token.physicalClass == nativeClaimOwnedObserved
	var stream *streamRecord
	for _, s := range e.scopes[f.capture.Key()].sessions {
		s.mu.Lock()
		stream = s.streams[call.id]
		s.mu.Unlock()
	}
	e.mu.Unlock()
	if !held || stream == nil {
		t.Fatal("physical terminal prematurely released live body/logical stream")
	}
	if e.dependencies.Store.VerifyStartReceipt(context.Background(), stream.receipt, time.Now().UTC()) == nil {
		t.Fatal("terminal physical audit still grants send")
	}
	approval := freshGETRow(t, f, `SELECT * FROM native_tool_approvals WHERE operation_id=?`, call.id)
	once := freshGETRow(t, f, `SELECT * FROM native_tool_once WHERE operation_id=?`, call.id)
	audit := freshGETRow(t, f, `SELECT * FROM native_tool_deliveries WHERE operation_id=?`, call.id)
	release()
	select {
	case terminal := <-done:
		done <- terminal
	case <-time.After(time.Second):
		t.Fatal("released physical consumer did not drain")
	}
	if len(approval) == 0 || len(once) == 0 || len(audit) == 0 || f.transport.sends() != 4 {
		t.Fatal("real approval/once/audit/send evidence missing")
	}
	facts = readFreshGETFacts(t, f, call.id)
	if facts.OperationState != "started" || facts.AuditKnown != 0 {
		t.Fatal("local drain changed logical/unknown effects")
	}
	requireNativeClaimAfterDrain(t, e, call.id, false)
}

func TestNativeRecoveryRetirementCollectsOnlyExactHeldChild(t *testing.T) {
	f := newNativeFixtureMode(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	e := f.executor.(*executor)
	entered, releaseWire := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseWire) }) }
	f.transport.handler = func(r *http.Request, _ []byte) (*http.Response, error) {
		if r.Method == "GET" {
			close(entered)
			<-releaseWire
			return nil, io.ErrUnexpectedEOF
		}
		return wireResponse("id: recovery-proof\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":\"p\",\"progress\":1}}\n\n", true), nil
	}
	parent := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"ping","params":{"_meta":{"progressToken":"p"}}}`, "synthetic-session")
	e.Serve(context.Background(), parent, httptest.NewRecorder())
	requireNativeClaimAfterDrain(t, e, parent.id, false)
	parentAudit := freshGETRow(t, f, `SELECT * FROM native_tool_deliveries WHERE operation_id=?`, parent.id)
	child := f.captured(t, "GET", "", "synthetic-session")
	child.request.Header.Set("Last-Event-Id", "recovery-proof")
	done := make(chan Terminal, 1)
	go func() { done <- e.Serve(context.Background(), child, httptest.NewRecorder()) }()
	defer func() {
		release()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("recovery attachment did not join")
		}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("actual recovery transport missing")
	}
	e.mu.Lock()
	var stream *streamRecord
	var s *session
	for _, candidate := range e.scopes[f.capture.Key()].sessions {
		candidate.mu.Lock()
		s = candidate
		stream = candidate.streams[parent.id]
		candidate.mu.Unlock()
	}
	token := e.claimed[child.id]
	e.mu.Unlock()
	if s == nil || stream == nil || token == nil {
		t.Fatal("real child attachment association missing")
	}
	e.retireStream(s, stream)
	e.mu.Lock()
	provedHeld := e.claimed[child.id] == token && token.physicalTerminal && !token.consumerDone
	e.mu.Unlock()
	if !provedHeld {
		t.Fatal("stream retirement did not select exact included child proof while attached")
	}
	if !reflect.DeepEqual(parentAudit, freshGETRow(t, f, `SELECT * FROM native_tool_deliveries WHERE operation_id=?`, parent.id)) {
		t.Fatal("child retirement rewrote original physical audit")
	}
	release()
	select {
	case terminal := <-done:
		done <- terminal
	case <-time.After(time.Second):
		t.Fatal("retired child did not drain")
	}
	requireNativeClaimAfterDrain(t, e, child.id, false)
	if f.transport.sends() != 5 {
		t.Fatal("retirement repeated original POST")
	}
}

type callbackRetirementCallObserver struct {
	nativetool.Repository
	calls                                 atomic.Int32
	parent                                nativetool.ID
	parentRead, releaseRead, readReturned chan struct{}
	readOnce, releaseOnce                 sync.Once
	child                                 nativetool.ID
	childRead, releaseChild               chan struct{}
	childOnce, childReleaseOnce           sync.Once
}

func (o *callbackRetirementCallObserver) Get(ctx context.Context, id nativetool.ID) (nativetool.Operation, error) {
	op, err := o.Repository.Get(ctx, id)
	if id == o.child && o.childRead != nil && err == nil && op.CompletedAt != nil {
		o.childOnce.Do(func() { close(o.childRead) })
		select {
		case <-o.releaseChild:
		case <-ctx.Done():
			return op, ctx.Err()
		}
	}
	if id == o.parent && o.parentRead != nil {
		o.readOnce.Do(func() { close(o.parentRead) })
		select {
		case <-o.releaseRead:
		case <-ctx.Done():
			return op, ctx.Err()
		}
		select {
		case o.readReturned <- struct{}{}:
		default:
		}
	}
	return op, err
}

func (o *callbackRetirementCallObserver) release() {
	o.childReleaseOnce.Do(func() {
		if o.releaseChild != nil {
			close(o.releaseChild)
		}
	})
	o.releaseOnce.Do(func() {
		if o.releaseRead != nil {
			close(o.releaseRead)
		}
	})
}

func (o *callbackRetirementCallObserver) RetireObservedCallback(ctx context.Context, a nativetool.CallbackAuthority, now time.Time) ([]nativetool.StartReceipt, error) {
	o.calls.Add(1)
	if o.parentRead != nil {
		select {
		case <-o.parentRead:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	proof, err := o.Repository.RetireObservedCallback(ctx, a, now)
	o.release()
	return proof, err
}

func TestNativeSentCallbackFailureDrainsAndReachesRetirement(t *testing.T) {
	for _, parentFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "child_retirement_first", true: "parent_cleanup_first"}[parentFirst], func(t *testing.T) { testNativeSentCallbackRetirementOrder(t, parentFirst) })
	}
}

func testNativeSentCallbackRetirementOrder(t *testing.T, parentFirst bool) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	e := f.executor.(*executor)
	observer := &callbackRetirementCallObserver{Repository: e.dependencies.Store.(nativetool.Repository)}
	e.dependencies.Store = observer
	f.handshake(t)
	f.transport.handler = func(r *http.Request, _ []byte) (*http.Response, error) {
		if r.Method == "GET" {
			return wireResponse("data: {\"jsonrpc\":\"2.0\",\"id\":17,\"method\":\"ping\"}\n\n", true), nil
		}
		return nil, io.ErrUnexpectedEOF
	}
	parent := f.captured(t, "GET", "", "synthetic-session")
	if result := e.Serve(context.Background(), parent, httptest.NewRecorder()); result.Outcome != OutcomeCompleted {
		t.Fatal(result)
	}
	parentAudit := freshGETRow(t, f, `SELECT * FROM native_tool_deliveries WHERE operation_id=?`, parent.id)
	if !parentFirst {
		observer.parent, observer.parentRead, observer.releaseRead, observer.readReturned = parent.id, make(chan struct{}), make(chan struct{}), make(chan struct{}, 1)
	}
	defer observer.release()
	e.mu.Lock()
	var s *session
	for _, candidate := range e.scopes[f.capture.Key()].sessions {
		s = candidate
	}
	e.mu.Unlock()
	child := f.captured(t, "POST", `{"jsonrpc":"2.0","id":17,"result":{}}`, "synthetic-session")
	if parentFirst {
		observer.child, observer.childRead, observer.releaseChild = child.id, make(chan struct{}), make(chan struct{})
	}
	done := make(chan Terminal, 1)
	defer func() {
		observer.release()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("failed reply worker did not drain")
		}
	}()
	before := f.transport.sends()
	go func() { done <- e.Serve(context.Background(), child, httptest.NewRecorder()) }()
	f.approve(t, child.id)
	if parentFirst {
		select {
		case <-observer.childRead:
		case <-time.After(time.Second):
			t.Fatal("real terminal child read did not enter")
		}
		deadline := time.Now().Add(time.Second)
		for {
			s.mu.Lock()
			cleared := s.parents["17"] == nil
			s.mu.Unlock()
			if cleared {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("real parent cleanup did not win")
			}
			time.Sleep(time.Millisecond)
		}
		observer.release()
	}
	select {
	case result := <-done:
		done <- result
		if result.Outcome != OutcomeUncertain {
			t.Fatal(result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("sent reply did not join")
	}
	facts := readFreshGETFacts(t, f, child.id)
	if f.transport.sends() != before+1 || facts.OperationState != "uncertain" || facts.AuditKnown != 0 || facts.AuditOutcome != "failed" {
		t.Fatal("actual failed reply lost its unknown physical effects")
	}
	if !parentFirst && observer.calls.Load() == 0 {
		t.Fatal("complete failed-reply caller never reached actual callback retirement")
	}
	if parentFirst && observer.calls.Load() != 0 {
		t.Fatal("parent-first empty association unexpectedly called retirement")
	}
	if !parentFirst {
		select {
		case <-observer.readReturned:
		case <-time.After(time.Second):
			t.Fatal("real parent read did not join after callback boundary")
		}
	}
	deadline := time.Now().Add(time.Second)
	for {
		s.mu.Lock()
		retired := s.streams[parent.id] == nil
		s.mu.Unlock()
		if retired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("competing parent retirement did not drain")
		}
		time.Sleep(time.Millisecond)
	}
	if !reflect.DeepEqual(parentAudit, freshGETRow(t, f, `SELECT * FROM native_tool_deliveries WHERE operation_id=?`, parent.id)) {
		t.Fatal("failed reply rewrote known parent audit")
	}
	requireNativeClaimAfterDrain(t, e, child.id, false)
}

func TestNativeCallbackRetirementProofMatchesHeldReplyOnly(t *testing.T) {
	f := newNativeFixtureMode(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	e := f.executor.(*executor)
	f.transport.handler = func(*http.Request, []byte) (*http.Response, error) {
		return wireResponse("data: {\"jsonrpc\":\"2.0\",\"id\":17,\"method\":\"ping\"}\n\n", true), nil
	}
	parent := f.captured(t, "GET", "", "synthetic-session")
	if result := e.Serve(context.Background(), parent, httptest.NewRecorder()); result.Outcome != OutcomeCompleted {
		t.Fatal(result)
	}
	parentAudit := freshGETRow(t, f, `SELECT * FROM native_tool_deliveries WHERE operation_id=?`, parent.id)
	child := f.captured(t, "POST", `{"jsonrpc":"2.0","id":17,"result":{}}`, "synthetic-session")
	verifyEntered, verifyRelease := make(chan struct{}), make(chan struct{})
	startRelease := make(chan struct{})
	close(startRelease)
	held := &heldFreshGETStart{Repository: e.dependencies.Store.(nativetool.Repository), target: child.id, started: make(chan nativetool.StartReceipt, 1), release: startRelease, verified: make(chan error, 1), claimedAtVerify: make(chan bool, 1)}
	held.claim = func() bool { e.mu.Lock(); defer e.mu.Unlock(); return e.claimed[child.id] != nil }
	held.beforeVerify = func() { close(verifyEntered); <-verifyRelease }
	e.dependencies.Store = held
	done := make(chan Terminal, 1)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(verifyRelease) }) }
	go func() { done <- e.Serve(context.Background(), child, httptest.NewRecorder()) }()
	defer func() {
		release()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("held reply consumer did not join")
		}
	}()
	select {
	case <-verifyEntered:
	case <-time.After(time.Second):
		t.Fatal("real reply did not reach first Verify")
	}
	receipt := <-held.started
	if receipt.OperationID() != child.id || !<-held.claimedAtVerify {
		t.Fatal("reply has no real committed first claim")
	}
	e.mu.Lock()
	var ownerSession *session
	var callback *callbackParent
	for _, s := range e.scopes[f.capture.Key()].sessions {
		s.mu.Lock()
		ownerSession = s
		callback = s.parents["17"]
		s.mu.Unlock()
	}
	token := e.claimed[child.id]
	e.mu.Unlock()
	if ownerSession == nil || callback == nil || token == nil {
		t.Fatal("owned callback association missing")
	}
	before := f.transport.sends()
	if err := e.retireCallbackPhysical(context.Background(), ownerSession, callback.authority, child.id); err != nil {
		t.Fatal(err)
	}
	facts := readFreshGETFacts(t, f, child.id)
	if facts.OperationState != "uncertain" || facts.AuditOutcome != "failed" || facts.AuditKnown != 0 || f.transport.sends() != before {
		t.Fatal("callback retirement invented known0/send or failed to close actual child")
	}
	if !reflect.DeepEqual(parentAudit, freshGETRow(t, f, `SELECT * FROM native_tool_deliveries WHERE operation_id=?`, parent.id)) {
		t.Fatal("child retirement changed parent physical history")
	}
	e.mu.Lock()
	provedButHeld := e.claimed[child.id] == token && token.physicalTerminal && !token.consumerDone
	e.mu.Unlock()
	if !provedButHeld {
		t.Fatal("exact callback retirement result not consumed independently of held reply")
	}
	release()
	select {
	case result := <-done:
		done <- result
	case <-time.After(time.Second):
		t.Fatal("retired reply did not drain")
	}
	if f.transport.sends() != before {
		t.Fatal("retired reply escaped actual Verify")
	}
	requireNativeClaimAfterDrain(t, e, child.id, false)
}

type claimHeldTerminalWriter struct {
	*httptest.ResponseRecorder
	writes           int
	entered, release chan struct{}
}

func (w *claimHeldTerminalWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == 2 {
		close(w.entered)
		<-w.release
	}
	return w.ResponseRecorder.Write(p)
}

func TestNativeLogicalTerminalDoesNotProvePhysicalClaim(t *testing.T) {
	testNativeLogicalTerminalClaim(t, "normal")
}

func TestNativeEmptyRetirementCannotBorrowUnobservedPriorProof(t *testing.T) {
	testNativeLogicalTerminalClaim(t, "prior_retirement")
}

func TestNativeRegisteredRetirementProofWaitsForConsumer(t *testing.T) {
	testNativeLogicalTerminalClaim(t, "registered")
}

func TestNativeRetirementSnapshotRejectsWrongIdentityAndClass(t *testing.T) {
	testNativeLogicalTerminalClaim(t, "snapshot")
}

func testNativeLogicalTerminalClaim(t *testing.T, retirement string) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	e := f.executor.(*executor)
	wire := "id: logical-one\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":\"p\",\"progress\":1}}\n\nid: logical-two\ndata: {\"jsonrpc\":\"2.0\",\"id\":3,\"result\":{\"content\":[]}}\n\n"
	f.transport.handler = func(*http.Request, []byte) (*http.Response, error) { return wireResponse(wire, true), nil }
	call := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"},"_meta":{"progressToken":"p"}}}`, "synthetic-session")
	w := &claimHeldTerminalWriter{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan Terminal, 1)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(w.release) }) }
	go func() { done <- e.Serve(context.Background(), call, w) }()
	defer func() {
		release()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("logical-terminal writer did not join")
		}
	}()
	f.approve(t, call.id)
	select {
	case <-w.entered:
	case <-time.After(time.Second):
		t.Fatal("terminal downstream not held")
	}
	facts := readFreshGETFacts(t, f, call.id)
	if facts.OperationState != "completed" || facts.AuditOutcome != "" {
		t.Fatalf("logical/physical boundary not distinct: %s/%s", facts.OperationState, facts.AuditOutcome)
	}
	e.mu.Lock()
	token := e.claimed[call.id]
	retained := token != nil && !token.physicalTerminal && !token.consumerDone
	e.mu.Unlock()
	if !retained {
		t.Fatal("CompleteStreamRPC invented physical proof or released live writer")
	}
	if retirement != "normal" {
		e.mu.Lock()
		var stream *streamRecord
		var session *session
		for _, s := range e.scopes[f.capture.Key()].sessions {
			s.mu.Lock()
			stream = s.streams[call.id]
			session = s
			s.mu.Unlock()
		}
		e.mu.Unlock()
		if stream == nil {
			t.Fatal("registered actual stream missing")
		}
		if retirement == "snapshot" {
			snapshot := e.snapshotRetirementClaims(session.owner.claimKey, session.claimKey, "")
			proof, err := e.dependencies.Store.RetireObservedStream(context.Background(), stream.authority, time.Now().UTC())
			if err != nil || len(proof) != 1 || proof[0] != stream.receipt {
				t.Fatal("snapshot lacks actual positive retirement", err)
			}
			wrongClass := append([]nativeClaimSnapshot(nil), snapshot...)
			for i := range wrongClass {
				wrongClass[i].class = nativeClaimOwnedOrdinary
			}
			checks := []struct {
				snapshot       []nativeClaimSnapshot
				receipts       []nativetool.StartReceipt
				owner, session uint64
			}{
				{nil, proof, session.owner.claimKey, session.claimKey},
				{snapshot, nil, session.owner.claimKey, session.claimKey},
				{snapshot, []nativetool.StartReceipt{{}}, session.owner.claimKey, session.claimKey},
				{snapshot, proof, 0, session.claimKey},
				{snapshot, proof, session.owner.claimKey, 0},
				{snapshot, proof, session.owner.claimKey + 1000, session.claimKey},
				{snapshot, proof, session.owner.claimKey, session.claimKey + 1000},
				{wrongClass, proof, session.owner.claimKey, session.claimKey},
			}
			for i, check := range checks {
				e.noteRetirementProof(check.snapshot, check.receipts, check.owner, check.session)
				e.mu.Lock()
				invalid := token.physicalTerminal || token.consumerDone || e.claimed[call.id] != token
				e.mu.Unlock()
				if invalid {
					t.Fatalf("invalid snapshot %d selected a claim", i)
				}
			}
			e.noteRetirementProof(snapshot, proof, session.owner.claimKey, session.claimKey)
			e.noteRetirementProof(snapshot, proof, session.owner.claimKey, session.claimKey)
		} else if retirement == "registered" {
			e.retireStream(session, stream)
			closed := readFreshGETFacts(t, f, call.id)
			e.mu.Lock()
			provedButHeld := e.claimed[call.id] == token && token.physicalTerminal && !token.consumerDone
			e.mu.Unlock()
			if closed.OperationState != "completed" || closed.AuditOutcome != "failed" || closed.AuditKnown != 0 {
				t.Fatal("actual retirement did not close only physical audit")
			}
			if !provedButHeld {
				t.Fatal("actual registered retirement lost its positive proof before consumer drain")
			}
		} else {
			// Model an independently completed Store retirement whose proof was
			// not observed by the executor (e.g. an existing Store timer).
			proof, err := e.dependencies.Store.RetireObservedStream(context.Background(), stream.authority, time.Now().UTC())
			if err != nil || len(proof) != 1 || proof[0] != stream.receipt {
				t.Fatal("independent actual retirement did not verify original receipt", err)
			}
			if proof, err := e.dependencies.Store.RetireObservedStream(context.Background(), stream.authority, time.Now().UTC()); err != nil || len(proof) != 0 {
				t.Fatal("missing registration retirement returned physical proof", err)
			}
			stillOpen := readFreshGETFacts(t, f, call.id)
			e.mu.Lock()
			unproven := e.claimed[call.id] == token && !token.physicalTerminal && !token.consumerDone
			e.mu.Unlock()
			if stillOpen.OperationState != "completed" || stillOpen.AuditOutcome != "failed" || stillOpen.AuditKnown != 0 || !unproven {
				t.Fatalf("empty-proof projection logical=%s audit=%s unproven=%t", stillOpen.OperationState, stillOpen.AuditOutcome, unproven)
			}
		}
	}
	approval := freshGETRow(t, f, `SELECT * FROM native_tool_approvals WHERE operation_id=?`, call.id)
	release()
	select {
	case result := <-done:
		done <- result
		if retirement == "normal" && result.Outcome != OutcomeCompleted {
			t.Fatal(result)
		}
	case <-time.After(time.Second):
		t.Fatal("physical completion did not join")
	}
	facts = readFreshGETFacts(t, f, call.id)
	wantOutcome, wantKnown := "completed", int64(1)
	if retirement != "normal" {
		wantOutcome, wantKnown = "failed", 0
	}
	if facts.AuditOutcome != wantOutcome || int64(facts.AuditKnown) != wantKnown || !reflect.DeepEqual(approval, freshGETRow(t, f, `SELECT * FROM native_tool_approvals WHERE operation_id=?`, call.id)) {
		t.Fatal("physical completion changed consumed approval or counts")
	}
	requireNativeClaimAfterDrain(t, e, call.id, retirement == "prior_retirement")
}

func TestNativeUnindexedKnownTerminalCollectsPhysicalClaim(t *testing.T) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	wire := "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":\"p\",\"progress\":1}}\n\ndata: {\"jsonrpc\":\"2.0\",\"id\":3,\"result\":{\"content\":[]}}\n\n"
	f.transport.handler = func(*http.Request, []byte) (*http.Response, error) { return wireResponse(wire, true), nil }
	call := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"},"_meta":{"progressToken":"p"}}}`, "synthetic-session")
	done := make(chan Terminal, 1)
	go func() {
		done <- f.executor.Serve(context.Background(), call, &terminalDeliveryFailure{ResponseRecorder: httptest.NewRecorder()})
	}()
	f.approve(t, call.id)
	select {
	case result := <-done:
		if result.Outcome != OutcomeCompleted {
			t.Fatal(result)
		}
	case <-time.After(time.Second):
		t.Fatal("known terminal delivery failure did not join")
	}
	facts := readFreshGETFacts(t, f, call.id)
	if facts.OperationState != "completed" || facts.AuditOutcome != "failed" || facts.AuditKnown != 0 || f.transport.sends() != 4 {
		t.Fatalf("known logical/unknown physical proof missing: %s/%s known%d", facts.OperationState, facts.AuditOutcome, facts.AuditKnown)
	}
	requireNativeClaimAfterDrain(t, f.executor.(*executor), call.id, false)
}
