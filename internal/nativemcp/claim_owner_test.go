package nativemcp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/originidentity"
)

func secondClaimCapture(t *testing.T, f *nativeFixture) *nativeFixture {
	t.Helper()
	ctx := context.Background()
	manuals, err := manualcapture.NewManager(ctx, manualcapture.DefaultOptions(f.store.ManualCaptureRepository()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manuals.Shutdown(ctx) })
	grant, err := manuals.Create(ctx, manualcapture.CreateCommand{Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "Independent claim fixture", ClientClass: manualcapture.ClientDesktopApp, Lifetime: manualcapture.LifetimeTemporary, ExpiresIn: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	runs, err := capturerun.NewManager(ctx, capturerun.DefaultOptions(f.store.CaptureRunRepository()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runs.Shutdown(ctx) })
	authorizer, err := captureadmission.NewAuthorizer(runs, manuals)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := captureadmission.NewProxyCredential(grant.Credential.Value())
	if err != nil {
		t.Fatal(err)
	}
	admission, err := authorizer.Authorize(ctx, credential)
	if err != nil {
		t.Fatal(err)
	}
	capture, err := admission.CaptureReference()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.assignments.Create(ctx, captureassignment.CreateCommand{Capture: capture, EnvironmentID: "native.synthetic", Source: captureassignment.SourceManualCreate}); err != nil {
		t.Fatal(err)
	}
	origin, _ := originidentity.ParseProviderOrigin("https://native.example")
	connection, err := f.assignments.RegisterProviderConnection(ctx, capture, "synthetic.connection", origin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(connection.Close)
	other := *f
	other.capture, other.admission = capture, admission
	return &other
}

func TestNativeRevokeProofExcludesOldOwnerOtherCaptureAndUnbound(t *testing.T) {
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	e := f.executor.(*executor)
	other := secondClaimCapture(t, f)
	other.handshake(t)
	old := f.captured(t, "POST", `{"jsonrpc":"2.0","id":700,"method":"ping"}`, "synthetic-session")
	held, finish := holdUnsentLifecycle(t, f, old)
	e.mu.Lock()
	oldOwner := e.scopes[f.capture.Key()]
	e.mu.Unlock()
	held.beforeVerify = func() {
		scope, err := held.Repository.GetScope(context.Background(), f.capture)
		if err != nil {
			t.Error(err)
			return
		}
		if err = held.Repository.Revoke(context.Background(), f.capture, scope.Revision, "revoked", time.Now().UTC()); err != nil {
			t.Error(err)
		}
	}
	finish()
	requireNativeClaimAfterDrain(t, e, old.id, true)
	// The wrapper sees an already revoked scope: no actual transaction/proof.
	if err := e.Revoke(context.Background(), f.capture, RevocationOwnerChanged); err != nil {
		t.Fatal(err)
	}
	requireNativeClaimAfterDrain(t, e, old.id, true)
	f.handshake(t) // Genuine EnsureScope creates a new epoch and new local key.
	e.mu.Lock()
	newOwner := e.scopes[f.capture.Key()]
	e.mu.Unlock()
	if newOwner == oldOwner || newOwner.claimKey == oldOwner.claimKey {
		t.Fatal("actual owner replacement absent")
	}
	receipt, outgoing, plan := prepareRealReceipt(t, f)
	borrowed := e.sendCommitted(context.Background(), outgoing, plan, receipt)
	if borrowed.err != nil || borrowed.claim.token == nil {
		t.Fatal("real borrowed receipt did not send", borrowed.err)
	}
	responseBytes, err := io.ReadAll(borrowed.response.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = borrowed.response.Body.Close()
	if err = held.Repository.Finish(context.Background(), receipt.OperationID(), nativetool.TerminalInput{Outcome: "completed", Reason: "completed", BytesIn: int64(len(responseBytes)), BytesOut: outgoing.ContentLength, ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	e.drainClaimConsumer(borrowed.claim)
	enteredA, enteredB := make(chan struct{}), make(chan struct{})
	releaseA, releaseB := make(chan struct{}), make(chan struct{})
	var onceA, onceB sync.Once
	release := func() { onceA.Do(func() { close(releaseA) }); onceB.Do(func() { close(releaseB) }) }
	f.transport.handler = func(_ *http.Request, body []byte) (*http.Response, error) {
		if strings.Contains(string(body), `"id":777`) {
			close(enteredA)
			<-releaseA
			return wireResponse(`{"jsonrpc":"2.0","id":777,"result":{}}`, false), nil
		}
		close(enteredB)
		<-releaseB
		return wireResponse(`{"jsonrpc":"2.0","id":888,"result":{}}`, false), nil
	}
	current := f.captured(t, "POST", `{"jsonrpc":"2.0","id":777,"method":"ping"}`, "synthetic-session")
	independent := other.captured(t, "POST", `{"jsonrpc":"2.0","id":888,"method":"ping"}`, "synthetic-session")
	doneA, doneB := make(chan Terminal, 1), make(chan Terminal, 1)
	go func() { doneA <- e.Serve(context.Background(), current, httptest.NewRecorder()) }()
	go func() { doneB <- e.Serve(context.Background(), independent, httptest.NewRecorder()) }()
	defer func() {
		release()
		for _, done := range []chan Terminal{doneA, doneB} {
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Error("owned transport consumer did not join")
			}
		}
	}()
	for _, entered := range []chan struct{}{enteredA, enteredB} {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("real current/independent transport did not enter")
		}
	}
	otherBefore, err := held.Repository.GetScope(context.Background(), other.capture)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.revokeOwner(context.Background(), f.capture, oldOwner, RevocationOwnerChanged); err != nil {
		t.Fatal(err)
	}
	if scope, err := held.Repository.GetScope(context.Background(), f.capture); err != nil || scope.State != "active" {
		t.Fatal("late old-owner wrapper fenced replacement", err)
	}
	if err = e.Revoke(context.Background(), f.capture, RevocationOwnerChanged); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	oldToken, ownToken, otherToken := e.claimed[old.id], e.claimed[current.id], e.claimed[independent.id]
	valid := oldToken != nil && !oldToken.physicalTerminal && ownToken != nil && ownToken.physicalTerminal && !ownToken.consumerDone && otherToken != nil && !otherToken.physicalTerminal && !otherToken.consumerDone && e.claimed[borrowed.claim.id] == borrowed.claim.token && !borrowed.claim.token.physicalTerminal && borrowed.claim.token.ownerKey == 0
	e.mu.Unlock()
	if !valid {
		t.Fatal("current Revoke proof crossed old owner/other Capture/unbound token boundary")
	}
	otherAfter, err := held.Repository.GetScope(context.Background(), other.capture)
	if err != nil || otherAfter != otherBefore {
		t.Fatal("current Revoke changed independent actual owner", err)
	}
	release()
	select {
	case result := <-doneA:
		doneA <- result
		if result.Outcome == OutcomeCompleted {
			t.Fatal("revoked current request became success")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("current request did not return")
	}
	select {
	case result := <-doneB:
		doneB <- result
		if result.Outcome != OutcomeCompleted {
			t.Fatal("independent valid owner failed", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("independent request did not return")
	}
	requireNativeClaimAfterDrain(t, e, current.id, false)
	requireNativeClaimAfterDrain(t, e, independent.id, false)
	requireNativeClaimAfterDrain(t, e, old.id, true)
}
