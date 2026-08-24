package serverhost

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/instanceguard"
)

func TestServerHostRetainsGenerationUntilTimedOutShutdownCanFinish(t *testing.T) {
	t.Parallel()
	lockPath := filepath.Join(t.TempDir(), "server.lock")
	if err := os.Chmod(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatal(err)
	}
	guard, err := instanceguard.Acquire(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		close(entered)
		<-release
		writer.WriteHeader(http.StatusNoContent)
	})}
	host := &Host{
		guard: guard, listener: listener, server: server,
		shutdown: 50 * time.Millisecond,
		done:     make(chan struct{}),
	}
	go host.serve()
	requestDone := make(chan struct{})
	go func() {
		response, requestErr := http.Get("http://" + listener.Addr().String())
		if requestErr == nil {
			response.Body.Close()
		}
		close(requestDone)
	}()
	<-entered

	firstContext, cancelFirst := context.WithTimeout(context.Background(), 30*time.Millisecond)
	firstErr := host.Shutdown(firstContext)
	cancelFirst()
	if firstErr == nil {
		close(release)
		t.Fatal("timed-out shutdown succeeded")
	}
	probe, probeErr := instanceguard.Acquire(lockPath)
	if probeErr == nil {
		_ = probe.Release()
		close(release)
		t.Fatal("generation lock was released while a handler was still active")
	}
	if !errors.Is(probeErr, instanceguard.ErrAlreadyOwned) {
		close(release)
		t.Fatalf("generation probe error = %v", probeErr)
	}

	close(release)
	<-requestDone
	secondContext, cancelSecond := context.WithTimeout(context.Background(), time.Second)
	defer cancelSecond()
	if err := host.Shutdown(secondContext); err != nil {
		t.Fatalf("retry Shutdown() error = %v", err)
	}
	probe, err = instanceguard.Acquire(lockPath)
	if err != nil {
		t.Fatalf("generation lock remained after completed shutdown: %v", err)
	}
	if err := probe.Release(); err != nil {
		t.Fatal(err)
	}
}
