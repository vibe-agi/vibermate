package nativemcp

import (
	"context"
	"errors"
	"fmt"
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

type nativeClaimLifetimeSnapshot struct {
	Claims, Pending, Sessions, Active, Parents, Streams, Pages int
	Budget, SessionRetained                                    int64
	Once, Audits, TerminalAudits, KnownAudits, Completed       int
	SQLSessions, ReadySessions, RevokedSessions                int
	Scope                                                      string
	Barrier, Closed                                            bool
}

func requireNativeClaimAfterDrain(t *testing.T, e *executor, id nativetool.ID, retained bool) {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	token := e.claimed[id]
	if retained {
		if token == nil || !token.consumerDone || token.physicalTerminal {
			t.Fatal("history/error was treated as positive physical proof")
		}
	} else if token != nil {
		t.Fatal("own successful whole Finish and consumer drain retained claim")
	}
}

type claimAfterFinishStore struct {
	nativetool.Repository
	target nativetool.ID
	after  func()
}

type claimHeldObservationStore struct {
	nativetool.Repository
	entered chan struct{}
	release chan struct{}
}

func (s *claimHeldObservationStore) OpenObservedStream(ctx context.Context, receipt nativetool.StartReceipt, binding nativetool.Binding, lifetime time.Duration, now time.Time) (nativetool.StreamAuthority, error) {
	close(s.entered)
	select {
	case <-s.release:
	case <-ctx.Done():
		return nativetool.StreamAuthority{}, ctx.Err()
	}
	return s.Repository.OpenObservedStream(ctx, receipt, binding, lifetime, now)
}

func (s *claimAfterFinishStore) Finish(ctx context.Context, id nativetool.ID, input nativetool.TerminalInput) error {
	err := s.Repository.Finish(ctx, id, input)
	if err == nil && id == s.target {
		s.after()
	}
	return err
}

type claimHeldCloseBody struct {
	io.ReadCloser
	closes                     atomic.Int32
	entered, release, returned chan struct{}
}

func (b *claimHeldCloseBody) Close() error {
	if b.closes.Add(1) == 1 {
		defer close(b.returned)
		close(b.entered)
		<-b.release
	}
	return b.ReadCloser.Close()
}

// Catches refund/work completion before the real Close consumer returns,
// including an AfterFunc callback already running when its stop returns false.
func TestNativeClaimWaitsForRealResponseCleanup(t *testing.T) {
	for _, bodyKind := range []string{"response", "input"} {
		for _, cleanup := range []string{"not_started", "running", "returned"} {
			t.Run(bodyKind+"/"+cleanup, func(t *testing.T) {
				f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
				f.handshake(t)
				e := f.executor.(*executor)
				request := f.captured(t, "POST", `{"jsonrpc":"2.0","id":700,"method":"ping"}`, "synthetic-session")
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				response := wireResponse(`{"jsonrpc":"2.0","id":700,"result":{}}`, false)
				body := &claimHeldCloseBody{ReadCloser: response.Body, entered: make(chan struct{}), release: make(chan struct{}), returned: make(chan struct{})}
				if bodyKind == "response" {
					response.Body = body
				} else {
					body.ReadCloser = request.request.Body
					request.request.Body = body
				}
				var releaseOnce sync.Once
				release := func() { releaseOnce.Do(func() { close(body.release) }) }
				if cleanup == "returned" {
					release()
				}
				f.transport.handler = func(*http.Request, []byte) (*http.Response, error) { return response, nil }
				if cleanup != "not_started" {
					e.dependencies.Store = &claimAfterFinishStore{Repository: e.dependencies.Store, target: request.id, after: func() {
						cancel()
						<-body.entered
						if cleanup == "returned" {
							<-body.returned
						}
					}}
				}
				done := make(chan Terminal, 1)
				go func() { done <- e.Serve(ctx, request, httptest.NewRecorder()) }()
				defer func() {
					release()
					select {
					case <-body.returned:
					case <-time.After(time.Second):
						t.Error("body cleanup worker did not return")
					}
					select {
					case <-done:
					case <-time.After(time.Second):
						t.Error("real consumer did not join after explicit release")
					}
				}()
				select {
				case <-body.entered:
				case <-time.After(time.Second):
					t.Fatal("body Close did not enter")
				}
				facts := readFreshGETFacts(t, f, request.id)
				if facts.OperationState != "completed" || facts.AuditOutcome != "completed" || facts.AuditKnown != 1 {
					t.Fatal("held cleanup lacks real terminal physical proof")
				}
				if cleanup == "returned" {
					select {
					case terminal := <-done:
						done <- terminal
					case <-time.After(time.Second):
						t.Fatal("already returned cleanup blocked Serve")
					}
					if nativeClaimLifetimeFacts(t, f).Claims != 0 {
						t.Error("returned body retained claim")
					}
					return
				}
				select {
				case terminal := <-done:
					done <- terminal
					t.Error("Serve/work drained before its running response Close returned")
				case <-time.After(20 * time.Millisecond):
				}
				e.mu.Lock()
				token := e.claimed[request.id]
				held := token != nil && token.ownerKey != 0 && token.sessionKey != 0 && token.ownerKey != token.sessionKey && token.physicalClass == nativeClaimOwnedOrdinary && token.physicalTerminal && !token.consumerDone && e.pending == 1
				e.mu.Unlock()
				if !held {
					t.Error("terminal SQL refunded the claim while a real body consumer remained")
				}
			})
		}
	}
}

func TestNativeClaimCloseTimeoutKeepsRealLiveConsumer(t *testing.T) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	e := f.executor.(*executor)
	request := f.captured(t, "POST", `{"jsonrpc":"2.0","id":701,"method":"ping"}`, "synthetic-session")
	response := wireResponse(`{"jsonrpc":"2.0","id":701,"result":{}}`, false)
	body := &claimHeldCloseBody{ReadCloser: response.Body, entered: make(chan struct{}), release: make(chan struct{}), returned: make(chan struct{})}
	response.Body = body
	f.transport.handler = func(*http.Request, []byte) (*http.Response, error) { return response, nil }
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(body.release) }) }
	done := make(chan Terminal, 1)
	go func() { done <- e.Serve(context.Background(), request, httptest.NewRecorder()) }()
	defer func() {
		release()
		<-body.returned
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("live consumer failed to drain")
		}
	}()
	select {
	case <-body.entered:
	case <-time.After(time.Second):
		t.Fatal("body cleanup missing")
	}
	e.mu.Lock()
	h := nativeClaimHandle{id: request.id, token: e.claimed[request.id]}
	e.mu.Unlock()
	closeCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err := e.Close(closeCtx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("Close did not report actual held consumer", err)
	}
	e.mu.Lock()
	retained := h.token != nil && e.claimed[request.id] == h.token && h.token.physicalTerminal && !h.token.consumerDone
	e.mu.Unlock()
	if !retained {
		t.Fatal("Close timeout discarded a live consumer token")
	}
	var notes sync.WaitGroup
	for i := 0; i < 8; i++ {
		notes.Add(1)
		go func() { defer notes.Done(); e.notePhysicalTerminal(h) }()
	}
	closed := make(chan error, 1)
	go func() { closed <- e.Close(context.Background()) }()
	release()
	select {
	case terminal := <-done:
		done <- terminal
	case <-time.After(time.Second):
		t.Fatal("Serve did not join")
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not join")
	}
	notes.Wait()
	e.drainClaimConsumer(h)
	if facts := nativeClaimLifetimeFacts(t, f); facts.Claims != 0 || facts.Budget != 0 {
		t.Fatal("collection/Close duplicate refund", facts)
	}
}

