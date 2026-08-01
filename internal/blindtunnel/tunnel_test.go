package blindtunnel_test

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/blindtunnel"
)

// Real loopback TCP rather than net.Pipe: production tunnels TCP, and a pipe
// supports neither half-close nor deadlines.
func pipePair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			accepted <- nil
			return
		}
		accepted <- connection
	}()
	dialed, err := net.DialTimeout(
		"tcp4",
		listener.Addr().String(),
		2*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	server := <-accepted
	if server == nil {
		t.Fatal("accept failed")
	}
	t.Cleanup(func() {
		_ = dialed.Close()
		_ = server.Close()
	})
	return dialed, server
}

// A blind tunnel copies bytes in both directions and never interprets them.
// It reports only counts, so no tunnelled byte can reach a record.
func TestTunnelCopiesBothDirectionsAndCountsOnly(t *testing.T) {
	t.Parallel()

	clientSide, proxyClient := pipePair(t)
	proxyUpstream, upstreamSide := pipePair(t)

	done := make(chan blindtunnel.Result, 1)
	go func() {
		result, _ := blindtunnel.Copy(
			context.Background(),
			proxyClient,
			proxyUpstream,
		)
		done <- result
	}()

	go func() {
		_, _ = clientSide.Write([]byte("client-to-upstream"))
	}()
	received := make([]byte, len("client-to-upstream"))
	if _, err := io.ReadFull(upstreamSide, received); err != nil {
		t.Fatal(err)
	}
	if string(received) != "client-to-upstream" {
		t.Fatalf("upstream received %q", received)
	}
	if _, err := upstreamSide.Write([]byte("upstream-back")); err != nil {
		t.Fatal(err)
	}
	back := make([]byte, len("upstream-back"))
	if _, err := io.ReadFull(clientSide, back); err != nil {
		t.Fatal(err)
	}
	if string(back) != "upstream-back" {
		t.Fatalf("client received %q", back)
	}
	_ = upstreamSide.Close()
	_ = clientSide.Close()

	select {
	case result := <-done:
		if result.BytesOut != int64(len("client-to-upstream")) {
			t.Fatalf("bytes out = %d", result.BytesOut)
		}
		if result.BytesIn != int64(len("upstream-back")) {
			t.Fatalf("bytes in = %d", result.BytesIn)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("tunnel did not finish")
	}
}

// Owner cancellation must drain an active tunnel rather than leaking either
// half.
func TestTunnelStopsOnOwnerCancellation(t *testing.T) {
	t.Parallel()

	_, proxyClient := pipePair(t)
	_, proxyUpstream := pipePair(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := blindtunnel.Copy(ctx, proxyClient, proxyUpstream)
		done <- err
	}()
	cancel()

	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("tunnel did not stop on cancellation")
	}
}

// A half-closed peer must not leave the other half running forever.
func TestTunnelFinishesWhenOnePeerCloses(t *testing.T) {
	t.Parallel()

	clientSide, proxyClient := pipePair(t)
	_, proxyUpstream := pipePair(t)

	done := make(chan struct{})
	go func() {
		_, _ = blindtunnel.Copy(
			context.Background(),
			proxyClient,
			proxyUpstream,
		)
		close(done)
	}()
	_ = clientSide.Close()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("tunnel did not finish after one peer closed")
	}
}
