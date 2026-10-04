package loopbackproxy_test

import (
	"context"
	"errors"
	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/nativemcp"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type nativeRawTerminalBarrier struct{ raw *rawevidence.Manager }

func (barrier nativeRawTerminalBarrier) PrepareManualCapture(ctx context.Context, id string) (manualcapture.TerminalEvidence, error) {
	return barrier.raw.PrepareTerminalScope(ctx, rawevidence.ScopeManualCapture, id)
}

func secondNativeStopCapture(t *testing.T, first *nativeFixture, managed bool) *nativeFixture {
	t.Helper()
	ctx := context.Background()
	second := *first
	source := captureassignment.SourceManualCreate
	var credential string
	var err error
	if managed {
		second.managedGrant, err = first.runs.Create(ctx, capturerun.CreateCommand{CWD: t.TempDir(), CanonicalExecutablePath: "/synthetic/codex", ExecutableLabel: "Synthetic B", CatalogRevision: 1, Lifetime: 5 * time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		credential = second.managedGrant.ProxyCapability.Value()
		source = captureassignment.SourceLaunch
	} else {
		second.manualGrant, err = first.manuals.Create(ctx, manualcapture.CreateCommand{Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "Synthetic B", ClientClass: manualcapture.ClientDesktopApp, Lifetime: manualcapture.LifetimeTemporary, ExpiresIn: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		credential = second.manualGrant.Credential.Value()
	}
	authorizer, err := captureadmission.NewAuthorizer(first.runs, first.manuals)
	if err != nil {
		t.Fatal(err)
	}
	proxyCredential, err := captureadmission.NewProxyCredential(credential)
	if err != nil {
		t.Fatal(err)
	}
	second.admission, err = authorizer.Authorize(ctx, proxyCredential)
	if err != nil {
		t.Fatal(err)
	}
	second.capture, err = second.admission.CaptureReference()
	if err != nil {
		t.Fatal(err)
	}
	second.credential = credential
	if _, err = first.assignments.Create(ctx, captureassignment.CreateCommand{Capture: second.capture, EnvironmentID: "native.synthetic", Source: source}); err != nil {
		t.Fatal(err)
	}
	return &second
}

func TestNativeCaptureStopClosesPendingReviewAndPreservesOtherCapture(t *testing.T) {
	for _, managed := range []bool{false, true} {
		name := "manual"
		if managed {
			name = "managed"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			revoker := &nativeFixtureRevoker{}
			a := newNativeFixtureMode(t, environment.ToolPolicyReview, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, &nativeHandlerSettings{raw: true, managed: managed, revocation: revoker})
			revoker.executor = a.executor
			b := secondNativeStopCapture(t, a, managed)
			if a.capture == b.capture || a.store != b.store || a.executor != b.executor || a.assignments != b.assignments {
				t.Fatal("two-Capture proof did not compose one actual runtime")
			}
			connA, connB := nativeHandshake(t, a), nativeHandshake(t, b)
			before := a.transport.sends()
			if before != 6 {
				t.Fatalf("two handshakes sends%d", before)
			}
			doneA, doneB := make(chan *http.Response, 1), make(chan *http.Response, 1)
			go func() {
				doneA <- nativeRequest(t, connA, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"},"_meta":{"callId":"capture-a-pending-stop"}}}`, "synthetic-session")
			}()
			callA := a.waitingCall(t)
			go func() {
				doneB <- nativeRequest(t, connB, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"b"},"_meta":{"callId":"capture-b-survives-stop"}}}`, "synthetic-session")
			}()
			callB := b.waitingCall(t)
			approvalA, err := a.store.NativeToolRepository().GetApproval(ctx, *callA.ApprovalID)
			if err != nil {
				t.Fatal(err)
			}
			approvalB, err := a.store.NativeToolRepository().GetApproval(ctx, *callB.ApprovalID)
			if err != nil {
				t.Fatal(err)
			}
			if approvalA.State != "pending" || approvalB.State != "pending" || a.transport.sends() != before {
				t.Fatal("Review was not actually pending before stop")
			}
			var observed captureidentity.Reference
			revoker.before = func(_ context.Context, c captureidentity.Reference, reason string) error {
				if reason == "capture_stopped" {
					observed = c
				}
				return nil
			}
			if managed {
				err = a.runs.Finish(ctx, a.managedGrant.Run.ID, a.managedGrant.ControlCapability)
			} else {
				id, _ := manualcapture.ParseID(a.manualGrant.Capture.ID)
				_, err = a.manuals.Revoke(ctx, manualcapture.RevokeCommand{Owner: manualcapture.NewLocalOwnerScope(), ID: id, ExpectedCredentialRevision: a.manualGrant.Capture.CredentialRevision})
			}
			if err != nil {
				t.Fatal(err)
			}
			if observed != a.capture {
				t.Fatal("stop notification was not scoped to A")
			}
			revoker.before = nil
			var responseA *http.Response
			select {
			case responseA = <-doneA:
			case <-time.After(2 * time.Second):
				t.Fatal("stopped pending Review did not close")
			}
			dataA, _ := io.ReadAll(responseA.Body)
			_ = responseA.Body.Close()
			if strings.Contains(string(dataA), `"result"`) {
				t.Fatal("stopped call returned an execution result")
			}
			closed, err := a.store.NativeToolRepository().GetApproval(ctx, approvalA.ID)
			if err != nil || closed.State != "canceled" {
				t.Fatalf("stopped approval %+v %v", closed, err)
			}
			if _, err = a.store.NativeToolRepository().DecideApproval(ctx, nativetool.DecisionInput{ApprovalID: closed.ID, ExpectedRevision: closed.Revision, Decision: nativetool.DecisionAllowOnce, IdempotencyKey: "late-stop-allow-once-synthetic", ObservedAt: time.Now().UTC()}); !errors.Is(err, nativetool.ErrConflict) {
				t.Fatalf("late Allow did not conflict with closed approval: %v", err)
			}
			if a.transport.sends() != before {
				t.Fatal("stopped or late-allowed A call reached outbound")
			}
			var onceA int
			if err = a.db.QueryRow(`SELECT count(*) FROM native_tool_once WHERE operation_id=?`, callA.ID.String()).Scan(&onceA); err != nil || onceA != 0 {
				t.Fatalf("stopped call once claim%d %v", onceA, err)
			}
			live, err := a.store.NativeToolRepository().GetApproval(ctx, approvalB.ID)
			if err != nil || live.State != "pending" || live.Revision != approvalB.Revision {
				t.Fatalf("A stop changed B question %+v %v", live, err)
			}
			if _, err = a.store.NativeToolRepository().DecideApproval(ctx, nativetool.DecisionInput{ApprovalID: live.ID, ExpectedRevision: live.Revision, Decision: nativetool.DecisionAllowOnce, IdempotencyKey: "survivor-allow-once-synthetic", ObservedAt: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			var responseB *http.Response
			select {
			case responseB = <-doneB:
			case <-time.After(2 * time.Second):
				t.Fatal("surviving Capture call did not finish")
			}
			dataB, _ := io.ReadAll(responseB.Body)
			_ = responseB.Body.Close()
			if responseB.StatusCode != 200 || !strings.Contains(string(dataB), "synthetic result") || a.transport.sends() != before+1 {
				t.Fatalf("B survival status%d body%s sends%d", responseB.StatusCode, dataB, a.transport.sends())
			}
			completed, err := a.store.NativeToolRepository().Get(ctx, callB.ID)
			if err != nil || completed.Capture != b.capture || completed.State != "completed" {
				t.Fatalf("B real completion %+v %v", completed, err)
			}
			var onceB int
			if err = a.db.QueryRow(`SELECT count(*) FROM native_tool_once WHERE operation_id=?`, callB.ID.String()).Scan(&onceB); err != nil || onceB != 1 {
				t.Fatalf("surviving B once claim%d %v", onceB, err)
			}
		})
	}
}

type nativeRunRawTerminalBarrier struct{ raw *rawevidence.Manager }

func (barrier nativeRunRawTerminalBarrier) PrepareManagedRun(ctx context.Context, id string) (capturerun.TerminalEvidence, error) {
	return barrier.raw.PrepareTerminalScope(ctx, rawevidence.ScopeManagedRun, id)
}

type nativeHeldStream struct {
	first  *strings.Reader
	closed chan struct{}
	once   sync.Once
}

func (body *nativeHeldStream) Read(data []byte) (int, error) {
	if body.first.Len() > 0 {
		return body.first.Read(data)
	}
	<-body.closed
	return 0, io.ErrClosedPipe
}
func (body *nativeHeldStream) Close() error { body.once.Do(func() { close(body.closed) }); return nil }

func TestNativeManualStopClosesActualStreamWithoutRawDrainDeadlock(t *testing.T) {
	for _, managed := range []bool{false, true} {
		name := "manual"
		if managed {
			name = "managed"
		}
		t.Run(name, func(t *testing.T) {
			revoker := &nativeFixtureRevoker{}
			f := newNativeFixtureMode(t, environment.ToolPolicyReview, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, &nativeHandlerSettings{raw: true, managed: managed, revocation: revoker})
			revoker.executor = f.executor
			secured := nativeHandshake(t, f)
			stream := &nativeHeldStream{first: strings.NewReader("id: stop-event\ndata: {\"jsonrpc\":\"2.0\",\"id\":44,\"result\":{}}\n\n"), closed: make(chan struct{})}
			t.Cleanup(func() {
				_ = stream.Close()
				_ = f.executor.Revoke(context.Background(), f.capture, nativemcp.RevocationCaptureStopped)
			})
			f.transport.mu.Lock()
			f.transport.handler = func(*http.Request, []byte) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: stream}, nil
			}
			f.transport.mu.Unlock()
			response := nativeRequest(t, secured, `{"jsonrpc":"2.0","id":44,"method":"ping"}`, "synthetic-session")
			if response.StatusCode != 200 {
				data, _ := io.ReadAll(response.Body)
				t.Fatalf("stream status%d body%s", response.StatusCode, data)
			}
			done := make(chan struct{})
			go func() { _, _ = io.ReadAll(response.Body); _ = response.Body.Close(); close(done) }()
			id, _ := manualcapture.ParseID(f.manualGrant.Capture.ID)
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			if managed {
				if err := f.runs.Finish(ctx, f.managedGrant.Run.ID, f.managedGrant.ControlCapability); err != nil {
					t.Fatalf("managed native stop: %v", err)
				}
			} else {
				if _, err := f.manuals.Revoke(ctx, manualcapture.RevokeCommand{Owner: manualcapture.NewLocalOwnerScope(), ID: id, ExpectedCredentialRevision: f.manualGrant.Capture.CredentialRevision}); err != nil {
					t.Fatalf("native stop blocked on Raw scope: %v", err)
				}
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("durably stopped native stream was not canceled")
			}
		})
	}
}