func TestNativeClaimOrdinarySSEAndFreshGETCompletion(t *testing.T) {
	for _, kind := range []string{"ping", "fresh_get"} {
		t.Run(kind, func(t *testing.T) {
			f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
			f.handshake(t)
			method, body := "POST", `{"jsonrpc":"2.0","id":702,"method":"ping"}`
			wire := "data: {\"jsonrpc\":\"2.0\",\"id\":702,\"result\":{}}\n\n"
			if kind == "fresh_get" {
				method, body = "GET", ""
				wire = ": comment only\n\n"
			}
			f.transport.handler = func(*http.Request, []byte) (*http.Response, error) { return wireResponse(wire, true), nil }
			request := f.captured(t, method, body, "synthetic-session")
			if result := f.executor.Serve(context.Background(), request, httptest.NewRecorder()); result.Outcome != OutcomeCompleted {
				t.Fatal(result)
			}
			facts := readFreshGETFacts(t, f, request.id)
			if facts.AuditOutcome != "completed" || facts.AuditKnown != 1 || nativeClaimLifetimeFacts(t, f).Claims != 0 {
				t.Fatal("nonobserved whole-Finish retained physical claim")
			}
		})
	}
}

func TestNativeClaimReplyIsIndependentOfObservedParent(t *testing.T) {
	f := newNativeFixtureStoreLifetime(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, time.Minute)
	f.handshake(t)
	e := f.executor.(*executor)
	f.transport.handler = func(r *http.Request, _ []byte) (*http.Response, error) {
		if r.Method == "GET" {
			return wireResponse("data: {\"jsonrpc\":\"2.0\",\"id\":17,\"method\":\"ping\"}\n\n", true), nil
		}
		return &http.Response{StatusCode: 202, Header: http.Header{}, Body: http.NoBody}, nil
	}
	parent := f.captured(t, "GET", "", "synthetic-session")
	if result := e.Serve(context.Background(), parent, httptest.NewRecorder()); result.Outcome != OutcomeCompleted {
		t.Fatal(result)
	}
	e.mu.Lock()
	parentClaim := e.claimed[parent.id]
	valid := parentClaim == nil
	for _, s := range e.scopes[f.capture.Key()].sessions {
		s.mu.Lock()
		valid = valid && s.parents["17"] != nil && s.streams[parent.id] != nil && s.streams[parent.id].observed
		s.mu.Unlock()
	}
	e.mu.Unlock()
	if !valid {
		t.Fatal("physical collection changed independent observed callback lifetime")
	}
	child := f.captured(t, "POST", `{"jsonrpc":"2.0","id":17,"result":{}}`, "synthetic-session")
	if result := e.Serve(context.Background(), child, httptest.NewRecorder()); result.Outcome != OutcomeCompleted {
		t.Fatal(result)
	}
	requireNativeClaimAfterDrain(t, e, child.id, false)
	requireNativeClaimAfterDrain(t, e, parent.id, false)
}

