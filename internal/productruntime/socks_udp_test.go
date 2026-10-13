package productruntime

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/socksproxy"
	"github.com/vibe-agi/vibermate/internal/vlessprofile"
	"golang.org/x/net/dns/dnsmessage"
)

type udpRuntimeNetwork struct{ vlessRuntimeNetwork }

func (n *udpRuntimeNetwork) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network == "udp" && address == n.direct {
		n.directDials.Add(1)
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	return n.vlessRuntimeNetwork.DialContext(ctx, network, address)
}

// Missing Runtime composition, a guessed account, Direct fallback, lost packet
// boundaries, or an unauthenticated ingress breaks real owned echo replies.
func TestActualRuntimeSOCKSVLESSUDP(t *testing.T) {
	binary := os.Getenv("VIBERMATE_TEST_XRAY_BINARY")
	if binary == "" {
		t.Skip("requires explicit pinned Xray; no vertical proof executed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	echo, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	echoed := make(chan struct{})
	go func() {
		defer close(echoed)
		b := make([]byte, 65535)
		for {
			n, a, e := echo.ReadFromUDPAddrPort(b)
			if e != nil {
				return
			}
			echo.WriteToUDPAddrPort(b[:n], a)
		}
	}()
	defer func() { echo.Close(); <-echoed }()
	peer := echo.LocalAddr().(*net.UDPAddr).AddrPort()
	directPeer := runtimeUDPEcho(t)
	var questions atomic.Int64
	handshake := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, e := io.ReadAll(io.LimitReader(r.Body, 65536))
		if e != nil {
			t.Error(e)
			return
		}
		var p dnsmessage.Parser
		header, e := p.Start(data)
		if e != nil {
			t.Error(e)
			return
		}
		q, e := p.Question()
		if e != nil || q.Name.String() != "proxy.fixture." {
			t.Error("unexpected UDP DoH question")
			return
		}
		questions.Add(1)
		b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: header.ID, Response: true, RecursionAvailable: true})
		b.StartQuestions()
		b.Question(q)
		b.StartAnswers()
		if q.Type == dnsmessage.TypeA {
			b.AResource(dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 30}, dnsmessage.AResource{A: [4]byte{127, 0, 0, 1}})
		}
		reply, e := b.Finish()
		if e != nil {
			t.Error(e)
			return
		}
		w.Header().Set("Content-Type", "application/dns-message")
		w.Write(reply)
	}))
	handshake.Config.ErrorLog = log.New(io.Discard, "", 0)
	cert, roots := runtimeVLESSCertificate(t)
	handshake.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, NextProtos: []string{"h2", "http/1.1"}, CurvePreferences: []tls.CurveID{tls.X25519}}
	handshake.StartTLS()
	defer handshake.Close()
	dohPeer := netip.MustParseAddrPort(handshake.Listener.Addr().String())
	uri := runtimeVLESSServer(t, ctx, binary, dohPeer, []netip.AddrPort{peer, dohPeer})
	network := &udpRuntimeNetwork{vlessRuntimeNetwork: vlessRuntimeNetwork{direct: directPeer.String()}}
	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	options := testOptions(t, hostcontract.Desktop(), gate)
	options.VLESSExecutable = binary
	options.NetworkOptions = egressnetwork.BuilderOptions{BaseDialer: network, SystemResolver: network, TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}}
	r, err := Start(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownRuntime(t, r)
	profile, err := r.VLESSProfiles().Import(ctx, vlessprofile.ImportCommand{ID: "profile.udp", DisplayName: "Owned UDP", URI: uri, Resolver: egressnetwork.ResolverPolicy{Kind: egressnetwork.ResolverDoH, DoHURL: fmt.Sprintf("https://doh.fixture:%d/dns-query", dohPeer.Port()), Transport: egressnetwork.ResolverTransportProxy}, DirectDomains: "direct.fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.EgressDefaults().SetDefault(ctx, egressprofile.SetDefaultCommand{ExpectedRevision: 1, Profile: &egressprofile.ProfileRef{ID: profile.ID, Revision: profile.Revision}}); err != nil {
		t.Fatal(err)
	}
	if _, err = r.ConnectionRules().Replace(ctx, r.ConnectionRules().Current().Revision, []connectionpolicy.Rule{
		{ID: "udp.owned", Priority: 1, Decision: connectionpolicy.DecisionAllow, Match: connectionpolicy.MatchExactHostPort("proxy.fixture", peer.Port())},
		{ID: "udp.direct", Priority: 2, Decision: connectionpolicy.DecisionAllow, Match: connectionpolicy.MatchExactHostPort("direct.fixture", directPeer.Port())},
	}, connectionpolicy.ModeDenyUnknown); err != nil {
		t.Fatal(err)
	}
	grant, err := r.ManualCaptures().Create(ctx, manualcapture.CreateCommand{Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "UDP Capture", ClientClass: manualcapture.ClientCLI, Lifetime: manualcapture.LifetimeUntilRevoked})
	if err != nil {
		t.Fatal(err)
	}
	capture, _ := captureidentity.New(captureidentity.KindManualCapture, grant.Capture.ID)
	if _, err = r.CaptureAssignments().Create(ctx, captureassignment.CreateCommand{Capture: capture, EnvironmentID: environment.SystemTransparentID, Source: captureassignment.SourceSystemTransparent}); err != nil {
		t.Fatal(err)
	}
	composition, ok := any(r).(interface{ SOCKSProxy() *socksproxy.Server })
	if !ok || composition.SOCKSProxy() == nil {
		t.Fatal("actual Runtime has no owned SOCKS UDP composition")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- composition.SOCKSProxy().Serve(listener) }()
	defer func() { composition.SOCKSProxy().Shutdown(context.Background()); <-served }()
	control, relay, udp := runtimeUDPAssociate(t, listener.Addr().String(), grant.Credential.Value())
	defer control.Close()
	defer udp.Close()
	var total int64
	for _, p := range [][]byte{{0, 255, 128, 1}, bytes.Repeat([]byte{255, 0, 127}, 120), []byte("separate-third-datagram")} {
		total += int64(len(p))
		packet := runtimeUDPDomain("proxy.fixture", peer.Port(), p)
		udp.SetDeadline(time.Now().Add(4 * time.Second))
		if _, err = udp.WriteToUDPAddrPort(packet, relay); err != nil {
			t.Fatal(err)
		}
		b := make([]byte, 65535)
		n, a, e := udp.ReadFromUDPAddrPort(b)
		if e != nil {
			page, _ := r.egress.List(ctx, egressaudit.PageRequest{Limit: 20})
			for _, record := range page.Items {
				a := record.Attempt
				t.Logf("UDP diagnostic terminal=%t class=%s bytes=%d/%d target=%s", a.Terminal(), a.ErrorClass(), a.BytesOut(), a.BytesIn(), a.TargetOrigin())
			}
			connections, ce := r.connections.List(ctx, connectionevent.PageRequest{Limit: 20})
			t.Log("connection diagnostic", ce, connections.Items)
			t.Fatal("actual Runtime UDP reply:", e)
		}
		if a != relay || !bytes.Equal(b[:n], append(append([]byte{0, 0, 0}, runtimeUDPWire(peer)...), p...)) {
			t.Fatal("real Runtime UDP packet integrity/reply authority mismatch")
		}
	}
	if network.denied.Load() != 0 || network.directDials.Load() != 0 || network.nodeDNS.Load() != 1 || questions.Load() != 2 || network.dohDNS.Load() != 2 {
		t.Fatal("Runtime VLESS path escaped closed network fixture")
	}
	t.Log("actual credential-authenticated Runtime SOCKS→imported REALITY+Vision→owned UDP echo: three exact binary datagrams and proxied DoH")
	runtimeUDPSend(t, udp, relay, runtimeUDPDomain("DIRECT.fixture.", directPeer.Port(), []byte("direct-owned")))
	runtimeUDPReceive(t, udp, relay, directPeer, []byte("direct-owned"))
	if network.directDNS.Load() != 1 || network.directDials.Load() != 1 || questions.Load() != 2 {
		t.Fatal("Direct-domain did not exclusively use Runtime system DNS and direct packets")
	}
	control.Close()
	runtimeUDPWait(t, func() bool { return gate.Snapshot().ActiveEgress == 0 && gate.Snapshot().ActiveActions == 0 })
	page, e := r.egress.List(ctx, egressaudit.PageRequest{Purpose: egressaudit.PurposeDatagram, Limit: 20})
	if e != nil || len(page.Items) != 2 {
		t.Fatal("packet audits", e, len(page.Items))
	}
	for _, item := range page.Items {
		a := item.Attempt
		want := total
		if a.TargetOrigin() == fmt.Sprintf("udp://direct.fixture:%d", directPeer.Port()) {
			want = 12
		}
		if !a.Terminal() || a.BytesOut() != want || a.BytesIn() != want || a.Decision().AccountID != "" || a.Decision().ProxyID != "profile.udp" || a.Outcome() != egressaudit.OutcomeCanceled {
			t.Fatal("truthful packet terminal/frozen exit lost", egressaudit.ViewOf(item))
		}
	}
	// A new flow queues during Hold. Editing the default while it waits must
	// not change its imported session, resolver or probe identity.
	if _, e = r.EnterOfflineHold(ctx, gate.Snapshot().Revision); e != nil {
		t.Fatal(e)
	}
	held, heldRelay, heldUDP := runtimeUDPAssociate(t, listener.Addr().String(), grant.Credential.Value())
	runtimeUDPSend(t, heldUDP, heldRelay, runtimeUDPDomain("proxy.fixture", peer.Port(), []byte("held-frozen")))
	runtimeUDPWait(t, func() bool { return gate.Snapshot().QueuedRequests == 1 })
	targets := gate.PendingProbeTargets()
	if len(targets) != 1 || targets[0].Transport != offlinehold.ProbeTransportUDPSetup || targets[0].HTTPAuthority != "" || targets[0].EgressPolicy != profile.Policy {
		t.Fatal("Hold manufactured TLS identity or lost packet exit")
	}
	if _, e = r.EgressDefaults().SetDefault(ctx, egressprofile.SetDefaultCommand{ExpectedRevision: 2}); e != nil {
		t.Fatal(e)
	}
	if _, e = r.ResumeOfflineHold(ctx, gate.Snapshot().Revision); e != nil {
		t.Fatal("real packet Hold resume", e)
	}
	runtimeUDPReceive(t, heldUDP, heldRelay, peer, []byte("held-frozen"))
	held.Close()
	heldUDP.Close()
	runtimeUDPWait(t, func() bool { return gate.Snapshot().ActiveActions == 0 && gate.Snapshot().ActiveEgress == 0 })
	if _, e = r.EgressDefaults().SetDefault(ctx, egressprofile.SetDefaultCommand{ExpectedRevision: 3, Profile: &egressprofile.ProfileRef{ID: profile.ID, Revision: profile.Revision}}); e != nil {
		t.Fatal(e)
	}
	if _, e = r.EnterOfflineHold(ctx, gate.Snapshot().Revision); e != nil {
		t.Fatal(e)
	}
	canceled, cancelRelay, cancelUDP := runtimeUDPAssociate(t, listener.Addr().String(), grant.Credential.Value())
	runtimeUDPSend(t, cancelUDP, cancelRelay, runtimeUDPDomain("proxy.fixture", peer.Port(), []byte("never-sent")))
	runtimeUDPWait(t, func() bool { return gate.Snapshot().QueuedRequests == 1 })
	canceled.Close()
	cancelUDP.Close()
	runtimeUDPWait(t, func() bool { return gate.Snapshot().QueuedRequests == 0 && gate.Snapshot().ActiveActions == 0 })
	if _, e = r.ResumeOfflineHold(ctx, gate.Snapshot().Revision); e != nil {
		t.Fatal(e)
	}
	// Explicit denied flow has connection evidence and no outbound attempt.
	denied, deniedRelay, deniedUDP := runtimeUDPAssociate(t, listener.Addr().String(), grant.Credential.Value())
	runtimeUDPSend(t, deniedUDP, deniedRelay, runtimeUDPDomain("denied.fixture", peer.Port(), []byte("denied")))
	runtimeUDPWait(t, func() bool {
		p, _ := r.connections.List(ctx, connectionevent.PageRequest{Limit: 20})
		for _, i := range p.Items {
			if i.RequestedHost == "denied.fixture" && i.Outcome == connectionevent.OutcomeDenied {
				return true
			}
		}
		return false
	})
	denied.Close()
	deniedUDP.Close()
	connect := runtimeSOCKSAuthenticate(t, listener.Addr().String(), grant.Credential.Value())
	connectTarget := runtimeUDPDomain("denied.fixture", peer.Port(), nil)
	connect.Write(append([]byte{5, 1, 0}, connectTarget[3:]...))
	var connectReply [10]byte
	if _, e = io.ReadFull(connect, connectReply[:]); e != nil || connectReply[1] != 2 {
		t.Fatal("SOCKS CONNECT bypassed authoritative Runtime ProxyHandler denial", e, connectReply)
	}
	connect.Close()
	revoked, revokedRelay, revokedUDP := runtimeUDPAssociate(t, listener.Addr().String(), grant.Credential.Value())
	runtimeUDPSend(t, revokedUDP, revokedRelay, runtimeUDPDomain("proxy.fixture", peer.Port(), []byte("revoked-live")))
	runtimeUDPReceive(t, revokedUDP, revokedRelay, peer, []byte("revoked-live"))
	id, _ := manualcapture.ParseID(grant.Capture.ID)
	if _, e = r.ManualCaptures().Revoke(ctx, manualcapture.RevokeCommand{Owner: manualcapture.NewLocalOwnerScope(), ID: id, ExpectedCredentialRevision: 1}); e != nil {
		t.Fatal(e)
	}
	revoked.SetReadDeadline(time.Now().Add(time.Second))
	var one [1]byte
	if _, e = revoked.Read(one[:]); e == nil {
		t.Fatal("revocation retained SOCKS association")
	}
	revoked.Close()
	revokedUDP.Close()
	runtimeUDPWait(t, func() bool { return gate.Snapshot().ActiveActions == 0 && gate.Snapshot().ActiveEgress == 0 })
	page, e = r.egress.List(ctx, egressaudit.PageRequest{Purpose: egressaudit.PurposeDatagram, Limit: 20})
	if e != nil || len(page.Items) != 5 {
		t.Fatal("scoped packet audit count", e, len(page.Items))
	}
	var zero, heldCount int
	for _, i := range page.Items {
		a := i.Attempt
		if !a.Terminal() || a.Decision().ProxyID != "profile.udp" || a.Decision().AccountID != "" || a.BytesOut() != a.BytesIn() {
			t.Fatal("packet terminal mismatch", egressaudit.ViewOf(i))
		}
		if a.BytesOut() == 0 {
			zero++
			if a.Outcome() != egressaudit.OutcomeCanceled {
				t.Fatal("held cancel terminal")
			}
		}
		if a.BytesOut() == 11 {
			heldCount++
			if a.Decision().DefaultRevision != 2 {
				t.Fatal("queued flow default changed")
			}
		}
	}
	if zero != 1 || heldCount != 1 || network.denied.Load() != 0 {
		t.Fatal("held cancel/frozen exit/no fallback evidence missing", zero, heldCount)
	}
	t.Log("real Direct-domain, Hold→resume across default edit, held cancel, denied destination, live Capture revoke and exact metadata-only packet terminals passed")
	// Runtime itself owns authenticated control and open datagram workers.
	nextGrant, e := r.ManualCaptures().Create(ctx, manualcapture.CreateCommand{Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "shutdown UDP", ClientClass: manualcapture.ClientCLI, Lifetime: manualcapture.LifetimeUntilRevoked})
	if e != nil {
		t.Fatal(e)
	}
	closing, closingRelay, closingUDP := runtimeUDPAssociate(t, listener.Addr().String(), nextGrant.Credential.Value())
	runtimeUDPSend(t, closingUDP, closingRelay, runtimeUDPDomain("proxy.fixture", peer.Port(), []byte("shutdown-owned")))
	runtimeUDPReceive(t, closingUDP, closingRelay, peer, []byte("shutdown-owned"))
	shutdownRuntime(t, r)
	closing.SetReadDeadline(time.Now().Add(time.Second))
	if _, e = closing.Read(one[:]); e == nil {
		t.Fatal("Runtime shutdown retained control")
	}
	closing.Close()
	closingUDP.Close()
}

func runtimeUDPEcho(t *testing.T) netip.AddrPort {
	t.Helper()
	u, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		b := make([]byte, 65535)
		for {
			n, a, e := u.ReadFromUDPAddrPort(b)
			if e != nil {
				return
			}
			u.WriteToUDPAddrPort(b[:n], a)
		}
	}()
	t.Cleanup(func() { u.Close(); <-done })
	return u.LocalAddr().(*net.UDPAddr).AddrPort()
}
func runtimeUDPDomain(host string, port uint16, p []byte) []byte {
	b := append([]byte{0, 0, 0, 3, byte(len(host))}, []byte(host)...)
	b = binary.BigEndian.AppendUint16(b, port)
	return append(b, p...)
}
func runtimeUDPSend(t *testing.T, u *net.UDPConn, relay netip.AddrPort, p []byte) {
	t.Helper()
	u.SetDeadline(time.Now().Add(4 * time.Second))
	if _, e := u.WriteToUDPAddrPort(p, relay); e != nil {
		t.Fatal(e)
	}
}
func runtimeUDPReceive(t *testing.T, u *net.UDPConn, relay, peer netip.AddrPort, p []byte) {
	t.Helper()
	u.SetReadDeadline(time.Now().Add(4 * time.Second))
	b := make([]byte, 65535)
	n, a, e := u.ReadFromUDPAddrPort(b)
	want := append(append([]byte{0, 0, 0}, runtimeUDPWire(peer)...), p...)
	if e != nil || a != relay || !bytes.Equal(b[:n], want) {
		t.Fatal("actual packet reply mismatch", e)
	}
}
func runtimeUDPWait(t *testing.T, predicate func() bool) {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if predicate() {
			return
		}
		select {
		case <-tick.C:
		case <-timer.C:
			t.Fatal("Runtime packet state did not reach expected ownership boundary")
		}
	}
}

