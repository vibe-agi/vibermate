package nativemcp

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type nativeClosedNetwork struct {
	proxies               map[string]bool
	direct, unexpectedDNS atomic.Int64
}

func (n *nativeClosedNetwork) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if !n.proxies[address] {
		n.direct.Add(1)
		return nil, errors.New("synthetic fixture denied unexpected/direct dial")
	}
	return (&net.Dialer{}).DialContext(ctx, network, address)
}
func (n *nativeClosedNetwork) LookupNetIP(_ context.Context, network, host string) ([]netip.Addr, error) {
	if host != "native.example" || network != "ip" {
		n.unexpectedDNS.Add(1)
		return nil, errors.New("synthetic fixture denied DNS")
	}
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}

type nativeTLSFixtureTransport struct {
	builder *egressnetwork.Builder
	roots   *x509.CertPool
}

func (w *nativeTLSFixtureTransport) RoundTrip(request *http.Request, policy egressnetwork.Policy) (*http.Response, error) {
	if request.URL.Host != "native.example" || request.URL.Scheme != "https" || request.URL.Path != environment.ClientToolMCPPath {
		return nil, errors.New("unexpected synthetic native origin")
	}
	dialer, err := w.builder.Dialer(policy)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{Proxy: nil, DialContext: dialer.DialContext, DisableKeepAlives: true, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: w.roots}, TLSHandshakeTimeout: time.Second, ResponseHeaderTimeout: time.Second}
	return transport.RoundTrip(request)
}
func nativeSOCKSOnlyFixture(t *testing.T, upstream string) (net.Listener, *atomic.Int64) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	calls := &atomic.Int64{}
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer connection.Close()
				connection.SetDeadline(time.Now().Add(3 * time.Second))
				greeting := make([]byte, 3)
				if _, err := io.ReadFull(connection, greeting); err != nil || string(greeting) != "\x05\x01\x00" {
					return
				}
				if _, err := connection.Write([]byte{5, 0}); err != nil {
					return
				}
				request := make([]byte, 10)
				if _, err := io.ReadFull(connection, request); err != nil || string(request) != "\x05\x01\x00\x01\x7f\x00\x00\x01\x01\xbb" {
					return
				}
				peer, err := net.DialTimeout("tcp4", upstream, time.Second)
				if err != nil {
					return
				}
				defer peer.Close()
				peer.SetDeadline(time.Now().Add(3 * time.Second))
				calls.Add(1)
				if _, err := connection.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
					return
				}
				done := make(chan struct{})
				go func() {
					io.Copy(peer, connection)
					if tcp, ok := peer.(*net.TCPConn); ok {
						tcp.CloseWrite()
					}
					close(done)
				}()
				io.Copy(connection, peer)
				connection.Close()
				peer.Close()
				<-done
			}()
		}
	}()
	t.Cleanup(func() { listener.Close(); workers.Wait() })
	return listener, calls
}

