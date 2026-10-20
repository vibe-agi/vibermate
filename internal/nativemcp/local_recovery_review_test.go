package nativemcp

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
)

func TestLocalRecoveryNativeObserveWithoutBarrierDoesNotPrompt(t *testing.T) {
	f := newNativeFixtureMode(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	terminal, _ := f.serve(t, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"ordinary"}}}`, "synthetic-session")
	operation, err := f.authority.Get(t.Context(), terminal.OperationID)
	if terminal.Outcome != OutcomeCompleted || err != nil || operation.ApprovalID != nil || f.transport.sends() != 4 {
		t.Fatalf("ordinary native Observe unexpectedly required a decision: terminal=%+v operation=%+v sends=%d err=%v", terminal, operation, f.transport.sends(), err)
	}
}

func TestLocalRecoveryBarrierPreservesNativeStrictDenial(t *testing.T) {
	f := newNativeFixtureMode(t, environment.ToolPolicyStrict, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	scope, err := f.authority.GetScope(t.Context(), f.capture)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.authority.MarkReplayBarrier(t.Context(), f.capture, scope.Revision, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	terminal, _ := f.serve(t, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"strict"}}}`, "synthetic-session")
	operation, err := f.authority.Get(t.Context(), terminal.OperationID)
	if terminal.Outcome == OutcomeCompleted || err != nil || operation.ApprovalID != nil || f.transport.sends() != 3 {
		t.Fatalf("native recovery weakened Strict: terminal=%+v operation=%+v sends=%d err=%v", terminal, operation, f.transport.sends(), err)
	}
}

func TestLocalRecoveryBarrierShowsFreshNativeReviewUnderObserve(t *testing.T) {
	f := newNativeFixtureMode(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.executor.(*executor).dependencies.ContentProfiles = syntheticProfiles{}
	f.handshake(t)
	scope, err := f.authority.GetScope(t.Context(), f.capture)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.authority.MarkReplayBarrier(t.Context(), f.capture, scope.Revision, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	request := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"fresh"}}}`, "synthetic-session")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	results := make(chan Terminal, 1)
	go func() { results <- f.executor.Serve(ctx, request, httptest.NewRecorder()) }()
	approval := waitNativeQuestion(t, f, request.id)
	preview, err := f.executor.PendingPreview(t.Context(), f.capture, request.id, approval.BindingDigest)
	if err != nil || preview.OperationID != request.id || len(preview.Intents) != 1 || preview.Intents[0].ToolName != "read_file" {
		t.Fatalf("fresh recovery decision did not retain its safe native preview: %+v %v", preview, err)
	}
	if f.transport.sends() != 3 {
		t.Fatal("recovery native request reached transport before its fresh decision")
	}
	f.approve(t, request.id)
	select {
	case terminal := <-results:
		if terminal.Outcome != OutcomeCompleted || f.transport.sends() != 4 {
			t.Fatalf("fresh native decision did not release one request: %+v sends=%d", terminal, f.transport.sends())
		}
	case <-time.After(4 * time.Second):
		t.Fatal("fresh native decision did not finish")
	}
}
