package loopbackproxy

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
)

func TestDeferredConnectionCloseWinsBeforeBind(t *testing.T) {
	t.Parallel()
	handle := &deferredConnectionCloseHandle{}
	if err := handle.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	client, server := net.Pipe()
	defer client.Close()
	if err := handle.Bind(server); !errors.Is(err, ErrProxyStopping) {
		t.Fatalf("Bind after close = %v", err)
	}
	if _, err := server.Write([]byte("closed")); !errors.Is(err, net.ErrClosed) &&
		!errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("bound connection remains open: %v", err)
	}
}

func TestDeferredConnectionCloseClosesBoundSocketIdempotently(t *testing.T) {
	t.Parallel()
	handle := &deferredConnectionCloseHandle{}
	client, server := net.Pipe()
	defer client.Close()
	if err := handle.Bind(server); err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := server.Write([]byte("closed")); !errors.Is(err, net.ErrClosed) &&
		!errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("closed connection accepted a write: %v", err)
	}
}

func TestDeferredConnectionCloseRejectsASecondBind(t *testing.T) {
	t.Parallel()
	handle := &deferredConnectionCloseHandle{}
	firstClient, firstServer := net.Pipe()
	defer firstClient.Close()
	defer firstServer.Close()
	secondClient, secondServer := net.Pipe()
	defer secondClient.Close()
	defer secondServer.Close()
	if err := handle.Bind(firstServer); err != nil {
		t.Fatal(err)
	}
	if err := handle.Bind(secondServer); !errors.Is(err, errConnectionAlreadyBound) {
		t.Fatalf("second Bind = %v", err)
	}
}
