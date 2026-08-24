package runlauncher

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
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

func TestLocalServerRelayCloseRejectsConnectionReturnedByRacingAccept(t *testing.T) {
	t.Parallel()
	relaySide, clientSide := net.Pipe()
	listener := &racingRelayListener{
		connection:  relaySide,
		accept:      make(chan struct{}),
		closeCalled: make(chan struct{}),
	}
	dialer := &pipeServerDialer{remotes: make(chan net.Conn, 1)}
	relay := &localServerRelay{
		listener: listener,
		dialer:   dialer,
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