func TestNativeInheritedExitUsesActualSOCKSTLSAndDefaultEditChangesNextOperation(t *testing.T) {
	f := newNativeFixtureWithChoice(t, environment.ToolPolicyReview, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, 0, egressprofile.Inherit())
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"native.example"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	var requests, badCredentials atomic.Int64
	var callbackMode atomic.Bool
	var recoveryMode atomic.Bool
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "native.example" || r.URL.Path != environment.ClientToolMCPPath || r.Header.Get("Authorization") != "Bearer synthetic-original-login" {
			badCredentials.Add(1)
			w.WriteHeader(403)
			return
		}
		requests.Add(1)
		if recoveryMode.Load() && r.Method == "GET" {
			if r.Header.Get("Last-Event-Id") != "wire-progress" {
				w.WriteHeader(400)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "id: wire-terminal\ndata: {\"jsonrpc\":\"2.0\",\"id\":44,\"result\":{\"content\":[]}}\n\n")
			return
		}
		if callbackMode.Load() && r.Method == "GET" {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"id\":\"wire-parent-ping\",\"method\":\"ping\"}\n\n")
			return
		}
		body, _ := io.ReadAll(r.Body)
		var rpc map[string]json.RawMessage
		if json.Unmarshal(body, &rpc) != nil {
			w.WriteHeader(400)
			return
		}
		var method string
		json.Unmarshal(rpc["method"], &method)
		if callbackMode.Load() && method == "" {
			w.WriteHeader(202)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "synthetic-session")
			io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"synthetic","version":"1"}}}`)
		case "notifications/initialized":
			w.WriteHeader(202)
		case "tools/list":
			io.WriteString(w, `{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"read_file","inputSchema":{"type":"object"}}]}}`)
		case "tools/call":
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "id: wire-progress\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":\"wire-token\",\"progress\":1}}\n\n")
		case "ping":
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": rpc["id"], "result": map[string]any{}})
		default:
			w.WriteHeader(400)
		}
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	server.StartTLS()
	t.Cleanup(server.Close)
	first, firstCalls := nativeSOCKSOnlyFixture(t, server.Listener.Addr().String())
	second, secondCalls := nativeSOCKSOnlyFixture(t, server.Listener.Addr().String())
	network := &nativeClosedNetwork{proxies: map[string]bool{first.Addr().String(): true, second.Addr().String(): true}}
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	builder, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{BaseDialer: network, SystemResolver: network})
	if err != nil {
		t.Fatal(err)
	}
	f.executor.(*executor).dependencies.Transport = &nativeTLSFixtureTransport{builder: builder, roots: roots}
	profiles, err := egressprofile.NewManager(f.store.EgressProfileRepository(), nil)
	if err != nil {
		t.Fatal(err)
	}
	publish := func(id string, proxy net.Listener) egressprofile.ProfileRevision {
		policy := egressnetwork.DefaultPolicy()
		policy.Proxy = egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: proxy.Addr().String()}
		p, err := profiles.Publish(t.Context(), egressprofile.PublishCommand{ID: egressprofile.ID(id), DisplayName: id, Policy: policy})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	one, two := publish("profile.native-one", first), publish("profile.native-two", second)
	defaults, err := egressprofile.NewDefaultManager(f.store.EgressDefaultRepository(), profiles)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := defaults.SetDefault(t.Context(), egressprofile.SetDefaultCommand{ExpectedRevision: 1, Profile: &egressprofile.ProfileRef{ID: one.ID, Revision: 1}}); err != nil {
		t.Fatal(err)
	}
	counted := &countedNativeDefaults{resolver: defaults}
	f.executor.(*executor).dependencies.EgressDefaults = counted
	f.handshake(t)
	if firstCalls.Load() != 3 || secondCalls.Load() != 0 {
		t.Fatalf("handshake actual proxies first=%d second=%d", firstCalls.Load(), secondCalls.Load())
	}
	callbackMode.Store(true)
	parent := f.captured(t, "GET", "", "synthetic-session")
	if got := f.executor.Serve(t.Context(), parent, httptest.NewRecorder()); got.Outcome != OutcomeCompleted {
		t.Fatal("actual TLS callback parent failed", got)
	}
	toolParent := f.captured(t, "POST", `{"jsonrpc":"2.0","id":44,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"},"_meta":{"progressToken":"wire-token"}}}`, "synthetic-session")
	toolDone := make(chan Terminal, 1)
	go func() { toolDone <- f.executor.Serve(t.Context(), toolParent, httptest.NewRecorder()) }()
	f.approve(t, toolParent.id)
	select {
	case got := <-toolDone:
		if got.Outcome != OutcomeFailed || got.Reason != ReasonStreamDetached {
			t.Fatal("approved physical POST did not detach", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("approved POST failed to detach")
	}
	original, err := f.store.NativeToolRepository().Get(t.Context(), toolParent.id)
	if err != nil || original.State != "started" {
		t.Fatal("physical detach fabricated logical completion", original, err)
	}
	beforeRelatedReads := counted.calls.Load()
	if _, err := defaults.SetDefault(t.Context(), egressprofile.SetDefaultCommand{ExpectedRevision: 2, Profile: &egressprofile.ProfileRef{ID: two.ID, Revision: 1}}); err != nil {
		t.Fatal(err)
	}
	counted.unavailable.Store(true)
	child := f.captured(t, "POST", `{"jsonrpc":"2.0","id":"wire-parent-ping","result":{}}`, "synthetic-session")
	childDone := make(chan Terminal, 1)
	go func() { childDone <- f.executor.Serve(t.Context(), child, httptest.NewRecorder()) }()
	f.approve(t, child.id)
	select {
	case got := <-childDone:
		if got.Outcome != OutcomeCompleted {
			t.Fatal("actual callback did not preserve parent exit", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("approved callback failed to drain")
	}
	recoveryMode.Store(true)
	recovery := f.captured(t, "GET", "", "synthetic-session")
	recovery.request.Header.Set("Last-Event-Id", "wire-progress")
	if got := f.executor.Serve(t.Context(), recovery, httptest.NewRecorder()); got.Outcome != OutcomeCompleted {
		t.Fatal("physical GET recovery failed", got)
	}
	if firstCalls.Load() != 7 || secondCalls.Load() != 0 {
		t.Fatalf("related sends reselected current proxy: A=%d B=%d", firstCalls.Load(), secondCalls.Load())
	}
	var childProxy string
	var childRevision uint64
	if err := f.db.QueryRow(`SELECT proxy_id,default_revision FROM native_tool_deliveries WHERE operation_id=?`, child.id).Scan(&childProxy, &childRevision); err != nil {
		t.Fatal(err)
	}
	if childProxy != one.ID.String() || childRevision != 2 {
		t.Fatal("callback wire/audit used different frozen exit")
	}
	var recoveryProxy string
	var recoveryRevision uint64
	if err := f.db.QueryRow(`SELECT proxy_id,default_revision FROM native_tool_deliveries WHERE operation_id=?`, recovery.id).Scan(&recoveryProxy, &recoveryRevision); err != nil {
		t.Fatal(err)
	}
	if recoveryProxy != one.ID.String() || recoveryRevision != 2 {
		t.Fatal("recovery wire/audit used different frozen exit")
	}
	if counted.calls.Load() != beforeRelatedReads {
		t.Fatal("callback/recovery reread unavailable current default")
	}
	original, err = f.store.NativeToolRepository().Get(t.Context(), toolParent.id)
	if err != nil || original.State != "completed" {
		t.Fatal("physical GET did not separately complete real original RPC", original, err)
	}
	var postOnce int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM native_tool_once WHERE operation_id=?`, toolParent.id).Scan(&postOnce); err != nil || postOnce != 1 {
		t.Fatalf("original POST once=%d err=%v", postOnce, err)
	}
	for _, id := range []string{toolParent.id.String(), child.id.String()} {
		var consumed int
		if err := f.db.QueryRow(`SELECT COUNT(*) FROM native_tool_approvals WHERE operation_id=? AND consumed_at_unix_ms IS NOT NULL`, id).Scan(&consumed); err != nil || consumed != 1 {
			t.Fatalf("real Review once=%d err=%v", consumed, err)
		}
	}
	counted.unavailable.Store(false)
	terminal, _ := f.serve(t, `{"jsonrpc":"2.0","id":33,"method":"ping"}`, "synthetic-session")
	if terminal.Outcome != OutcomeCompleted {
		t.Fatal(terminal)
	}
	if firstCalls.Load() != 7 || secondCalls.Load() != 1 || network.direct.Load() != 0 || network.unexpectedDNS.Load() != 0 || badCredentials.Load() != 0 || requests.Load() != 8 {
		t.Fatalf("wire route/containment mismatch: proxy=%d/%d direct=%d dns=%d credentials=%d requests=%d", firstCalls.Load(), secondCalls.Load(), network.direct.Load(), network.unexpectedDNS.Load(), badCredentials.Load(), requests.Load())
	}
	var proxy, source string
	var revision uint64
	if err := f.db.QueryRow(`SELECT proxy_id,selection_source,default_revision FROM native_tool_deliveries WHERE operation_id=?`, terminal.OperationID).Scan(&proxy, &source, &revision); err != nil {
		t.Fatal(err)
	}
	if proxy != two.ID.String() || source != "global" || revision != 3 {
		t.Fatalf("actual wire/audit mismatch %s/%s/%d", proxy, source, revision)
	}
	// The chosen proxy disappears. A network error may be uncertain, but it
	// cannot silently resend through direct or the previously selected proxy.
	second.Close()
	failed, _ := f.serve(t, `{"jsonrpc":"2.0","id":34,"method":"ping"}`, "synthetic-session")
	if failed.Outcome == OutcomeCompleted || requests.Load() != 8 || firstCalls.Load() != 7 || secondCalls.Load() != 1 || network.direct.Load() != 0 {
		t.Fatal("failed explicit selected proxy retried via another path")
	}
}
