//go:build darwin || linux

package serverhost_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/desktopcontrol"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
	"github.com/vibe-agi/vibermate/internal/serverhost"
)

// Missing endpoint publication, real grant authentication, VLESS routing,
// datagram boundaries, revocation or joined shutdown breaks this user entry.
func TestServerManualSOCKSREALITYDelivery(t *testing.T) {
	binaryPath := os.Getenv("VIBERMATE_TEST_XRAY_BINARY")
	if binaryPath == "" {
		t.Skip("VIBERMATE_TEST_XRAY_BINARY is required; no packaged user-entry proof executed")
	}
	data, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	const qualifiedSHA256 = "b6ea6fb1730c77f52e8f49cfa545e23d1bd75c4bc2281ba941c4c400f931f24c"
	if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != qualifiedSHA256 {
		t.Fatalf("unqualified packaged helper SHA256 %s", got)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	version, err := exec.CommandContext(ctx, binaryPath, "version").Output()
	if err != nil || !bytes.Contains(version, []byte("26.3.27")) || !bytes.Contains(version, []byte("d2758a0-vibermate-reliability2")) || !bytes.Contains(version, []byte("go1.26.10")) {
		t.Fatal("packaged helper version identity failed")
	}
	t.Logf("packaged SHA256 %s; %s", qualifiedSHA256, strings.TrimSpace(string(version)))
	fixture := newDeliveryFixture(t, ctx, binaryPath)
	options := serverOptions(t, t.TempDir())
	options.Transport = serverhost.TransportOptions{Mode: serverhost.TransportHTTP}
	options.SOCKSListenAddress = "127.0.0.1:0"
	options.Runtime.VLESSExecutable = binaryPath
	options.Runtime.NetworkOptions = egressnetwork.BuilderOptions{BaseDialer: fixture.network, SystemResolver: fixture.network, TLSClientConfig: &tls.Config{RootCAs: fixture.roots, MinVersion: tls.VersionTLS13}}
	host, err := serverhost.Start(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownServer(t, host) })
	control := deliveryControl{t: t, client: &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 10 * time.Second}, base: "http://" + host.Status().ListenAddress, dohPort: fixture.doh.Port()}
	t.Cleanup(func() { control.client.CloseIdleConnections() })
	key, err := os.ReadFile(host.Status().RecoveryKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	var session servercontrol.WebSession
	control.call(http.MethodPost, servercontrol.WebSetupPath, "", 0, servercontrol.WebSetup{Schema: servercontrol.WebSetupSchema, RecoveryKey: strings.TrimSpace(string(key)), Username: "owned-socks-owner", Password: "synthetic-socks-owner-password"}, 201, &session)
	clear(key)
	control.read, control.write = session.ReadToken, session.WriteToken
	profile := control.importProfile("profile.delivery", fixture.uri)
	control.selectProfile(profile)
	var rules desktopcontrol.ConnectionRuleSetResponse
	control.call(http.MethodGet, "/api/v1/policies/connections", control.read, 0, nil, 200, &rules)
	inputs := []desktopcontrol.ConnectionRuleInput{}
	for index, target := range []struct {
		host string
		peer netip.AddrPort
	}{{"proxy.fixture", fixture.tcp}, {"proxy.fixture", fixture.udp}, {"direct.fixture", fixture.directTCP}, {"direct.fixture", fixture.directUDP}} {
		inputs = append(inputs, desktopcontrol.ConnectionRuleInput{ID: fmt.Sprintf("owned.delivery-%d", index), Priority: uint32(index + 1), Decision: "allow", Match: "exact_host_port", Host: target.host, Port: target.peer.Port()})
	}
	control.call(http.MethodPatch, "/api/v1/policies/connections", control.write, rules.Revision, desktopcontrol.ConnectionRuleSetInput{Rules: inputs, Mode: "deny_unknown"}, 200, &rules)
	grant, tag := control.grant("owned-real-SOCKS")
	// A newly committed Capture is required authority, not proof that its
	// original fence has reached the optional recording mirror yet.
	deliveryWait(t, func() bool { return host.Runtime().RawEvidenceStatistics().WriterID != "" })
	waitServerRecording(t, host.Runtime())
	var status struct {
		NetworkAccess struct {
			SOCKSAddress   string `json:"socksAddress"`
			VLESSAvailable bool   `json:"vlessAvailable"`
		} `json:"networkAccess"`
	}
	control.call(http.MethodGet, "/api/v1/status", control.read, 0, nil, 200, &status)
	if grant.SOCKSAddress == "" || grant.SOCKSAddress != status.NetworkAccess.SOCKSAddress || !status.NetworkAccess.VLESSAvailable {
		t.Fatal("actual status/context/grant endpoint publication lost")
	}
	if reply := socksDeliveryAuth(t, grant.SOCKSAddress, grant.ProxyUsername, session.WriteToken); reply == [2]byte{1, 0} {
		t.Fatal("owner control token accepted as Capture credential")
	}
	t.Log("authenticated owner import/default/status/review/grant controls delivered the bound SOCKS address")

	payload := []byte{0, 255, 128, 1, 13, 10, 19, 0, 42}
	tcp := deliveryConnect(t, grant, "proxy.fixture", fixture.tcp.Port(), 0)
	deliveryEcho(t, tcp, payload)
	tcp.Close()
	if fixture.network.directDials.Load() != 0 || fixture.network.denied.Load() != 0 || fixture.questions.Load() != 2 || fixture.tcpBytes.Load() != int64(len(payload)) || fixture.network.nodeDNS.Load() == 0 {
		t.Fatal("TCP did not traverse REALITY/Vision and proxied DoH without Direct")
	}
	before := fixture.questions.Load()
	directTCP := deliveryConnect(t, grant, "DIRECT.fixture.", fixture.directTCP.Port(), 0)
	deliveryEcho(t, directTCP, payload)
	directTCP.Close()
	if fixture.network.directDNS.Load() != 1 || fixture.network.directDials.Load() != 1 || fixture.questions.Load() != before {
		t.Fatal("matching TCP domain did not exclusively use SystemDNS+Direct")
	}
	association, relay, udp := deliveryAssociate(t, grant)
	payloads := [][]byte{{0, 255, 128, 1, 13, 10}, []byte("second datagram\x00separate boundary"), bytes.Repeat([]byte{0, 1, 128, 255}, 64)}
	var total int64
	for _, p := range payloads {
		deliveryPacket(t, udp, relay, "proxy.fixture", fixture.udp, p)
		total += int64(len(p))
	}
	if fixture.udpBytes.Load() != total || fixture.network.directDials.Load() != 1 || fixture.network.denied.Load() != 0 || fixture.questions.Load() != before+2 {
		t.Fatal("three separate UDP packets did not use VLESS/DoH")
	}
	before = fixture.questions.Load()
	deliveryPacket(t, udp, relay, "direct.fixture", fixture.directUDP, []byte("direct-owned"))
	if fixture.network.directDNS.Load() != 2 || fixture.network.directDials.Load() != 2 || fixture.questions.Load() != before {
		t.Fatal("matching UDP domain did not exclusively use SystemDNS+Direct")
	}
	association.Close()
	deliveryWait(t, func() bool {
		s := options.Runtime.OfflineHold.Snapshot()
		return s.ActiveEgress == 0 && s.ActiveActions == 0
	})
	deliveryNoPacket(t, udp, relay, fixture.udp, "after-EOF")
	udp.Close()
	if fixture.udpBytes.Load() != total {
		t.Fatal("control EOF sent another target datagram")
	}
	// The association's actual producer is drained above. Now join its
	// accepted optional writes before asserting persisted terminal bytes.
	deliveryWait(t, func() bool { return host.Runtime().Status().Recording.ReservedRecords == 0 })
	page, err := host.Runtime().EgressAttempts().List(ctx, egressaudit.PageRequest{Purpose: egressaudit.PurposeDatagram, Limit: 20})
	if err != nil || len(page.Items) != 2 {
		t.Fatal("missing two real datagram audit terminals", err)
	}
	for _, item := range page.Items {
		a := item.Attempt
		want := total
		if strings.Contains(a.TargetOrigin(), "direct.fixture") {
			want = 12
		}
		if !a.Terminal() || a.BytesIn() != want || a.BytesOut() != want || a.Decision().ProxyID != profile.ID || a.Decision().AccountID != "" {
			t.Fatal("datagram audit lost actual bytes/frozen selected profile")
		}
	}
	t.Log("exact binary TCP and three UDP replies; unmatched proxy/DoH and matching SystemDNS/Direct; control EOF and byte audit verified")

	// Select an owned listening socket that cannot speak REALITY. A fabricated
	// success or a Direct fallback would reach the observed echo and fail.
	badURI, _ := url.Parse(fixture.uri)
	badURI.Host = net.JoinHostPort("node.fixture", strconv.Itoa(int(fixture.bad.Port())))
	bad := control.importProfile("profile.delivery-bad", badURI.String())
	control.selectProfile(bad)
	priorTarget := fixture.tcpBytes.Load()
	deliveryConnect(t, grant, "proxy.fixture", fixture.tcp.Port(), 1)
	if fixture.tcpBytes.Load() != priorTarget || fixture.network.directDials.Load() != 2 || fixture.network.denied.Load() != 0 {
		t.Fatal("bad node reached target or attempted Direct fallback")
	}
	badControl, badRelay, badUDP := deliveryAssociate(t, grant)
	if _, err := badUDP.WriteToUDPAddrPort(deliveryDomain("proxy.fixture", fixture.udp.Port(), []byte("bad-node-never-delivered")), badRelay); err != nil {
		t.Fatal(err)
	}
	deliveryWait(t, func() bool {
		page, err := host.Runtime().EgressAttempts().List(ctx, egressaudit.PageRequest{Purpose: egressaudit.PurposeDatagram, Limit: 20})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			a := item.Attempt
			if a.Decision().ProxyID == bad.ID && a.Terminal() {
				if a.Outcome() != egressaudit.OutcomeFailed || a.BytesOut() != 0 || a.BytesIn() != 0 {
					t.Fatal("bad node fabricated UDP success audit")
				}
				return true
			}
		}
		return false
	})
	deliveryNoPacket(t, badUDP, badRelay, fixture.udp, "after-bad-node")
	badControl.Close()
	badUDP.Close()
	if fixture.udpBytes.Load() != total || fixture.network.directDials.Load() != 2 || fixture.network.denied.Load() != 0 {
		t.Fatal("bad node delivered UDP or attempted Direct fallback")
	}
	control.selectProfile(profile)
	restored := deliveryConnect(t, grant, "proxy.fixture", fixture.tcp.Port(), 0)
	deliveryEcho(t, restored, payload)
	restored.Close()
	t.Log("bad owned node returned no TCP/UDP target bytes, truthful failed UDP audit and no Direct dial; restored control selection created a fresh successful operation")

	revoked, revokedRelay, revokedUDP := deliveryAssociate(t, grant)
	deliveryPacket(t, revokedUDP, revokedRelay, "proxy.fixture", fixture.udp, []byte("revoked-live"))
	control.call(http.MethodPost, "/api/v1/manual-captures/"+grant.Capture.ID+"/actions/revoke", control.write, tag, nil, 204, nil)
	deliveryClosed(t, revoked, "revocation")
	priorUDP := fixture.udpBytes.Load()
	deliveryNoPacket(t, revokedUDP, revokedRelay, fixture.udp, "after-revoke")
	if fixture.udpBytes.Load() != priorUDP {
		t.Fatal("revoked association still sent target datagrams")
	}
	if reply := socksDeliveryAuth(t, grant.SOCKSAddress, grant.ProxyUsername, grant.ProxyPassword); reply == [2]byte{1, 0} {
		t.Fatal("revoked real grant still authenticates")
	}
	revoked.Close()
	revokedUDP.Close()
	next, _ := control.grant("owned-shutdown")
	openTCP := deliveryConnect(t, next, "proxy.fixture", fixture.tcp.Port(), 0)
	deliveryEcho(t, openTCP, payload)
	closing, closingRelay, closingUDP := deliveryAssociate(t, next)
	deliveryPacket(t, closingUDP, closingRelay, "proxy.fixture", fixture.udp, []byte("shutdown-owned"))
	managed := deliveryManagedChildren(t, binaryPath, fixture.pid)
	if len(managed) == 0 {
		t.Fatal("no actual managed packaged engine observed")
	}
	shutdownServer(t, host)
	deliveryClosed(t, openTCP, "Host TCP shutdown")
	deliveryClosed(t, closing, "Host UDP shutdown")
	priorUDP = fixture.udpBytes.Load()
	deliveryNoPacket(t, closingUDP, closingRelay, fixture.udp, "after-shutdown")
	if fixture.udpBytes.Load() != priorUDP {
		t.Fatal("Host shutdown retained UDP target delivery")
	}
	openTCP.Close()
	closing.Close()
	closingUDP.Close()
	for _, child := range managed {
		if _, err := os.Stat(child.config); !os.IsNotExist(err) {
			t.Fatal("managed engine private configuration retained")
		}
		if deliveryPIDAlive(child.pid) {
			t.Fatal("Host returned before managed engine exited")
		}
		t.Logf("managed engine pid=%d exited and private config removed", child.pid)
	}
	if conn, e := net.DialTimeout("tcp", grant.SOCKSAddress, 100*time.Millisecond); e == nil {
		conn.Close()
		t.Fatal("Host retained SOCKS listener")
	}
	t.Log("real grant revocation, Host TCP/UDP close, managed engine Wait and private-config cleanup verified")
}

