package runlauncher

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/netip"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/flowingress"
)

type tunnelObservation struct {
	target, proxyAuth string
}

// echoConnectBackend stands in for the Runtime proxy: it accepts one CONNECT,
// records what arrived and echoes the tunneled bytes back.
func echoConnectBackend(t *testing.T) (string, <-chan tunnelObservation) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	observed := make(chan tunnelObservation, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		request, err := http.ReadRequest(reader)
		if err != nil {
			return
		}
		observed <- tunnelObservation{request.Host, request.Header.Get("Proxy-Authorization")}
		_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\n")
		_, _ = io.Copy(conn, reader)
	}()
	return "http://" + listener.Addr().String(), observed
}

// A flow from the platform capture layer reaches the Runtime through the
// run's real frontier: the frontier's child-facing credential admits it, the
// Runtime sees the run's own capability and the sniffed destination, and the
// client's bytes round-trip unchanged.
func TestFlowIngressReachesRuntimeThroughFrontier(t *testing.T) {
	backendOrigin, observed := echoConnectBackend(t)
	frontier := newTestLocalProxyFrontier(t, "generation-1", backendOrigin, "runtime-run-capability")
	ingress, err := flowingress.New(func() (flowingress.Proxy, error) {
		return flowingress.Proxy{Origin: frontier.Origin(), Token: frontier.Token()}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.Dial("tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- ingress.Serve(context.Background(), server) }()

	hello := recordClientHello(t, "api.anthropic.com")
	if err := flowingress.WriteHeader(client, flowingress.Header{
		Version: 1, Protocol: "tcp", Destination: netip.MustParseAddrPort("203.0.113.9:443"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write(hello); err != nil {
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	echoed := make([]byte, len(hello))
	if _, err := io.ReadFull(client, echoed); err != nil {
		t.Fatalf("read echoed ClientHello: %v", err)
	}
	if string(echoed) != string(hello) {
		t.Fatal("tunneled bytes changed on the way through the frontier")
	}
	select {
	case got := <-observed:
		if got.target != "api.anthropic.com:443" {
			t.Fatalf("Runtime CONNECT target = %q", got.target)
		}
		if got.proxyAuth != frontierBasic("capture", "runtime-run-capability") {
			t.Fatalf("Runtime saw proxy credential %q, want the run capability", got.proxyAuth)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Runtime saw no CONNECT")
	}
	client.Close()
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("ingress did not finish after the client closed")
	}
}

func recordClientHello(t *testing.T, serverName string) []byte {
	t.Helper()
	left, right := net.Pipe()
	defer left.Close()
	go func() {
		_ = tls.Client(left, &tls.Config{ServerName: serverName}).Handshake()
	}()
	_ = right.SetReadDeadline(time.Now().Add(5 * time.Second))
	header := make([]byte, 5)
	if _, err := io.ReadFull(right, header); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, int(header[3])<<8|int(header[4]))
	if _, err := io.ReadFull(right, body); err != nil {
		t.Fatal(err)
	}
	right.Close()
	return append(header, body...)
}
