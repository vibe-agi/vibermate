package socksproxy

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/capturecredential"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/vlesscore"
)

// This is protocol integration with a synthetic Authorizer, not Runtime policy proof.
// Removing listener authentication, the supplied HTTP bridge, packet source binding,
// target metadata, or control/flow ownership must break this test.
func TestOwnedIngressIntegration(t *testing.T) {
	binaryPath := os.Getenv("VIBERMATE_TEST_XRAY_BINARY")
	if binaryPath == "" {
		t.Skip("requires explicit verified local core; no integration executed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	config, tcpTarget, _, udpTarget, _ := ingressFixture(t, ctx, binaryPath)
	client, err := vlesscore.Start(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	raw, _ := capturecredential.New(capturecredential.KindManualCapture, bytes.Repeat([]byte{42}, 32))
	credential, _ := captureadmission.NewProxyCredential(raw.Value())
	auth := &fixtureAuthorizer{credential: credential}
	auth.enabled.Store(true)
	opener := &fixtureOpener{client: client, peer: udpTarget}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("Authorization", r.Header.Get("Proxy-Authorization"))
		user, password, ok := r.BasicAuth()
		if !ok || user != "capture" || password != raw.Value() || r.Method != "CONNECT" || r.Host != tcpTarget.String() || egressnetwork.EffectiveTargetAccess(r.Context()) != egressnetwork.TargetAccessPublicOnly || r.RemoteAddr == "" || r.RemoteAddr == "pipe" {
			http.Error(w, "rejected", 403)
			return
		}
		upstream, e := client.DialContext(r.Context(), "tcp", r.Host)
		if e != nil {
			http.Error(w, "failed", 502)
			return
		}
		defer upstream.Close()
		conn, b, e := w.(http.Hijacker).Hijack()
		if e != nil {
			return
		}
		defer conn.Close()
		b.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		b.Flush()
		done := make(chan struct{})
		go func() {
			n, e := io.Copy(upstream, b)
			t.Logf("fixture tunnel upload completed bytes=%d error=%v", n, e)
			upstream.Close()
			close(done)
		}()
		io.Copy(conn, upstream)
		conn.Close()
		<-done
	})
	server, err := New(Options{Authorizer: auth, TCPHandler: handler, UDP: opener, TargetAccess: egressnetwork.TargetAccessPublicOnly})
	if err != nil {
		t.Fatalf("create owned ingress: %v", err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serving := make(chan error, 1)
	go func() { serving <- server.Serve(listener) }()
	t.Cleanup(func() { server.Shutdown(context.Background()); <-serving })
	dial := func() net.Conn {
		c, e := net.Dial("tcp", listener.Addr().String())
		if e != nil {
			t.Fatal(e)
		}
		c.SetDeadline(time.Now().Add(8 * time.Second))
		return c
	}
	noauth := dial()
	noauth.Write([]byte{5, 1, 0})
	answer := make([]byte, 2)
	if _, e := io.ReadFull(noauth, answer); e != nil || !bytes.Equal(answer, []byte{5, 255}) {
		t.Fatalf("no-auth accepted: %v %v", answer, e)
	}
	noauth.Close()
	wrong := dial()
	if socksAuth(wrong, "invalid") == nil {
		t.Fatal("incorrect credential accepted")
	}
	wrong.Close()
	tcp := dial()
	if e := socksAuth(tcp, raw.Value()); e != nil {
		t.Fatal(e)
	}
	request := append([]byte{5, 1, 0}, wireTarget(tcpTarget)...)
	payload := []byte("owned-CONNECT-buffered\x00\xff")
	// Pipeline bytes after CONNECT so dropping the reader buffer breaks the proof.
	tcp.Write(append(request, payload...))
	if _, e := readSocksReply(tcp); e != nil {
		t.Fatal(e)
	}
	result := make([]byte, len(payload))
	if _, e := io.ReadFull(tcp, result); e != nil || !bytes.Equal(result, payload) {
		t.Fatalf("CONNECT bridge echo: %v %v", result, e)
	}
	tcp.Close()
	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	control := dial()
	if e := socksAuth(control, raw.Value()); e != nil {
		t.Fatal(e)
	}
	control.Write(append([]byte{5, 3, 0}, wireTarget(udp.LocalAddr().(*net.UDPAddr).AddrPort())...))
	relay, e := readSocksReply(control)
	if e != nil || !relay.Addr().IsLoopback() || relay.Port() == 0 {
		t.Fatalf("reachable relay: %v %v", relay, e)
	}
	send := func(packet []byte) []byte {
		t.Helper()
		udp.SetDeadline(time.Now().Add(3 * time.Second))
		if _, e := udp.WriteToUDPAddrPort(packet, relay); e != nil {
			t.Fatal(e)
		}
		b := make([]byte, 65535)
		n, source, e := udp.ReadFromUDPAddrPort(b)
		if e != nil || source != relay {
			t.Fatalf("UDP receive: %v %v", source, e)
		}
		return b[:n]
	}
	for i, p := range [][]byte{[]byte("owned-UDP\x00\xff\x80"), bytes.Repeat([]byte{0, 255, 128, 1}, 64), []byte("third-datagram")} {
		target := wireTarget(udpTarget)
		if i == 1 {
			target = append([]byte{3, 12}, []byte("echo.invalid")...)
			target = binary.BigEndian.AppendUint16(target, udpTarget.Port())
		}
		packet := append(append([]byte{0, 0, 0}, target...), p...)
		got := send(packet)
		expected := append(append([]byte{0, 0, 0}, wireTarget(udpTarget)...), p...)
		if !bytes.Equal(got, expected) {
			t.Fatal("destination/reply packet integrity mismatch")
		}
	}
	if opener.opened.Load() != 2 {
		t.Fatalf("target flows not independent: %d", opener.opened.Load())
	}
	rogue, _ := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	defer rogue.Close()
	packet := append(append([]byte{0, 0, 0}, wireTarget(udpTarget)...), []byte("dropped")...)
	rogue.SetDeadline(time.Now().Add(120 * time.Millisecond))
	rogue.WriteToUDPAddrPort(packet, relay)
	if _, _, e := rogue.ReadFromUDPAddrPort(make([]byte, 100)); e == nil {
		t.Fatal("wrong source port accepted")
	}
	packet[2] = 1
	udp.SetDeadline(time.Now().Add(120 * time.Millisecond))
	udp.WriteToUDPAddrPort(packet, relay)
	if _, _, e := udp.ReadFromUDPAddrPort(make([]byte, 100)); e == nil {
		t.Fatal("fragment accepted")
	}
	control.Close()
	deadline := time.Now().Add(2 * time.Second)
	for opener.active.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if opener.active.Load() != 0 {
		t.Fatal("control close retained flow workers")
	}
	// A retained credential is reauthorized on sends; rejection must close control.
	control = dial()
	if e := socksAuth(control, raw.Value()); e != nil {
		t.Fatal(e)
	}
	control.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0})
	relay, e = readSocksReply(control)
	if e != nil {
		t.Fatal(e)
	}
	auth.enabled.Store(false)
	packet[2] = 0
	udp.SetDeadline(time.Now().Add(2 * time.Second))
	if _, e := udp.WriteToUDPAddrPort(packet, relay); e != nil {
		t.Fatalf("reauthorization packet was not sent: %v", e)
	}
	if _, e := control.Read(make([]byte, 1)); e == nil {
		t.Fatal("reauthorization rejection retained association")
	} else if timeout, ok := e.(net.Error); ok && timeout.Timeout() {
		t.Fatalf("reauthorization did not close control: %v", e)
	}
	control.Close()
	auth.enabled.Store(true)
	control = dial()
	if e := socksAuth(control, raw.Value()); e != nil {
		t.Fatal(e)
	}
	control.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0})
	if _, e := readSocksReply(control); e != nil {
		t.Fatal(e)
	}
	drainCtx, drainCancel := context.WithTimeout(ctx, 2*time.Second)
	defer drainCancel()
	if e := server.Shutdown(drainCtx); e != nil {
		t.Fatal(e)
	}
	if _, e := control.Read(make([]byte, 1)); e == nil {
		t.Fatal("shutdown retained control")
	} else if timeout, ok := e.(net.Error); ok && timeout.Timeout() {
		t.Fatalf("shutdown did not close control: %v", e)
	}
	control.Close()
	t.Log("owned authenticated SOCKS listener: pipelined CONNECT via supplied HTTP handler; three binary UDP packets through REALITY+Vision; independent literal/domain flows; peer/fragment rejection; reauthorization, control-close and shutdown drain")
}

