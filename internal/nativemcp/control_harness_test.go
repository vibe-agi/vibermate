package nativemcp

import (
	"context"
	"database/sql"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

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

// Test-only completion barrier around the real executor/Store. The child
// finishes before the handler retries; the parent can be held live separately.
func ExerciseNativeHistoricalControl(t *testing.T, parentCompleted bool, check func(Executor, *runtimepersistence.Store, manualcapture.Controller, captureidentity.Reference, nativetool.Approval, func())) {
	t.Helper()
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
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
		audit               egressaudit.Page
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
		out.audit, err = f.store.EgressAttemptRepository().List(ctx, egressaudit.PageRequest{ParentKind: egressaudit.ParentClientOperation, ParentID: child.id.String(), Limit: 10})
		if err != nil {
			t.Fatal(err)
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
		if beforeHistory.once != 1 || len(beforeHistory.audit.Items) != 1 || beforeHistory.sends != 5 {
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
	t.Helper()
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	manuals, err := manualcapture.NewManager(context.Background(), manualcapture.DefaultOptions(f.store.ManualCaptureRepository()))
	if err != nil {
		t.Fatal(err)
	}
	defer manuals.Shutdown(context.Background())
	replied := make(chan struct{}, 1)
	const callback = `{"jsonrpc":"2.0","id":17,"method":"elicitation/create","params":{"message":"callback-private-sentinel","requestedSchema":{"type":"object","properties":{"answer":{"type":"string"}}}}}`
	const reply = `{"jsonrpc":"2.0","id":17,"result":{"action":"accept","content":{"answer":"form-proof-private-sentinel"}}}`
	f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
		if strings.Contains(string(body), `"method":"tools/call"`) {
			reader, writer := io.Pipe()
			go func() {
				defer writer.Close()
				io.WriteString(writer, "data: "+callback+"\n\n")
				select {
				case <-replied:
				case <-r.Context().Done():
					return
				}
				io.WriteString(writer, "data: {\"jsonrpc\":\"2.0\",\"id\":3,\"result\":{\"content\":[]}}\n\n")
			}()
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: reader}, nil
		}
		replied <- struct{}{}
		return &http.Response{StatusCode: 202, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	parent := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"}}}`, "synthetic-session")
	parents := make(chan Terminal, 1)
	go func() { parents <- f.executor.Serve(context.Background(), parent, httptest.NewRecorder()) }()
	f.approve(t, parent.id)
	deadline := time.Now().Add(time.Second)
	found := false
	for time.Now().Before(deadline) {
		e := f.executor.(*executor)
		e.mu.Lock()
		for _, s := range e.scopes[f.capture.Key()].sessions {
			s.mu.Lock()
			found = s.parents["17"] != nil
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
	children := make(chan Terminal, 1)
	go func() { children <- f.executor.Serve(context.Background(), child, httptest.NewRecorder()) }()
	approval := waitNativeQuestion(t, f, child.id)
	check(f.executor, f.store, manuals, f.capture, approval)
	if result := <-children; result.Outcome != OutcomeCompleted {
		t.Fatalf("child %+v", result)
	}
	if result := <-parents; result.Outcome != OutcomeCompleted {
		t.Fatalf("parent %+v", result)
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
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.executor.(*executor).dependencies.ContentProfiles = syntheticProfiles{}
	f.handshake(t)
	manuals, err := manualcapture.NewManager(context.Background(), manualcapture.DefaultOptions(f.store.ManualCaptureRepository()))
	if err != nil {
		t.Fatal(err)
	}
	defer manuals.Shutdown(context.Background())
	const wire = `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"frozen.txt"},"_meta":{"private":"metadata-sentinel"}}}`
	call := f.captured(t, "POST", wire, "synthetic-session")
	results := make(chan Terminal, 1)
	go func() { results <- f.executor.Serve(context.Background(), call, httptest.NewRecorder()) }()
	approval := waitNativeQuestion(t, f, call.id)
	check(f.executor, f.store, manuals, f.capture, approval)
	if result := <-results; result.Outcome != OutcomeCompleted {
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

// Real executor -> native repository bridge, intentionally test-only.
func ExerciseNativeRecordedControl(t *testing.T, recording environment.ContentRecordingPolicy, result string, check func(*runtimepersistence.Store, manualcapture.Controller, captureidentity.Reference, nativetool.Operation), mutate ...func(*sql.DB, nativetool.Operation)) {
	exerciseNativeRecordedControl(t, recording, result, true, check, mutate...)
}
func ExerciseNativeUnknownRecordedControl(t *testing.T, recording environment.ContentRecordingPolicy, result string, check func(*runtimepersistence.Store, manualcapture.Controller, captureidentity.Reference, nativetool.Operation), mutate ...func(*sql.DB, nativetool.Operation)) {
	exerciseNativeRecordedControl(t, recording, result, false, check, mutate...)
}
func exerciseNativeRecordedControl(t *testing.T, recording environment.ContentRecordingPolicy, result string, known bool, check func(*runtimepersistence.Store, manualcapture.Controller, captureidentity.Reference, nativetool.Operation), mutate ...func(*sql.DB, nativetool.Operation)) {
	t.Helper()
	f := newNativeFixtureMode(t, environment.ToolPolicyObserve, recording)
	if known {
		f.executor.(*executor).dependencies.ContentProfiles = syntheticProfiles{}
	}
	f.handshake(t)
	manuals, err := manualcapture.NewManager(context.Background(), manualcapture.DefaultOptions(f.store.ManualCaptureRepository()))
	if err != nil {
		t.Fatal(err)
	}
	defer manuals.Shutdown(context.Background())
	f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":3,"result":` + result + `}`))}, nil
	}
	call := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"frozen.txt"},"_meta":{"private":"metadata-sentinel"}}}`, "synthetic-session")
	terminal := f.executor.Serve(context.Background(), call, httptest.NewRecorder())
	if terminal.Outcome != OutcomeCompleted {
		t.Fatalf("recorded terminal %+v", terminal)
	}
	op, err := f.store.NativeToolRepository().Get(context.Background(), call.id)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range mutate {
		change(f.db, op)
	}
	check(f.store, manuals, f.capture, op)
	assertNoSensitiveRows(t, f, []string{"metadata-sentinel", "synthetic-original-login"})
}

func ExerciseNativeUnavailableControl(t *testing.T, check func(Executor, *runtimepersistence.Store, manualcapture.Controller, captureidentity.Reference, nativetool.Approval)) {
	exerciseNativeRejectedControl(t, false, check)
}

func exerciseNativeRejectedControl(t *testing.T, known bool, check func(Executor, *runtimepersistence.Store, manualcapture.Controller, captureidentity.Reference, nativetool.Approval)) {
	t.Helper()
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
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
	results := make(chan Terminal, 1)
	go func() { results <- f.executor.Serve(context.Background(), call, httptest.NewRecorder()) }()
	a := waitNativeQuestion(t, f, call.id)
	check(f.executor, f.store, manuals, f.capture, a)
	if result := <-results; result.Outcome != OutcomeRejected || f.transport.sends() != 3 {
		t.Fatalf("expired question sent %+v", result)
	}
}
