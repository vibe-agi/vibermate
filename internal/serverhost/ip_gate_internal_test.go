package serverhost

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/ipallowlist"
)

func gateNow() time.Time { return time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC) }

func mustAllowlist(t *testing.T, entries ...string) ipallowlist.List {
	t.Helper()
	list, err := ipallowlist.Parse(entries)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// peerRewritingListener accepts real loopback connections but reports the
// next queued address as their peer, so tests can act as any client.
type peerRewritingListener struct {
	net.Listener
	mu    sync.Mutex
	peers []netip.Addr
}

func (listener *peerRewritingListener) connectAs(address string) {
	listener.mu.Lock()
	defer listener.mu.Unlock()
	listener.peers = append(listener.peers, netip.MustParseAddr(address))
}

func (listener *peerRewritingListener) Accept() (net.Conn, error) {
	conn, err := listener.Listener.Accept()
	if err != nil {
		return nil, err
	}
	listener.mu.Lock()
	defer listener.mu.Unlock()
	if len(listener.peers) == 0 {
		_ = conn.Close()
		return nil, errors.New("test connected without a queued peer address")
	}
	peer := listener.peers[0]
	listener.peers = listener.peers[1:]
	return &rewrittenPeerConn{Conn: conn, peer: net.TCPAddrFromAddrPort(netip.AddrPortFrom(peer, 40000))}, nil
}

type rewrittenPeerConn struct {
	net.Conn
	peer net.Addr
}

func (conn *rewrittenPeerConn) RemoteAddr() net.Addr { return conn.peer }

func newPeerRewritingListener(t *testing.T) *peerRewritingListener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return &peerRewritingListener{Listener: listener}
}

func serveHello(t *testing.T, listener net.Listener) {
	t.Helper()
	server := &http.Server{
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(writer, "hello")
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
}

// get sends one HTTP/1.1 request on a fresh connection and returns the body,
// or the error that ended the connection.
func get(t *testing.T, address string) (string, error) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: server\r\nConnection: close\r\n\r\n"); err != nil {
		return "", err
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	return string(body), err
}

func TestGateRefusesClientsOutsideTheAllowlistBeforeHTTP(t *testing.T) {
	t.Parallel()

	gate := newConnectionGate(mustAllowlist(t, "203.0.113.0/24"), ipallowlist.List{}, gateNow)
	peers := newPeerRewritingListener(t)
	serveHello(t, gate.listen(peers, false))

	peers.connectAs("203.0.113.9")
	if body, err := get(t, peers.Addr().String()); err != nil || body != "hello" {
		t.Fatalf("allowed client got %q, %v", body, err)
	}
	peers.connectAs("198.51.100.4")
	if body, err := get(t, peers.Addr().String()); err == nil {
		t.Fatalf("refused client got a response %q", body)
	}
	peers.connectAs("127.0.0.1")
	if body, err := get(t, peers.Addr().String()); err != nil || body != "hello" {
		t.Fatalf("loopback client got %q, %v", body, err)
	}

	refusals := gate.refusals()
	if refusals.count != 1 || refusals.lastAddress != netip.MustParseAddr("198.51.100.4") ||
		!refusals.lastAt.Equal(gateNow()) {
		t.Fatalf("refusals = %+v", refusals)
	}
}

func TestSavingAListClosesConnectionsItNoLongerAllows(t *testing.T) {
	t.Parallel()

	gate := newConnectionGate(ipallowlist.List{}, ipallowlist.List{}, gateNow)
	peers := newPeerRewritingListener(t)
	gated := gate.listen(peers, false)
	accepted := make(chan net.Conn, 2)
	go func() {
		for {
			conn, err := gated.Accept()
			if err != nil {
				return
			}
			accepted <- conn
		}
	}()

	dial := func(address string) (client, server net.Conn) {
		peers.connectAs(address)
		client, err := net.Dial("tcp", peers.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Close() })
		select {
		case server = <-accepted:
		case <-time.After(5 * time.Second):
			t.Fatal("connection was not accepted")
		}
		return client, server
	}
	keptClient, _ := dial("203.0.113.9")
	cutClient, _ := dial("198.51.100.4")

	gate.update(mustAllowlist(t, "203.0.113.0/24"))

	_ = cutClient.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := cutClient.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("connection outside the new list stayed open: %v", err)
	}
	_ = keptClient.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	var timeout net.Error
	if _, err := keptClient.Read(make([]byte, 1)); !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("connection inside the new list was closed: %v", err)
	}
	gate.mu.Lock()
	open := len(gate.open)
	gate.mu.Unlock()
	if open != 1 {
		t.Fatalf("gate tracks %d open connections, want 1", open)
	}
}

