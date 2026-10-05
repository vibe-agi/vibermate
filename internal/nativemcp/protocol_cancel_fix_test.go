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
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

func TestNativeProtocolCancelClosesOrdinaryAndMACQuestions(t *testing.T) {
	for _, sensitive := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "MAC"}[sensitive], func(t *testing.T) {
			f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
			if sensitive {
				f.executor.(*executor).dependencies.ContentProfiles = syntheticProfiles{argumentPaths: []string{"/proof"}}
			}
			f.handshake(t)
			args := `{"path":"a"}`
			if sensitive {
				args = `{"path":"a","proof":"synthetic-private"}`
			}
			c := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":`+args+`}}`, "synthetic-session")
			results := make(chan Terminal, 1)
			go func() { results <- f.executor.Serve(context.Background(), c, httptest.NewRecorder()) }()
			deadline := time.Now().Add(time.Second)
			var approvalID nativetool.ID
			for time.Now().Before(deadline) {
				op, err := f.store.NativeToolRepository().Get(context.Background(), c.id)
				if err == nil && op.ApprovalID != nil {
					approvalID = *op.ApprovalID
					break
				}
				time.Sleep(time.Millisecond)
			}
			if approvalID == "" {
				t.Fatal("real question absent")
			}
			terminal, _ := f.serve(t, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":3}}`, "synthetic-session")
			if terminal.Outcome != OutcomeCompleted {
				t.Fatalf("cancel own receipt/send %+v", terminal)
			}
			requireNativeClaimAfterDrain(t, f.executor.(*executor), terminal.OperationID, false)
			approval, err := f.store.NativeToolRepository().GetApproval(context.Background(), approvalID)
			if err != nil || approval.State != "canceled" {
				t.Fatalf("protocol cancel left question %s %v", approval.State, err)
			}
			if _, err = f.store.NativeToolRepository().DecideApproval(context.Background(), nativetool.DecisionInput{ApprovalID: approval.ID, ExpectedRevision: approval.Revision, Decision: nativetool.DecisionAllowOnce, IdempotencyKey: "after-protocol-cancel", ObservedAt: time.Now().UTC()}); err == nil {
				t.Fatal("later allow revived canceled target")
			}
			select {
			case terminal = <-results:
				if terminal.Outcome == OutcomeCompleted {
					t.Fatal("target sent after cancel")
				}
			case <-time.After(time.Second):
				t.Fatal("target context remained alive")
			}
			if f.transport.sends() != 4 {
				t.Fatalf("canceled target sent %d", f.transport.sends())
			}
		})
	}
}

func TestNativeProtocolCancelAfterStartPreservesSiblingQuestion(t *testing.T) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	entered := make(chan struct{})
	f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
		if strings.Contains(string(body), `"id":3`) {
			close(entered)
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		if strings.Contains(string(body), `"notifications/cancelled"`) {
			return &http.Response{StatusCode: 202, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		rpc, _ := parseRequest(body, DefaultLimits())
		return wireResponse(`{"jsonrpc":"2.0","id":`+string(rpc.id)+`,"result":{"content":[]}}`, false), nil
	}
	results := make(chan Terminal, 1)
	parent := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"}}}`, "synthetic-session")
	go func() { results <- f.executor.Serve(context.Background(), parent, httptest.NewRecorder()) }()
	f.approve(t, parent.id)
	<-entered
	sibling := f.captured(t, "POST", `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"b"}}}`, "synthetic-session")
	siblingResults := make(chan Terminal, 1)
	go func() { siblingResults <- f.executor.Serve(context.Background(), sibling, httptest.NewRecorder()) }()
	deadline := time.Now().Add(time.Second)
	for {
		op, err := f.store.NativeToolRepository().Get(context.Background(), sibling.id)
		if err == nil && op.ApprovalID != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("sibling question absent")
		}
		time.Sleep(time.Millisecond)
	}
	terminal, _ := f.serve(t, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":3}}`, "synthetic-session")
	if terminal.Outcome != OutcomeCompleted {
		t.Fatal(terminal)
	}
	if terminal = <-results; terminal.Outcome != OutcomeUncertain {
		t.Fatalf("started target lost uncertainty %+v", terminal)
	}
	op, err := f.store.NativeToolRepository().Get(context.Background(), parent.id)
	scope, scopeErr := f.store.NativeToolRepository().GetScope(context.Background(), f.capture)
	if err != nil || scopeErr != nil || op.BytesIn != nil || op.BytesOut != nil || !scope.ReplayBarrier || scope.State != "active" {
		t.Fatalf("unknown effect/sibling scope %+v %+v %v %v", op, scope, err, scopeErr)
	}
	if terminal = <-siblingResults; terminal.Outcome == OutcomeCompleted {
		t.Fatalf("old sibling escaped replay barrier %+v", terminal)
	}
	old, err := f.store.NativeToolRepository().Get(context.Background(), sibling.id)
	if err != nil || old.State != "canceled" || old.ApprovalID == nil {
		t.Fatalf("barrier did not retire old question %+v %v", old, err)
	}
	approval, err := f.store.NativeToolRepository().GetApproval(context.Background(), *old.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.NativeToolRepository().DecideApproval(context.Background(), nativetool.DecisionInput{ApprovalID: approval.ID, ExpectedRevision: approval.Revision, Decision: nativetool.DecisionAllowOnce, IdempotencyKey: "after-start-cancel", ObservedAt: time.Now().UTC()}); err == nil {
		t.Fatal("old allow escaped barrier")
	}
	var once int
	if err = f.db.QueryRow(`SELECT COUNT(*) FROM native_tool_once WHERE operation_id=?`, parent.id).Scan(&once); err != nil || once != 1 {
		t.Fatalf("unknown parent once lost %d %v", once, err)
	}
	fresh := f.captured(t, "POST", `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"b"}}}`, "synthetic-session")
	go func() { siblingResults <- f.executor.Serve(context.Background(), fresh, httptest.NewRecorder()) }()
	f.approve(t, fresh.id)
	if terminal = <-siblingResults; terminal.Outcome != OutcomeCompleted {
		t.Fatalf("live session fresh review denied %+v", terminal)
	}
}
