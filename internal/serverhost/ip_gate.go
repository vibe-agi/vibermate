package serverhost

import (
	"bufio"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vibe-agi/vibermate/internal/ipallowlist"
	"github.com/vibe-agi/vibermate/internal/proxyprotocol"
)

const (
	// acmeTLSALPNProtocol is the only protocol an ACME TLS-ALPN-01 validation
	// handshake offers (RFC 8737).
	acmeTLSALPNProtocol = "acme-tls/1"
	// proxyHeaderTimeout bounds how long a trusted load balancer may take to
	// send the PROXY header that names the client it relays.
	proxyHeaderTimeout = 5 * time.Second
)

var errClientAddressRefused = errors.New("client address is not on the Runtime Server IP allowlist")

// connectionGate enforces the Server IP Allowlist on every connection to the
// Server's listener: the Web workbench, the API, CLI login and member Agent
// traffic all share it. A client is judged by its TCP peer address, or, only
// when that peer is a load balancer the operator listed as trusted, by the
// address in the PROXY protocol header the load balancer sends. HTTP headers
// are never trusted.
type connectionGate struct {
	list    atomic.Pointer[ipallowlist.List]
	trusted ipallowlist.List
	now     func() time.Time

	mu   sync.Mutex
	open map[*gatedConn]struct{}

	refusedCount        atomic.Uint64
	lastRefusal         atomic.Pointer[ipRefusal]
	proxyHeaderProblems atomic.Uint64
}

type ipRefusal struct {
	address netip.Addr
	at      time.Time
}

// ipRefusals summarizes what the gate refused since the Server started.
type ipRefusals struct {
	count       uint64
	lastAddress netip.Addr
	lastAt      time.Time
	// proxyHeaderProblems counts connections from a trusted load balancer
	// without a valid PROXY header, the sign of a load balancer that is not
	// configured to send one.
	proxyHeaderProblems uint64
}

func newConnectionGate(
	list ipallowlist.List,
	trusted ipallowlist.List,
	now func() time.Time,
) *connectionGate {
	gate := &connectionGate{trusted: trusted, now: now, open: make(map[*gatedConn]struct{})}
	gate.list.Store(&list)
	return gate
}

func (gate *connectionGate) allows(address netip.Addr) bool {
	return gate.list.Load().Allows(address)
}

// update puts a new list in effect and closes admitted connections it no
// longer allows, including long-lived Agent tunnels.
func (gate *connectionGate) update(list ipallowlist.List) {
	gate.list.Store(&list)
	gate.mu.Lock()
	var refused []*gatedConn
	for conn := range gate.open {
		if conn.admitted.Load() && !conn.balancerOwn && !list.Allows(conn.address) {
			refused = append(refused, conn)
		}
	}
	gate.mu.Unlock()
	for _, conn := range refused {
		_ = conn.Close()
	}
}

func (gate *connectionGate) refuse(address netip.Addr) {
	gate.refusedCount.Add(1)
	gate.lastRefusal.Store(&ipRefusal{address: address.Unmap(), at: gate.now().UTC()})
}

func (gate *connectionGate) refusals() ipRefusals {
	summary := ipRefusals{
		count:               gate.refusedCount.Load(),
		proxyHeaderProblems: gate.proxyHeaderProblems.Load(),
	}
	if last := gate.lastRefusal.Load(); last != nil {
		summary.lastAddress, summary.lastAt = last.address, last.at
	}
	return summary
}

// listen wraps the Server's TCP listener. A refused direct client is
// disconnected before TLS or HTTP. With deferToTLS, the decision moves into
// the TLS handshake (see guardTLS) so an ACME TLS-ALPN validation can still
// reach the listener from any address.
func (gate *connectionGate) listen(listener net.Listener, deferToTLS bool) net.Listener {
	return &gatedListener{Listener: listener, gate: gate, deferToTLS: deferToTLS}
}

// guardTLS refuses the handshake of a client outside the allowlist unless it
// is an ACME TLS-ALPN validation. Such a handshake negotiates acme-tls/1, and
// net/http closes a connection with a negotiated protocol it does not serve,
// so it can never carry an HTTP request.
func (gate *connectionGate) guardTLS(config *tls.Config) *tls.Config {
	guarded := config.Clone()
	next := config.GetConfigForClient
	guarded.GetConfigForClient = func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		if conn, ok := hello.Conn.(*gatedConn); ok {
			if err := conn.resolve(); err != nil {
				return nil, err
			}
			if !conn.admitted.Load() {
				if gate.allows(conn.address) {
					conn.admitted.Store(true)
				} else if len(hello.SupportedProtos) != 1 ||
					hello.SupportedProtos[0] != acmeTLSALPNProtocol {
					gate.refuse(conn.address)
					return nil, errClientAddressRefused
				}
			}
		}
		if next != nil {
			return next(hello)
		}
		return nil, nil
	}
	return guarded
}

func (gate *connectionGate) track(conn *gatedConn) {
	gate.mu.Lock()
	gate.open[conn] = struct{}{}
	gate.mu.Unlock()
}

func (gate *connectionGate) untrack(conn *gatedConn) {
	gate.mu.Lock()
	delete(gate.open, conn)
	gate.mu.Unlock()
}

type gatedListener struct {
	net.Listener
	gate       *connectionGate
	deferToTLS bool
}

