package transportprofile

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"slices"
	"testing"
	"time"

	utls "github.com/refraction-networking/utls"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

func TestPrepareObservedSpecALPNCompatibility(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		alpn     []string
		protocol wireprofile.ApplicationProtocol
		wantALPN []string
		wantFail bool
	}{
		{name: "implicit_http1", protocol: wireprofile.ApplicationProtocolHTTP1},
		{name: "explicit_http1", alpn: []string{"http/1.1"}, protocol: wireprofile.ApplicationProtocolHTTP1, wantALPN: []string{"http/1.1"}},
		{name: "both_to_http1", alpn: []string{"h2", "http/1.1"}, protocol: wireprofile.ApplicationProtocolHTTP1, wantALPN: []string{"http/1.1"}},
		{name: "both_to_http2", alpn: []string{"h2", "http/1.1"}, protocol: wireprofile.ApplicationProtocolHTTP2, wantALPN: []string{"h2"}},
		{name: "missing_alpn_cannot_satisfy_http2", protocol: wireprofile.ApplicationProtocolHTTP2, wantFail: true},
		{name: "http2_only_cannot_satisfy_http1", alpn: []string{"h2"}, protocol: wireprofile.ApplicationProtocolHTTP1, wantFail: true},
		{name: "http1_only_cannot_satisfy_http2", alpn: []string{"http/1.1"}, protocol: wireprofile.ApplicationProtocolHTTP2, wantFail: true},
		{name: "unsupported_is_not_implicit_http1", alpn: []string{"unsupported"}, protocol: wireprofile.ApplicationProtocolHTTP1, wantFail: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			observation := captureGoClientHello(t, "agent.example", test.alpn)
			plan := testTransportPlanForProtocol(t, wireprofile.UpstreamWireProfileFollowClientValue, test.protocol)
			spec, offered, reason, err := prepareObservedSpec(observation, plan.Requested(), "example.com")
			if test.wantFail {
				if err == nil || spec != nil || reason != FallbackApplicationProtocolMissing {
					t.Fatalf("prepareObservedSpec() reason=%q error=%v", reason, err)
				}
				return
			}
			if err != nil || spec == nil || reason != FallbackNone || !slices.Equal(offered, test.wantALPN) {
				t.Fatalf("prepareObservedSpec() offered=%v reason=%q error=%v", offered, reason, err)
			}
			var extensionALPN []string
			alpnCount := 0
			for _, extension := range spec.Extensions {
				if alpn, ok := extension.(*utls.ALPNExtension); ok {
					alpnCount++
					extensionALPN = alpn.AlpnProtocols
				}
			}
			wantCount := 0
			if len(test.wantALPN) != 0 {
				wantCount = 1
			}
			if alpnCount != wantCount || !slices.Equal(extensionALPN, test.wantALPN) {
				t.Fatalf("ALPN extensions=%d protocols=%v, want count=%d protocols=%v", alpnCount, extensionALPN, wantCount, test.wantALPN)
			}
			if !slices.Equal(observation.OfferedALPN(), test.alpn) {
				t.Fatal("preparing the upstream mutated the downstream observation")
			}
		})
	}
}

func TestConnectorRetainsPreflightFailureEvidence(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		observation Observation
		wantReason  FallbackReason
	}{
		{name: "missing_observation", wantReason: FallbackObservationUnavailable},
		{name: "incompatible_alpn", observation: captureGoClientHello(t, "agent.example", []string{"h2"}), wantReason: FallbackApplicationProtocolMissing},
		{name: "missing_sni_without_alpn", observation: captureGoClientHello(t, "127.0.0.1", nil), wantReason: FallbackClientHelloUnsupported},
	} {
		t.Run(test.name, func(t *testing.T) {
			dialer := &countingErrorDialer{err: errors.New("unexpected test dial")}
			connector, err := NewConnector(ConnectorOptions{Dialer: dialer})
			if err != nil {
				t.Fatal(err)
			}
			connection, evidence, err := connector.Connect(context.Background(), ConnectRequest{
				Network: "tcp", Address: "example.com:443", TLSServerName: "example.com",
				Plan: testTransportPlan(t), Observation: test.observation,
			})
			if !errors.Is(err, ErrNoTransportProfile) || connection != nil || dialer.calls != 0 {
				t.Fatalf("preflight connection=%v dials=%d error=%v", connection, dialer.calls, err)
			}
			if evidence.FallbackReason() != test.wantReason || evidence.UsedFallback() || evidence.Effective().Ref != "" {
				t.Fatalf("failure evidence=%+v, want reason=%q with no fallback", evidence, test.wantReason)
			}
		})
	}
}

func TestConnectorImplicitHTTP1StillVerifiesCertificates(t *testing.T) {
	t.Parallel()
	observation := captureGoClientHello(t, "agent.example", nil)
	roots, serverConfig := testTLSAuthority(t)
	for _, test := range []struct {
		name       string
		serverName string
		roots      *x509.CertPool
	}{
		{name: "untrusted_ca", serverName: "example.com", roots: x509.NewCertPool()},
		{name: "wrong_hostname", serverName: "wrong.invalid", roots: roots},
	} {
		t.Run(test.name, func(t *testing.T) {
			dialer := newPipeTLSDialer(serverConfig)
			connector, err := NewConnector(ConnectorOptions{Dialer: dialer, RootCAs: test.roots, HandshakeTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			connection, evidence, err := connector.Connect(context.Background(), ConnectRequest{
				Network: "tcp", Address: "example.com:443", TLSServerName: test.serverName,
				Plan: testTransportPlan(t), Observation: observation,
			})
			if connection != nil || !strictVerificationFailure(err) || dialer.dialCount() != 1 {
				t.Fatalf("certificate failure connection=%v dials=%d error=%v", connection, dialer.dialCount(), err)
			}
			if evidence.FallbackReason() != FallbackObservedTLSHandshakeRejected || evidence.UsedFallback() {
				t.Fatalf("certificate failure evidence=%+v", evidence)
			}
		})
	}
}

func TestConnectorExplicitALPNStillRequiresNegotiation(t *testing.T) {
	t.Parallel()
	observation := captureGoClientHello(t, "agent.example", []string{"http/1.1"})
	roots, serverConfig := testTLSAuthority(t)
	serverConfig.NextProtos = nil
	dialer := newPipeTLSDialer(serverConfig)
	connector, err := NewConnector(ConnectorOptions{Dialer: dialer, RootCAs: roots, HandshakeTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	connection, evidence, err := connector.Connect(context.Background(), ConnectRequest{
		Network: "tcp", Address: "example.com:443", TLSServerName: "example.com",
		Plan: testTransportPlan(t), Observation: observation,
	})
	if connection != nil || !errors.Is(err, ErrNoTransportProfile) || dialer.dialCount() != 1 {
		t.Fatalf("missing negotiation connection=%v dials=%d error=%v", connection, dialer.dialCount(), err)
	}
	if evidence.FallbackReason() != FallbackObservedTLSHandshakeRejected || evidence.UsedFallback() {
		t.Fatalf("negotiation failure evidence=%+v", evidence)
	}
}

type countingErrorDialer struct {
	calls int
	err   error
}

func (dialer *countingErrorDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	dialer.calls++
	return nil, dialer.err
}
