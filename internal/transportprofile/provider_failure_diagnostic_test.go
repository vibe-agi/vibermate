//go:build diagnostics

package transportprofile

import (
	"context"
	"errors"
	"net"
	"testing"
)

// This opt-in diagnostic guards the repaired v0.1.10 failure boundary. It
// reaches a deliberately failing dialer, never a real network connection.
func TestDiagnoseObservedClientWithoutALPNReachesDialer(t *testing.T) {
	observation := captureGoClientHello(t, "agent.example", nil)
	if !observation.Available() || len(observation.OfferedALPN()) != 0 {
		t.Fatal("fixture must be a valid ClientHello with no ALPN extension")
	}
	dialer := &diagnosticNoNetworkDialer{}
	connector, err := NewConnector(ConnectorOptions{Dialer: dialer})
	if err != nil {
		t.Fatal(err)
	}
	connection, evidence, err := connector.Connect(context.Background(), ConnectRequest{
		Network:       "tcp",
		Address:       "provider.example:443",
		TLSServerName: "provider.example",
		Plan:          testTransportPlan(t),
		Observation:   observation,
	})
	if connection != nil {
		_ = connection.Close()
		t.Fatal("unexpected upstream connection")
	}
	if !errors.Is(err, ErrNoTransportProfile) ||
		!errors.Is(err, errDiagnosticNetworkDisabled) {
		t.Fatalf("HTTP/1.1 without ALPN did not reach the test dialer: %v", err)
	}
	if dialer.calls != 1 {
		t.Fatalf("unexpected outbound dial attempts: %d", dialer.calls)
	}
	t.Logf("actual error: %v", err)
	t.Logf("outbound dial attempts=%d; requested=%s; client ALPN=%v; upstream ALPN=%v",
		dialer.calls, evidence.Requested().Ref,
		evidence.ClientOfferedALPN(), evidence.UpstreamOfferedALPN())
}

type diagnosticNoNetworkDialer struct{ calls int }

var errDiagnosticNetworkDisabled = errors.New("diagnostic dialer does not allow network access")

func (dialer *diagnosticNoNetworkDialer) DialContext(
	context.Context, string, string,
) (net.Conn, error) {
	dialer.calls++
	return nil, errDiagnosticNetworkDisabled
}
