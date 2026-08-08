package providertransport

import (
	"context"
	"net"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/vibe-agi/vibermate/internal/originidentity"
)

func TestLoopbackTransportRejectsChangedPeerBeforeHTTPWrite(t *testing.T) {
	t.Parallel()

	clientSide, serverSide := net.Pipe()
	defer serverSide.Close()
	connection := &countingPeerConnection{
		Conn: clientSide,
		remote: &net.TCPAddr{
			IP:   net.ParseIP("127.0.0.1"),
			Port: 23334,
		},
	}
	transport, err := newLoopbackTransport(
		fixedConnectionDialer{connection: connection},
		DefaultTransportTimeouts(),
	)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := originidentity.ParseProviderOrigin(
		"http://127.0.0.1:23333/v1",
	)
	if err != nil {
		t.Fatal(err)
	}
	target := targetFromProviderOrigin(origin)
	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"http://127.0.0.1:23333/v1/chat/completions",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = origin.HTTPAuthority()
	request.Header.Set("Authorization", "Bearer must-not-be-written")
	if _, _, err := transport.RoundTrip(
		request,
		TransportDispatch{target: target},
	); err == nil {
		t.Fatal("changed loopback peer reached HTTP transport")
	}
	if writes := connection.writes.Load(); writes != 0 {
		t.Fatalf("changed loopback peer received %d HTTP writes", writes)
	}
}

type fixedConnectionDialer struct {
	connection net.Conn
}

func (dialer fixedConnectionDialer) DialContext(
	context.Context,
	string,
	string,
) (net.Conn, error) {
	return dialer.connection, nil
}

type countingPeerConnection struct {
	net.Conn
	remote net.Addr
	writes atomic.Int32
}

func (connection *countingPeerConnection) RemoteAddr() net.Addr {
	return connection.remote
}

func (connection *countingPeerConnection) Write(value []byte) (int, error) {
	connection.writes.Add(1)
	return connection.Conn.Write(value)
}