func (listener *gatedListener) Accept() (net.Conn, error) {
	for {
		conn, err := listener.Listener.Accept()
		if err != nil {
			return nil, err
		}
		gate := listener.gate
		peer := peerAddress(conn)
		if peer.IsValid() && gate.trusted.Contains(peer) {
			// Judged once the load balancer's PROXY header is read, in the
			// connection's own goroutine, so a slow peer cannot stall Accept.
			return &gatedConn{
				Conn: conn, gate: gate, balancer: peer, deferToTLS: listener.deferToTLS,
				reader: bufio.NewReader(conn),
			}, nil
		}
		admitted := peer.IsValid() && gate.allows(peer)
		if !admitted && (!listener.deferToTLS || !peer.IsValid()) {
			gate.refuse(peer)
			_ = conn.Close()
			continue
		}
		direct := &gatedConn{Conn: conn, gate: gate, address: peer}
		direct.resolveOnce.Do(func() {})
		direct.admitted.Store(admitted)
		gate.track(direct)
		return direct, nil
	}
}

// gatedConn is one accepted connection with the address it is judged by.
type gatedConn struct {
	net.Conn
	gate       *connectionGate
	balancer   netip.Addr // trusted load balancer that relayed the connection
	deferToTLS bool
	reader     *bufio.Reader // reads through the PROXY header when relayed

	resolveOnce sync.Once
	resolveErr  error
	address     netip.Addr     // the client address the allowlist judges
	source      netip.AddrPort // the client address from the PROXY header
	balancerOwn bool           // a load balancer's own connection, such as a health check
	admitted    atomic.Bool

	deadlineMu   sync.Mutex
	readDeadline time.Time
	closeOnce    sync.Once
}

// resolve reads the PROXY header of a relayed connection and applies the
// allowlist to the client it names. Direct connections are resolved at
// Accept.
func (conn *gatedConn) resolve() error {
	conn.resolveOnce.Do(func() {
		header, err := conn.readProxyHeader()
		switch {
		case err == nil && header.Local:
			// The trusted load balancer's own connection, such as a health
			// check: nothing is relayed, so there is no client to judge.
			conn.address, conn.balancerOwn = conn.balancer, true
			conn.admitted.Store(true)
			conn.gate.track(conn)
			return
		case err == nil:
			conn.address, conn.source = header.Source.Addr().Unmap(), header.Source
		case errors.Is(err, proxyprotocol.ErrNoHeader):
			// A load balancer that sends no header hides every client behind
			// its own address. Judge that address strictly, without the
			// loopback exception, so a missing header can never widen access
			// even for a load balancer on this machine, and surface the
			// problem to the Owner.
			conn.gate.proxyHeaderProblems.Add(1)
			conn.address = conn.balancer
			list := conn.gate.list.Load()
			if list.Len() > 0 && !list.Contains(conn.balancer) {
				conn.gate.refuse(conn.balancer)
				conn.fail(errClientAddressRefused)
				return
			}
		case errors.Is(err, proxyprotocol.ErrInvalidHeader):
			conn.gate.proxyHeaderProblems.Add(1)
			conn.fail(errClientAddressRefused)
			return
		default:
			// Closed or timed out before sending anything, such as a TCP
			// health check. Nothing was attempted, so nothing is counted.
			conn.fail(io.EOF)
			return
		}
		admitted := conn.gate.allows(conn.address)
		if !admitted && !conn.deferToTLS {
			conn.gate.refuse(conn.address)
			conn.fail(errClientAddressRefused)
			return
		}
		conn.admitted.Store(admitted)
		conn.gate.track(conn)
	})
	return conn.resolveErr
}

func (conn *gatedConn) fail(err error) {
	conn.resolveErr = err
	_ = conn.Conn.Close()
}

func (conn *gatedConn) readProxyHeader() (proxyprotocol.Header, error) {
	conn.deadlineMu.Lock()
	limit := time.Now().Add(proxyHeaderTimeout)
	if !conn.readDeadline.IsZero() && conn.readDeadline.Before(limit) {
		limit = conn.readDeadline
	}
	_ = conn.Conn.SetReadDeadline(limit)
	conn.deadlineMu.Unlock()
	header, err := proxyprotocol.Read(conn.reader)
	// Restore the deadline the HTTP or TLS layer asked for.
	conn.deadlineMu.Lock()
	_ = conn.Conn.SetReadDeadline(conn.readDeadline)
	conn.deadlineMu.Unlock()
	return header, err
}

func (conn *gatedConn) Read(buffer []byte) (int, error) {
	if err := conn.resolve(); err != nil {
		return 0, err
	}
	if conn.reader != nil {
		return conn.reader.Read(buffer)
	}
	return conn.Conn.Read(buffer)
}

// RemoteAddr is the client a trusted load balancer relayed, so the HTTP
// layer, the requester check and the audit all see who actually connected.
func (conn *gatedConn) RemoteAddr() net.Addr {
	_ = conn.resolve()
	if conn.source.IsValid() {
		return net.TCPAddrFromAddrPort(conn.source)
	}
	return conn.Conn.RemoteAddr()
}

func (conn *gatedConn) SetDeadline(deadline time.Time) error {
	conn.deadlineMu.Lock()
	defer conn.deadlineMu.Unlock()
	conn.readDeadline = deadline
	return conn.Conn.SetDeadline(deadline)
}

func (conn *gatedConn) SetReadDeadline(deadline time.Time) error {
	conn.deadlineMu.Lock()
	defer conn.deadlineMu.Unlock()
	conn.readDeadline = deadline
	return conn.Conn.SetReadDeadline(deadline)
}

func (conn *gatedConn) Close() error {
	err := conn.Conn.Close()
	conn.closeOnce.Do(func() { conn.gate.untrack(conn) })
	return err
}

func peerAddress(conn net.Conn) netip.Addr {
	switch remote := conn.RemoteAddr().(type) {
	case *net.TCPAddr:
		return remote.AddrPort().Addr().Unmap()
	case nil:
		return netip.Addr{}
	default:
		parsed, err := netip.ParseAddrPort(remote.String())
		if err != nil {
			return netip.Addr{}
		}
		return parsed.Addr().Unmap()
	}
}
