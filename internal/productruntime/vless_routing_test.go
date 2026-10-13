package productruntime

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/vlessprofile"
	"golang.org/x/net/dns/dnsmessage"
)

type vlessRuntimeNetwork struct {
	direct      string
	directDials atomic.Int64
	directDNS   atomic.Int64
	nodeDNS     atomic.Int64
	dohDNS      atomic.Int64
	denied      atomic.Int64
}

func (n *vlessRuntimeNetwork) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" || address != n.direct {
		n.denied.Add(1)
		return nil, errors.New("contained fixture rejects unexpected Direct dial")
	}
	n.directDials.Add(1)
	return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, address)
}
func (n *vlessRuntimeNetwork) LookupNetIP(_ context.Context, network, host string) ([]netip.Addr, error) {
	switch host {
	case "direct.fixture":
		n.directDNS.Add(1)
	case "node.fixture":
		n.nodeDNS.Add(1)
	case "doh.fixture":
		n.dohDNS.Add(1)
	default:
		return nil, errors.New("contained fixture rejects ambient target DNS")
	}
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}

// Removing private import, shared Runtime builder composition, proxy DoH
// bootstrap, or the pre-DNS domain override breaks actual application bytes.
func TestActualRuntimeImportedVLESSConnectAndDirectDomains(t *testing.T) {
	binary := os.Getenv("VIBERMATE_TEST_XRAY_BINARY")
	if binary == "" {
		t.Skip("requires pinned explicit Xray binary; no integration executed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	var proxyHTTP, directHTTP, dohQueries atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHTTP.Add(1)
		io.WriteString(w, "owned-vless-http-bytes")
	}))
	defer target.Close()
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		directHTTP.Add(1)
		io.WriteString(w, "owned-direct-http-bytes")
	}))
	defer direct.Close()
	doh := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(io.LimitReader(r.Body, 65536))
		if err != nil {
			t.Error(err)
			return
		}
		var parser dnsmessage.Parser
		header, err := parser.Start(data)
		if err != nil {
			t.Error(err)
			return
		}
		question, err := parser.Question()
		if err != nil || question.Name.String() != "proxy.fixture." {
			t.Error("unexpected private DoH question")
			return
		}
		dohQueries.Add(1)
		b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: header.ID, Response: true, RecursionAvailable: true})
		b.StartQuestions()
		b.Question(question)
		b.StartAnswers()
		if question.Type == dnsmessage.TypeA {
			b.AResource(dnsmessage.ResourceHeader{Name: question.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 30}, dnsmessage.AResource{A: [4]byte{127, 0, 0, 1}})
		}
		reply, err := b.Finish()
		if err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/dns-message")
		w.Write(reply)
	}))
	doh.Config.ErrorLog = log.New(io.Discard, "", 0)
	cert, roots := runtimeVLESSCertificate(t)
	doh.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, NextProtos: []string{"h2", "http/1.1"}, CurvePreferences: []tls.CurveID{tls.X25519}}
	doh.StartTLS()
	defer doh.Close()
	targetPeer := netip.MustParseAddrPort(target.Listener.Addr().String())
	directPeer := netip.MustParseAddrPort(direct.Listener.Addr().String())
	dohPeer := netip.MustParseAddrPort(doh.Listener.Addr().String())
	uri := runtimeVLESSServer(t, ctx, binary, dohPeer, []netip.AddrPort{targetPeer, dohPeer})
	network := &vlessRuntimeNetwork{direct: directPeer.String()}
	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	options := testOptions(t, hostcontract.Desktop(), gate)
	options.VLESSExecutable = binary
	options.NetworkOptions = egressnetwork.BuilderOptions{BaseDialer: network, SystemResolver: network, TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}}
	runtime, err := Start(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	liveRuntime := runtime
	defer func() {
		if liveRuntime != nil {
			shutdownRuntime(t, liveRuntime)
		}
	}()
	profile, err := runtime.VLESSProfiles().Import(ctx, vlessprofile.ImportCommand{ID: "profile.runtime-vless", DisplayName: "Owned REALITY node", URI: uri, Resolver: egressnetwork.ResolverPolicy{Kind: egressnetwork.ResolverDoH, DoHURL: fmt.Sprintf("https://doh.fixture:%d/dns-query", dohPeer.Port()), Transport: egressnetwork.ResolverTransportProxy}, DirectDomains: "DIRECT.fixture.\ndirect.fixture"})
	if err != nil {
		t.Fatalf("private URI import: %v", err)
	}
	public, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(uri)
	for _, secret := range []string{uri, parsed.User.Username(), parsed.Query().Get("pbk"), "node.fixture", "reality.fixture.invalid"} {
		if bytes.Contains(public, []byte(secret)) {
			t.Fatal("private node material entered public profile")
		}
	}
	if profile.Policy.Proxy.CredentialRevision != 1 || profile.Policy.DirectDomains != "direct.fixture" {
		t.Fatal("public immutable identity or domain normalization lost")
	}
	if _, err := runtime.EgressDefaults().SetDefault(ctx, egressprofile.SetDefaultCommand{ExpectedRevision: 1, Profile: &egressprofile.ProfileRef{ID: profile.ID, Revision: profile.Revision}}); err != nil {
		t.Fatal(err)
	}
	_, err = runtime.ConnectionRules().Replace(ctx, runtime.ConnectionRules().Current().Revision, []connectionpolicy.Rule{
		{ID: "allow.proxy-fixture", Priority: 1, Decision: connectionpolicy.DecisionAllow, Match: connectionpolicy.MatchExactHostPort("proxy.fixture", targetPeer.Port())},
		{ID: "allow.direct-fixture", Priority: 2, Decision: connectionpolicy.DecisionAllow, Match: connectionpolicy.MatchExactHostPort("direct.fixture", directPeer.Port())},
	}, connectionpolicy.ModeDenyUnknown)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := runtime.ManualCaptures().Create(ctx, manualcapture.CreateCommand{Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "Owned VLESS capture", ClientClass: manualcapture.ClientCLI, Lifetime: manualcapture.LifetimeUntilRevoked})
	if err != nil {
		t.Fatal(err)
	}
	capture, err := captureidentity.New(captureidentity.KindManualCapture, grant.Capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.CaptureAssignments().Create(ctx, captureassignment.CreateCommand{Capture: capture, EnvironmentID: environment.SystemTransparentID, Source: captureassignment.SourceSystemTransparent}); err != nil {
		t.Fatal(err)
	}
	listener := httptest.NewUnstartedServer(runtime.ProxyHandler())
	listener.Config.BaseContext = func(net.Listener) context.Context {
		return egressnetwork.WithTargetAccess(ctx, egressnetwork.TargetAccessPublicOnly)
	}
	listener.Start()
	defer listener.Close()
	request := func(host string, port uint16, want string) {
		t.Helper()
		conn, err := net.DialTimeout("tcp4", listener.Listener.Addr().String(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(6 * time.Second))
		authority := net.JoinHostPort(host, fmt.Sprint(port))
		auth := base64.StdEncoding.EncodeToString([]byte("capture:" + grant.Credential.Value()))
		fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\n\r\n", authority, authority, auth)
		reader := bufio.NewReader(conn)
		response, err := http.ReadResponse(reader, nil)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != 200 {
			response.Body.Close()
			t.Fatalf("actual Runtime CONNECT status %d", response.StatusCode)
		}
		response.Body.Close()
		fmt.Fprintf(conn, "GET /owned HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", authority)
		response, err = http.ReadResponse(reader, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil || string(body) != want {
			t.Fatalf("actual HTTP bytes mismatch: %v", err)
		}
	}
	request("proxy.fixture", targetPeer.Port(), "owned-vless-http-bytes")
	if proxyHTTP.Load() != 1 || dohQueries.Load() != 2 || network.directDials.Load() != 0 || network.nodeDNS.Load() != 1 || network.dohDNS.Load() != 2 || network.denied.Load() != 0 {
		t.Fatalf("VLESS/DoH route counters proxy=%d DoH=%d direct=%d nodeDNS=%d dohDNS=%d denied=%d", proxyHTTP.Load(), dohQueries.Load(), network.directDials.Load(), network.nodeDNS.Load(), network.dohDNS.Load(), network.denied.Load())
	}
	before := dohQueries.Load()
	request("direct.fixture", directPeer.Port(), "owned-direct-http-bytes")
	if directHTTP.Load() != 1 || network.directDNS.Load() != 1 || network.directDials.Load() != 1 || dohQueries.Load() != before || network.denied.Load() != 0 {
		t.Fatal("Direct-domain request did not bypass proxy and configured DoH")
	}
	t.Log("actual imported global VLESS Runtime CONNECT carried owned HTTP bytes through REALITY+Vision and proxied DoH; matching domain used only system DNS+Direct")
	listener.Close()
	shutdownRuntime(t, runtime)
	liveRuntime = nil
	nextGate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	reopenedOptions := testOptionsWithPaths(t, options.Paths, hostcontract.Desktop(), nextGate)
	reopenedOptions.VLESSExecutable = binary
	reopenedOptions.NetworkOptions = options.NetworkOptions
	reopened, err := Start(ctx, reopenedOptions)
	if err != nil {
		t.Fatal(err)
	}
	liveRuntime = reopened
	persisted, err := reopened.EgressProfiles().GetRevision(ctx, profile.ID, profile.Revision)
	if err != nil || !persisted.Equal(profile) {
		t.Fatal("public SQL reopen lost exact private identity")
	}
}

func runtimeVLESSCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(31), DNSNames: []string{"reality.fixture.invalid", "doh.fixture"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, roots
}

func runtimeVLESSServer(t *testing.T, ctx context.Context, binary string, handshake netip.AddrPort, targets []netip.AddrPort) string {
	t.Helper()
	version, err := exec.CommandContext(ctx, binary, "version").Output()
	if err != nil || !bytes.Contains(version, []byte("26.3.27")) || !bytes.Contains(version, []byte("d2758a0")) {
		t.Fatal("requires pinned Xray 26.3.27/d2758a0")
	}
	reserve, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := netip.MustParseAddrPort(reserve.Addr().String())
	reserve.Close()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	identity := make([]byte, 16)
	if _, err := rand.Read(identity); err != nil {
		t.Fatal(err)
	}
	id := hex.EncodeToString(identity)
	uuid := id[:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:]
	sid := id[:16]
	rules := make([]any, 0, len(targets))
	for _, target := range targets {
		rules = append(rules, map[string]any{"type": "field", "ip": []string{target.Addr().String()}, "port": target.Port(), "outboundTag": "owned"})
	}
	config := map[string]any{"log": map[string]any{"loglevel": "none"}, "inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": server.Port(), "protocol": "vless", "settings": map[string]any{"clients": []any{map[string]any{"id": uuid, "flow": "xtls-rprx-vision"}}, "decryption": "none"}, "streamSettings": map[string]any{"network": "raw", "security": "reality", "realitySettings": map[string]any{"target": handshake.String(), "serverNames": []string{"reality.fixture.invalid"}, "privateKey": base64.RawURLEncoding.EncodeToString(key.Bytes()), "shortIds": []string{sid}, "xver": 0}}}}, "outbounds": []any{map[string]any{"tag": "blocked", "protocol": "blackhole"}, map[string]any{"tag": "owned", "protocol": "freedom"}}, "routing": map[string]any{"rules": rules}}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(data)
	path := filepath.Join(t.TempDir(), "owned-reality.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "run", "-c", path)
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Start(); err != nil {
		t.Fatal("owned REALITY server start failed")
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	t.Cleanup(func() {
		command.Process.Signal(os.Interrupt)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			command.Process.Kill()
			<-done
		}
	})
	for {
		select {
		case <-done:
			t.Fatal("owned REALITY server exited before readiness")
		default:
		}
		conn, err := (&net.Dialer{Timeout: 100 * time.Millisecond}).DialContext(ctx, "tcp", server.String())
		if err == nil {
			conn.Close()
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("owned REALITY server readiness timeout")
		case <-time.After(20 * time.Millisecond):
		}
	}
	values := url.Values{"security": {"reality"}, "encryption": {"none"}, "type": {"tcp"}, "flow": {"xtls-rprx-vision"}, "sni": {"reality.fixture.invalid"}, "pbk": {base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())}, "sid": {sid}, "fp": {"chrome"}}
	return "vless://" + uuid + "@node.fixture:" + fmt.Sprint(server.Port()) + "?" + values.Encode() + "#Owned"
}
