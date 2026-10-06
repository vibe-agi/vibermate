package blindtunnel

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
)

// Every actual socket is this exact private proxy listener. Even an incorrect
// Direct selection cannot reach the target or consult the machine's DNS.
type containedBlindNetwork struct {
	proxy      string
	direct     atomic.Int32
	proxied    atomic.Int32
	closeProxy func() error
	deadline   atomic.Int64
}

func (n *containedBlindNetwork) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" || address != n.proxy {
		n.direct.Add(1)
		return nil, errors.New("fixture rejected non-proxy destination")
	}
	n.proxied.Add(1)
	if deadline, ok := ctx.Deadline(); ok {
		n.deadline.Store(deadline.UnixNano())
	}
	return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, address)
}

func TestBlindDialRejectsNilContextAndBoundsEntireConnect(t *testing.T) {
	for _, nilContext := range []bool{true, false} {
		t.Run(fmt.Sprint(nilContext), func(t *testing.T) {
			builder, network, resolved := blindSOCKSFixture(t)
			gate, err := offlinehold.New(offlinehold.DefaultConfig())
			if err != nil {
				t.Fatal(err)
			}
			if err = gate.Start(context.Background(), offlinehold.RuntimeBinding{InstanceID: "blind-context"}); err != nil {
				t.Fatal(err)
			}
			action, err := gate.BeginAction(context.Background(), offlinehold.ActionRequest{ActionID: "context-action"})
			if err != nil {
				t.Fatal(err)
			}
			defer action.Release()
			dialer, err := NewDialerWithBuilder(gate, builder)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if nilContext {
				ctx = nil
			}
			defer func() {
				if v := recover(); v != nil {
					t.Errorf("nil context panicked instead of rejecting: %v", v)
				}
			}()
			conn, lease, err := dialer.Dial(ctx, DialRequest{RequestID: "context-request", Action: action, Authority: "blind.fixture:443", Host: "blind.fixture", Port: 443, Egress: resolved})
			if conn != nil {
				defer conn.Close()
			}
			if lease != nil {
				defer lease.Release()
			}
			if nilContext {
				if err == nil || network.proxied.Load() != 0 || network.direct.Load() != 0 {
					t.Fatalf("nil context err=%v proxy=%d direct=%d", err, network.proxied.Load(), network.direct.Load())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			remaining := time.Until(time.Unix(0, network.deadline.Load()))
			if network.deadline.Load() == 0 || remaining <= 0 || remaining > DefaultDialTimeout {
				t.Fatalf("whole connect lacks 15s bound: hasDeadline=%t remaining=%s", network.deadline.Load() != 0, remaining)
			}
		})
	}
}

func (*containedBlindNetwork) LookupNetIP(_ context.Context, network, host string) ([]netip.Addr, error) {
	if network != "ip" || host != "blind.fixture" {
		return nil, errors.New("fixture rejected DNS destination")
	}
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}

func blindSOCKSFixture(t *testing.T) (*egressnetwork.Builder, *containedBlindNetwork, egressprofile.Resolved) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- nil
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		var hello [3]byte
		if _, err = io.ReadFull(conn, hello[:]); err != nil {
			done <- err
			return
		}
		if hello != [3]byte{5, 1, 0} {
			done <- fmt.Errorf("unexpected SOCKS greeting")
			return
		}
		if _, err = conn.Write([]byte{5, 0}); err != nil {
			done <- err
			return
		}
		var request [10]byte
		if _, err = io.ReadFull(conn, request[:]); err != nil {
			done <- err
			return
		}
		if request[0] != 5 || request[1] != 1 || request[2] != 0 || request[3] != 1 ||
			net.IP(request[4:8]).String() != "127.0.0.1" || binary.BigEndian.Uint16(request[8:]) != 443 {
			done <- errors.New("unexpected SOCKS target")
			return
		}
		_, err = conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
		if err == nil {
			var data [1]byte
			n, readErr := conn.Read(data[:])
			if n != 0 || readErr != io.EOF {
				err = fmt.Errorf("probe/application bytes or unclosed connection: n=%d err=%v", n, readErr)
			}
		}
		done <- err
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		if err := <-done; err != nil {
			t.Errorf("contained SOCKS: %v", err)
		}
	})
	network := &containedBlindNetwork{proxy: listener.Addr().String(), closeProxy: listener.Close}
	builder, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{BaseDialer: network, SystemResolver: network})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := (egressnetwork.Policy{Proxy: egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: network.proxy}}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := egressprofile.RestoreResolved(egressprofile.ProfileRevision{
		ID: "blind-test", Revision: 1, DisplayName: "Private blind proxy", PublishedAt: time.Unix(1, 0).UTC(), Policy: policy,
	}, egressprofile.SelectionGlobal, 3)
	if err != nil {
		t.Fatal(err)
	}
	return builder, network, resolved
}