func TestNativeClaimObservedClassificationPrecedesClosureRace(t *testing.T) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	e := f.executor.(*executor)
	held := &claimHeldObservationStore{Repository: e.dependencies.Store, entered: make(chan struct{}), release: make(chan struct{})}
	e.dependencies.Store = held
	f.transport.handler = func(*http.Request, []byte) (*http.Response, error) {
		return wireResponse("data: {\"jsonrpc\":\"2.0\",\"id\":17,\"method\":\"ping\"}\n\n", true), nil
	}
	request := f.captured(t, "GET", "", "synthetic-session")
	done := make(chan Terminal, 1)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(held.release) }) }
	go func() { done <- e.Serve(context.Background(), request, httptest.NewRecorder()) }()
	defer func() {
		release()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("observation race did not drain")
		}
	}()
	select {
	case <-held.entered:
	case <-time.After(time.Second):
		t.Fatal("real observation issuance boundary missing")
	}
	e.mu.Lock()
	token := e.claimed[request.id]
	classified := token != nil && token.physicalClass == nativeClaimOwnedObserved && token.ownerKey != 0 && token.sessionKey != 0 && !token.consumerDone && !token.physicalTerminal
	e.mu.Unlock()
	if !classified {
		t.Fatal("observed issuance could race closure while classified ordinary")
	}
	scope, err := held.Repository.GetScope(context.Background(), f.capture)
	if err != nil {
		t.Fatal(err)
	}
	if err = held.Repository.Revoke(context.Background(), f.capture, scope.Revision, "revoked", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	release()
	select {
	case result := <-done:
		done <- result
		if result.Outcome == OutcomeCompleted {
			t.Fatal("revoked observation succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("revoked observation did not return")
	}
	requireNativeClaimAfterDrain(t, e, request.id, true)
}

func TestNativeClaimOwnerSessionKeysNeverReuse(t *testing.T) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	e := f.executor.(*executor)
	keys := func() (uint64, uint64) {
		e.mu.Lock()
		defer e.mu.Unlock()
		o := e.scopes[f.capture.Key()]
		for _, s := range o.sessions {
			return o.claimKey, s.claimKey
		}
		t.Fatal("real session missing")
		return 0, 0
	}
	o1, s1 := keys()
	f.transport.handler = func(*http.Request, []byte) (*http.Response, error) {
		return &http.Response{StatusCode: 202, Header: http.Header{}, Body: http.NoBody}, nil
	}
	request := f.captured(t, "DELETE", "", "synthetic-session")
	if result := e.Serve(context.Background(), request, httptest.NewRecorder()); result.Outcome != OutcomeCompleted {
		t.Fatal(result)
	}
	f.transport.handler = nil
	f.handshake(t)
	o2, s2 := keys()
	if o1 == 0 || s1 == 0 || o2 != o1 || s2 <= s1 {
		t.Fatal("same owner reused a session cleanup key")
	}
	if err := e.Revoke(context.Background(), f.capture, RevocationOwnerChanged); err != nil {
		t.Fatal(err)
	}
	f.handshake(t)
	o3, s3 := keys()
	if o3 <= s2 || s3 <= o3 {
		t.Fatal("new actual owner reused cleanup identities")
	}
}

func TestNativeClaimExactTokenCollectionAndOldReceipt(t *testing.T) {
	for _, drainFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("drain_first_%t", drainFirst), func(t *testing.T) {
			f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
			f.handshake(t)
			e := f.executor.(*executor)
			receipt, request, plan := prepareRealReceipt(t, f)
			before := f.transport.sends()
			sent := e.sendCommitted(context.Background(), request, plan, receipt)
			if sent.err != nil || sent.claim.token == nil || sent.claim.id != receipt.OperationID() {
				t.Fatal("real receipt did not obtain exclusive handle")
			}
			if sent.claim.token.ownerKey != 0 || sent.claim.token.sessionKey != 0 || sent.claim.token.physicalClass != nativeClaimUnbound {
				t.Fatal("borrowed real receipt acquired Serve ownership")
			}
			responseBytes, err := io.ReadAll(sent.response.Body)
			if err != nil {
				t.Fatal(err)
			}
			_ = sent.response.Body.Close()
			duplicate := e.sendCommitted(context.Background(), request, plan, receipt)
			if !errors.Is(duplicate.err, nativetool.ErrReplay) || duplicate.claim.token != nil {
				t.Fatal("duplicate borrowed existing token")
			}
			if drainFirst {
				e.drainClaimConsumer(sent.claim)
			}
			if nativeClaimLifetimeFacts(t, f).Claims != 1 {
				t.Fatal("consumer drain without physical proof refunded")
			}
			if err := e.dependencies.Store.Finish(context.Background(), receipt.OperationID(), nativetool.TerminalInput{Outcome: "completed", Reason: "completed", BytesIn: int64(len(responseBytes)), BytesOut: request.ContentLength, ObservedAt: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			e.notePhysicalTerminal(sent.claim)
			if !drainFirst && nativeClaimLifetimeFacts(t, f).Claims != 1 {
				t.Fatal("physical proof without consumer drain refunded")
			}
			e.drainClaimConsumer(sent.claim)
			if nativeClaimLifetimeFacts(t, f).Claims != 0 {
				t.Fatal("terminal+drained token retained")
			}
			oldAudit := freshGETRow(t, f, `SELECT * FROM native_tool_deliveries WHERE operation_id=?`, receipt.OperationID())
			oldOnce := freshGETRow(t, f, `SELECT * FROM native_tool_once WHERE operation_id=?`, receipt.OperationID())
			if err := e.dependencies.Store.VerifyStartReceipt(context.Background(), receipt, time.Now().UTC()); err == nil {
				t.Fatal("terminal physical audit still granted send")
			}
			if _, err := e.dependencies.Store.Start(context.Background(), receipt.OperationID(), "new-attempt", 1, time.Now().UTC()); err == nil {
				t.Fatal("collected receipt acquired a new Start")
			}
			later := e.sendCommitted(context.Background(), request, plan, receipt)
			if later.err == nil || later.phase != nativeSendFirstClaimUnsent || later.claim.token == nil || later.claim.token == sent.claim.token || f.transport.sends() != before+1 {
				t.Fatal("collected receipt replay sent or reused generation")
			}
			e.notePhysicalTerminal(sent.claim)
			e.drainClaimConsumer(sent.claim)
			e.drainClaimConsumer(later.claim)
			if facts := nativeClaimLifetimeFacts(t, f); facts.Claims != 1 || facts.Budget != facts.SessionRetained+128 {
				t.Fatal("old-token ABA event collected a new token")
			}
			if !reflect.DeepEqual(oldAudit, freshGETRow(t, f, `SELECT * FROM native_tool_deliveries WHERE operation_id=?`, receipt.OperationID())) || !reflect.DeepEqual(oldOnce, freshGETRow(t, f, `SELECT * FROM native_tool_once WHERE operation_id=?`, receipt.OperationID())) {
				t.Fatal("replayed receipt changed physical history")
			}
			if err := e.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			e.notePhysicalTerminal(sent.claim)
			e.drainClaimConsumer(later.claim)
			if facts := nativeClaimLifetimeFacts(t, f); facts.Claims != 0 || facts.Budget != 0 {
				t.Fatal("duplicate events after Close refunded twice", facts)
			}
		})
	}
}

func TestNativeClaimKeysExhaustBeforeDurableAdmission(t *testing.T) {
	for _, ownerState := range []string{"absent", "live", "revoked"} {
		t.Run(ownerState, func(t *testing.T) {
			f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
			e := f.executor.(*executor)
			if ownerState != "absent" {
				f.handshake(t)
			}
			if ownerState == "revoked" {
				if err := e.Revoke(context.Background(), f.capture, RevocationOwnerChanged); err != nil {
					t.Fatal(err)
				}
			}
			// Exercise the checked counter boundary without allocating billions
			// of owners, and without altering any Store authority or claim map.
			e.mu.Lock()
			e.lastClaimKey = ^uint64(0) - 1
			e.mu.Unlock()
			count := func() [2]int {
				var n [2]int
				if err := f.db.QueryRow(`SELECT (SELECT COUNT(*) FROM native_tool_scopes),(SELECT COUNT(*) FROM native_tool_sessions)`).Scan(&n[0], &n[1]); err != nil {
					t.Fatal(err)
				}
				return n
			}
			body := `{"jsonrpc":"2.0","id":900,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`
			request := f.captured(t, "POST", body, "")
			defer request.lease.Release()
			plan, _ := request.lease.Plan().ClientToolPlan()
			r, err := parseRequest([]byte(body), e.limits)
			if err != nil {
				t.Fatal(err)
			}
			before := count()
			s, err := e.admitSession(context.Background(), request, r, plan)
			if ownerState == "live" {
				if err != nil || s == nil || s.claimKey != ^uint64(0) || s.owner.claimKey == 0 || s.owner.claimKey == s.claimKey {
					t.Fatal("last session key not allocated independently", err)
				}
				before = count()
				if _, err = e.admitSession(context.Background(), request, r, plan); !errors.Is(err, errBudget) {
					t.Fatal("exhausted session key wrapped", err)
				}
			} else if !errors.Is(err, errBudget) || s != nil {
				t.Fatal("two-key admission accepted only one remaining key", err)
			}
			if after := count(); before != after {
				t.Fatal("key exhaustion mutated durable admission", before, after)
			}
		})
	}
}

func nativeClaimLifetimeFacts(t *testing.T, f *nativeFixture) nativeClaimLifetimeSnapshot {
	t.Helper()
	e := f.executor.(*executor)
	var out nativeClaimLifetimeSnapshot
	e.mu.Lock()
	out.Claims, out.Pending, out.Closed = len(e.claimed), e.pending, e.closed
	for _, owner := range e.scopes {
		out.Sessions += len(owner.sessions)
		for _, s := range owner.sessions {
			s.mu.Lock()
			out.Active += len(s.active)
			out.Parents += len(s.parents)
			out.Streams += len(s.streams)
			out.Pages += len(s.pages)
			out.SessionRetained += s.retained
			s.mu.Unlock()
		}
	}
	e.mu.Unlock()
	e.budget.mu.Lock()
	out.Budget = e.budget.used
	e.budget.mu.Unlock()
	err := f.db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM native_tool_once),
		(SELECT COUNT(*) FROM native_tool_deliveries),
		(SELECT COUNT(*) FROM native_tool_deliveries WHERE completed_at_unix_ms IS NOT NULL),
		(SELECT COUNT(*) FROM native_tool_deliveries WHERE completed_at_unix_ms IS NOT NULL AND byte_counts_known=1),
		(SELECT COUNT(*) FROM native_tool_operations WHERE state='completed'),
		(SELECT COUNT(*) FROM native_tool_sessions),
		(SELECT COUNT(*) FROM native_tool_sessions WHERE state='ready'),
		(SELECT COUNT(*) FROM native_tool_sessions WHERE state='revoked')`).Scan(&out.Once, &out.Audits, &out.TerminalAudits, &out.KnownAudits, &out.Completed, &out.SQLSessions, &out.ReadySessions, &out.RevokedSessions)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := f.store.NativeToolRepository().GetScope(context.Background(), f.capture)
	if err != nil {
		t.Fatal(err)
	}
	out.Scope, out.Barrier = scope.State, scope.ReplayBarrier
	return out
}

func TestCompletedNativeRequestsDoNotRetainLifetimeClaims(t *testing.T) {
	limits := DefaultLimits()
	limits.BufferBytes = 64 << 10
	limits.RequestBytes, limits.ResultBytes, limits.EventBytes = 8<<10, 8<<10, 8<<10
	limits.CatalogPageBytes, limits.CatalogBytes = 8<<10, 16<<10
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, limits)
	e := f.executor.(*executor)
	now := time.Now().UTC().Truncate(time.Millisecond)
	e.dependencies.Clock = func() time.Time { return now } // Immutable before any Serve.
	f.handshake(t)
	baseline := nativeClaimLifetimeFacts(t, f)
	if baseline.Once != 3 || baseline.Audits != 3 || baseline.TerminalAudits != 3 || baseline.KnownAudits != 3 || baseline.Completed != 3 || baseline.ReadySessions != 1 || baseline.Scope != "active" || baseline.Barrier || baseline.Pending != 0 || baseline.Active != 0 || f.transport.sends() != 3 {
		t.Fatalf("64KiB real handshake preflight did not establish a usable baseline: %+v", baseline)
	}
	t.Logf("64KiB handshake baseline %+v", baseline)
	respond := func(r *http.Request, body []byte) (*http.Response, error) {
		if r.Method == http.MethodDelete {
			return &http.Response{StatusCode: 202, Header: http.Header{}, Body: http.NoBody}, nil
		}
		request, err := parseRequest(body, limits)
		if err != nil {
			return nil, err
		}
		return wireResponse(`{"jsonrpc":"2.0","id":`+string(request.id)+`,"result":{}}`, false), nil
	}
	f.transport.handler = respond
	ping := func(rpcID int) {
		t.Helper()
		request := f.captured(t, "POST", fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"ping"}`, rpcID), "synthetic-session")
		before := f.transport.sends()
		terminal := e.Serve(context.Background(), request, httptest.NewRecorder())
		facts := readFreshGETFacts(t, f, request.id)
		var once, audits int
		if err := f.db.QueryRow(`SELECT (SELECT COUNT(*) FROM native_tool_once WHERE operation_id=?),(SELECT COUNT(*) FROM native_tool_deliveries WHERE operation_id=?)`, request.id, request.id).Scan(&once, &audits); err != nil {
			t.Fatal(err)
		}
		if terminal.Outcome != OutcomeCompleted || facts.OperationState != "completed" || facts.AuditOutcome != "completed" || facts.AuditKnown != 1 || !facts.BytesIn.Valid || !facts.BytesOut.Valid || facts.BytesIn.Int64 != facts.AuditBytesIn || facts.BytesOut.Int64 != facts.AuditBytesOut || once != 1 || audits != 1 || f.transport.sends() != before+1 {
			t.Fatalf("ping%d did not complete one real once/known physical audit/send", rpcID)
		}
	}
	for rpcID := 100; rpcID < 116; rpcID++ {
		ping(rpcID)
		observed := nativeClaimLifetimeFacts(t, f)
		if observed.Pending != 0 || observed.Active != 0 || observed.Parents != 0 || observed.Streams != 0 || observed.SessionRetained != baseline.SessionRetained {
			t.Fatalf("ping%d did not unwind request resources: %+v", rpcID, observed)
		}
		t.Logf("completed ping%d claims=%d charged=%d retainedSession=%d once=%d terminalAudit=%d knownAudit=%d transport=%d", rpcID, observed.Claims, observed.Budget, observed.SessionRetained, observed.Once, observed.TerminalAudits, observed.KnownAudits, f.transport.sends())
	}
	afterPings := nativeClaimLifetimeFacts(t, f)
	deleteRequest := f.captured(t, "DELETE", "", "synthetic-session")
	if terminal := e.Serve(context.Background(), deleteRequest, httptest.NewRecorder()); terminal.Outcome != OutcomeCompleted {
		t.Fatal("real DELETE failed", terminal)
	}
	drained := nativeClaimLifetimeFacts(t, f)
	if drained.Closed || drained.Sessions != 0 || drained.Pending != 0 || drained.Active != 0 || drained.Parents != 0 || drained.Streams != 0 || drained.Pages != 0 || drained.SessionRetained != 0 || drained.Once != 20 || drained.Audits != 20 || drained.TerminalAudits != 20 || drained.KnownAudits != 20 || drained.Completed != 20 || drained.RevokedSessions != 1 || f.transport.sends() != 20 {
		t.Fatalf("real DELETE did not drain session while preserving durable history: %+v", drained)
	}
	t.Logf("after real DELETE, executor remains open: %+v", drained)
	f.transport.handler = nil
	f.handshake(t)
	f.transport.handler = respond
	ping(200)
	renewed := nativeClaimLifetimeFacts(t, f)
	if renewed.Closed || renewed.Sessions != 1 || renewed.ReadySessions != 1 || renewed.Once != 24 || renewed.KnownAudits != 24 || renewed.Completed != 24 || f.transport.sends() != 24 {
		t.Fatalf("new real session/request control failed: %+v", renewed)
	}
	t.Logf("new session and ping200 control: %+v", renewed)
	if err := e.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	closed := nativeClaimLifetimeFacts(t, f)
	if !closed.Closed || closed.Claims != 0 || closed.Budget != 0 || closed.Once != 24 || closed.KnownAudits != 24 {
		t.Fatalf("final Close control failed: %+v", closed)
	}
	t.Logf("final executor.Close control: %+v", closed)
	// Assert only after all bounded accounting/drain/control evidence is retained.
	if afterPings.Claims > baseline.Claims || afterPings.Budget > baseline.Budget {
		t.Errorf("16 durably completed pings retain executor-lifetime claims: deltaClaims=%d deltaChargedBytes=%d", afterPings.Claims-baseline.Claims, afterPings.Budget-baseline.Budget)
	}
	if drained.Claims != 0 || drained.Budget != 0 {
		t.Errorf("completed session drain retained claims=%d chargedBytes=%d with no live request/session consumers", drained.Claims, drained.Budget)
	}
}
