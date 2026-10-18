package nativemcp

import (
	"context"
	"database/sql"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// Control authorization/history checks exercise the production decision budget.
// The general native fixture's accelerated expiry belongs to deadline tests;
// it must not race a sequence of authenticated HTTP and SQLite operations.
func newNativeControlFixture(t *testing.T) *nativeFixture {
	t.Helper()
	return newNativeFixtureWithApprovalConfig(t, environment.ToolPolicyReview, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, 0, authoredEgress(t, egressprofile.Direct()), toolapproval.DefaultConfig())
}

// Cancellation owns cleanup even if a control assertion fails before answering
// the question. The production decision lifetime must never become a test join.
func startNativeControl(t *testing.T, f *nativeFixture, request CapturedRequest) func() Terminal {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var result Terminal
	go func() { result = f.executor.Serve(ctx, request, httptest.NewRecorder()); close(done) }()
	await := func() Terminal {
		t.Helper()
		select {
		case <-done:
			return result
		case <-time.After(3 * time.Second):
			t.Fatal("native control operation did not join")
		}
		return Terminal{}
	}
	t.Cleanup(func() { cancel(); await() })
	return await
}

func TestNativeControlFixtureCancellationJoinsPendingApproval(t *testing.T) {
	f := newNativeControlFixture(t)
	f.handshake(t)
	call := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"}}}`, "synthetic-session")
	var approval nativetool.Approval
	t.Run("unanswered_control", func(t *testing.T) {
		startNativeControl(t, f, call)
		approval = waitNativeQuestion(t, f, call.id)
	})
	current, err := f.store.NativeToolRepository().GetApproval(context.Background(), approval.ID)
	if err != nil || current.State != "canceled" || current.ConsumedAt != nil || f.transport.sends() != 3 {
		t.Fatalf("unanswered control cleanup: approval=%+v err=%v sends=%d", current, err, f.transport.sends())
	}
}

type historyObservedWriter struct {
	*httptest.ResponseRecorder
	observed chan struct{}
	once     sync.Once
}

func (w *historyObservedWriter) Write(body []byte) (int, error) {
	n, err := w.ResponseRecorder.Write(body)
	if strings.Contains(string(body), `"method":"elicitation/create"`) {
		w.once.Do(func() { close(w.observed) })
	}
	return n, err
}

// controlDelayedStart models scheduling between the real decision and Start.
// It changes only the observation time supplied to the real repository; the
// approval, receipt checks, transitions and executor cleanup remain real.
type controlDelayedStart struct {
	nativetool.Repository
	entered chan time.Time
	resume  chan struct{}
	advance func(time.Time)
}

func (s *controlDelayedStart) Start(ctx context.Context, id nativetool.ID, attempt string, revision uint64, now time.Time) (nativetool.StartReceipt, error) {
	op, err := s.Repository.Get(ctx, id)
	if err != nil {
		return nativetool.StartReceipt{}, err
	}
	if op.Kind == nativetool.RequestServerReply {
		approval, err := s.Repository.GetApproval(ctx, *op.ApprovalID)
		if err != nil {
			return nativetool.StartReceipt{}, err
		}
		// The old control fixture expired at this exact boundary, although the
		// authenticated control checks were not testing the decision deadline.
		now = approval.CreatedAt.Add(3 * time.Second)
		s.entered <- now
		select {
		case <-s.resume:
			s.advance(now)
		case <-ctx.Done():
			return nativetool.StartReceipt{}, ctx.Err()
		}
	}
	return s.Repository.Start(ctx, id, attempt, revision, now)
}

func ExerciseNativeDelayedStartControl(t *testing.T, check func(Executor, *runtimepersistence.Store, manualcapture.Controller, captureidentity.Reference, nativetool.Approval, func())) {
	exerciseNativeHistoricalControl(t, false, true, check)
}

// Test-only completion barrier around the real executor/Store. The child
// finishes before the handler retries; the parent can be held live separately.
func ExerciseNativeHistoricalControl(t *testing.T, parentCompleted bool, check func(Executor, *runtimepersistence.Store, manualcapture.Controller, captureidentity.Reference, nativetool.Approval, func())) {
	t.Helper()
	exerciseNativeHistoricalControl(t, parentCompleted, false, check)
}

func exerciseNativeHistoricalControl(t *testing.T, parentCompleted, delayed bool, check func(Executor, *runtimepersistence.Store, manualcapture.Controller, captureidentity.Reference, nativetool.Approval, func())) {
	t.Helper()
	f := newNativeControlFixture(t)
	var delayedStart *controlDelayedStart
	var resumeOnce sync.Once
	resume := func() {
		if delayedStart != nil {
			resumeOnce.Do(func() { close(delayedStart.resume) })
		}
	}
	defer resume()
	if delayed {
		delayedStart = &controlDelayedStart{Repository: f.store.NativeToolRepository(), entered: make(chan time.Time, 1), resume: make(chan struct{})}
		clock := &unsentAtomicClock{}
		delayedStart.advance = clock.set
		e := f.executor.(*executor)
		e.dependencies.Store = delayedStart
		e.dependencies.Clock = func() time.Time {
			now := time.Now().UTC()
			if advanced := clock.now(); advanced.After(now) {
				return advanced
			}
			return now
		}
	}
	f.handshake(t)
	manuals, err := manualcapture.NewManager(context.Background(), manualcapture.DefaultOptions(f.store.ManualCaptureRepository()))
	if err != nil {
		t.Fatal(err)
	}
	defer manuals.Shutdown(context.Background())
	parentRelease := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(parentRelease) }) }
	defer release()
	const callback = `{"jsonrpc":"2.0","id":17,"method":"elicitation/create","params":{"message":"callback-private-sentinel","requestedSchema":{"type":"object","properties":{"answer":{"type":"string"}}}}}`
	const reply = `{"jsonrpc":"2.0","id":17,"result":{"action":"accept","content":{"answer":"form-proof-private-sentinel"}}}`
	f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
		if strings.Contains(string(body), `"method":"tools/call"`) {
			reader, writer := io.Pipe()
			go func() {
				defer writer.Close()
				_, _ = io.WriteString(writer, "data: "+callback+"\n\n")
				select {
				case <-parentRelease:
				case <-r.Context().Done():
					return
				}
				_, _ = io.WriteString(writer, "data: {\"jsonrpc\":\"2.0\",\"id\":3,\"result\":{\"content\":[]}}\n\n")
			}()
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: reader}, nil
		}
		return &http.Response{StatusCode: 202, Header: http.Header{}, Body: http.NoBody}, nil
	}
	await := func(done <-chan struct{}) {
		t.Helper()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("real callback completion barrier timed out")
		}
	}
	parent := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"}}}`, "synthetic-session")
	w := &historyObservedWriter{ResponseRecorder: httptest.NewRecorder(), observed: make(chan struct{})}
	parentDone := make(chan struct{})
	var childDone chan struct{}
	var parentResult Terminal
	go func() { parentResult = f.executor.Serve(context.Background(), parent, w); close(parentDone) }()
	defer func() {
		resume()
		release()
		_ = f.executor.Close(context.Background())
		if childDone != nil {
			await(childDone)
		}
		await(parentDone)
	}()
	f.approve(t, parent.id)
	await(w.observed)
	child := f.captured(t, "POST", reply, "synthetic-session")
	childDone = make(chan struct{})
	var childResult Terminal
	go func() {
		childResult = f.executor.Serve(context.Background(), child, httptest.NewRecorder())
		close(childDone)
	}()
	a := waitNativeQuestion(t, f, child.id)
	completed := false
	type state struct {
		approval            nativetool.Approval
		operation           nativetool.Operation
		audit               []any
		key                 string
		once, events, sends int
	}
	snapshot := func() state {
		t.Helper()
		var out state
		ctx := context.Background()
		var err error
		out.approval, err = f.store.NativeToolRepository().GetApproval(ctx, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		out.operation, err = f.store.NativeToolRepository().Get(ctx, child.id)
		if err != nil {
			t.Fatal(err)
		}
		var deliveries int
		if err := f.db.QueryRow(`SELECT COUNT(*) FROM native_tool_deliveries WHERE operation_id=?`, child.id).Scan(&deliveries); err != nil {
			t.Fatal(err)
		}
		if deliveries == 1 {
			out.audit = freshGETRow(t, f, `SELECT * FROM native_tool_deliveries WHERE operation_id=?`, child.id)
		} else if deliveries != 0 {
			t.Fatal("multiple physical native deliveries")
		}

		err = f.db.QueryRow(`SELECT idempotency_key,(SELECT COUNT(*) FROM native_tool_once WHERE operation_id=?),(SELECT COUNT(*) FROM native_tool_events WHERE operation_id=?) FROM native_tool_approvals WHERE approval_id=?`, child.id, child.id, a.ID).Scan(&out.key, &out.once, &out.events)
		if err != nil {
			t.Fatal(err)
		}
		out.sends = f.transport.sends()
		return out
	}
	var beforeHistory state
	complete := func() {
		if delayedStart != nil {
			select {
			case observed := <-delayedStart.entered:
				t.Logf("delayed Start observed=%s approvalExpires=%s", observed.Format(time.RFC3339Nano), a.ExpiresAt.Format(time.RFC3339Nano))
			case <-time.After(3 * time.Second):
				t.Fatal("approved child never reached Start")
			}
			resume()
			await(childDone)
			actual, err := f.store.NativeToolRepository().GetApproval(context.Background(), a.ID)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("delayed child=%s/%s approval=%s revision=%d consumed=%t", childResult.Outcome, childResult.Reason, actual.State, actual.Revision, actual.ConsumedAt != nil)
		}
		await(childDone)
		if childResult.Outcome != OutcomeCompleted {
			t.Fatalf("real child completion=%+v", childResult)
		}
		approval, err := f.store.NativeToolRepository().GetApproval(context.Background(), a.ID)
		if err != nil || approval.ConsumedAt == nil {
			t.Fatal("real callback approval was not consumed")
		}
		e := f.executor.(*executor)
		e.mu.Lock()
		for _, s := range e.scopes[f.capture.Key()].sessions {
			s.mu.Lock()
			_, registered := s.parents["17"]
			s.mu.Unlock()
			if registered {
				t.Error("completed callback still registered in executor")
			}
		}
		e.mu.Unlock()
		if parentCompleted {
			release()
			await(parentDone)
			if parentResult.Outcome != OutcomeCompleted {
				t.Fatal(parentResult)
			}
		}
		op, err := f.store.NativeToolRepository().Get(context.Background(), parent.id)
		if err != nil || parentCompleted && op.State != "completed" || !parentCompleted && op.State != "started" {
			t.Fatalf("parent completion ordering=%+v %v", op, err)
		}
		completed = true
		beforeHistory = snapshot()
		if beforeHistory.once != 1 || len(beforeHistory.audit) == 0 || beforeHistory.sends != 5 {
			t.Fatal("callback did not produce exactly one once/audit/send")
		}
	}
	check(f.executor, f.store, manuals, f.capture, a, complete)
	if !completed {
		t.Fatal("handler did not exercise real completion barrier")
	}
	if !reflect.DeepEqual(beforeHistory, snapshot()) {
		t.Fatal("historical HTTP responses changed approval/key/operation/once/audit/events/outbound")
	}
	release()
	await(parentDone)
	before := f.transport.sends()
	if before != 5 {
		t.Fatalf("real sends=%d", before)
	}
	scope, err := f.store.NativeToolRepository().GetScope(context.Background(), f.capture)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.NativeToolRepository().Start(context.Background(), child.id, "history-late-start", scope.Revision, time.Now().UTC()); err == nil {
		t.Fatal("historical response renewed Start")
	}
	result, _ := f.serve(t, reply, "synthetic-session")
	if result.Outcome == OutcomeCompleted || f.transport.sends() != before {
		t.Fatal("historical response renewed reply send")
	}
}

