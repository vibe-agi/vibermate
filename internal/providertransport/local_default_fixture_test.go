package providertransport

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

// Test-only bridge keeps real account/Store assembly outside the transport
// package's production dependency graph, with no generic target dial ability.
type LocalDiscoveryLease struct {
	Endpoint             upstreamendpoint.Endpoint
	Lease                providerauth.Lease
	DefaultReads         func() int64
	Rejected             bool
	WantSource           egressprofile.SelectionSource
	WantProfile          egressprofile.ProfileRevision
	ExpectedDefaultReads int64
	WhileHeld            func() error
	Model                func(*Client, *offlinehold.Gate, transportprofile.Observation) providerauth.AccountRef
	ExpectModelRejected  bool
}

func ExerciseLocalDefaultDiscovery(t *testing.T, acquire func(*testing.T, originidentity.ProviderOrigin, egressprofile.ProfileRevision, secretstore.Store) LocalDiscoveryLease, privateDNS ...bool) {
	t.Helper()
	var requests atomic.Int64
	expectedHost := ""
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Host != expectedHost || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer synthetic-local-token" {
			t.Error("unexpected private discovery request")
			w.WriteHeader(403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/models":
			_, _ = io.WriteString(w, `{"data":[{"id":"local-synthetic"}]}`)
		case "POST /v1/responses":
			_, _ = io.Copy(io.Discard, r.Body)
			_, _ = io.WriteString(w, `{"id":"resp_local","object":"response","created_at":1,"status":"completed","model":"synthetic","output":[{"id":"msg_local","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"local model complete","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
		default:
			t.Error("unexpected local operation")
			w.WriteHeader(403)
		}
	}))
	expectedHost = server.Listener.Addr().String()
	useDNS := len(privateDNS) > 0 && privateDNS[0]
	if useDNS {
		_, port, err := net.SplitHostPort(expectedHost)
		if err != nil {
			t.Fatal(err)
		}
		expectedHost = net.JoinHostPort("local.fixture", port)
	}
	server.Start()
	defer server.Close()
	proxy, proxyConnections := newAccountSOCKSFixture(t, server.Listener.Addr().String())
	network := &localDiscoveryNetwork{local: server.Listener.Addr().String(), proxy: proxy.Addr().String()}
	if useDNS {
		network.expectedDNS = "local.fixture"
		network.expectedDNSAddress = expectedHost
	}
	dialers, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{BaseDialer: network, SystemResolver: network})
	if err != nil {
		t.Fatal(err)
	}
	transport, err := newCleartextTransportWithDialers(dialers, DefaultTransportTimeouts())
	if err != nil {
		t.Fatal(err)
	}
	secrets := &accountGlobalSecrets{secretReaderStub: &secretReaderStub{}}
	origin, err := originidentity.ParseProviderOrigin("http://" + expectedHost + "/v1")
	if err != nil {
		t.Fatal(err)
	}
	profile := testAccountEgress()
	profile.Policy.Proxy.Endpoint = proxy.Addr().String()
	selected := acquire(t, origin, profile, secrets)
	if selected.Rejected {
		if selected.Lease != nil || requests.Load() != 0 || network.localCalls.Load() != 0 || network.proxyCalls.Load() != 0 || proxyConnections.Load() != 0 || network.unexpected.Load() != 0 {
			t.Fatal("rejected acquisition touched network")
		}
		return
	}
	var ref providerauth.AccountRef
	if selected.Model == nil {
		defer selected.Lease.Release()
		var ok bool
		ref, ok = selected.Lease.Account()
		if !ok {
			t.Fatal("actual account lease absent")
		}
	}
	auth, err := NewStaticBearerAuthenticator(secrets)
	if err != nil {
		t.Fatal(err)
	}
	audit := &runtimeAuditRecorder{}
	gate := newStartedGate(t)
	client, err := NewClient(ClientOptions{Coordinator: gate, Authenticator: auth, Transport: transport, Audit: audit, InstanceIDs: &sequentialInstanceIDs{}})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownClient(t, client)
	if selected.Model != nil {
		ref = selected.Model(client, gate, captureTransportClientHello(t, nil))
		if selected.ExpectModelRejected {
			if requests.Load() != 0 || network.localCalls.Load() != 0 || network.proxyCalls.Load() != 0 || audit.appendCount() != 0 || selected.DefaultReads() != 0 {
				t.Fatal("rejected compiled model reached outbound work")
			}
			return
		}
		if requests.Load() != 1 || network.localCalls.Load() != 1 || network.proxyCalls.Load() != 0 || proxyConnections.Load() != 0 || network.unexpected.Load() != 0 || selected.DefaultReads() != 0 {
			t.Fatal("compiled local model changed exact network/default counts")
		}
		started, terminal := audit.attempts()
		if ref.ResolvedEgress.Source() != selected.WantSource || ref.ResolvedEgress.DefaultRevision() != 0 || !ref.EgressProfile.Equal(selected.WantProfile) || started.Decision().SelectionSource != selected.WantSource || started.Decision().DefaultRevision != 0 || started.Decision().ProxyID != ref.EgressProfile.ID.String() || terminal.Decision() != started.Decision() || terminal.Outcome() != egressaudit.OutcomeCompleted {
			t.Fatal("compiled local model changed authored selection")
		}
		return
	}
	var response *http.Response
	wantDials := int64(1)
	if selected.WhileHeld == nil {
		response, err = client.FetchEndpointModels(t.Context(), selected.Endpoint, selected.Lease)
	} else {
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		if _, err = gate.Enter(ctx, gate.Snapshot().Revision); err != nil {
			t.Fatal(err)
		}
		done := make(chan runtimeFetchResult, 1)
		joined := false
		defer func() {
			cancel()
			if !joined {
				result := <-done
				if result.response != nil {
					result.response.Body.Close()
				}
			}
		}()
		go func() {
			r, e := client.FetchEndpointModels(ctx, selected.Endpoint, selected.Lease)
			done <- runtimeFetchResult{response: r, err: e}
		}()
		waitForGateQueue(t, gate, 1)
		if network.localCalls.Load() != 0 || network.proxyCalls.Load() != 0 || audit.appendCount() != 0 || secrets.readCount() != 0 || selected.DefaultReads() != 0 {
			t.Fatal("held local request touched default/secret/network/audit")
		}
		if err = selected.WhileHeld(); err != nil {
			t.Fatal(err)
		}
		prober := &ProviderProber{dialers: dialers, timeout: time.Second}
		if _, err = gate.Resume(ctx, gate.Snapshot().Revision, offlinehold.ResumeRequest{Targets: gate.PendingProbeTargets()}, prober); err != nil {
			t.Fatal(err)
		}
		result := <-done
		joined = true
		response, err = result.response, result.err
		wantDials = 2
	}
	if err != nil {
		t.Fatalf("local discovery was disabled by external default: source=%s defaultRev=%d localHTTP=%d localDial=%d proxyDial=%d err=%v", ref.ResolvedEgress.Source(), ref.ResolvedEgress.DefaultRevision(), requests.Load(), network.localCalls.Load(), network.proxyCalls.Load(), err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != `{"data":[{"id":"local-synthetic"}]}` || requests.Load() != 1 || network.localCalls.Load() != wantDials || network.proxyCalls.Load() != 0 || proxyConnections.Load() != 0 || network.unexpected.Load() != 0 {
		t.Fatal("local discovery changed exact physical path/count/body", err)
	}
	wantDNS := int64(0)
	if useDNS {
		wantDNS = wantDials
	}
	if network.dnsCalls.Load() != wantDNS {
		t.Fatal("local resolution changed exact DNS count")
	}
	started, terminal := audit.attempts()
	wantSource, wantProfile := selected.WantSource, selected.WantProfile
	if wantSource == "" {
		wantSource = egressprofile.SelectionTransport
	}
	if wantProfile == (egressprofile.ProfileRevision{}) {
		wantProfile = egressprofile.Direct()
	}
	if ref.ResolvedEgress.Source() != wantSource || ref.ResolvedEgress.DefaultRevision() != 0 || !ref.EgressProfile.Equal(wantProfile) || selected.DefaultReads() != selected.ExpectedDefaultReads {
		t.Fatal("local lease borrowed global state or invented authored selection")
	}
	if started.Decision().SelectionSource != wantSource || started.Decision().DefaultRevision != 0 || started.Decision().ProxyID != wantProfile.ID.String() || started.Decision().ProxyRevision != uint64(wantProfile.Revision) || terminal.Decision() != started.Decision() || terminal.Outcome() != egressaudit.OutcomeCompleted {
		t.Fatal("local physical audit did not preserve truthful direct-only selection")
	}
}

type localDiscoveryNetwork struct {
	local, proxy                       string
	expectedDNS                        string
	expectedDNSAddress                 string
	dnsCalls                           atomic.Int64
	localCalls, proxyCalls, unexpected atomic.Int64
}

func (n *localDiscoveryNetwork) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" {
		n.unexpected.Add(1)
		return nil, errors.New("unexpected network")
	}
	// System/direct delegates resolution to BaseDialer. Resolve only this
	// predeclared fixture authority without ever consulting host DNS.
	if n.expectedDNSAddress != "" && address == n.expectedDNSAddress {
		n.dnsCalls.Add(1)
		address = n.local
	}
	switch address {
	case n.local:
		n.localCalls.Add(1)
	case n.proxy:
		n.proxyCalls.Add(1)
	default:
		n.unexpected.Add(1)
		return nil, errors.New("unexpected destination")
	}
	return (&net.Dialer{}).DialContext(ctx, network, address)
}
func (n *localDiscoveryNetwork) LookupNetIP(_ context.Context, network, host string) ([]netip.Addr, error) {
	if n.expectedDNS != "" && host == n.expectedDNS && (network == "ip" || network == "ip4") {
		n.dnsCalls.Add(1)
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	n.unexpected.Add(1)
	return nil, errors.New("unexpected DNS")
}
