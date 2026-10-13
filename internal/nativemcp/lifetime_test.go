package nativemcp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
)

func TestNativeCancellationClosesRealQuestionWithoutSending(t *testing.T) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	c := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"}}}`, "synthetic-session")
	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan Terminal, 1)
	go func() { results <- f.executor.Serve(ctx, c, httptest.NewRecorder()) }()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		op, err := f.store.NativeToolRepository().Get(context.Background(), c.id)
		if err == nil && op.ApprovalID != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	terminal := <-results
	if terminal.Outcome == OutcomeCompleted || f.transport.sends() != 3 {
		t.Fatalf("canceled call sent %+v", terminal)
	}
	op, err := f.store.NativeToolRepository().Get(context.Background(), c.id)
	if err != nil || op.State != "canceled" || op.ApprovalID == nil {
		t.Fatalf("real question lifecycle %+v %v", op, err)
	}
	approval, err := f.store.NativeToolRepository().GetApproval(context.Background(), *op.ApprovalID)
	if err != nil || approval.State != "canceled" || approval.ConsumedAt != nil {
		t.Fatalf("question %+v %v", approval, err)
	}
}
func TestNativeAccepted404AndEOFBecomeUncertainBeforeReplacement(t *testing.T) {
	for _, failure := range []string{"404", "EOF"} {
		t.Run(failure, func(t *testing.T) {
			f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
			f.handshake(t)
			f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
				status := 200
				if failure == "404" {
					status = 404
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
			}
			c := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"},"_meta":{"callId":"same-observed-intent"}}}`, "synthetic-session")
			w := httptest.NewRecorder()
			results := make(chan Terminal, 1)
			go func() { results <- f.executor.Serve(context.Background(), c, w) }()
			f.approve(t, c.id)
			terminal := <-results
			if terminal.Outcome != OutcomeUncertain || w.Code != 409 || f.transport.sends() != 4 {
				t.Fatalf("accepted failure %+v status%d", terminal, w.Code)
			}
			op, err := f.store.NativeToolRepository().Get(context.Background(), c.id)
			scope, scopeErr := f.store.NativeToolRepository().GetScope(context.Background(), f.capture)
			if err != nil || scopeErr != nil || op.State != "uncertain" || op.BytesIn != nil || op.BytesOut != nil || !scope.ReplayBarrier {
				t.Fatalf("durable uncertainty %+v %+v %v %v", op, scope, err, scopeErr)
			}
			f.transport.handler = nil
			f.handshake(t)
			replacement := f.captured(t, "POST", `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"},"_meta":{"callId":"same-observed-intent"}}}`, "synthetic-session")
			before := f.transport.sends()
			go func() { results <- f.executor.Serve(context.Background(), replacement, httptest.NewRecorder()) }()
			f.approve(t, replacement.id)
			terminal = <-results
			if terminal.Outcome == OutcomeCompleted || f.transport.sends() != before || terminal.Reason != ReasonExecutionUncertain {
				t.Fatalf("replacement replayed %+v sends%d", terminal, f.transport.sends())
			}
		})
	}
}
func TestNativeDeleteSessionHasOwnStartAndAudit(t *testing.T) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	c := f.captured(t, "DELETE", "", "synthetic-session")
	terminal := f.executor.Serve(context.Background(), c, httptest.NewRecorder())
	if terminal.Outcome != OutcomeCompleted || f.transport.sends() != 4 {
		t.Fatal(terminal)
	}
	op, err := f.store.NativeToolRepository().Get(context.Background(), c.id)
	if err != nil || op.State != "completed" || op.Kind != "delete_session" {
		t.Fatalf("DELETE native history %+v %v", op, err)
	}
	var audits int
	if err = f.db.QueryRow(`SELECT COUNT(*) FROM runtime_egress_attempts WHERE purpose='client_tool_service' AND parent_kind='client_operation' AND parent_id=? AND payload_class='none' AND account_id=''`, c.id).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("DELETE audit%d %v", audits, err)
	}
}

func TestNativeOFFWinsBeforeStartAndSendsZero(t *testing.T) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	c := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"}}}`, "synthetic-session")
	results := make(chan Terminal, 1)
	go func() { results <- f.executor.Serve(context.Background(), c, httptest.NewRecorder()) }()
	deadline := time.Now().Add(time.Second)
	found := false
	for time.Now().Before(deadline) {
		op, err := f.store.NativeToolRepository().Get(context.Background(), c.id)
		if err == nil && op.ApprovalID != nil {
			found = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !found {
		t.Fatal("real question absent")
	}
	ctx := context.Background()
	snapshot, err := f.environments.Resolve("native.synthetic")
	if err != nil {
		t.Fatal(err)
	}
	candidate := snapshot.Aggregate()
	candidate.Revision++
	candidate.ClientToolAccess = nil
	draft, err := f.environments.SaveDraft(ctx, environment.DraftCommand{ExpectedBaseRevision: snapshot.Revision(), Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := f.environments.Preview(ctx, candidate.ID, draft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.environments.Publish(ctx, preview); err != nil {
		t.Fatal(err)
	}
	terminal := <-results
	if terminal.Outcome == OutcomeCompleted || f.transport.sends() != 3 {
		t.Fatalf("OFF sent %+v", terminal)
	}
	op, err := f.store.NativeToolRepository().Get(ctx, c.id)
	if err != nil || op.State != "canceled" {
		t.Fatalf("OFF lifecycle %+v %v", op, err)
	}
	var once int
	if err = f.db.QueryRow(`SELECT COUNT(*) FROM native_tool_once WHERE operation_id=?`, c.id).Scan(&once); err != nil || once != 0 {
		t.Fatalf("OFF once%d %v", once, err)
	}
}
