package originaltransport

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/providertransport"
)

type closedProbeNetwork struct {
	socket   string
	attempts *atomic.Int64
}

func (d closedProbeNetwork) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if d.attempts != nil {
		d.attempts.Add(1)
	}
	if address != "example.com:443" && address != "wrong.invalid:443" {
		return nil, errors.New("private probe fixture denied destination")
	}
	return (&net.Dialer{}).DialContext(ctx, network, d.socket)
}

func TestUpdateTLSProbeRejectsAlteredClosedIdentityBeforeDial(t *testing.T) {
	var attempts atomic.Int64
	closed := closedProbeNetwork{attempts: &attempts}
	builder, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{BaseDialer: closed, SystemResolver: closed})
	if err != nil {
		t.Fatal(err)
	}
	prober, err := NewTLSProberWithBuilder(builder, x509.NewCertPool())
	if err != nil {
		t.Fatal(err)
	}
	base := offlinehold.ProbeTarget{Kind: offlinehold.EgressUpdate, Transport: offlinehold.ProbeTransportStrictTLS, TargetRef: "https://api.github.com", NetworkOrigin: "https://api.github.com", HTTPAuthority: "api.github.com", TLSServerName: "api.github.com", EgressPolicy: egressnetwork.DefaultPolicy()}
	for _, edge := range []string{"plugin", "origin", "authority", "sni", "target-ref", "plan-revision", "plan-digest", "access", "cleartext", "query", "port", "userinfo"} {
		t.Run(edge, func(t *testing.T) {
			target := base
			switch edge {
			case "plugin":
				target.Kind = offlinehold.EgressPlugin
			case "origin":
				target.NetworkOrigin = "https://unapproved.invalid"
			case "authority":
				target.HTTPAuthority = "unapproved.invalid"
			case "sni":
				target.TLSServerName = "unapproved.invalid"
			case "target-ref":
				target.TargetRef = "not-the-fixed-origin"
			case "plan-revision":
				target.PlanRevision = 1
			case "plan-digest":
				target.PlanDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			case "access":
				target.TargetAccess = egressnetwork.TargetAccessOwnerConfigured
			case "cleartext":
				target.Transport = offlinehold.ProbeTransportLoopbackCleartext
			case "query":
				target.NetworkOrigin += "?x=1"
			case "port":
				target.NetworkOrigin = "https://api.github.com:444"
			case "userinfo":
				target.NetworkOrigin = "https://user@api.github.com"
			}
			if err := prober.Probe(t.Context(), offlinehold.ProbeRequest{Targets: []offlinehold.ProbeTarget{target}}); err == nil {
				t.Fatal("closed update probe accepted altered target")
			}
			if attempts.Load() != 0 {
				t.Fatal("altered closed target reached socket", attempts.Load())
			}
		})
	}
}
func (closedProbeNetwork) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return nil, errors.New("private probe fixture denied DNS")
}

func TestTrustedProbeBuildersKeepStrictTLSAndPrivateNetworkContainment(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("TLS probe sent HTTP payload") }))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	closed := closedProbeNetwork{socket: server.Listener.Addr().String()}
	builder, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{BaseDialer: closed, SystemResolver: closed})
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	for _, kind := range []string{"original", "provider"} {
		t.Run(kind, func(t *testing.T) {
			var prober offlinehold.Prober
			if kind == "original" {
				value, err := NewTLSProberWithBuilder(builder, roots)
				if err != nil {
					t.Fatal(err)
				}
				prober = value
			} else {
				value, err := providertransport.NewProviderProberWithBuilder(builder, roots)
				if err != nil {
					t.Fatal(err)
				}
				prober = value
			}
			target := frozenOriginalProbeTarget(t, offlinehold.EgressOpaque, "https://example.com")
			if kind == "provider" {
				target.Kind = offlinehold.EgressProvider
				target.TargetRef = "private-provider"
				target.PlanRevision = 1
				target.PlanDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			}
			if err := prober.Probe(t.Context(), offlinehold.ProbeRequest{Targets: []offlinehold.ProbeTarget{target}}); err != nil {
				t.Fatal("real contained TLS probe failed", err)
			}
			target.NetworkOrigin = "https://wrong.invalid"
			target.HTTPAuthority = "wrong.invalid"
			target.TLSServerName = "wrong.invalid"
			if kind == "original" {
				target.TargetRef = target.NetworkOrigin
			}
			if err := prober.Probe(t.Context(), offlinehold.ProbeRequest{Targets: []offlinehold.ProbeTarget{target}}); err == nil {
				t.Fatal("strict probe accepted wrong certificate name")
			}
		})
	}
}

func TestTrustedProbeBuildersRejectNilAndDoNotBorrowMutableRootPool(t *testing.T) {
	if p, err := NewTLSProberWithBuilder(nil, nil); err == nil || p != nil {
		t.Fatal("nil original builder accepted")
	}
	if p, err := providertransport.NewProviderProberWithBuilder(nil, nil); err == nil || p != nil {
		t.Fatal("nil provider builder accepted")
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("TLS probe sent HTTP payload") }))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	closed := closedProbeNetwork{socket: server.Listener.Addr().String()}
	builder, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{BaseDialer: closed, SystemResolver: closed})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"original", "provider"} {
		t.Run(kind, func(t *testing.T) {
			mutable := x509.NewCertPool()
			var prober offlinehold.Prober
			if kind == "original" {
				p, err := NewTLSProberWithBuilder(builder, mutable)
				if err != nil {
					t.Fatal(err)
				}
				prober = p
			} else {
				p, err := providertransport.NewProviderProberWithBuilder(builder, mutable)
				if err != nil {
					t.Fatal(err)
				}
				prober = p
			}
			mutable.AddCert(server.Certificate()) // This cannot grant the existing prober new trust.
			target := frozenOriginalProbeTarget(t, offlinehold.EgressOpaque, "https://example.com")
			if kind == "provider" {
				target.Kind = offlinehold.EgressProvider
				target.TargetRef = "private-provider"
				target.PlanRevision = 1
				target.PlanDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			}
			if err := prober.Probe(t.Context(), offlinehold.ProbeRequest{Targets: []offlinehold.ProbeTarget{target}}); err == nil {
				t.Fatal("mutated caller root pool expanded existing probe trust")
			}
		})
	}
}