func selfSignedCertificate(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "runtime.example.com"},
		DNSNames:     []string{"runtime.example.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func TestTLSALPNModeLetsOnlyACMEValidationThroughForRefusedClients(t *testing.T) {
	t.Parallel()

	gate := newConnectionGate(mustAllowlist(t, "203.0.113.0/24"), ipallowlist.List{}, gateNow)
	peers := newPeerRewritingListener(t)
	configuration := gate.guardTLS(&tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{selfSignedCertificate(t)},
		NextProtos:   []string{"http/1.1", acmeTLSALPNProtocol},
	})
	serveHello(t, tls.NewListener(gate.listen(peers, true), configuration))

	handshake := func(peer string, protocols ...string) (*tls.Conn, error) {
		peers.connectAs(peer)
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp",
			peers.Addr().String(), &tls.Config{
				ServerName: "runtime.example.com", InsecureSkipVerify: true, // test certificate
				NextProtos: protocols, MinVersion: tls.VersionTLS13,
			})
		if err == nil {
			t.Cleanup(func() { _ = conn.Close() })
		}
		return conn, err
	}

	if _, err := handshake("198.51.100.4", "http/1.1"); err == nil {
		t.Fatal("a refused client completed an HTTPS handshake")
	}
	acme, err := handshake("198.51.100.5", acmeTLSALPNProtocol)
	if err != nil {
		t.Fatalf("ACME validation handshake failed: %v", err)
	}
	if acme.ConnectionState().NegotiatedProtocol != acmeTLSALPNProtocol {
		t.Fatalf("negotiated %q", acme.ConnectionState().NegotiatedProtocol)
	}
	// The validation connection cannot carry HTTP: net/http closes it.
	_ = acme.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.WriteString(acme, "GET / HTTP/1.1\r\nHost: runtime.example.com\r\n\r\n")
	if reply, err := io.ReadAll(acme); err != nil || strings.Contains(string(reply), "hello") {
		t.Fatalf("ACME validation connection served HTTP: %q, %v", reply, err)
	}

	allowed, err := handshake("203.0.113.9", "http/1.1")
	if err != nil {
		t.Fatalf("allowed client handshake failed: %v", err)
	}
	_ = allowed.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.WriteString(allowed, "GET / HTTP/1.1\r\nHost: runtime.example.com\r\nConnection: close\r\n\r\n")
	if reply, err := io.ReadAll(allowed); err != nil || !strings.Contains(string(reply), "hello") {
		t.Fatalf("allowed client reply = %q, %v", reply, err)
	}
	if refusals := gate.refusals(); refusals.count != 1 ||
		refusals.lastAddress != netip.MustParseAddr("198.51.100.4") {
		t.Fatalf("refusals = %+v", refusals)
	}
}

