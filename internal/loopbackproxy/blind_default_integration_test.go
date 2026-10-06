package loopbackproxy_test

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/blindtunnel"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/loopbackproxy"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

type blindContainedNetwork struct {
	allowed map[string]bool // immutable before first use
	denied  atomic.Int64
}

func (n *blindContainedNetwork) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" || !n.allowed[address] {
		n.denied.Add(1)
		return nil, errors.New("fixture blocked direct/unlisted dial")
	}
	return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, address)
}
func (*blindContainedNetwork) LookupNetIP(_ context.Context, network, host string) ([]netip.Addr, error) {
	if network != "ip" || host != "blind.fixture" {
		return nil, errors.New("fixture blocked DNS")
	}
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}

type blindPrivateProxy struct {
	listener net.Listener
	requests atomic.Int64
	invalid  atomic.Int64
	refuse   atomic.Bool
	wg       sync.WaitGroup
}

func newBlindPrivateProxy(t *testing.T, upstream string, port uint16) *blindPrivateProxy {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &blindPrivateProxy{listener: l}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			p.wg.Add(1)
			go func() {
				defer p.wg.Done()
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(4 * time.Second))
				var hello [3]byte
				if _, err := io.ReadFull(c, hello[:]); err != nil {
					return
				}
				if hello != [3]byte{5, 1, 0} {
					p.invalid.Add(1)
					return
				}
				if _, err := c.Write([]byte{5, 0}); err != nil {
					return
				}
				var target [10]byte
				if _, err := io.ReadFull(c, target[:]); err != nil {
					return
				}
				if target[0] != 5 || target[1] != 1 || target[2] != 0 || target[3] != 1 || net.IP(target[4:8]).String() != "127.0.0.1" || binary.BigEndian.Uint16(target[8:]) != port {
					p.invalid.Add(1)
					return
				}
				if p.requests.Add(1) > 12 {
					p.invalid.Add(1)
					return
				}
				if p.refuse.Load() {
					_, _ = c.Write([]byte{5, 4, 0, 1, 0, 0, 0, 0, 0, 0})
					return
				}
				// The only onward socket is the exact private listener supplied by this test.
				peer, err := net.DialTimeout("tcp4", upstream, time.Second)
				if err != nil {
					return
				}
				defer peer.Close()
				_ = peer.SetDeadline(time.Now().Add(4 * time.Second))
				if _, err = c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
					return
				}
				done := make(chan struct{})
				go func() { io.Copy(peer, c); peer.Close(); close(done) }()
				io.Copy(c, peer)
				c.Close()
				peer.Close()
				<-done
			}()
		}
	}()
	t.Cleanup(func() {
		l.Close()
		p.wg.Wait()
		if p.invalid.Load() != 0 {
			t.Errorf("invalid/private fixture overflow=%d", p.invalid.Load())
		}
	})
	return p
}

