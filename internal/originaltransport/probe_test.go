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
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originidentity"
)

func TestOriginalTLSProbeUsesFrozenOriginWithoutHTTPOrCredential(t *testing.T) {
	t.Parallel()

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(
		http.ResponseWriter,
		*http.Request,
	) {
		t.Error("TLS-only original-origin probe sent an HTTP request")
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()

	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	host := certificateHost(t, server.Certificate())
	prober, err := newTLSProber(
		&probeMappingDialer{address: server.Listener.Addr().String()},
		roots,
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []offlinehold.EgressKind{
		offlinehold.EgressOpaque,
		offlinehold.EgressAuxiliary,
	} {
		origin := "https://" + net.JoinHostPort(
			host,
			strconv.Itoa(listenerPort(t, server.Listener.Addr())),
		)
		err := prober.Probe(
			context.Background(),
			offlinehold.ProbeRequest{Targets: []offlinehold.ProbeTarget{
				frozenOriginalProbeTarget(t, kind, origin),
			}},
		)
		if err != nil {
			t.Fatalf("Probe(%s) error = %v", kind, err)
		}
	}
}

func TestOriginalTLSProbeRejectsWrongKindAndHostname(t *testing.T) {
	t.Parallel()

	prober, err := newTLSProber(&probeMappingDialer{
		err: context.DeadlineExceeded,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = prober.Probe(
		context.Background(),
		offlinehold.ProbeRequest{Targets: []offlinehold.ProbeTarget{
			frozenOriginalProbeTarget(
				t,
				offlinehold.EgressProvider,
				"https://example.com:443",
			),
		}},
	)
	requireProbeReason(t, err, offlinehold.ProbeReasonFailed)

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
	prober, err = newTLSProber(
		&probeMappingDialer{address: server.Listener.Addr().String()},
		roots,
	)
	if err != nil {
		t.Fatal(err)
	}
	invalidOrigin := "https://" + net.JoinHostPort(
		"untrusted.invalid",
		strconv.Itoa(listenerPort(t, server.Listener.Addr())),
	)
	err = prober.Probe(
		context.Background(),
		offlinehold.ProbeRequest{Targets: []offlinehold.ProbeTarget{
			frozenOriginalProbeTarget(
				t,
				offlinehold.EgressOpaque,
				invalidOrigin,
			),
		}},
	)
	requireProbeReason(t, err, offlinehold.ProbeReasonTLSRejected)
}

func TestOriginalTLSProbeClassifiesCanceledDial(t *testing.T) {
	t.Parallel()

	prober, err := newTLSProber(
		&probeMappingDialer{err: context.Canceled},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	err = prober.Probe(
		context.Background(),
		offlinehold.ProbeRequest{Targets: []offlinehold.ProbeTarget{
			frozenOriginalProbeTarget(
				t,
				offlinehold.EgressOpaque,
				"https://example.com:443",
			),
		}},
	)
	requireProbeReason(t, err, offlinehold.ProbeReasonCanceled)
}

func TestOriginalTLSProbeOwnsTargetDeadline(t *testing.T) {
	t.Parallel()

	dialer := &silentOriginalProbeDialer{}
	defer dialer.Close()
	prober, err := newTLSProberWithTimeout(
		dialer,
		nil,
		20*time.Millisecond,
	)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	err = prober.Probe(
		context.Background(),
		offlinehold.ProbeRequest{Targets: []offlinehold.ProbeTarget{
			frozenOriginalProbeTarget(
				t,
				offlinehold.EgressOpaque,
				"https://example.com:443",
			),
		}},
	)
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("bounded probe elapsed = %s", elapsed)
	}
	requireProbeReason(
		t,
		err,
		offlinehold.ProbeReasonTransportUnavailable,
	)
}

type probeMappingDialer struct {
	address string
	err     error
}

func (dialer *probeMappingDialer) DialContext(
	ctx context.Context,
	network string,
	_ string,
) (net.Conn, error) {
	if dialer.err != nil {
		return nil, dialer.err
	}
	var native net.Dialer
	return native.DialContext(ctx, network, dialer.address)
}

type silentOriginalProbeDialer struct {
	mu    sync.Mutex
	peers []net.Conn
}

func (dialer *silentOriginalProbeDialer) DialContext(
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

func (dialer *silentOriginalProbeDialer) Close() {
	dialer.mu.Lock()
	defer dialer.mu.Unlock()
	for _, peer := range dialer.peers {
		_ = peer.Close()
	}
	dialer.peers = nil
}

func frozenOriginalProbeTarget(
	t *testing.T,
	kind offlinehold.EgressKind,
	rawOrigin string,
) offlinehold.ProbeTarget {
	t.Helper()
	origin, err := originidentity.ParseClientOrigin(rawOrigin)
	if err != nil {
		t.Fatalf("ParseClientOrigin() error = %v", err)
	}
	return offlinehold.ProbeTarget{
		Kind:          kind,
		Transport:     offlinehold.ProbeTransportStrictTLS,
		TargetRef:     origin.String(),
		NetworkOrigin: origin.String(),
		HTTPAuthority: origin.HTTPAuthority(),
		TLSServerName: origin.Host(),
	}
}

func certificateHost(t *testing.T, certificate *x509.Certificate) string {
	t.Helper()
	if len(certificate.DNSNames) != 0 {
		return certificate.DNSNames[0]
	}
	if len(certificate.IPAddresses) != 0 {
		return certificate.IPAddresses[0].String()
	}
	t.Fatal("fixture certificate has no host identity")
	return ""
}

func listenerPort(t *testing.T, address net.Addr) int {
	t.Helper()
	_, rawPort, err := net.SplitHostPort(address.String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func requireProbeReason(
	t *testing.T,
	err error,
	expected offlinehold.ProbeReason,
) {
	t.Helper()
	var failure *offlinehold.ProbeFailure
	if !errors.As(err, &failure) || failure.Reason != expected {
		t.Fatalf("probe error = %v, want reason %s", err, expected)
	}
}