// Test-only bridge lets the external control integration use the same native
// fixture without an import cycle or any test hook in production code.
func ExerciseNativePendingControl(t *testing.T, check func(Executor, *runtimepersistence.Store, manualcapture.Controller, captureidentity.Reference, nativetool.Approval)) {
	exerciseNativePendingControl(t, false, check)
}

func ExerciseNativePingControl(t *testing.T, check func(Executor, *runtimepersistence.Store, manualcapture.Controller, captureidentity.Reference, nativetool.Approval)) {
	exerciseNativePendingControl(t, true, check)
}

func exerciseNativePendingControl(t *testing.T, ping bool, check func(Executor, *runtimepersistence.Store, manualcapture.Controller, captureidentity.Reference, nativetool.Approval)) {
	t.Helper()
	f := newNativeControlFixture(t)
	f.handshake(t)
	manuals, err := manualcapture.NewManager(context.Background(), manualcapture.DefaultOptions(f.store.ManualCaptureRepository()))
	if err != nil {
		t.Fatal(err)
	}
	defer manuals.Shutdown(context.Background())
	replied := make(chan struct{}, 1)
	callback := `{"jsonrpc":"2.0","id":17,"method":"elicitation/create","params":{"message":"callback-private-sentinel","requestedSchema":{"type":"object","properties":{"answer":{"type":"string"}}}}}`
	reply := `{"jsonrpc":"2.0","id":17,"result":{"action":"accept","content":{"answer":"form-proof-private-sentinel"}}}`
	if ping {
		callback = `{"jsonrpc":"2.0","id":17,"method":"ping"}`
		reply = `{"jsonrpc":"2.0","id":17,"result":{}}`
	}
	replyCount := 0
	f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
		if r.Method == "GET" || strings.Contains(string(body), `"method":"tools/call"`) {
			reader, writer := io.Pipe()
			go func() {
				defer writer.Close()
				io.WriteString(writer, "data: "+callback+"\n\n")
				select {
				case <-replied:
				case <-r.Context().Done():
					return
				}
				if !ping {
					io.WriteString(writer, "data: {\"jsonrpc\":\"2.0\",\"id\":3,\"result\":{\"content\":[]}}\n\n")
				}
			}()
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: reader}, nil
		}
		if string(body) != reply {
			t.Error("registered reply wire changed")
		}
		replyCount++
		replied <- struct{}{}
		return &http.Response{StatusCode: 202, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	var parent CapturedRequest
	if ping {
		parent = f.captured(t, "GET", "", "synthetic-session")
	} else {
		parent = f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"}}}`, "synthetic-session")
	}
	parents := startNativeControl(t, f, parent)
	if !ping {
		f.approve(t, parent.id)
	}
	deadline := time.Now().Add(time.Second)
	found := false
	var registeredSession *session
	for time.Now().Before(deadline) {
		e := f.executor.(*executor)
		e.mu.Lock()
		for _, s := range e.scopes[f.capture.Key()].sessions {
			s.mu.Lock()
			found = s.parents["17"] != nil
			if found {
				registeredSession = s
			}
			s.mu.Unlock()
			if found {
				break
			}
		}
		e.mu.Unlock()
		if found {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !found {
		t.Fatal("registered callback absent")
	}
	child := f.captured(t, "POST", reply, "synthetic-session")
	children := startNativeControl(t, f, child)
	approval := waitNativeQuestion(t, f, child.id)
	if ping {
		e := f.executor.(*executor)
		r, err := parseRequest([]byte(reply), e.limits)
		if err != nil {
			t.Fatal(err)
		}
		binding, _, witness, err := e.bindRequest(child, r, registeredSession, registeredSession.parents["17"].binding.Plan())
		if err != nil || witness == nil || witness.Verify(child.id, binding) != nil {
			t.Fatal("real ping child witness unavailable")
		}
		for _, missing := range []string{"observer", "classification", "live"} {
			registeredSession.mu.Lock()
			p := registeredSession.parents["17"]
			saved := *p
			switch missing {
			case "observer":
				p.observer = nil
			case "classification":
				p.kind = ""
			case "live":
				p.consumed = true
			}
			registeredSession.mu.Unlock()
			preview, err := e.previewIntents(binding, nil, r, registeredSession)
			registeredSession.mu.Lock()
			*p = saved
			registeredSession.mu.Unlock()
			if err != nil || len(preview) != 0 {
				t.Fatalf("counterfeit ping preview accepted missing %s", missing)
			}
		}
	}
	check(f.executor, f.store, manuals, f.capture, approval)
	if result := children(); result.Outcome != OutcomeCompleted {
		t.Fatalf("child %+v", result)
	}
	if result := parents(); result.Outcome != OutcomeCompleted {
		t.Fatalf("parent %+v", result)
	}
	if replyCount != 1 {
		t.Fatalf("registered reply sends %d", replyCount)
	}
	before := f.transport.sends()
	if before != 5 {
		t.Fatalf("sends %d", before)
	}
	scope, err := f.store.NativeToolRepository().GetScope(context.Background(), f.capture)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.NativeToolRepository().Start(context.Background(), child.id, "repeat-start", scope.Revision, time.Now().UTC()); err == nil {
		t.Fatal("duplicate Start granted receipt")
	}
	for _, wire := range []string{reply, strings.Replace(reply, "form-proof-private-sentinel", "changed-proof", 1)} {
		result, _ := f.serve(t, wire, "synthetic-session")
		if result.Outcome == OutcomeCompleted || f.transport.sends() != before {
			t.Fatal("consumed parent replay sent")
		}
	}
	assertNoSensitiveRows(t, f, []string{"callback-private-sentinel", "form-proof-private-sentinel", "synthetic-original-login"})
	var modelQuestions int
	if err = f.db.QueryRow("SELECT count(*) FROM tool_approvals").Scan(&modelQuestions); err != nil {
		t.Fatal(err)
	}
	if modelQuestions != 0 {
		t.Fatal("native approval polluted model approvals")
	}
}

func ExerciseNativeToolControl(t *testing.T, check func(Executor, *runtimepersistence.Store, manualcapture.Controller, captureidentity.Reference, nativetool.Approval)) {
	t.Helper()
	f := newNativeControlFixture(t)
	f.executor.(*executor).dependencies.ContentProfiles = syntheticProfiles{}
	f.handshake(t)
	manuals, err := manualcapture.NewManager(context.Background(), manualcapture.DefaultOptions(f.store.ManualCaptureRepository()))
	if err != nil {
		t.Fatal(err)
	}
	defer manuals.Shutdown(context.Background())
	const wire = `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"frozen.txt"},"_meta":{"private":"metadata-sentinel"}}}`
	call := f.captured(t, "POST", wire, "synthetic-session")
	results := startNativeControl(t, f, call)
	approval := waitNativeQuestion(t, f, call.id)
	check(f.executor, f.store, manuals, f.capture, approval)
	if result := results(); result.Outcome != OutcomeCompleted {
		t.Fatalf("ordinary call %+v", result)
	}
	if f.transport.sends() != 4 {
		t.Fatalf("ordinary sends%d", f.transport.sends())
	}
	f.transport.mu.Lock()
	sent := f.transport.requests[len(f.transport.requests)-1]
	f.transport.mu.Unlock()
	if sent != wire {
		t.Fatalf("send substituted bytes %s", sent)
	}
	scope, err := f.store.NativeToolRepository().GetScope(context.Background(), f.capture)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.NativeToolRepository().Start(context.Background(), call.id, "repeat-start", scope.Revision, time.Now().UTC()); err == nil {
		t.Fatal("duplicate receipt")
	}
	for _, body := range []string{wire, strings.Replace(wire, "frozen.txt", "changed.txt", 1)} {
		terminal, _ := f.serve(t, body, "synthetic-session")
		if terminal.Outcome == OutcomeCompleted || f.transport.sends() != 4 {
			t.Fatal("duplicate/substituted RPC sent")
		}
	}
	assertNoSensitiveRows(t, f, []string{"frozen.txt", "metadata-sentinel", "synthetic-original-login"})
	var modelQuestions int
	if err = f.db.QueryRow("SELECT count(*) FROM tool_approvals").Scan(&modelQuestions); err != nil || modelQuestions != 0 {
		t.Fatalf("model question pollution %d %v", modelQuestions, err)
	}
}

func ExerciseNativeExpiredControl(t *testing.T, check func(Executor, *runtimepersistence.Store, manualcapture.Controller, captureidentity.Reference, nativetool.Approval)) {
	exerciseNativeRejectedControl(t, true, check)
}

// Separate-role reader/HTTP fixture, not production Runtime composition.
// The actual completed operation is input to the optional writer; expected
// HTTP output is never borrowed from the authority repository.
type NativeRecordedCheck func(*runtimepersistence.Store, manualcapture.Controller, captureidentity.Reference, nativetool.Operation, nativetool.InspectionReader, func(bool))
type NativeRecordedMutation func(*sql.DB, nativetool.InspectionOperation)

func ExerciseNativeRecordedControl(t *testing.T, policy environment.ContentRecordingPolicy, result string, check NativeRecordedCheck, mutate ...NativeRecordedMutation) {
	exerciseNativeRecordedControl(t, policy, result, true, true, check, mutate...)
}
func ExerciseNativeUnknownRecordedControl(t *testing.T, policy environment.ContentRecordingPolicy, result string, check NativeRecordedCheck, mutate ...NativeRecordedMutation) {
	exerciseNativeRecordedControl(t, policy, result, false, true, check, mutate...)
}
func ExerciseNativeContentOnlyControl(t *testing.T, policy environment.ContentRecordingPolicy, result string, check NativeRecordedCheck) {
	exerciseNativeRecordedControl(t, policy, result, true, false, check)
}
func exerciseNativeRecordedControl(t *testing.T, policy environment.ContentRecordingPolicy, result string, known, history bool, check NativeRecordedCheck, mutate ...NativeRecordedMutation) {
	t.Helper()
	ctx := context.Background()
	f := newNativeFixtureMode(t, environment.ToolPolicyObserve, policy)
	if known {
		f.executor.(*executor).dependencies.ContentProfiles = syntheticProfiles{}
	}
	f.handshake(t)
	manuals, err := manualcapture.NewManager(ctx, manualcapture.DefaultOptions(f.store.ManualCaptureRepository()))
	if err != nil {
		t.Fatal(err)
	}
	defer manuals.Shutdown(ctx)
	f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":3,"result":` + result + `}`))}, nil
	}
	call := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"frozen.txt"},"_meta":{"private":"metadata-sentinel"}}}`, "synthetic-session")
	terminal := f.executor.Serve(ctx, call, httptest.NewRecorder())
	if terminal.Outcome != OutcomeCompleted {
		t.Fatalf("recorded terminal %+v", terminal)
	}
	op, err := f.store.NativeToolRepository().Get(ctx, call.id)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.content.flush(); err != nil {
		t.Fatal(err)
	}
	if history {
		var kind string
		var at int64
		if err = f.db.QueryRow(`SELECT last_event_kind,last_event_at_unix_ms FROM native_tool_operations WHERE operation_id=?`, op.ID).Scan(&kind, &at); err != nil {
			t.Fatal(err)
		}
		if err = f.content.store.PutNativeHistory(ctx, nativetool.RecordedHistory{Scope: op.RecordingScope, Observation: nativetool.HistoryObservation{Operation: op, EventKind: kind, EventAt: time.UnixMilli(at).UTC(), MaximumEventAt: op.MaximumEventAt, MetadataExpiresAt: op.MetadataExpiresAt}}); err != nil {
			t.Fatal(err)
		}
	}
	var observationPath string
	if err = f.contentDB.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&observationPath); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(filepath.Dir(observationPath))
	if err != nil {
		t.Fatal(err)
	}
	controlPath := filepath.Join(root, "inspection-control.db")
	control, err := runtimepersistence.OpenControl(ctx, runtimepersistence.ControlOptions{DatabasePath: controlPath, LayoutID: "native-fixture", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer control.Shutdown(ctx)
	controlDB, err := sql.Open("sqlite", controlPath)
	if err != nil {
		t.Fatal(err)
	}
	defer controlDB.Close()
	if _, err = controlDB.Exec(`UPDATE runtime_storage_state SET archive_epoch=?`, op.RecordingScope.Fence.ArchiveEpoch); err != nil {
		t.Fatal(err)
	}
	if _, err = controlDB.Exec(`INSERT INTO recording_capture_fences(capture_kind,capture_id,generation,deleted) VALUES(?,?,?,0)`, op.Capture.Kind, op.Capture.ID, op.RecordingScope.Fence.CaptureGeneration); err != nil {
		t.Fatal(err)
	}
	catalog, err := runtimepersistence.NewInspectionCatalog(runtimepersistence.InspectionCatalogOptions{Root: root, LayoutID: "native-fixture", Generation: 1, Sources: []runtimepersistence.InspectionSource{{ID: "archive", RelativePath: "observations.db", Database: runtimepersistence.SchemaState{Identity: "vibermate-observations", Revision: 1}, Current: true}}})
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close(ctx)
	reader := runtimepersistence.NewNativeInspectionReader(catalog, control)
	inspected, err := reader.Get(ctx, op.Capture, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertNoSensitiveRows(t, f, []string{"metadata-sentinel", "synthetic-original-login"})
	for _, change := range mutate {
		change(f.contentDB, inspected)
	}
	invalidate := func(all bool) {
		tx, err := controlDB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if all {
			_, err = tx.Exec(`UPDATE runtime_storage_state SET archive_epoch=archive_epoch+1,visibility_revision=visibility_revision+1`)
		} else {
			_, err = tx.Exec(`UPDATE recording_capture_fences SET generation=generation+1,deleted=1 WHERE capture_kind=? AND capture_id=?`, op.Capture.Kind, op.Capture.ID)
			if err == nil {
				_, err = tx.Exec(`UPDATE runtime_storage_state SET visibility_revision=visibility_revision+1`)
			}
		}
		if err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if all {
			_, err = f.store.ClearEvidence(ctx)
		} else {
			_, err = f.store.DeleteCapture(ctx, string(op.Capture.Kind), op.Capture.ID)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	check(f.store, manuals, f.capture, op, reader, invalidate)
	if len(mutate) == 0 {
		assertNoSensitiveRows(t, f, []string{"metadata-sentinel", "synthetic-original-login"})
	}
}

func ExerciseNativeUnavailableControl(t *testing.T, check func(Executor, *runtimepersistence.Store, manualcapture.Controller, captureidentity.Reference, nativetool.Approval)) {
	exerciseNativeRejectedControl(t, false, check)
}

func exerciseNativeRejectedControl(t *testing.T, known bool, check func(Executor, *runtimepersistence.Store, manualcapture.Controller, captureidentity.Reference, nativetool.Approval)) {
	t.Helper()
	f := newNativeControlFixture(t)
	if known {
		f.executor.(*executor).dependencies.ContentProfiles = syntheticProfiles{}
	}
	f.handshake(t)
	manuals, err := manualcapture.NewManager(context.Background(), manualcapture.DefaultOptions(f.store.ManualCaptureRepository()))
	if err != nil {
		t.Fatal(err)
	}
	defer manuals.Shutdown(context.Background())
	call := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"frozen.txt"}}}`, "synthetic-session")
	results := startNativeControl(t, f, call)
	a := waitNativeQuestion(t, f, call.id)
	check(f.executor, f.store, manuals, f.capture, a)
	if result := results(); result.Outcome != OutcomeRejected || f.transport.sends() != 3 {
		t.Fatalf("expired question sent %+v", result)
	}
}
