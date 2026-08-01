package desktophost

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

// Shutdown closes listener admission before stopping the server, so the
// server's own listener close finds it already closed. That is the intended
// order, not a failure, and reporting it as one makes a clean shutdown look
// broken under load.
func TestStoppingAServerWhoseListenerIsAlreadyClosedIsClean(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tracked := newTrackedListener(listener)
	server := &http.Server{
		Handler: http.HandlerFunc(func(
			writer http.ResponseWriter,
			_ *http.Request,
		) {
			writer.WriteHeader(http.StatusNoContent)
		}),
	}
	go func() {
		_ = server.Serve(tracked)
	}()
	time.Sleep(10 * time.Millisecond)

	// The real shutdown path closes admission first, so the server's own
	// listener close finds it already gone.
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := stopHTTPServer(ctx, server, tracked); err != nil {
		t.Fatalf("stopping an already-closed listener reported: %v", err)
	}
}
