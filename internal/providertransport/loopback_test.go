package providertransport

import (
	"bufio"
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
	transport, err := newCleartextTransport(
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

func TestPrivateCleartextTransportRejectsPublicPeerBeforeHTTPWrite(t *testing.T) {
	t.Parallel()

	clientSide, serverSide := net.Pipe()
	defer serverSide.Close()
	connection := &countingPeerConnection{
		Conn: clientSide,
		remote: &net.TCPAddr{
			IP:   net.ParseIP("203.0.113.9"),
			Port: 8888,
		},
	}
	transport, err := newCleartextTransport(
		fixedConnectionDialer{connection: connection},
		DefaultTransportTimeouts(),
	)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := originidentity.ParseProviderOrigin("http://spark-2a59:8888")
	if err != nil {
		t.Fatal(err)
	}
	target := targetFromProviderOrigin(origin)
	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"http://spark-2a59:8888/v1/messages",
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
		t.Fatal("public peer reached private cleartext HTTP transport")
	}
	if writes := connection.writes.Load(); writes != 0 {
		t.Fatalf("public peer received %d HTTP writes", writes)
	}
}

func TestPrivateCleartextPeerAllowsDNSBoundPrivateAddress(t *testing.T) {
	t.Parallel()
	origin, err := originidentity.ParseProviderOrigin("http://spark-2a59:8888")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCleartextPeer(&net.TCPAddr{
		IP:   net.ParseIP("192.168.50.12"),
		Port: 8888,
	}, targetFromProviderOrigin(origin)); err != nil {
		t.Fatalf("private peer rejected: %v", err)
	}
}

func TestPrivateCleartextTransportWritesAfterPrivatePeerValidation(t *testing.T) {
	t.Parallel()

	clientSide, serverSide := net.Pipe()
	connection := &countingPeerConnection{
		Conn: clientSide,
		remote: &net.TCPAddr{
			IP:   net.ParseIP("192.168.50.12"),
			Port: 8888,
		},
	}
	transport, err := newCleartextTransport(
		fixedConnectionDialer{connection: connection},
		DefaultTransportTimeouts(),
	)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := originidentity.ParseProviderOrigin("http://spark-2a59:8888")
	if err != nil {
		t.Fatal(err)
	}

	requestObserved := make(chan *http.Request, 1)
	serverError := make(chan error, 1)
	go func() {
		defer serverSide.Close()
		request, readErr := http.ReadRequest(bufio.NewReader(serverSide))
		if readErr != nil {
			serverError <- readErr
			return
		}
		requestObserved <- request
		_, writeErr := serverSide.Write([]byte(
			"HTTP/1.1 204 No Content\r\nContent-Length: 0\r\n\r\n",
		))
		serverError <- writeErr
	}()

	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"http://spark-2a59:8888/v1/messages",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = origin.HTTPAuthority()
	request.Header.Set("Authorization", "Bearer private-relay-token")
	response, _, err := transport.RoundTrip(
		request,
		TransportDispatch{target: targetFromProviderOrigin(origin)},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-serverError; err != nil {
		t.Fatal(err)
	}
	observed := <-requestObserved
	if observed.Host != "spark-2a59:8888" ||
		observed.Header.Get("Authorization") != "Bearer private-relay-token" {
		t.Fatalf("private relay request = host %q, authorization %q", observed.Host, observed.Header.Get("Authorization"))
	}
	if connection.writes.Load() == 0 {
		t.Fatal("validated private peer received no HTTP request")
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