type fixtureAuthorizer struct {
	credential captureadmission.ProxyCredential
	enabled    atomic.Bool
}

func (a *fixtureAuthorizer) Authorize(_ context.Context, c captureadmission.ProxyCredential) (captureadmission.Admission, error) {
	if !a.enabled.Load() || c != a.credential {
		return captureadmission.Admission{}, captureadmission.ErrCredentialRejected
	}
	return captureadmission.NewManual("owned-fixture", 1, "local protocol fixture")
}

type fixtureOpener struct {
	client         *vlesscore.Client
	peer           netip.AddrPort
	opened, active atomic.Int32
}

func (o *fixtureOpener) OpenUDP(ctx context.Context, _ captureadmission.Admission, target Target) (OpenedUDP, error) {
	if target.Port != o.peer.Port() || (target.Host != o.peer.Addr().String() && target.Host != "echo.invalid") {
		return OpenedUDP{}, errors.New("fixture target denied")
	}
	c, e := o.client.DialUDP(ctx, o.peer)
	if e != nil {
		return OpenedUDP{}, e
	}
	o.opened.Add(1)
	o.active.Add(1)
	return OpenedUDP{Flow: &fixtureFlow{DatagramConn: c, owner: o}, Peer: o.peer}, nil
}

type fixtureFlow struct {
	vlesscore.DatagramConn
	owner *fixtureOpener
	once  sync.Once
}