func TestBlindProxyFailureKeepsFrozenAuditWithoutDirectFallback(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("refused proxy reached application") }))
	defer upstream.Close()
	p := newBlindPrivateProxy(t, upstream.Listener.Addr().String(), 443)
	p.refuse.Store(true)
	f, n := newContainedBlindProxyFixture(t, p)
	defer f.Close(t)
	profile := publishBlindDefault(t, f, p)
	c, response := f.Connect(t, f.grant.ProxyCapability.Value(), "blind.fixture:443")
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	c.Close()
	if response.StatusCode != 502 || p.requests.Load() != 1 || n.denied.Load() != 0 {
		t.Fatalf("proxy refusal status%d proxy%d direct%d", response.StatusCode, p.requests.Load(), n.denied.Load())
	}
	deadline := time.Now().Add(time.Second)
	for {
		page, err := f.egress.List(context.Background(), egressaudit.PageRequest{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) == 1 && page.Items[0].Attempt.Terminal() {
			attempt := page.Items[0].Attempt
			d := attempt.Decision()
			if attempt.Outcome() != egressaudit.OutcomeFailed || d.ProxyID != profile.ID.String() || d.DefaultRevision != 2 || d.SelectionSource != egressprofile.SelectionGlobal {
				t.Fatalf("failed attempt lost frozen decision %+v", d)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("proxy failure missing terminal")
		}
		time.Sleep(time.Millisecond)
	}
}

func newContainedBlindProxyFixture(t *testing.T, p *blindPrivateProxy, configure ...func(*loopbackproxy.Options)) (*proxyFixture, *blindContainedNetwork) {
	return newContainedBlindProxyFixtureWithPolicy(t, p, allowEverythingTestPolicy(t), nil, configure...)
}

func newContainedBlindProxyFixtureWithPolicy(t *testing.T, p *blindPrivateProxy, policy connectionpolicy.Snapshot, ingress context.Context, configure ...func(*loopbackproxy.Options)) (*proxyFixture, *blindContainedNetwork) {
	t.Helper()
	n := &blindContainedNetwork{allowed: map[string]bool{p.listener.Addr().String(): true}}
	builder, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{BaseDialer: n, SystemResolver: n})
	if err != nil {
		t.Fatal(err)
	}
	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err = gate.Start(context.Background(), offlinehold.RuntimeBinding{InstanceID: "blind-default-fixture"}); err != nil {
		t.Fatal(err)
	}
	dialer, err := blindtunnel.NewDialerWithBuilder(gate, builder)
	if err != nil {
		t.Fatal(err)
	}
	fixture := newProxyFixtureConfigured(t, protocolspec.DialectAnthropicMessages, nil, policy, nil, "", &proxyFixtureComposition{compiler: testEnvironmentCompiler(t, protocolspec.DialectAnthropicMessages), ingressContext: ingress, configureProxy: func(options *loopbackproxy.Options) {
		options.BlindTunnels = &blindTunnelFixture{gate: gate, dialer: dialer}
		for _, apply := range configure {
			apply(options)
		}
	}})
	t.Cleanup(func() {
		gate.BeginShutdown()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := gate.Drain(ctx); err != nil {
			t.Error(err)
		}
	})
	return fixture, n
}

func TestBlindConfiguredRequiresDefaultResolver(t *testing.T) {
	composition := &proxyFixtureComposition{compiler: testEnvironmentCompiler(t, protocolspec.DialectAnthropicMessages), configureProxy: func(options *loopbackproxy.Options) {
		missing := *options
		missing.EgressDefaults = nil
		handler, err := loopbackproxy.New(missing)
		if handler != nil {
			handler.Shutdown(context.Background())
		}
		if err == nil {
			t.Error("configured blind transport accepted missing default resolver")
		}
	}}
	f := newProxyFixtureConfigured(t, protocolspec.DialectAnthropicMessages, nil, allowEverythingTestPolicy(t), nil, "", composition)
	defer f.Close(t)
}

type blindResolverObservation struct {
	delegate     egressprofile.Resolver
	fault        string
	calls        atomic.Int64
	wrongChoices atomic.Int64
}

func (r *blindResolverObservation) Resolve(ctx context.Context, account, flow egressprofile.Choice) (egressprofile.Resolved, error) {
	r.calls.Add(1)
	if account != egressprofile.Inherit() || flow != egressprofile.Inherit() {
		r.wrongChoices.Add(1)
		return egressprofile.Resolved{}, errors.New("unexpected authored selection")
	}
	switch r.fault {
	case "unavailable":
		return egressprofile.Resolved{}, egressprofile.ErrDefaultUnavailable
	case "invalid":
		return egressprofile.Resolved{}, nil
	case "account":
		return egressprofile.RestoreResolved(egressprofile.Direct(), egressprofile.SelectionAccount, 0)
	case "flow":
		return egressprofile.RestoreResolved(egressprofile.Direct(), egressprofile.SelectionFlow, 0)
	case "transport":
		return egressprofile.RestoreResolved(egressprofile.Direct(), egressprofile.SelectionTransport, 0)
	}
	return r.delegate.Resolve(ctx, account, flow)
}