func runtimeUDPWire(peer netip.AddrPort) []byte {
	b := append([]byte{1}, peer.Addr().AsSlice()...)
	return binary.BigEndian.AppendUint16(b, peer.Port())
}
func runtimeUDPAssociate(t *testing.T, address, credential string) (net.Conn, netip.AddrPort, *net.UDPConn) {
	t.Helper()
	c := runtimeSOCKSAuthenticate(t, address, credential)
	u, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { u.Close() })
	c.Write(append([]byte{5, 3, 0}, runtimeUDPWire(u.LocalAddr().(*net.UDPAddr).AddrPort())...))
	var reply [10]byte
	if _, e = io.ReadFull(c, reply[:]); e != nil || reply[1] != 0 || reply[3] != 1 {
		t.Fatal("UDP ASSOCIATE", reply, e)
	}
	relay := netip.AddrPortFrom(netip.AddrFrom4([4]byte(reply[4:8])), binary.BigEndian.Uint16(reply[8:]))
	if !relay.Addr().IsLoopback() || relay.Port() == 0 {
		t.Fatal(fmt.Sprint("invalid relay ", relay))
	}
	c.SetDeadline(time.Time{})
	return c, relay, u
}
func runtimeSOCKSAuthenticate(t *testing.T, address, credential string) net.Conn {
	t.Helper()
	c, e := net.DialTimeout("tcp4", address, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { c.Close() })
	c.SetDeadline(time.Now().Add(5 * time.Second))
	c.Write([]byte{5, 1, 2})
	var h [2]byte
	if _, e = io.ReadFull(c, h[:]); e != nil || h != [2]byte{5, 2} {
		t.Fatal("SOCKS method", h, e)
	}
	auth := append([]byte{1, 7}, []byte("capture")...)
	auth = append(auth, byte(len(credential)))
	auth = append(auth, []byte(credential)...)
	c.Write(auth)
	if _, e = io.ReadFull(c, h[:]); e != nil || h != [2]byte{1, 0} {
		t.Fatal("real Capture auth", h, e)
	}
	return c
}
