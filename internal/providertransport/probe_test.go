package providertransport

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
)

func TestProbeEndpointAuthorityAddsDefaultTLSPort(t *testing.T) {
	t.Parallel()

	target := testTarget("example.com", 443)
	authority, err := target.endpointAuthority()
	if err != nil {
		t.Fatal(err)
	}
	if authority != "example.com:443" {
		t.Fatalf("endpoint authority = %q", authority)
	}
}

func TestTLSProbeUsesFrozenTargetWithoutHTTPOrCredential(t *testing.T) {
	t.Parallel()

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(
		http.ResponseWriter,
		*http.Request,
	) {
		t.Error("TLS-only probe sent an HTTP request")
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()

	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	port := listenerPort(t, server.Listener.Addr())
	target := testTarget("example.com", port)
	dialer := &recordingMappingDialer{
		address: server.Listener.Addr().String(),
	}
	prober, err := newTLSProber(
		dialer,
		roots,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := prober.Probe(context.Background(), offlinehold.ProbeRequest{
		Targets: []offlinehold.ProbeTarget{
			frozenProviderProbeTarget(t, "target-test", target),
		},
	}); err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
	if dialer.requested != targetAuthority("example.com", port) {
		t.Fatalf(
			"probe dial authority = %q, want %q",
			dialer.requested,
			targetAuthority("example.com", port),
		)
	}
}

func TestTLSProbeClassifiesHostnameRejection(t *testing.T) {
	t.Parallel()

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(
		http.ResponseWriter,
		*http.Request,
	) {
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	port := listenerPort(t, server.Listener.Addr())
	target := testTarget("untrusted.invalid", port)
	prober, err := newTLSProber(
		&mappingDialer{address: server.Listener.Addr().String()},
		roots,
	)
	if err != nil {
		t.Fatal(err)
	}
	err = prober.Probe(context.Background(), offlinehold.ProbeRequest{
		Targets: []offlinehold.ProbeTarget{
			frozenProviderProbeTarget(t, "target-test", target),
		},
	})
	var failure *offlinehold.ProbeFailure
	if !errors.As(err, &failure) ||
		failure.Reason != offlinehold.ProbeReasonTLSRejected {
		t.Fatalf("Probe() error = %v, want TLS-rejected ProbeFailure", err)
	}
}

func TestTLSProbeOwnsTargetDeadline(t *testing.T) {
	t.Parallel()

	dialer := &silentProbeDialer{}
	defer dialer.Close()
	target := testTarget("example.com", 443)
	prober, err := newTLSProberWithTimeout(
		dialer,
		nil,
		20*time.Millisecond,
	)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	err = prober.Probe(context.Background(), offlinehold.ProbeRequest{
		Targets: []offlinehold.ProbeTarget{
			frozenProviderProbeTarget(t, "target-test", target),
		},
	})
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("bounded probe elapsed = %s", elapsed)
	}
	var failure *offlinehold.ProbeFailure
	if !errors.As(err, &failure) ||
		failure.Reason != offlinehold.ProbeReasonTransportUnavailable {
		t.Fatalf("bounded probe error = %v", err)
	}
}

type recordingMappingDialer struct {
	address   string
	requested string
}

func (dialer *recordingMappingDialer) DialContext(
	ctx context.Context,
	network string,
	address string,
) (net.Conn, error) {
	dialer.requested = address
	var system net.Dialer
	return system.DialContext(ctx, network, dialer.address)
}

type silentProbeDialer struct {
	mu    sync.Mutex
	peers []net.Conn
}

func (dialer *silentProbeDialer) DialContext(
	context.Context,
	string,
	string,
) (net.Conn, error) {
	client, peer := net.Pipe()
	dialer.mu.Lock()
	dialer.peers = append(dialer.peers, peer)
	dialer.mu.Unlock()
	return client, nil
}

func (dialer *silentProbeDialer) Close() {
	dialer.mu.Lock()
	defer dialer.mu.Unlock()
	for _, peer := range dialer.peers {
		_ = peer.Close()
	}
	dialer.peers = nil
}

func frozenProviderProbeTarget(
	t *testing.T,
	reference string,
	target Target,
) offlinehold.ProbeTarget {
	t.Helper()
	frozen, err := NewProbeTarget(
		reference,
		1,
		access.PlanHash{1},
		target,
	)
	if err != nil {
		t.Fatalf("NewProbeTarget() error = %v", err)
	}
	return frozen
}

func targetAuthority(host string, port int) string {
	return host + ":" + strconv.Itoa(port)
}