func TestBlindProxyFailureHasNoDirectFallback(t *testing.T) {
	builder, network, resolved := blindSOCKSFixture(t)
	if err := network.closeProxy(); err != nil {
		t.Fatal(err)
	}
	prober, err := newReachabilityProber(builder, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	target := offlinehold.ProbeTarget{Kind: offlinehold.EgressBlindTunnel, Transport: offlinehold.ProbeTransportTCP,
		TargetRef: "blind.fixture:443", NetworkOrigin: "blind.fixture:443", HTTPAuthority: "blind.fixture:443",
		EgressPolicy: resolved.Profile().Policy, TargetAccess: egressnetwork.TargetAccessOwnerConfigured}
	if err = prober.Probe(context.Background(), offlinehold.ProbeRequest{Targets: []offlinehold.ProbeTarget{target}}); err == nil {
		t.Fatal("unavailable proxy passed probe")
	}
	if network.proxied.Load() != 1 || network.direct.Load() != 0 {
		t.Fatalf("proxy=%d direct=%d", network.proxied.Load(), network.direct.Load())
	}
}

func TestBlindProbeRejectsNonTCPIdentityBeforeNetwork(t *testing.T) {
	builder, network, resolved := blindSOCKSFixture(t)
	prober, err := newReachabilityProber(builder, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	target := offlinehold.ProbeTarget{Kind: offlinehold.EgressBlindTunnel, Transport: offlinehold.ProbeTransportStrictTLS,
		TargetRef: "blind.fixture:443", NetworkOrigin: "blind.fixture:443", HTTPAuthority: "blind.fixture:443", TLSServerName: "blind.fixture",
		EgressPolicy: resolved.Profile().Policy}
	err = prober.Probe(context.Background(), offlinehold.ProbeRequest{Targets: []offlinehold.ProbeTarget{target}})
	var failure *offlinehold.ProbeFailure
	if !errors.As(err, &failure) || failure.Reason != offlinehold.ProbeReasonFailed || network.proxied.Load() != 0 || network.direct.Load() != 0 {
		t.Fatalf("non-TCP blind identity: err=%v proxy=%d direct=%d", err, network.proxied.Load(), network.direct.Load())
	}
}

func TestBlindDialUsesFrozenGlobalProxy(t *testing.T) {
	builder, network, resolved := blindSOCKSFixture(t)
	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err = gate.Start(ctx, offlinehold.RuntimeBinding{InstanceID: "blind-egress-test"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		gate.BeginShutdown()
		if err := gate.Drain(ctx); err != nil {
			t.Error(err)
		}
	})
	action, err := gate.BeginAction(ctx, offlinehold.ActionRequest{ActionID: "blind-action"})
	if err != nil {
		t.Fatal(err)
	}
	defer action.Release()
	dialer, err := NewDialerWithBuilder(gate, builder)
	if err != nil {
		t.Fatal(err)
	}
	conn, lease, err := dialer.Dial(ctx, DialRequest{RequestID: "blind-request", Action: action, Authority: "blind.fixture:443", Host: "blind.fixture", Port: 443, Egress: resolved})
	if err != nil {
		t.Fatalf("frozen global proxy not used: %v (proxy=%d rejected-direct=%d)", err, network.proxied.Load(), network.direct.Load())
	}
	defer lease.Release()
	defer conn.Close()
	if network.proxied.Load() != 1 || network.direct.Load() != 0 {
		t.Fatalf("proxy=%d direct=%d", network.proxied.Load(), network.direct.Load())
	}
}

func TestBlindProbeUsesFrozenProxyAndAllowance(t *testing.T) {
	for _, access := range []egressnetwork.TargetAccess{egressnetwork.TargetAccessOwnerConfigured, egressnetwork.TargetAccessPublicOnly} {
		t.Run(fmt.Sprint(access), func(t *testing.T) {
			builder, network, resolved := blindSOCKSFixture(t)
			prober, err := newReachabilityProber(builder, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			target := offlinehold.ProbeTarget{Kind: offlinehold.EgressBlindTunnel, Transport: offlinehold.ProbeTransportTCP,
				TargetRef: "blind.fixture:443", NetworkOrigin: "blind.fixture:443", HTTPAuthority: "blind.fixture:443",
				EgressPolicy: resolved.Profile().Policy, TargetAccess: access}
			ambient := egressnetwork.TargetAccessPublicOnly
			if access == egressnetwork.TargetAccessPublicOnly {
				ambient = egressnetwork.TargetAccessOwnerConfigured
			}
			err = prober.Probe(egressnetwork.WithTargetAccess(context.Background(), ambient), offlinehold.ProbeRequest{Targets: []offlinehold.ProbeTarget{target}})
			if access == egressnetwork.TargetAccessPublicOnly {
				if !errors.Is(err, egressnetwork.ErrPrivateTarget) || network.proxied.Load() != 0 || network.direct.Load() != 0 {
					t.Fatalf("public-only private probe: err=%v proxy=%d direct=%d", err, network.proxied.Load(), network.direct.Load())
				}
			} else if err != nil || network.proxied.Load() != 1 || network.direct.Load() != 0 {
				t.Fatalf("owner proxy probe: err=%v proxy=%d direct=%d", err, network.proxied.Load(), network.direct.Load())
			}
		})
	}
}

func TestBlindConstructorsAndProbeBound(t *testing.T) {
	builder, network, resolved := blindSOCKSFixture(t)
	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewDialerWithBuilder(nil, builder); err == nil {
		t.Fatal("nil gate accepted")
	}
	if _, err = NewDialerWithBuilder(gate, nil); err == nil {
		t.Fatal("nil dial builder accepted")
	}
	if _, err = NewReachabilityProberWithBuilder(nil); err == nil {
		t.Fatal("nil probe builder accepted")
	}
	prober, err := NewReachabilityProberWithBuilder(builder)
	if err != nil {
		t.Fatal(err)
	}
	target := offlinehold.ProbeTarget{Kind: offlinehold.EgressBlindTunnel, Transport: offlinehold.ProbeTransportTCP, TargetRef: "blind.fixture:443", NetworkOrigin: "blind.fixture:443", HTTPAuthority: "blind.fixture:443", EgressPolicy: resolved.Profile().Policy, TargetAccess: egressnetwork.TargetAccessOwnerConfigured}
	if err = prober.Probe(nil, offlinehold.ProbeRequest{Targets: []offlinehold.ProbeTarget{target}}); err == nil {
		t.Fatal("nil probe context accepted")
	}
	if err = prober.Probe(context.Background(), offlinehold.ProbeRequest{Targets: []offlinehold.ProbeTarget{target}}); err != nil {
		t.Fatal(err)
	}
	remaining := time.Until(time.Unix(0, network.deadline.Load()))
	if network.deadline.Load() == 0 || remaining <= 0 || remaining > DefaultReachabilityTimeout {
		t.Fatalf("probe bound lost: %s", remaining)
	}
}

type blindAfterProbe struct {
	prober offlinehold.Prober
	after  func() error
}

func (p blindAfterProbe) Probe(ctx context.Context, request offlinehold.ProbeRequest) error {
	if err := p.prober.Probe(ctx, request); err != nil {
		return err
	}
	return p.after()
}

func TestBlindHoldFreezesProbeAndReleasedDialWithoutFallback(t *testing.T) {
	builder, network, resolved := blindSOCKSFixture(t)
	gate, err := offlinehold.New(offlinehold.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err = gate.Start(ctx, offlinehold.RuntimeBinding{InstanceID: "blind-hold"}); err != nil {
		t.Fatal(err)
	}
	if _, err = gate.Enter(ctx, gate.Snapshot().Revision); err != nil {
		t.Fatal(err)
	}
	action, err := gate.BeginAction(ctx, offlinehold.ActionRequest{ActionID: "held-action"})
	if err != nil {
		t.Fatal(err)
	}
	dialer, err := NewDialerWithBuilder(gate, builder)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		conn, lease, err := dialer.Dial(egressnetwork.WithTargetAccess(ctx, egressnetwork.TargetAccessOwnerConfigured), DialRequest{
			RequestID: "held-request", Action: action, Authority: "blind.fixture:443", Host: "blind.fixture", Port: 443, Egress: resolved})
		if conn != nil {
			conn.Close()
		}
		if lease != nil {
			lease.Release()
		}
		action.Release()
		done <- err
	}()
	joined := false
	defer func() {
		cancel()
		gate.BeginShutdown()
		if !joined {
			<-done
		}
		drain, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		if err := gate.Drain(drain); err != nil {
			t.Error(err)
		}
	}()
	deadline := time.Now().Add(time.Second)
	for gate.Snapshot().QueuedRequests != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	targets := gate.PendingProbeTargets()
	if len(targets) != 1 || targets[0].EgressPolicy != resolved.Profile().Policy || targets[0].TargetAccess != egressnetwork.TargetAccessOwnerConfigured || network.proxied.Load() != 0 || network.direct.Load() != 0 {
		t.Fatalf("held snapshot=%+v proxy=%d direct=%d", targets, network.proxied.Load(), network.direct.Load())
	}
	prober, err := newReachabilityProber(builder, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = gate.Resume(ctx, gate.Snapshot().Revision, offlinehold.ResumeRequest{Targets: targets}, blindAfterProbe{prober: prober, after: network.closeProxy}); err != nil {
		t.Fatal(err)
	}
	err = <-done
	joined = true
	if err == nil || network.proxied.Load() != 2 || network.direct.Load() != 0 {
		t.Fatalf("released failure err=%v proxy=%d direct=%d", err, network.proxied.Load(), network.direct.Load())
	}
}
