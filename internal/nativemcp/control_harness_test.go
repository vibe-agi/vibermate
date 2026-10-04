package nativemcp

import (
	"context"
	"database/sql"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

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
