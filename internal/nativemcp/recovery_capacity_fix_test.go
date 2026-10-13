package nativemcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
)

func TestNativePreStartRecoveryCancellationReusesCapacity(t *testing.T) {
	f := newNativeFixtureMode(t, environment.ToolPolicyObserve, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	f.handshake(t)
	f.transport.handler = func(*http.Request, []byte) (*http.Response, error) {
		return wireResponse("id: capacity\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":9,\"progress\":1}}\n\n", true), nil
	}
	origin := f.captured(t, "POST", `{"jsonrpc":"2.0","id":40,"method":"ping","params":{"_meta":{"progressToken":9}}}`, "synthetic-session")
	f.executor.Serve(context.Background(), origin, httptest.NewRecorder())
	if _, err := f.gate.Enter(context.Background(), f.gate.Snapshot().Revision); err != nil {
		t.Fatal(err)
	}
	before := f.transport.sends()
	for i := 0; i < 1025; i++ {
		get := f.captured(t, "GET", "", "synthetic-session")
		get.request.Header.Set("Last-Event-Id", "capacity")
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan Terminal, 1)
		go func() { done <- f.executor.Serve(ctx, get, httptest.NewRecorder()) }()
		deadline := time.Now().Add(time.Second)
		for f.gate.Snapshot().QueuedRequests != 1 {
			select {
			case result := <-done:
				cancel()
				t.Fatalf("recovery %d rejected before actual Offline wait: %+v", i, result)
			default:
			}
			if time.Now().After(deadline) {
				cancel()
				<-done
				t.Fatalf("recovery %d never waited", i)
			}
			time.Sleep(time.Millisecond)
		}
		cancel()
		<-done
		op, err := f.store.NativeToolRepository().Get(context.Background(), get.id)
		if err != nil || op.CompletedAt == nil {
			t.Fatalf("canceled recovery %d not durably terminal: %s %v", i, op.State, err)
		}
		var attempts int
		if err := f.db.QueryRow(`SELECT COUNT(*) FROM native_tool_once WHERE operation_id=?`, get.id).Scan(&attempts); err != nil || attempts != 0 {
			t.Fatalf("pre-Start cancellation sent: %d %v", attempts, err)
		}
	}
	if f.transport.sends() != before {
		t.Fatal("held recovery reached transport")
	}
	op, err := f.store.NativeToolRepository().Get(context.Background(), origin.id)
	if err != nil || op.State != "started" {
		t.Fatalf("child cancellation changed original: %s %v", op.State, err)
	}
}
