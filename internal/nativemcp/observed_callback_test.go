package nativemcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime/pprof"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

func TestNativeGETCallbackKeepsObservedAuthorityAfterEOF(t *testing.T) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
		if r.Method == "GET" {
			return wireResponse("data: {\"jsonrpc\":\"2.0\",\"id\":\"get-ping\",\"method\":\"ping\"}\n\n", true), nil
		}
		return &http.Response{StatusCode: 202, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	get := f.captured(t, "GET", "", "synthetic-session")
	got := f.executor.Serve(context.Background(), get, httptest.NewRecorder())
	if got.Outcome != OutcomeCompleted {
		t.Fatalf("authenticated GET callback rejected before clean EOF: %+v", got)
	}
	op, err := f.store.NativeToolRepository().Get(context.Background(), get.id)
	if err != nil || op.State != "completed" {
		t.Fatalf("GET did not close: %+v %v", op, err)
	}
	child := f.captured(t, "POST", `{"jsonrpc":"2.0","id":"get-ping","result":{}}`, "synthetic-session")
	done := make(chan Terminal, 1)
	go func() { done <- f.executor.Serve(context.Background(), child, httptest.NewRecorder()) }()
	f.approve(t, child.id)
	if got := <-done; got.Outcome != OutcomeCompleted {
		t.Fatalf("late approved callback: %+v", got)
	}
	requireNativeClaimAfterDrain(t, f.executor.(*executor), get.id, false)
	requireNativeClaimAfterDrain(t, f.executor.(*executor), child.id, false)
	var parent string
	if err := f.db.QueryRow(`SELECT parent_operation_id FROM native_tool_bindings WHERE operation_id=?`, child.id).Scan(&parent); err != nil || parent != string(get.id) {
		t.Fatalf("wrong callback owner %q %v", parent, err)
	}
	var consumed, once int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM native_tool_approvals WHERE operation_id=? AND consumed_at_unix_ms IS NOT NULL`, child.id).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM native_tool_once WHERE operation_id=?`, child.id).Scan(&once); err != nil {
		t.Fatal(err)
	}
	if consumed != 1 || once != 1 {
		t.Fatalf("callback consumption approval=%d once=%d", consumed, once)
	}
}