type deliveryControl struct {
	t                 *testing.T
	client            *http.Client
	base, read, write string
	operation         uint64
	dohPort           uint16
}

func (c *deliveryControl) call(method, path, token string, revision any, input any, want int, output any) string {
	c.t.Helper()
	var body []byte
	if input != nil {
		var err error
		body, err = json.Marshal(input)
		if err != nil {
			c.t.Fatal(err)
		}
		defer clear(body)
	}
	r, err := http.NewRequest(method, c.base+path, bytes.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if method != http.MethodGet {
		c.operation++
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("If-Match", fmt.Sprint(revision))
		r.Header.Set("Idempotency-Key", fmt.Sprintf("owned-delivery-operation-%d", c.operation))
	}
	response, err := c.client.Do(r)
	if err != nil {
		c.t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != want {
		c.t.Fatalf("%s %s status=%d want=%d (private body redacted)", method, path, response.StatusCode, want)
	}
	if output != nil && json.NewDecoder(response.Body).Decode(output) != nil {
		c.t.Fatalf("%s response decoding failed", path)
	}
	return response.Header.Get("ETag")
}
func (c *deliveryControl) importProfile(id, uri string) desktopcontrol.EgressProfileResponse {
	var p desktopcontrol.EgressProfileResponse
	c.call(http.MethodPost, "/api/v1/egress-profiles/import-vless", c.write, 0, map[string]any{"id": id, "displayName": "Owned REALITY delivery", "uri": uri, "resolver": egressnetwork.ResolverPolicy{Kind: egressnetwork.ResolverDoH, DoHURL: fmt.Sprintf("https://doh.fixture:%d/dns-query", c.dohPort), Transport: egressnetwork.ResolverTransportProxy}, "directDomains": "direct.fixture"}, 200, &p)
	if p.ID != id || p.Revision != 1 {
		c.t.Fatal("real import reference mismatch")
	}
	return p
}
func (c *deliveryControl) selectProfile(p desktopcontrol.EgressProfileResponse) {
	var setting struct {
		Revision uint64                                `json:"revision"`
		Profile  *desktopcontrol.EgressProfileResponse `json:"profile"`
	}
	c.call(http.MethodGet, "/api/v1/egress-default", c.read, 0, nil, 200, &setting)
	c.call(http.MethodPut, "/api/v1/egress-default", c.write, setting.Revision, map[string]any{"profile": map[string]any{"id": p.ID, "revision": p.Revision}}, 200, &setting)
	if setting.Profile == nil || setting.Profile.ID != p.ID || setting.Profile.Revision != p.Revision {
		c.t.Fatal("real default control did not select imported reference")
	}
}
func (c *deliveryControl) grant(name string) (capturecontrol.ManualCaptureGrant, string) {
	var reviewed struct {
		ConfirmationToken string `json:"confirmationToken"`
		SOCKSAddress      string `json:"socksAddress"`
		ProxyAddress      string `json:"proxyAddress"`
	}
	c.call(http.MethodGet, "/api/v1/manual-captures/context?environmentId=system_transparent", c.read, 0, nil, 200, &reviewed)
	var grant capturecontrol.ManualCaptureGrant
	tag := c.call(http.MethodPost, "/api/v1/manual-captures", c.write, 0, capturecontrol.ManualCaptureCreateRequest{EnvironmentID: "system_transparent", DisplayName: name, ClientClass: manualcapture.ClientCLI, Lifetime: manualcapture.LifetimeUntilRevoked, ConfirmationToken: reviewed.ConfirmationToken}, 201, &grant)
	if grant.SOCKSAddress != reviewed.SOCKSAddress || grant.ProxyAddress != reviewed.ProxyAddress || grant.ProxyPassword == "" || tag == "" {
		c.t.Fatal("real grant changed reviewed endpoints or lost credential/state tag")
	}
	return grant, tag
}
func deliveryAuthenticate(t *testing.T, g capturecontrol.ManualCaptureGrant) net.Conn {
	t.Helper()
	c, e := net.DialTimeout("tcp4", g.SOCKSAddress, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { c.Close() })
	c.SetDeadline(time.Now().Add(8 * time.Second))
	if _, e = c.Write([]byte{5, 1, 2}); e != nil {
		t.Fatal(e)
	}
	var h [2]byte
	if _, e = io.ReadFull(c, h[:]); e != nil || h != [2]byte{5, 2} {
		t.Fatal("SOCKS method", h, e)
	}
	b := append([]byte{1, byte(len(g.ProxyUsername))}, g.ProxyUsername...)
	b = append(b, byte(len(g.ProxyPassword)))
	b = append(b, g.ProxyPassword...)
	if _, e = c.Write(b); e != nil {
		t.Fatal(e)
	}
	if _, e = io.ReadFull(c, h[:]); e != nil || h != [2]byte{1, 0} {
		t.Fatal("real granted SOCKS credential rejected", h, e)
	}
	return c
}
func deliveryConnect(t *testing.T, g capturecontrol.ManualCaptureGrant, host string, port uint16, wantReply byte) net.Conn {
	t.Helper()
	c := deliveryAuthenticate(t, g)
	if _, e := c.Write(append([]byte{5, 1, 0}, deliveryDomain(host, port, nil)[3:]...)); e != nil {
		t.Fatal(e)
	}
	if err := deliveryConnectReply(c, wantReply); err != nil {
		t.Fatal("SOCKS CONNECT", err)
	}
	if wantReply != 0 {
		c.Close()
		t.Logf("bad node complete well-formed SOCKS CONNECT failure reply=%d", wantReply)
		return nil
	}
	return c
}

// This IPv4 fixture must observe the entire SOCKS response, including the
// reserved byte and bind address. Silence, truncation and success cannot prove
// the owned bad node returned a protocol failure.
func deliveryConnectReply(reader io.Reader, wantReply byte) error {
	var reply [10]byte
	if _, err := io.ReadFull(reader, reply[:]); err != nil {
		return fmt.Errorf("complete CONNECT reply: %w", err)
	}
	if reply[0] != 5 || reply[2] != 0 || reply[3] != 1 {
		return fmt.Errorf("malformed IPv4 CONNECT reply")
	}
	if reply[1] != wantReply {
		return fmt.Errorf("CONNECT reply=%d want=%d", reply[1], wantReply)
	}
	return nil
}

func TestDeliveryCONNECTRequiresCompleteFailureReply(t *testing.T) {
	for _, test := range []struct {
		name      string
		reader    io.Reader
		wantError bool
	}{
		{"complete failure", bytes.NewReader([]byte{5, 1, 0, 1, 0, 0, 0, 0, 0, 0}), false},
		{"fabricated success", bytes.NewReader([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}), true},
		{"truncated bind address", bytes.NewReader([]byte{5, 1, 0, 1, 0, 0, 0, 0, 0}), true},
		{"empty EOF", bytes.NewReader(nil), true},
		{"reply deadline", deliveryReplyReadError{}, true},
		{"wrong version", bytes.NewReader([]byte{4, 1, 0, 1, 0, 0, 0, 0, 0, 0}), true},
		{"nonzero reserved byte", bytes.NewReader([]byte{5, 1, 1, 1, 0, 0, 0, 0, 0, 0}), true},
		{"wrong address type", bytes.NewReader([]byte{5, 1, 0, 4, 0, 0, 0, 0, 0, 0}), true},
		{"wrong failure outcome", bytes.NewReader([]byte{5, 2, 0, 1, 0, 0, 0, 0, 0, 0}), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := deliveryConnectReply(test.reader, 1); (err != nil) != test.wantError {
				t.Fatalf("failure reply validation err=%v wantError=%v", err, test.wantError)
			}
		})
	}
}

type deliveryReplyReadError struct{}

func (deliveryReplyReadError) Read([]byte) (int, error) { return 0, os.ErrDeadlineExceeded }
func deliveryEcho(t *testing.T, c net.Conn, p []byte) {
	t.Helper()
	c.SetDeadline(time.Now().Add(6 * time.Second))
	if _, e := c.Write(p); e != nil {
		t.Fatal(e)
	}
	b := make([]byte, len(p))
	if _, e := io.ReadFull(c, b); e != nil || !bytes.Equal(b, p) {
		t.Fatal("exact TCP binary echo", e)
	}
}
func deliveryAssociate(t *testing.T, g capturecontrol.ManualCaptureGrant) (net.Conn, netip.AddrPort, *net.UDPConn) {
	t.Helper()
	c := deliveryAuthenticate(t, g)
	u, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { u.Close() })
	p := u.LocalAddr().(*net.UDPAddr).AddrPort()
	if _, e = c.Write(append([]byte{5, 3, 0}, deliveryWire(p)...)); e != nil {
		t.Fatal(e)
	}
	var b [10]byte
	if _, e = io.ReadFull(c, b[:]); e != nil || b[0] != 5 || b[1] != 0 || b[3] != 1 {
		t.Fatal("UDP ASSOCIATE", b, e)
	}
	relay := netip.AddrPortFrom(netip.AddrFrom4([4]byte(b[4:8])), binary.BigEndian.Uint16(b[8:]))
	if !relay.Addr().IsLoopback() || relay.Port() == 0 {
		t.Fatal("invalid owned UDP relay")
	}
	c.SetDeadline(time.Time{})
	return c, relay, u
}
func deliveryDomain(host string, port uint16, p []byte) []byte {
	b := append([]byte{0, 0, 0, 3, byte(len(host))}, host...)
	return append(binary.BigEndian.AppendUint16(b, port), p...)
}
func deliveryWire(p netip.AddrPort) []byte {
	return binary.BigEndian.AppendUint16(append([]byte{1}, p.Addr().AsSlice()...), p.Port())
}
func deliveryPacket(t *testing.T, u *net.UDPConn, relay netip.AddrPort, host string, target netip.AddrPort, p []byte) {
	t.Helper()
	u.SetDeadline(time.Now().Add(6 * time.Second))
	if _, e := u.WriteToUDPAddrPort(deliveryDomain(host, target.Port(), p), relay); e != nil {
		t.Fatal(e)
	}
	b := make([]byte, 65535)
	n, a, e := u.ReadFromUDPAddrPort(b)
	want := append(append([]byte{0, 0, 0}, deliveryWire(target)...), p...)
	if e != nil || a != relay || !bytes.Equal(b[:n], want) {
		t.Fatalf("exact SOCKS UDP source/target/boundary/payload mismatch n=%d err=%v", n, e)
	}
}
func deliveryClosed(t *testing.T, c net.Conn, reason string) {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(time.Second))
	n, e := c.Read(make([]byte, 1))
	if n != 0 || e == nil {
		t.Fatalf("%s retained connection", reason)
	}
	if timeout, ok := e.(net.Error); ok && timeout.Timeout() {
		t.Fatalf("%s timed out instead of closing", reason)
	}
}

func deliveryNoPacket(t *testing.T, udp *net.UDPConn, relay, target netip.AddrPort, payload string) {
	t.Helper()
	if _, err := udp.WriteToUDPAddrPort(deliveryDomain("proxy.fixture", target.Port(), []byte(payload)), relay); err != nil {
		t.Fatal(err)
	}
	udp.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if _, _, err := udp.ReadFromUDPAddrPort(make([]byte, 1024)); err == nil {
		t.Fatal("closed/failed association returned datagram")
	} else if e, ok := err.(net.Error); !ok || !e.Timeout() {
		t.Fatal("unexpected negative UDP observation", err)
	}
}
func deliveryWait(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if predicate() {
			return
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("owned operation did not reach terminal state")
		}
	}
}
