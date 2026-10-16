package originaltransport

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

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
)

type aliasNetwork struct {
	socket string
	peer   net.Addr
	writes atomic.Int64
	dials  atomic.Int64
}

func (d *aliasNetwork) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d.dials.Add(1)
	if address != "alias.test:8080" {
		return nil, errors.New("closed fixture rejects dial")
	}
	c, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, d.socket)
	if err != nil {
		return nil, err
	}
	return &aliasConnection{Conn: c, network: d}, nil
}
func (*aliasNetwork) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}

type aliasConnection struct {
	net.Conn
	network *aliasNetwork
}

func (c *aliasConnection) RemoteAddr() net.Addr { return c.network.peer }
func (c *aliasConnection) Write(b []byte) (int, error) {
	c.network.writes.Add(int64(len(b)))
	return c.Conn.Write(b)
}

func TestOriginalCleartextRejectsPublicPeerBeforeCredentialWrite(t *testing.T) {
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(202) }))
	defer peer.Close()
	for _, test := range []struct {
		name, ip string
		port     int
		access   egressnetwork.TargetAccess
		proxy    bool
		wantOK   bool
	}{
		{name: "private", ip: "127.0.0.1", port: 8080, access: egressnetwork.TargetAccessOwnerConfigured, wantOK: true},
		{name: "public", ip: "8.8.8.8", port: 8080, access: egressnetwork.TargetAccessOwnerConfigured},
		{name: "wrong_port", ip: "127.0.0.1", port: 8081, access: egressnetwork.TargetAccessOwnerConfigured},
		{name: "public_only", ip: "127.0.0.1", port: 8080, access: egressnetwork.TargetAccessPublicOnly},
		{name: "secondary_proxy", ip: "127.0.0.1", port: 8080, access: egressnetwork.TargetAccessOwnerConfigured, proxy: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			network := &aliasNetwork{socket: peer.Listener.Addr().String(), peer: &net.TCPAddr{IP: net.ParseIP(test.ip), Port: test.port}}
			builder, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{BaseDialer: network, SystemResolver: network})
			if err != nil {
				t.Fatal(err)
			}
			transport, err := NewProductionTransportWithBuilder(builder)
			if err != nil {
				t.Fatal(err)
			}
			request, _ := http.NewRequestWithContext(egressnetwork.WithTargetAccess(t.Context(), test.access), "GET", "http://alias.test:8080/base/models?q=exact%2Bvalue", nil)
			request.Header.Set("Authorization", "Bearer synthetic-alias-secret")
			policy := egressnetwork.DefaultPolicy()
			if test.proxy {
				policy.Proxy = egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: "proxy.invalid:1080"}
			}
			response, err := transport.RoundTrip(request, policy)
			if response != nil {
				io.Copy(io.Discard, response.Body)
				response.Body.Close()
			}
			if test.wantOK {
				if err != nil || response.StatusCode != 202 {
					t.Fatalf("private peer failed: %v", err)
				}
			} else if err == nil || network.writes.Load() != 0 {
				t.Fatalf("unsafe peer err=%v bytes=%d", err, network.writes.Load())
			}
			if (test.proxy || test.access == egressnetwork.TargetAccessPublicOnly) && network.dials.Load() != 0 {
				t.Fatalf("forbidden target dialed %d times", network.dials.Load())
			}

		})
	}
}

func TestOriginalCleartextProbeUsesPrivatePeerWithoutHTTP(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan int, 1)
	go func() {
		c, e := listener.Accept()
		if e != nil {
			done <- -1
			return
		}
		defer c.Close()
		c.SetReadDeadline(time.Now().Add(time.Second))
		b, _ := io.ReadAll(c)
		done <- len(b)
	}()
	network := &aliasNetwork{socket: listener.Addr().String(), peer: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8080}}
	builder, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{BaseDialer: network, SystemResolver: network})
	if err != nil {
		t.Fatal(err)
	}
	prober, err := NewTLSProberWithBuilder(builder, nil)
	if err != nil {
		t.Fatal(err)
	}
	target := offlinehold.ProbeTarget{Kind: offlinehold.EgressOpaque, TargetAccess: egressnetwork.TargetAccessOwnerConfigured, Transport: offlinehold.ProbeTransportPrivateCleartext, TargetRef: "http://alias.test:8080", NetworkOrigin: "http://alias.test:8080", HTTPAuthority: "alias.test:8080", EgressPolicy: egressnetwork.DefaultPolicy()}
	if err := prober.Probe(t.Context(), offlinehold.ProbeRequest{Targets: []offlinehold.ProbeTarget{target}}); err != nil {
		t.Fatal(err)
	}
	if n := <-done; n != 0 || network.writes.Load() != 0 {
		t.Fatalf("probe sent application bytes=%d", n)
	}
}

func TestOriginalCleartextProbeRejectsAlteredIdentityBeforeDial(t *testing.T) {
	network := &aliasNetwork{}
	builder, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{BaseDialer: network, SystemResolver: network})
	if err != nil {
		t.Fatal(err)
	}
	prober, err := NewTLSProberWithBuilder(builder, nil)
	if err != nil {
		t.Fatal(err)
	}
	base := offlinehold.ProbeTarget{Kind: offlinehold.EgressOpaque, TargetAccess: egressnetwork.TargetAccessOwnerConfigured, Transport: offlinehold.ProbeTransportPrivateCleartext, TargetRef: "http://alias.test:8080", NetworkOrigin: "http://alias.test:8080", HTTPAuthority: "alias.test:8080", EgressPolicy: egressnetwork.DefaultPolicy()}
	for _, mutate := range []func(*offlinehold.ProbeTarget){
		func(t *offlinehold.ProbeTarget) { t.TargetAccess = 0 },
		func(t *offlinehold.ProbeTarget) { t.TargetAccess = 99 },
		func(t *offlinehold.ProbeTarget) {
			t.NetworkOrigin = "https://alias.test:8080"
			t.TargetRef = t.NetworkOrigin
			t.Transport = offlinehold.ProbeTransportStrictTLS
			t.TLSServerName = "alias.test"
			t.TargetAccess = 0
		},
		func(t *offlinehold.ProbeTarget) {
			t.Transport = offlinehold.ProbeTransportStrictTLS
			t.TLSServerName = "alias.test"
		},
		func(t *offlinehold.ProbeTarget) { t.Transport = offlinehold.ProbeTransportLoopbackCleartext },
		func(t *offlinehold.ProbeTarget) { t.NetworkOrigin += "/base"; t.TargetRef = t.NetworkOrigin },
		func(t *offlinehold.ProbeTarget) { t.HTTPAuthority = "other.test:8080" },
		func(t *offlinehold.ProbeTarget) {
			t.EgressPolicy.Proxy = egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: "proxy.invalid:1080"}
		},
		func(t *offlinehold.ProbeTarget) { t.Kind = offlinehold.EgressUpdate },
	} {
		target := base
		mutate(&target)
		if err := prober.Probe(t.Context(), offlinehold.ProbeRequest{Targets: []offlinehold.ProbeTarget{target}}); err == nil || network.dials.Load() != 0 {
			t.Fatalf("altered probe escaped: err=%v dials=%d", err, network.dials.Load())
		}
	}
}