func TestBlindGlobalDefaultNeverGrantsTargetOrCapturePermission(t *testing.T) {
	for _, tc := range []struct {
		name, authority, fault string
		explicit, badCapture   bool
		status                 int
		lookups, socks         int64
	}{
		{"public-only-private", "blind.fixture:443", "", false, false, 403, 1, 0},
		{"explicit-owner-private", "blind.fixture:443", "", true, false, 200, 1, 1},
		{"unknown-host", "other.fixture:443", "", true, false, 403, 0, 0},
		{"unknown-port", "blind.fixture:444", "", true, false, 403, 0, 0},
		{"bad-capture", "blind.fixture:443", "", true, true, 403, 0, 0},
		{"default-unavailable", "blind.fixture:443", "unavailable", true, false, 503, 1, 0},
		{"default-invalid", "blind.fixture:443", "invalid", true, false, 503, 1, 0},
		{"borrowed-account", "blind.fixture:443", "account", true, false, 503, 1, 0},
		{"borrowed-flow", "blind.fixture:443", "flow", true, false, 503, 1, 0},
		{"borrowed-transport", "blind.fixture:443", "transport", true, false, 503, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("CONNECT-only test emitted application request") }))
			defer upstream.Close()
			p := newBlindPrivateProxy(t, upstream.Listener.Addr().String(), 443)
			policy := allowEverythingTestPolicy(t)
			if tc.explicit {
				policy = connectionpolicy.Snapshot{Revision: 9, Mode: connectionpolicy.ModeDenyUnknown, Rules: []connectionpolicy.Rule{{ID: "allow.private", Priority: 1, Decision: connectionpolicy.DecisionAllow, Match: connectionpolicy.MatchExactHostPort("blind.fixture", 443)}}}
			}
			observed := &blindResolverObservation{fault: tc.fault}
			f, n := newContainedBlindProxyFixtureWithPolicy(t, p, policy, egressnetwork.WithTargetAccess(context.Background(), egressnetwork.TargetAccessPublicOnly), func(options *loopbackproxy.Options) {
				observed.delegate = options.EgressDefaults
				options.EgressDefaults = observed
			})
			defer f.Close(t)
			publishBlindDefault(t, f, p)
			capability := f.grant.ProxyCapability.Value()
			if tc.badCapture {
				capability = "invalid-fixture-capability"
			}
			c, response := f.Connect(t, capability, tc.authority)
			response.Body.Close()
			c.Close()
			if response.StatusCode != tc.status || observed.calls.Load() != tc.lookups || observed.wrongChoices.Load() != 0 || p.requests.Load() != tc.socks || n.denied.Load() != 0 {
				t.Fatalf("status%d want%d resolve%d want%d proxy%d want%d direct%d", response.StatusCode, tc.status, observed.calls.Load(), tc.lookups, p.requests.Load(), tc.socks, n.denied.Load())
			}
		})
	}
}