func (f *fixtureFlow) Close() error {
	e := f.DatagramConn.Close()
	f.once.Do(func() { f.owner.active.Add(-1) })
	return e
}
func socksAuth(c net.Conn, password string) error {
	if _, e := c.Write([]byte{5, 1, 2}); e != nil {
		return e
	}
	a := make([]byte, 2)
	if _, e := io.ReadFull(c, a); e != nil {
		return e
	}
	if !bytes.Equal(a, []byte{5, 2}) {
		return errors.New("method rejected")
	}
	p := append([]byte{1, 7}, []byte("capture")...)
	p = append(p, byte(len(password)))
	p = append(p, password...)
	if _, e := c.Write(p); e != nil {
		return e
	}
	if _, e := io.ReadFull(c, a); e != nil {
		return e
	}
	if !bytes.Equal(a, []byte{1, 0}) {
		return errors.New("auth rejected")
	}
	return nil
}
func wireTarget(p netip.AddrPort) []byte {
	a := []byte{1}
	if p.Addr().Is6() {
		a[0] = 4
	}
	a = append(a, p.Addr().AsSlice()...)
	return binary.BigEndian.AppendUint16(a, p.Port())
}
func readSocksReply(c net.Conn) (netip.AddrPort, error) {
	h := make([]byte, 4)
	if _, e := io.ReadFull(c, h); e != nil {
		return netip.AddrPort{}, e
	}
	if h[0] != 5 || h[1] != 0 || h[2] != 0 {
		return netip.AddrPort{}, errors.New("SOCKS request rejected")
	}
	n := 4
	if h[3] == 4 {
		n = 16
	} else if h[3] != 1 {
		return netip.AddrPort{}, errors.New("unexpected reply address")
	}
	b := make([]byte, n+2)
	if _, e := io.ReadFull(c, b); e != nil {
		return netip.AddrPort{}, e
	}
	ip, _ := netip.AddrFromSlice(b[:n])
	return netip.AddrPortFrom(ip, binary.BigEndian.Uint16(b[n:])), nil
}

