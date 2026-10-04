package loopbackproxy_test

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/nativemcp"
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