func publishBlindDefault(t *testing.T, f *proxyFixture, p *blindPrivateProxy) egressprofile.ProfileRevision {
	t.Helper()
	profile, err := f.profiles.Publish(context.Background(), egressprofile.PublishCommand{ID: "profile.blind-global", DisplayName: "Blind global", Policy: egressnetwork.Policy{Proxy: egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: p.listener.Addr().String()}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.defaults.SetDefault(context.Background(), egressprofile.SetDefaultCommand{ExpectedRevision: 1, Profile: &egressprofile.ProfileRef{ID: profile.ID, Revision: profile.Revision}})
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func TestAuthorizedBlindForwardUsesRealGlobalSOCKSAndAudit(t *testing.T) {
	for _, connect := range []bool{true, false} {
		t.Run(fmt.Sprint(connect), func(t *testing.T) { runAuthorizedBlindForward(t, connect, false) })
	}
}

func TestCleartextDefaultPortKeepsRealConnectionAudit(t *testing.T) {
	runAuthorizedBlindForward(t, false, true)
}

func runAuthorizedBlindForward(t *testing.T, connect, omitPort bool) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Proxy-Authorization") != "" || r.Header.Get("Proxy-Connection") != "" || r.URL.IsAbs() {
			t.Error("proxy headers/absolute URI leaked")
		}
		io.WriteString(w, "private-blind-body")
	}))
	defer upstream.Close()
	port := uint16(80)
	if connect {
		port = 443
	}
	p := newBlindPrivateProxy(t, upstream.Listener.Addr().String(), port)
	f, n := newContainedBlindProxyFixture(t, p)
	defer f.Close(t)
	profile := publishBlindDefault(t, f, p)
	var c net.Conn
	var response *http.Response
	var err error
	if connect {
		c, response = f.Connect(t, f.grant.ProxyCapability.Value(), "blind.fixture:443")
		defer c.Close()
		if response.StatusCode != 200 {
			t.Fatalf("CONNECT status=%d proxy=%d direct=%d", response.StatusCode, p.requests.Load(), n.denied.Load())
		}
		response.Body.Close()
		_, err = io.WriteString(c, "GET /probe HTTP/1.1\r\nHost: blind.fixture\r\nConnection: close\r\n\r\n")
	} else {
		c, err = net.DialTimeout("tcp4", f.listener.Addr().String(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		credentials := base64.StdEncoding.EncodeToString([]byte("capture:" + f.grant.ProxyCapability.Value()))
		authority := "blind.fixture:80"
		if omitPort {
			authority = "blind.fixture"
		}
		_, err = io.WriteString(c, "GET http://"+authority+"/probe HTTP/1.1\r\nHost: "+authority+"\r\nProxy-Authorization: Basic "+credentials+"\r\nProxy-Connection: close\r\nConnection: close\r\n\r\n")
	}
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	response, err = http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	c.Close()
	if err != nil || response.StatusCode != 200 || string(body) != "private-blind-body" || p.requests.Load() != 1 || n.denied.Load() != 0 || calls.Load() != 1 {
		var reason struct {
			ReasonCode string `json:"reasonCode"`
		}
		_ = json.Unmarshal(body, &reason)
		t.Fatalf("response=%d reason=%s err=%v proxy=%d direct=%d upstream=%d", response.StatusCode, reason.ReasonCode, err, p.requests.Load(), n.denied.Load(), calls.Load())
	}
	deadline := time.Now().Add(time.Second)
	for {
		records, err := f.egress.List(context.Background(), egressaudit.PageRequest{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(records.Items) == 1 && records.Items[0].Attempt.Terminal() {
			d := records.Items[0].Attempt.Decision()
			if d.SelectionSource != egressprofile.SelectionGlobal || d.DefaultRevision != 2 || d.ProxyID != profile.ID.String() || d.ProxyRevision != 1 || d.PolicyID != "runtime.egress-default" || d.PolicyRevision != 2 || d.RuleID != "global.default" || d.AccountID != "" {
				t.Fatalf("wrong actual audit: %+v", d)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("missing terminal blind audit")
		}
		time.Sleep(time.Millisecond)
	}
	if len(f.exchanges.Requests()) != 0 || f.original.Count() != 0 {
		t.Fatal("blind payload reached semantic/original pipeline")
	}
	connections, err := f.connections.List(context.Background(), connectionevent.PageRequest{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, record := range connections.Items {
		if record.Phase != connectionevent.PhaseAttempted {
			if record.RequestedHost != "blind.fixture" || record.Port != port || record.RouteHost != "blind.fixture" {
				t.Fatalf("actual connection target lost: %+v", record)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("no allowed connection evidence")
	}
}

func TestMalformedCleartextKeepsRefusalVisibleWithoutEgress(t *testing.T) {
	for _, target := range []string{"http://blind.fixture:0/probe", "https://blind.fixture/probe"} {
		t.Run(target, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("malformed request reached upstream") }))
			defer upstream.Close()
			p := newBlindPrivateProxy(t, upstream.Listener.Addr().String(), 80)
			observed := &blindResolverObservation{}
			f, n := newContainedBlindProxyFixture(t, p, func(options *loopbackproxy.Options) {
				observed.delegate = options.EgressDefaults
				options.EgressDefaults = observed
			})
			defer f.Close(t)
			publishBlindDefault(t, f, p)
			c, err := net.DialTimeout("tcp4", f.listener.Addr().String(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			c.SetDeadline(time.Now().Add(2 * time.Second))
			credentials := base64.StdEncoding.EncodeToString([]byte("capture:" + f.grant.ProxyCapability.Value()))
			if _, err = io.WriteString(c, "GET "+target+" HTTP/1.1\r\nHost: blind.fixture\r\nProxy-Authorization: Basic "+credentials+"\r\nConnection: close\r\n\r\n"); err != nil {
				t.Fatal(err)
			}
			response, err := http.ReadResponse(bufio.NewReader(c), nil)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			c.Close()
			if response.StatusCode != 400 || observed.calls.Load() != 0 || p.requests.Load() != 0 || n.denied.Load() != 0 {
				t.Fatalf("malformed status%d resolve%d proxy%d direct%d", response.StatusCode, observed.calls.Load(), p.requests.Load(), n.denied.Load())
			}
			page, err := f.connections.List(context.Background(), connectionevent.PageRequest{Limit: 10})
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Items) == 0 {
				t.Fatal("refusal had no attempted journal")
			}
			for _, record := range page.Items {
				if record.Decision == connectionevent.DecisionAllow {
					t.Fatal("malformed target was allowed")
				}
			}
		})
	}
}
