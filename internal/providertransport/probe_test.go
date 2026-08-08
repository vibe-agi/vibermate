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

	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originidentity"
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

func TestProviderProbeRejectsIncompletePlanIdentity(t *testing.T) {
	t.Parallel()

	missingDigest := frozenProviderProbeTarget(
		t,
		"target-test",
		testTarget("example.com", 443),
	)
	missingDigest.PlanDigest = ""
	if _, err := targetFromProbe(missingDigest); err == nil {
		t.Fatal("provider probe accepted a plan revision without its digest")
	}

	missingRevision := frozenProviderProbeTarget(
		t,
		"target-test",
		testTarget("example.com", 443),
	)
	missingRevision.PlanRevision = 0
	if _, err := targetFromProbe(missingRevision); err == nil {
		t.Fatal("provider probe accepted a plan digest without its revision")
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
	prober, err := newProviderProber(
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
	prober, err := newProviderProber(
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
	prober, err := newProviderProberWithTimeout(
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

func TestLoopbackProbeUsesExactFrozenPeerWithoutHTTP(t *testing.T) {
	t.Parallel()

	listener := newLoopbackProbeListener(t)
	origin, err := originidentity.ParseProviderOrigin(
		"http://" + listener.Addr().String() + "/v1",
	)
	if err != nil {
		t.Fatal(err)
	}
	target := targetFromProviderOrigin(origin)
	prober, err := newProviderProber(&net.Dialer{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := prober.Probe(
		context.Background(),
		offlinehold.ProbeRequest{
			Targets: []offlinehold.ProbeTarget{
				frozenProviderProbeTarget(t, "loopback-target", target),
			},
		},
	); err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
}

func TestLoopbackProbeRejectsChangedPeer(t *testing.T) {
	t.Parallel()

	expected := newLoopbackProbeListener(t)
	actual := newLoopbackProbeListener(t)
	origin, err := originidentity.ParseProviderOrigin(
		"http://" + expected.Addr().String() + "/v1",
	)
	if err != nil {
		t.Fatal(err)
	}
	target := targetFromProviderOrigin(origin)
	prober, err := newProviderProber(
		&mappingDialer{address: actual.Addr().String()},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	err = prober.Probe(
		context.Background(),
		offlinehold.ProbeRequest{
			Targets: []offlinehold.ProbeTarget{
				frozenProviderProbeTarget(t, "loopback-target", target),
			},
		},
	)
	var failure *offlinehold.ProbeFailure
	if !errors.As(err, &failure) ||
		failure.Reason != offlinehold.ProbeReasonTransportUnavailable {
		t.Fatalf("Probe() error = %v", err)
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
		testRequestProvenance(t),
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

func newLoopbackProbeListener(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
	})
	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			_ = connection.Close()
		}
	}()
	return listener
}