func TestNativeGETURLCompletionOutlivesHTTP(t *testing.T) {
	limits := DefaultLimits()
	limits.Pending = 2
	f := newNativeFixtureMode(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, limits)
	f.handshake(t)
	wire := `{"jsonrpc":"2.0","id":18,"method":"elicitation/create","params":{"mode":"url","message":"Synthetic consent","url":"https://example.invalid/synthetic","elicitationId":"synthetic-elicitation"}}`
	f.transport.handler = func(r *http.Request, _ []byte) (*http.Response, error) {
		if r.Method == "GET" {
			return wireResponse("data: "+wire+"\n\n", true), nil
		}
		return &http.Response{StatusCode: 202, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	get := f.captured(t, "GET", "", "synthetic-session")
	if got := f.executor.Serve(context.Background(), get, httptest.NewRecorder()); got.Outcome != OutcomeCompleted {
		t.Fatal(got)
	}
	if got, _ := f.serve(t, `{"jsonrpc":"2.0","id":18,"result":{"action":"accept"}}`, "synthetic-session"); got.Outcome != OutcomeCompleted {
		t.Fatal(got)
	}
	e := f.executor.(*executor)
	e.mu.Lock()
	owner := e.scopes[f.capture.Key()]
	var s *session
	for _, found := range owner.sessions {
		s = found
	}
	e.mu.Unlock()
	s.mu.Lock()
	_, pending := s.elicitations["synthetic-elicitation"]
	s.mu.Unlock()
	if !pending {
		t.Fatal("HTTP EOF completed URL elicitation")
	}
	wire = `{"jsonrpc":"2.0","method":"notifications/elicitation/complete","params":{"elicitationId":"synthetic-elicitation"}}`
	complete := f.captured(t, "GET", "", "synthetic-session")
	if got := f.executor.Serve(context.Background(), complete, httptest.NewRecorder()); got.Outcome != OutcomeCompleted {
		t.Fatal(got)
	}
	s.mu.Lock()
	_, pending = s.elicitations["synthetic-elicitation"]
	s.mu.Unlock()
	if pending {
		t.Fatal("accepted completion retained URL obligation")
	}
}

func TestNativeGETCallbackPendingRevocation(t *testing.T) {
	for _, reason := range []RevocationReason{RevocationPermissionOff, RevocationCaptureStopped, RevocationAssignmentApplied, RevocationOwnerChanged, RevocationShutdown} {
		t.Run(string(reason), func(t *testing.T) {
			f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
			f.handshake(t)
			f.transport.handler = func(*http.Request, []byte) (*http.Response, error) {
				return wireResponse("data: {\"jsonrpc\":\"2.0\",\"id\":\"revoked\",\"method\":\"ping\"}\n\n", true), nil
			}
			get := f.captured(t, "GET", "", "synthetic-session")
			if got := f.executor.Serve(context.Background(), get, httptest.NewRecorder()); got.Outcome != OutcomeCompleted {
				t.Fatal(got)
			}
			child := f.captured(t, "POST", `{"jsonrpc":"2.0","id":"revoked","result":{}}`, "synthetic-session")
			done := make(chan Terminal, 1)
			go func() { done <- f.executor.Serve(context.Background(), child, httptest.NewRecorder()) }()
			deadline := time.Now().Add(time.Second)
			var approval nativetool.Approval
			for {
				op, err := f.store.NativeToolRepository().Get(context.Background(), child.id)
				if err == nil && op.ApprovalID != nil {
					approval, err = f.store.NativeToolRepository().GetApproval(context.Background(), *op.ApprovalID)
					if err != nil {
						t.Fatal(err)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("callback question absent")
				}
				time.Sleep(time.Millisecond)
			}
			before := f.transport.sends()
			if err := f.executor.Revoke(context.Background(), f.capture, reason); err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.NativeToolRepository().DecideApproval(context.Background(), nativetool.DecisionInput{ApprovalID: approval.ID, ExpectedRevision: approval.Revision, Decision: nativetool.DecisionAllowOnce, IdempotencyKey: "late-revoked-callback", ObservedAt: time.Now().UTC()}); err == nil {
				t.Fatal("revoked callback allowed")
			}
			select {
			case got := <-done:
				if got.Outcome == OutcomeCompleted || f.transport.sends() != before {
					t.Fatal("revoked callback sent", got)
				}
			case <-time.After(time.Second):
				t.Fatal("revoked callback wait retained")
			}
		})
	}
}

func TestNativeGETCallbackDenyReleasesParent(t *testing.T) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	f.transport.handler = func(*http.Request, []byte) (*http.Response, error) {
		return wireResponse("data: {\"jsonrpc\":\"2.0\",\"id\":\"deny\",\"method\":\"ping\"}\n\n", true), nil
	}
	get := f.captured(t, "GET", "", "synthetic-session")
	if got := f.executor.Serve(context.Background(), get, httptest.NewRecorder()); got.Outcome != OutcomeCompleted {
		t.Fatal(got)
	}
	child := f.captured(t, "POST", `{"jsonrpc":"2.0","id":"deny","result":{}}`, "synthetic-session")
	done := make(chan Terminal, 1)
	go func() { done <- f.executor.Serve(context.Background(), child, httptest.NewRecorder()) }()
	deadline := time.Now().Add(time.Second)
	var approval nativetool.Approval
	for {
		op, err := f.store.NativeToolRepository().Get(context.Background(), child.id)
		if err == nil && op.ApprovalID != nil {
			approval, err = f.store.NativeToolRepository().GetApproval(context.Background(), *op.ApprovalID)
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("callback question absent")
		}
		time.Sleep(time.Millisecond)
	}
	before := f.transport.sends()
	if _, err := f.store.NativeToolRepository().DecideApproval(context.Background(), nativetool.DecisionInput{ApprovalID: approval.ID, ExpectedRevision: approval.Revision, Decision: nativetool.DecisionDeny, IdempotencyKey: "deny-observed-callback", ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if got := <-done; got.Outcome == OutcomeCompleted || f.transport.sends() != before {
		t.Fatal("denied callback sent", got)
	}
	var once, audits int
	if err := f.db.QueryRow(`SELECT (SELECT COUNT(*) FROM native_tool_once WHERE operation_id=?),(SELECT COUNT(*) FROM native_tool_deliveries WHERE operation_id=?)`, child.id, child.id).Scan(&once, &audits); err != nil || once != 0 || audits != 0 {
		t.Fatal("denied callback created a Start/physical audit", err)
	}
	requireNativeClaimAfterDrain(t, f.executor.(*executor), child.id, false)
	requireNativeClaimAfterDrain(t, f.executor.(*executor), get.id, false)
	e := f.executor.(*executor)
	e.parentsMu.Lock()
	pending := e.parentPending
	e.parentsMu.Unlock()
	if pending != 0 {
		t.Fatalf("denied callback retained %d parent registrations", pending)
	}
}

func TestNativeGETCallbackDoesNotBorrowConcurrentToolsCall(t *testing.T) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
		if strings.Contains(string(body), `"method":"tools/call"`) {
			reader, writer := io.Pipe()
			go func() {
				defer writer.Close()
				close(entered)
				_, _ = io.WriteString(writer, ": waiting\n\n")
				<-release
				_, _ = io.WriteString(writer, "data: {\"jsonrpc\":\"2.0\",\"id\":3,\"result\":{\"content\":[]}}\n\n")
			}()
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: reader}, nil
		}
		if r.Method == "GET" {
			return wireResponse("data: {\"jsonrpc\":\"2.0\",\"id\":\"independent\",\"method\":\"ping\"}\n\n", true), nil
		}
		return &http.Response{StatusCode: 202, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	call := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"}}}`, "synthetic-session")
	done := make(chan Terminal, 1)
	go func() { done <- f.executor.Serve(context.Background(), call, httptest.NewRecorder()) }()
	f.approve(t, call.id)
	<-entered
	get := f.captured(t, "GET", "", "synthetic-session")
	if got := f.executor.Serve(context.Background(), get, httptest.NewRecorder()); got.Outcome != OutcomeCompleted {
		t.Fatal(got)
	}
	child := f.captured(t, "POST", `{"jsonrpc":"2.0","id":"independent","result":{}}`, "synthetic-session")
	replyDone := make(chan Terminal, 1)
	go func() { replyDone <- f.executor.Serve(context.Background(), child, httptest.NewRecorder()) }()
	f.approve(t, child.id)
	if got := <-replyDone; got.Outcome != OutcomeCompleted {
		t.Fatal(got)
	}
	var parent string
	if err := f.db.QueryRow(`SELECT parent_operation_id FROM native_tool_bindings WHERE operation_id=?`, child.id).Scan(&parent); err != nil || parent != string(get.id) || parent == string(call.id) {
		t.Fatal("GET borrowed concurrent call", parent, err)
	}
	once.Do(func() { close(release) })
	if got := <-done; got.Outcome != OutcomeCompleted {
		t.Fatal(got)
	}
}

func TestNativeGETCallbackFamiliesAndTransportBreak(t *testing.T) {
	data, err := os.ReadFile("testdata/codex-0.160-callbacks.synthetic.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Kind           string
		Request, Reply json.RawMessage
	}
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		for _, broken := range []bool{false, true} {
			t.Run(fixture.Kind+map[bool]string{false: "/EOF", true: "/break"}[broken], func(t *testing.T) {
				f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
				for _, b := range []string{`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{"elicitation":{"form":{},"url":{}},"extensions":{"openai/form":{},"openai/elicitation":{"form":{},"userVerification":{}}}},"clientInfo":{"name":"codex-mcp-client","version":"0.160.0","title":"Codex"}}}`, `{"jsonrpc":"2.0","method":"notifications/initialized"}`} {
					sid := "synthetic-session"
					if strings.Contains(b, `"initialize"`) {
						sid = ""
					}
					got, _ := f.serve(t, b, sid)
					if got.Outcome != OutcomeCompleted {
						t.Fatalf("initialize %+v", got)
					}
				}
				f.transport.handler = func(r *http.Request, _ []byte) (*http.Response, error) {
					if r.Method == "GET" {
						wire := "data: " + string(fixture.Request) + "\n\n"
						response := wireResponse(wire, true)
						if broken {
							response.Body = &callbackBreakBody{Reader: strings.NewReader(wire)}
						}
						return response, nil
					}
					return &http.Response{StatusCode: 202, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
				}
				get := f.captured(t, "GET", "", "synthetic-session")
				got := f.executor.Serve(context.Background(), get, httptest.NewRecorder())
				if !broken && got.Outcome != OutcomeCompleted || broken && got.Outcome != OutcomeFailed {
					t.Fatalf("listening attempt %+v", got)
				}
				e := f.executor.(*executor)
				e.mu.Lock()
				owner := e.scopes[f.capture.Key()]
				var s *session
				for _, found := range owner.sessions {
					s = found
				}
				e.mu.Unlock()
				s.mu.Lock()
				stream := s.streams[get.id]
				s.mu.Unlock()
				if stream == nil {
					t.Fatal("GET callback stream prematurely retired")
				}
				if err := f.store.NativeToolRepository().VerifyStartReceipt(context.Background(), stream.receipt, time.Now().UTC()); err == nil {
					t.Fatal("finished GET receipt still grants send")
				}
				child := f.captured(t, "POST", string(fixture.Reply), "synthetic-session")
				done := make(chan Terminal, 1)
				go func() { done <- f.executor.Serve(context.Background(), child, httptest.NewRecorder()) }()
				f.approve(t, child.id)
				if got := <-done; got.Outcome != OutcomeCompleted {
					t.Fatalf("late sensitive reply %+v", got)
				}
				var generation int
				var parent string
				if err := f.db.QueryRow(`SELECT parent_operation_id,catalog_generation FROM native_tool_bindings WHERE operation_id=?`, child.id).Scan(&parent, &generation); err != nil || parent != string(get.id) || generation != 0 {
					t.Fatalf("frozen GET scope parent=%s generation=%d %v", parent, generation, err)
				}
				assertNoSensitiveRows(t, f, []string{string(fixture.Request), string(fixture.Reply), "synthetic private input", "synthetic-private-credential", "synthetic-original-login"})
				before := f.transport.sends()
				replay, _ := f.serve(t, string(fixture.Reply), "synthetic-session")
				if replay.Outcome == OutcomeCompleted || f.transport.sends() != before {
					t.Fatal("consumed callback replayed")
				}
			})
		}
	}
}

type callbackBreakBody struct{ *strings.Reader }

func (b *callbackBreakBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}
func (b *callbackBreakBody) Close() error { return nil }

func TestNativeGETCallbacksReuseCapacity(t *testing.T) {
	limits := DefaultLimits()
	limits.Pending = 2
	f := newNativeFixtureMode(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, limits)
	f.handshake(t)
	for i := 0; i < 1025; i++ {
		id := fmt.Sprintf("capacity-%d", i)
		f.transport.handler = func(r *http.Request, _ []byte) (*http.Response, error) {
			if r.Method == "GET" {
				return wireResponse(fmt.Sprintf("data: {\"jsonrpc\":\"2.0\",\"id\":%q,\"method\":\"ping\"}\n\n", id), true), nil
			}
			return &http.Response{StatusCode: 202, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		get := f.captured(t, "GET", "", "synthetic-session")
		got := f.executor.Serve(context.Background(), get, httptest.NewRecorder())
		if got.Outcome != OutcomeCompleted {
			t.Fatalf("GET %d: %+v", i, got)
		}
		got, _ = f.serve(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"result":{}}`, id), "synthetic-session")
		if got.Outcome != OutcomeCompleted {
			t.Fatalf("reply %d: %+v", i, got)
		}
	}
	before := f.transport.sends()
	replay, _ := f.serve(t, `{"jsonrpc":"2.0","id":"capacity-0","result":{}}`, "synthetic-session")
	if replay.Outcome == OutcomeCompleted || f.transport.sends() != before {
		t.Fatal("reclaimed callback replayed")
	}
}

// This observer forwards every real repository method. Publication is signaled
// only after the actual approval transaction has committed successfully.
type approvalPublicationObserver struct {
	nativetool.LifecycleAuthority
	observe func(nativetool.Approval)
}

func (observer approvalPublicationObserver) RequestApproval(ctx context.Context, operation, id nativetool.ID, now, expires time.Time) (nativetool.Approval, error) {
	approval, err := observer.LifecycleAuthority.RequestApproval(ctx, operation, id, now, expires)
	if err == nil {
		observer.observe(approval)
	}
	return approval, err
}

func TestNativeObservedReviewDenyReusesSamePOSTCapacity(t *testing.T) {
	// This is a capacity/reclamation test, not a pre-publication latency SLA.
	// Keep the real three-second decision expiry and bound the entire fixture.
	deadline, bounded := t.Deadline()
	if !bounded {
		deadline = time.Now().Add(15 * time.Minute)
	}
	// Leave teardown time within the runner's normal or race execution budget.
	ctx, cancel := context.WithDeadline(t.Context(), deadline.Add(-5*time.Second))
	defer cancel()
	published := make(chan nativetool.Approval, 1)
	f := newNativeFixtureWithApprovalObservation(t, environment.ToolPolicyReview, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, 0, authoredEgress(t, egressprofile.Direct()), toolapproval.Config{DecisionTimeout: 3 * time.Second}, func(approval nativetool.Approval) {
		select {
		case published <- approval:
		case <-ctx.Done():
		}
	})
	dumpFailure := func() {
		var dump bytes.Buffer
		_ = pprof.Lookup("goroutine").WriteTo(&dump, 2)
		t.Log(dump.String())
	}
	awaitApproval := func(id nativetool.ID, done <-chan Terminal) nativetool.Approval {
		t.Helper()
		select {
		case approval := <-published:
			if approval.OperationID != id || approval.State != "pending" || approval.ExpiresAt.Sub(approval.CreatedAt) != 3*time.Second {
				t.Fatalf("wrong durable approval for %s: %+v", id, approval)
			}
			stored, err := f.store.NativeToolRepository().GetApproval(ctx, approval.ID)
			if err != nil || stored.OperationID != id || stored.Revision != approval.Revision || stored.State != "pending" {
				t.Fatalf("publication preceded durable approval: %+v %v", stored, err)
			}
			return approval
		case terminal := <-done:
			t.Fatalf("operation %s ended before approval publication: %+v", id, terminal)
		case <-ctx.Done():
			dumpFailure()
			t.Fatalf("capacity fixture stopped awaiting approval for %s: %v", id, ctx.Err())
		}
		return nativetool.Approval{}
	}
	awaitTerminal := func(done <-chan Terminal) Terminal {
		t.Helper()
		select {
		case terminal := <-done:
			return terminal
		case <-ctx.Done():
			dumpFailure()
			t.Fatalf("capacity fixture stopped awaiting terminal: %v", ctx.Err())
		}
		return Terminal{}
	}
	f.handshake(t)
	next := make(chan struct{}, 1)
	f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
		if !strings.Contains(string(body), `"method":"tools/call"`) {
			t.Error("denied callback reached transport")
			return nil, io.ErrUnexpectedEOF
		}
		reader, writer := io.Pipe()
		go func() {
			defer writer.Close()
			for i := 0; i < 1025; i++ {
				wire := fmt.Sprintf("data: {\"jsonrpc\":\"2.0\",\"id\":%d,\"method\":\"ping\"}\n\n", 30000+i)
				if _, err := io.WriteString(writer, wire); err != nil {
					return
				}
				select {
				case <-next:
				case <-r.Context().Done():
					return
				}
			}
			_, _ = io.WriteString(writer, "data: {\"jsonrpc\":\"2.0\",\"id\":3,\"result\":{\"content\":[]}}\n\n")
		}()
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: reader}, nil
	}
	parent := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"}}}`, "synthetic-session")
	parentDone := make(chan Terminal, 1)
	go func() { parentDone <- f.executor.Serve(ctx, parent, httptest.NewRecorder()) }()
	parentApproval := awaitApproval(parent.id, parentDone)
	if _, err := f.store.NativeToolRepository().DecideApproval(ctx, nativetool.DecisionInput{ApprovalID: parentApproval.ID, ExpectedRevision: parentApproval.Revision, Decision: nativetool.DecisionAllowOnce, IdempotencyKey: "capacity-parent-allow", ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	e := f.executor.(*executor)
	e.mu.Lock()
	owner := e.scopes[f.capture.Key()]
	var s *session
	for _, found := range owner.sessions {
		s = found
	}
	e.mu.Unlock()
	for i := 0; i < 1025; i++ {
		identity := fmt.Sprint(30000 + i)
		for {
			s.mu.Lock()
			present := s.parents[identity] != nil
			s.mu.Unlock()
			if present {
				break
			}
			select {
			case got := <-parentDone:
				t.Fatalf("parent ended at denied callback %d: %+v", i+1, got)
			default:
			}
			select {
			case <-ctx.Done():
				dumpFailure()
				t.Fatalf("denied callback %d unavailable: %v", i+1, ctx.Err())
			case <-time.After(time.Millisecond):
			}
		}
		child := f.captured(t, "POST", fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{}}`, identity), "synthetic-session")
		done := make(chan Terminal, 1)
		go func() { done <- f.executor.Serve(ctx, child, httptest.NewRecorder()) }()
		approval := awaitApproval(child.id, done)
		decisionStarted := time.Now().UTC()
		if _, err := f.store.NativeToolRepository().DecideApproval(ctx, nativetool.DecisionInput{ApprovalID: approval.ID, ExpectedRevision: approval.Revision, Decision: nativetool.DecisionDeny, IdempotencyKey: "capacity-deny-" + identity, ObservedAt: decisionStarted}); err != nil {
			t.Logf("callback=%d published=%+v decisionStarted=%s decisionElapsed=%s", i+1, approval, decisionStarted.Format(time.RFC3339Nano), time.Since(decisionStarted))
			dumpFailure()
			proof, stop := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			latest, readErr := f.store.NativeToolRepository().GetApproval(proof, approval.ID)
			stop()
			t.Logf("latest approval=%+v read=%v", latest, readErr)
			t.Fatal(err)
		}
		if got := awaitTerminal(done); got.Outcome == OutcomeCompleted {
			t.Fatal("denied callback completed send")
		}
		next <- struct{}{}
	}
	if got := awaitTerminal(parentDone); got.Outcome != OutcomeCompleted {
		t.Fatal(got)
	}
	if f.transport.sends() != 4 {
		t.Fatalf("denied replies sent: %d", f.transport.sends())
	}
	var denied, once int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM native_tool_operations WHERE rpc_kind='server_reply' AND state='denied'`).Scan(&denied); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM native_tool_once o JOIN native_tool_operations p ON p.operation_id=o.operation_id WHERE p.rpc_kind='server_reply'`).Scan(&once); err != nil {
		t.Fatal(err)
	}
	if denied != 1025 || once != 0 {
		t.Fatalf("denied=%d callback once=%d", denied, once)
	}
	before := f.transport.sends()
	got, _ := f.serve(t, `{"jsonrpc":"2.0","id":30000,"result":{}}`, "synthetic-session")
	if got.Outcome == OutcomeCompleted || f.transport.sends() != before {
		t.Fatal("denied identity replayed after reclamation")
	}
}