func serveRemoteAddress(t *testing.T, listener net.Listener) {
	t.Helper()
	server := &http.Server{
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			_, _ = io.WriteString(writer, request.RemoteAddr)
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
}

// send writes raw bytes, such as a PROXY header, then one HTTP request, and
// returns the response body or the error that ended the connection.
func send(t *testing.T, address, prefix string) (string, error) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(conn, prefix+"GET / HTTP/1.1\r\nHost: server\r\nConnection: close\r\n\r\n"); err != nil {
		return "", err
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	return string(body), err
}

func TestTrustedLoadBalancerNamesTheClient(t *testing.T) {
	t.Parallel()

	gate := newConnectionGate(
		mustAllowlist(t, "203.0.113.0/24"), mustAllowlist(t, "10.0.0.0/24"), gateNow,
	)
	peers := newPeerRewritingListener(t)
	serveRemoteAddress(t, gate.listen(peers, false))
	address := peers.Addr().String()

	peers.connectAs("10.0.0.5")
	if body, err := send(t, address, "PROXY TCP4 203.0.113.9 192.0.2.1 51000 443\r\n"); err != nil ||
		body != "203.0.113.9:51000" {
		t.Fatalf("relayed allowed client: %q, %v", body, err)
	}
	peers.connectAs("10.0.0.5")
	if body, err := send(t, address, "PROXY TCP4 198.51.100.4 192.0.2.1 51000 443\r\n"); err == nil {
		t.Fatalf("relayed refused client got %q", body)
	}
	if refusals := gate.refusals(); refusals.count != 1 ||
		refusals.lastAddress != netip.MustParseAddr("198.51.100.4") || refusals.proxyHeaderProblems != 0 {
		t.Fatalf("refusals after relayed clients = %+v", refusals)
	}

	// A client that reaches the Server directly cannot name an address.
	peers.connectAs("198.51.100.7")
	if body, err := send(t, address, "PROXY TCP4 203.0.113.9 192.0.2.1 51000 443\r\n"); err == nil {
		t.Fatalf("a direct client forged its address and got %q", body)
	}
	if refusals := gate.refusals(); refusals.count != 2 ||
		refusals.lastAddress != netip.MustParseAddr("198.51.100.7") {
		t.Fatalf("refusals after a forged header = %+v", refusals)
	}
}

func TestLoadBalancerWithoutAHeaderNeverWidensAccess(t *testing.T) {
	t.Parallel()

	gate := newConnectionGate(
		mustAllowlist(t, "203.0.113.0/24"), mustAllowlist(t, "10.0.0.0/24"), gateNow,
	)
	peers := newPeerRewritingListener(t)
	serveRemoteAddress(t, gate.listen(peers, false))
	address := peers.Addr().String()

	// No header: judged as the load balancer itself, which is not allowed.
	peers.connectAs("10.0.0.5")
	if body, err := send(t, address, ""); err == nil {
		t.Fatalf("a relayed connection without a header got %q", body)
	}
	// A malformed header is closed.
	peers.connectAs("10.0.0.5")
	if body, err := send(t, address, "PROXY TCP4 not-an-address\r\n"); err == nil {
		t.Fatalf("a malformed header got %q", body)
	}
	refusals := gate.refusals()
	if refusals.proxyHeaderProblems != 2 || refusals.count != 1 ||
		refusals.lastAddress != netip.MustParseAddr("10.0.0.5") {
		t.Fatalf("refusals = %+v", refusals)
	}

	// A load balancer on this machine gets no loopback exception without a
	// header, so a missing header still fails closed.
	localGate := newConnectionGate(
		mustAllowlist(t, "203.0.113.0/24"), mustAllowlist(t, "127.0.0.2"), gateNow,
	)
	localPeers := newPeerRewritingListener(t)
	serveRemoteAddress(t, localGate.listen(localPeers, false))
	localPeers.connectAs("127.0.0.2")
	if body, err := send(t, localPeers.Addr().String(), ""); err == nil {
		t.Fatalf("a local load balancer without a header got %q", body)
	}
	if local := localGate.refusals(); local.count != 1 || local.proxyHeaderProblems != 1 {
		t.Fatalf("local load balancer refusals = %+v", local)
	}

	// The load balancer's own health check is allowed and not counted.
	local := v2LocalHeader()
	peers.connectAs("10.0.0.5")
	if body, err := send(t, address, local); err != nil || !strings.HasPrefix(body, "10.0.0.5:") {
		t.Fatalf("LOCAL health check: %q, %v", body, err)
	}
	// A TCP health check connects and closes without sending anything.
	peers.connectAs("10.0.0.5")
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	time.Sleep(100 * time.Millisecond)
	if after := gate.refusals(); after.count != refusals.count ||
		after.proxyHeaderProblems != refusals.proxyHeaderProblems {
		t.Fatalf("health checks were counted: %+v", after)
	}
}

func v2LocalHeader() string {
	return "\r\n\r\n\x00\r\nQUIT\n\x20\x00\x00\x00"
}

func TestUnknownProxySourceDoesNotGetTheHealthCheckException(t *testing.T) {
	t.Parallel()
	for _, peer := range []string{"10.0.0.5", "127.0.0.2"} {
		for name, header := range map[string]string{
			"v1 unknown":     "PROXY UNKNOWN\r\n",
			"v2 unspecified": "\r\n\r\n\x00\r\nQUIT\n\x21\x00\x00\x00",
		} {
			t.Run(peer+"/"+name, func(t *testing.T) {
				gate := newConnectionGate(mustAllowlist(t, "203.0.113.0/24"), mustAllowlist(t, peer), gateNow)
				peers := newPeerRewritingListener(t)
				serveHello(t, gate.listen(peers, false))
				peers.connectAs(peer)
				if body, err := send(t, peers.Addr().String(), header); err == nil {
					t.Fatalf("unknown client bypassed allowlist: %q", body)
				}
				if got := gate.refusals(); got.count != 1 || got.proxyHeaderProblems != 1 {
					t.Fatalf("missing source was not reported: %+v", got)
				}
				// An explicitly permitted balancer can still use unknown source
				// headers, but its connections remain subject to later updates.
				gate.update(mustAllowlist(t, peer))
				peers.connectAs(peer)
				if body, err := send(t, peers.Addr().String(), header); err != nil || body != "hello" {
					t.Fatalf("allowed balancer failed: %q, %v", body, err)
				}
			})
		}
	}
}

func TestUpdatedGateRechecksUntrackedAndUnknownSourceConnections(t *testing.T) {
	for _, lateTrack := range []bool{false, true} {
		for _, address := range []string{"198.51.100.4", "127.0.0.2"} {
			gate := newConnectionGate(ipallowlist.List{}, ipallowlist.List{}, gateNow)
			client, server := net.Pipe()
			defer client.Close()
			conn := &gatedConn{Conn: server, gate: gate, address: netip.MustParseAddr(address)}
			if conn.address.IsLoopback() {
				conn.balancer = conn.address
			}
			conn.admitted.Store(true)
			if !lateTrack {
				gate.track(conn)
			}
			gate.update(mustAllowlist(t, "203.0.113.0/24"))
			if lateTrack {
				gate.track(conn)
			}
			_ = client.SetReadDeadline(time.Now().Add(time.Second))
			if _, err := client.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
				t.Fatalf("late=%t address=%s escaped updated policy: %v", lateTrack, address, err)
			}
			gate.track(conn) // A resolve finishing after Close must not leak a tracked connection.
			if len(gate.open) != 0 {
				t.Fatal("closed connection was tracked again")
			}
		}
	}
}