func ingressFixture(t *testing.T, ctx context.Context, binary string) (vlesscore.Config, netip.AddrPort, netip.AddrPort, netip.AddrPort, *x509.CertPool) {
	t.Helper()
	version, err := exec.CommandContext(ctx, binary, "version").Output()
	if err != nil || !bytes.Contains(version, []byte("26.3.27")) || !bytes.Contains(version, []byte("d2758a0")) {
		t.Fatal("test requires verified Xray 26.3.27/d2758a0 binary")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "reality.fixture.invalid"}, DNSNames: []string{"reality.fixture.invalid"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	tcp, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	secure, err := tls.Listen("tcp4", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, NextProtos: []string{"h2", "http/1.1"}, CurvePreferences: []tls.CurveID{tls.X25519}})
	if err != nil {
		t.Fatal(err)
	}
	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	var connections sync.Map
	serve := func(l net.Listener) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				c, e := l.Accept()
				if e != nil {
					return
				}
				connections.Store(c, true)
				workers.Add(1)
				go func() {
					defer workers.Done()
					defer connections.Delete(c)
					defer c.Close()
					c.SetDeadline(time.Now().Add(15 * time.Second))
					io.Copy(c, c)
				}()
			}
		}()
	}
	serve(tcp)
	serve(secure)
	workers.Add(1)
	go func() {
		defer workers.Done()
		b := make([]byte, 65535)
		for {
			n, p, e := udp.ReadFromUDP(b)
			if e != nil {
				return
			}
			if _, e = udp.WriteToUDP(b[:n], p); e != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		tcp.Close()
		secure.Close()
		udp.Close()
		connections.Range(func(k, v any) bool { k.(net.Conn).Close(); return true })
		workers.Wait()
	})
	portListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := netip.MustParseAddrPort(portListener.Addr().String())
	portListener.Close()
	realityKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	uuid := hex.EncodeToString(random[:4]) + "-" + hex.EncodeToString(random[4:6]) + "-" + hex.EncodeToString(random[6:8]) + "-" + hex.EncodeToString(random[8:10]) + "-" + hex.EncodeToString(random[10:])
	shortID := hex.EncodeToString(random[:8])
	config := vlesscore.Config{Executable: binary, Server: server, UUID: uuid, ServerName: "reality.fixture.invalid", Password: base64.RawURLEncoding.EncodeToString(realityKey.PublicKey().Bytes()), ShortID: shortID}
	serverConfig := map[string]any{"log": map[string]any{"loglevel": "none"}, "inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": server.Port(), "protocol": "vless", "settings": map[string]any{"clients": []any{map[string]any{"id": uuid, "flow": "xtls-rprx-vision"}}, "decryption": "none"}, "streamSettings": map[string]any{"network": "raw", "security": "reality", "realitySettings": map[string]any{"show": false, "target": secure.Addr().String(), "serverNames": []string{config.ServerName}, "privateKey": base64.RawURLEncoding.EncodeToString(realityKey.Bytes()), "shortIds": []string{shortID}, "xver": 0}}}}, "outbounds": []any{map[string]any{"tag": "blocked", "protocol": "blackhole"}, map[string]any{"tag": "fixtures", "protocol": "freedom"}}, "routing": map[string]any{"rules": []any{map[string]any{"type": "field", "ip": []string{"127.0.0.1"}, "port": netip.MustParseAddrPort(tcp.Addr().String()).Port(), "outboundTag": "fixtures"}, map[string]any{"type": "field", "ip": []string{"127.0.0.1"}, "port": netip.MustParseAddrPort(secure.Addr().String()).Port(), "outboundTag": "fixtures"}, map[string]any{"type": "field", "ip": []string{"127.0.0.1"}, "port": udp.LocalAddr().(*net.UDPAddr).Port, "outboundTag": "fixtures"}}}}
	encoded, err := json.Marshal(serverConfig)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "server.json")
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "run", "-c", path)
	if err := command.Start(); err != nil {
		t.Fatal("fixture core start failed")
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	t.Cleanup(func() {
		command.Process.Signal(os.Interrupt)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			command.Process.Kill()
			<-done
		}
	})
	for {
		select {
		case <-done:
			t.Fatal("fixture core exited before readiness")
		default:
		}
		c, e := (&net.Dialer{Timeout: 100 * time.Millisecond}).DialContext(ctx, "tcp", server.String())
		if e == nil {
			c.Close()
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("fixture readiness timed out")
		case <-time.After(20 * time.Millisecond):
		}
	}
	return config, netip.MustParseAddrPort(tcp.Addr().String()), netip.MustParseAddrPort(secure.Addr().String()), udp.LocalAddr().(*net.UDPAddr).AddrPort(), roots
}
