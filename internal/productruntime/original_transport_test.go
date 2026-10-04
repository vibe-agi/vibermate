package productruntime

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/blindtunnel"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
)

type noNetworkBlindTunnels struct{ coordinator offlinehold.Coordinator }

func (d *noNetworkBlindTunnels) BeginAction(ctx context.Context, r offlinehold.ActionRequest) (*offlinehold.ActionLease, error) {
	return d.coordinator.BeginAction(ctx, r)
}
func (*noNetworkBlindTunnels) Dial(context.Context, blindtunnel.DialRequest) (net.Conn, offlinehold.Lease, error) {
	return nil, nil, errors.New("blind dialing denied")
}

func TestBlindTunnelSelectionPreservesTrustedInstance(t *testing.T) {
	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	dialer := &noNetworkBlindTunnels{coordinator: gate}
	selected, err := selectBlindTunnels(Options{BlindTunnels: dialer, OfflineHold: gate})
	if err != nil || selected != dialer {
		t.Fatal("trusted blind dialer ignored; fixture would use production network")
	}
}

func TestBlindTunnelSelectionDefaultsRequireOfflineGate(t *testing.T) {
	if _, err := selectBlindTunnels(Options{}); err == nil {
		t.Fatal("default blind dialer omitted real offline gate")
	}
	gate, _ := offlinehold.New(offlinehold.DefaultConfig())
	if dialer, err := selectBlindTunnels(Options{OfflineHold: gate}); err != nil || dialer == nil {
		t.Fatal("production blind dialer unavailable")
	}
}

type noNetworkOriginalTransport struct{}

func (*noNetworkOriginalTransport) RoundTrip(*http.Request, egressnetwork.Policy) (*http.Response, error) {
	return nil, errors.New("no outbound in composition test")
}

func TestOriginalTransportSelectionPreservesTrustedInstance(t *testing.T) {
	injected := &noNetworkOriginalTransport{}
	selected, err := selectOriginalTransport(Options{OriginalTransport: injected})
	if err != nil || selected != injected {
		t.Fatal("trusted original transport ignored; fixture would use production network")
	}
}

func TestOriginalTransportSelectionDefaultsToProduction(t *testing.T) {
	selected, err := selectOriginalTransport(Options{})
	if err != nil || selected == nil {
		t.Fatal("normal production transport unavailable")
	}
	if _, err := selected.RoundTrip(nil, egressnetwork.DefaultPolicy()); err == nil {
		t.Fatal("strict production transport accepted missing request")
	}
}
