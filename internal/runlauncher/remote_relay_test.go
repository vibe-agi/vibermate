package runlauncher

import (
	"context"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/serverconnection"
	"github.com/vibe-agi/vibermate/internal/servertransport"
)

func TestLocalServerRelayClosesClientWhenServerEndsFirst(t *testing.T) {
	t.Parallel()
	dialer := &pipeServerDialer{remotes: make(chan net.Conn, 1)}
	relay, err := startLocalServerRelay(dialer)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()

	client, err := net.Dial("tcp", strings.TrimPrefix(relay.Origin(), "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	remote := <-dialer.remotes
	if err := remote.Close(); err != nil {
		t.Fatal(err)
	}
	if err := client.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 1)
	_, err = client.Read(buffer)
	if networkErr, ok := err.(net.Error); ok && networkErr.Timeout() {
		t.Fatal("client remained open after Runtime Server closed the relay stream")
	}
	if err == nil {
		t.Fatal("client read succeeded after Runtime Server closed the relay stream")
	}
}

func TestLocalServerRelayServerEOFUnblocksPendingServerWrite(t *testing.T) {
	t.Parallel()
	serverSide, peer := net.Pipe()
	defer peer.Close()
	server := &eofBlockedWriteConn{
		Conn: serverSide, writeStarted: make(chan struct{}), writeReturned: make(chan struct{}),
		readEOF: make(chan struct{}), closed: make(chan struct{}),
	}
	relay, err := startLocalServerRelay(oneServerDialer{connection: server})
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	client, err := net.Dial("tcp", strings.TrimPrefix(relay.Origin(), "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Write([]byte("synthetic request")); err != nil {
		t.Fatal(err)
	}
	awaitRelayEvent(t, server.writeStarted, "relay did not start forwarding the client request")
	close(server.readEOF)
	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("client read after server EOF = %v, want EOF", err)
	}
	awaitRelayEvent(t, server.closed, "server connection remained open after its read direction ended")
	awaitRelayEvent(t, server.writeReturned, "server write remained blocked after its read direction ended")
	closed := make(chan struct{})
	go func() {
		relay.Close()
		close(closed)
	}()
	awaitRelayEvent(t, closed, "relay forwarding lifecycle did not finish after server EOF")
}

func TestLocalServerRelayCloseCancelsPendingTLSHandshake(t *testing.T) {
	t.Parallel()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		if peer, err := listener.Accept(); err == nil {
			accepted <- peer
		}
	}()
	target, err := serverconnection.ParseTarget("https://" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	transport, err := servertransport.Open(servertransport.Options{
		Target: target, TrustDirectory: filepath.Join(t.TempDir(), "trust"),
		Clock: fixedRemoteClock{now: time.Now()}, Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	relay, err := startLocalServerRelay(transport)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	client, err := net.Dial("tcp", strings.TrimPrefix(relay.Origin(), "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var peer net.Conn
	select {
	case peer = <-accepted:
	case <-time.After(time.Second):
		t.Fatal("relay did not establish the TLS peer connection")
	}
	defer peer.Close()
	if err := peer.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	// Consume the TLS record header to establish that the handshake has begun,
	// then leave the real transport waiting for a ServerHello.
	if _, err := io.ReadFull(peer, make([]byte, 5)); err != nil {
		t.Fatalf("read TLS handshake header: %v", err)
	}
	closed := make(chan struct{})
	go func() {
		relay.Close()
		close(closed)
	}()
	awaitRelayEvent(t, closed, "relay Close did not cancel the pending TLS handshake")
	if _, err := io.Copy(io.Discard, peer); err != nil {
		t.Fatalf("TLS peer did not observe connection closure after relay Close: %v", err)
	}
	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("client read after relay Close = %v, want EOF", err)
	}
}

func TestLocalServerRelayStreamsBothDirectionsAcrossClientHalfClose(t *testing.T) {
	t.Parallel()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	peer, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	relay, err := startLocalServerRelay(oneServerDialer{connection: server})
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	client, err := net.DialTCP("tcp4", nil, relay.listener.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for _, connection := range []net.Conn{client, peer} {
		if err := connection.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := io.WriteString(client, "request"); err != nil {
		t.Fatal(err)
	}
	request := make([]byte, len("request"))
	if _, err := io.ReadFull(peer, request); err != nil || string(request) != "request" {
		t.Fatalf("server received %q, %v", request, err)
	}
	if _, err := io.WriteString(peer, "stream"); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, len("stream"))
	if _, err := io.ReadFull(client, response); err != nil || string(response) != "stream" {
		t.Fatalf("client received %q before half-close, %v", response, err)
	}
	if err := client.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if remaining, err := io.ReadAll(peer); err != nil || len(remaining) != 0 {
		t.Fatalf("server read after client half-close = %q, %v; want EOF", remaining, err)
	}
	if _, err := io.WriteString(peer, "final response"); err != nil {
		t.Fatalf("server could not respond after client half-close: %v", err)
	}
	if err := peer.Close(); err != nil {
		t.Fatal(err)
	}
	if remaining, err := io.ReadAll(client); err != nil || string(remaining) != "final response" {
		t.Fatalf("client read after half-close = %q, %v", remaining, err)
	}
}

func TestLocalServerRelayCloseClosesConnectionReturnedAfterCancellation(t *testing.T) {
	t.Parallel()
	server, peer := net.Pipe()
	defer peer.Close()
	dialer := &cancelledServerDialer{
		connection: server, started: make(chan struct{}), cancelled: make(chan struct{}),
		returnConnection: make(chan struct{}),
	}
	relay, err := startLocalServerRelay(dialer)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(dialer.returnConnection) }) }
	defer release()
	client, err := net.Dial("tcp", strings.TrimPrefix(relay.Origin(), "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	awaitRelayEvent(t, dialer.started, "relay did not begin dialing")
	if err := peer.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() {
		relay.Close()
		close(closed)
	}()
	awaitRelayEvent(t, dialer.cancelled, "relay did not cancel its pending dial")
	release()
	awaitRelayEvent(t, closed, "relay Close did not join a dial returning after cancellation")
	if _, err := peer.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("server peer read after relay Close = %v, want EOF", err)
	}
}

type cancelledServerDialer struct {
	connection       net.Conn
	started          chan struct{}
	cancelled        chan struct{}
	returnConnection chan struct{}
}

func (dialer *cancelledServerDialer) Dial(ctx context.Context) (net.Conn, error) {
	close(dialer.started)
	select {
	case <-ctx.Done():
		close(dialer.cancelled)
	case <-dialer.returnConnection:
		return dialer.connection, nil
	}
	<-dialer.returnConnection
	return dialer.connection, nil
}

// The external peer never reads, so Write blocks until the relay closes the
// connection. Its read direction independently reports the server's EOF.
type eofBlockedWriteConn struct {
	net.Conn
	writeStarted  chan struct{}
	writeReturned chan struct{}
	readEOF       chan struct{}
	closed        chan struct{}
	closeOnce     sync.Once
}

func (connection *eofBlockedWriteConn) Read([]byte) (int, error) {
	select {
	case <-connection.readEOF:
		return 0, io.EOF
	case <-connection.closed:
		return 0, net.ErrClosed
	}
}

func (connection *eofBlockedWriteConn) Write(buffer []byte) (int, error) {
	close(connection.writeStarted)
	defer close(connection.writeReturned)
	return connection.Conn.Write(buffer)
}

func (connection *eofBlockedWriteConn) Close() error {
	connection.closeOnce.Do(func() {
		_ = connection.Conn.Close()
		close(connection.closed)
	})
	return nil
}

type oneServerDialer struct{ connection net.Conn }

func (dialer oneServerDialer) Dial(context.Context) (net.Conn, error) {
	return dialer.connection, nil
}

func awaitRelayEvent(t *testing.T, event <-chan struct{}, failure string) {
	t.Helper()
	select {
	case <-event:
	case <-time.After(time.Second):
		t.Fatal(failure)
	}
}

func TestLocalServerRelayCloseRejectsConnectionReturnedByRacingAccept(t *testing.T) {
	t.Parallel()
	relaySide, clientSide := net.Pipe()
	listener := &racingRelayListener{
		connection:  relaySide,
		accept:      make(chan struct{}),
		closeCalled: make(chan struct{}),
	}
	dialer := &pipeServerDialer{remotes: make(chan net.Conn, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	relay := &localServerRelay{
		listener: listener,
		dialer:   dialer,
		ctx:      ctx,
		cancel:   cancel,
		done:     make(chan struct{}),
		active:   make(map[net.Conn]struct{}),
	}
	go relay.accept()

	closed := make(chan struct{})
	go func() {
		relay.Close()
		close(closed)
	}()
	<-listener.closeCalled
	close(listener.accept)

	select {
	case <-closed:
	case <-time.After(500 * time.Millisecond):
		_ = clientSide.Close()
		select {
		case remote := <-dialer.remotes:
			_ = remote.Close()
		default:
		}
		<-closed
		t.Fatal("relay Close hung on a connection returned concurrently by Accept")
	}
	_ = clientSide.Close()
	select {
	case remote := <-dialer.remotes:
		_ = remote.Close()
	default:
	}
}

type pipeServerDialer struct{ remotes chan net.Conn }

func (dialer *pipeServerDialer) Dial(context.Context) (net.Conn, error) {
	server, remote := net.Pipe()
	dialer.remotes <- remote
	return server, nil
}

type racingRelayListener struct {
	connection  net.Conn
	accept      chan struct{}
	closeCalled chan struct{}
	once        sync.Once
	mu          sync.Mutex
	returned    bool
}

func (listener *racingRelayListener) Accept() (net.Conn, error) {
	listener.mu.Lock()
	if listener.returned {
		listener.mu.Unlock()
		return nil, net.ErrClosed
	}
	listener.returned = true
	listener.mu.Unlock()
	<-listener.accept
	return listener.connection, nil
}

func (listener *racingRelayListener) Close() error {
	listener.once.Do(func() { close(listener.closeCalled) })
	return nil
}

func (*racingRelayListener) Addr() net.Addr { return relayTestAddress("relay") }

type relayTestAddress string

func (address relayTestAddress) Network() string { return "test" }
func (address relayTestAddress) String() string  { return string(address) }

var _ net.Listener = (*racingRelayListener)(nil)
var _ serverStreamDialer = (*pipeServerDialer)(nil)
