package egressnetwork_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
)

// After a network change, one address can blackhole while another answer for
// the same destination remains reachable. A proxy connection must not spend
// the caller's entire budget on the first address.
func TestConfiguredProxyTriesNextResolvedAddressWithinCallerBudget(t *testing.T) {
	proxy := &recoveringAddressProxy{}
	base := &disabledNetwork{}
	resolver := &recordingResolver{addresses: []netip.Addr{
		netip.MustParseAddr("203.0.113.41"), netip.MustParseAddr("203.0.113.42"),
	}}
	builder, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{
		BaseDialer: base, SystemResolver: resolver, VLESS: proxy,
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := egressnetwork.ProxyPolicy{
		Kind: egressnetwork.ProxyVLESS, CredentialRef: "secret://egress-vless/address-recovery", CredentialRevision: 1,
	}
	dialer, err := builder.Dialer(egressnetwork.Policy{Proxy: policy})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 600*time.Millisecond)
	defer cancel()
	conn, err := dialer.DialContext(ctx, "tcp", "agent.example:443")
	if err != nil {
		t.Fatalf("reachable second address was hidden by a stale first address: %v", err)
	}
	defer conn.Close()
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	if len(proxy.addresses) != 2 || proxy.addresses[0] != "203.0.113.41:443" ||
		proxy.addresses[1] != "203.0.113.42:443" || len(base.targets) != 0 ||
		proxy.policy != policy || resolver.callCount() != 1 {
		t.Fatalf("recovery changed the approved proxy or destination: addresses=%v direct=%v", proxy.addresses, base.targets)
	}
}

func TestSOCKSHandshakeStallLeavesBudgetForNextResolvedAddress(t *testing.T) {
	proxy := newSOCKSServerHoldingTarget(t, false, "203.0.113.41:443")
	resolver := &recordingResolver{addresses: []netip.Addr{
		netip.MustParseAddr("203.0.113.41"), netip.MustParseAddr("203.0.113.42"),
	}}
	builder, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{SystemResolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	dialer, err := builder.Dialer(egressnetwork.Policy{Proxy: egressnetwork.ProxyPolicy{
		Kind: egressnetwork.ProxySOCKS5, Endpoint: proxy.address(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 800*time.Millisecond)
	defer cancel()
	conn, err := dialer.DialContext(ctx, "tcp", "agent.example:443")
	if err != nil {
		t.Fatalf("stalled proxy handshake hid the reachable address: %v", err)
	}
	conn.Close()
	if first, second := proxy.nextTarget(t), proxy.nextTarget(t); first != "203.0.113.41:443" || second != "203.0.113.42:443" {
		t.Fatalf("recovery changed the selected proxy or target: %s, %s", first, second)
	}
}

func TestIncompatibleAnswersDoNotConsumeTheOnlyUsableAddressBudget(t *testing.T) {
	resolver := &recordingResolver{addresses: []netip.Addr{
		netip.MustParseAddr("203.0.113.42"),
		netip.MustParseAddr("2001:db8::1"), netip.MustParseAddr("2001:db8::2"),
		netip.MustParseAddr("2001:db8::3"), netip.MustParseAddr("2001:db8::4"),
	}}
	builder, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{
		SystemResolver: resolver, VLESS: &slowHealthyAddressProxy{},
	})
	if err != nil {
		t.Fatal(err)
	}
	dialer, err := builder.Dialer(egressnetwork.Policy{Proxy: egressnetwork.ProxyPolicy{
		Kind: egressnetwork.ProxyVLESS, CredentialRef: "secret://egress-vless/compatible-address", CredentialRevision: 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 1500*time.Millisecond)
	defer cancel()
	conn, err := dialer.DialContext(ctx, "tcp4", "agent.example:443")
	if err != nil {
		t.Fatalf("incompatible IPv6 answers shortened the usable IPv4 budget: %v", err)
	}
	conn.Close()
}

type slowHealthyAddressProxy struct{}

func (*slowHealthyAddressProxy) DialContext(ctx context.Context, _ egressnetwork.ProxyPolicy, network, address string) (net.Conn, error) {
	if network != "tcp4" || address != "203.0.113.42:443" {
		return nil, errors.New("unexpected address family")
	}
	timer := time.NewTimer(400 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
	}
	client, server := net.Pipe()
	server.Close()
	return client, nil
}

func TestProxyConnectionEstablishmentHasBudgetWithoutCallerDeadline(t *testing.T) {
	proxy := &boundedConnectionProxy{}
	base := &disabledNetwork{}
	builder, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{BaseDialer: base, VLESS: proxy})
	if err != nil {
		t.Fatal(err)
	}
	dialer, err := builder.Dialer(egressnetwork.Policy{Proxy: egressnetwork.ProxyPolicy{
		Kind: egressnetwork.ProxyVLESS, CredentialRef: "secret://egress-vless/bounded-connection", CredentialRevision: 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn, err := dialer.DialContext(ctx, "tcp", "203.0.113.42:443")
	if err != nil {
		t.Fatalf("proxy connection establishment had no finite budget: %v", err)
	}
	defer conn.Close()
	cancel()
	// The budget owns establishment, never the established Agent stream.
	if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("agent stream")); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, len("agent stream"))
	if _, err := io.ReadFull(conn, reply); err != nil || string(reply) != "agent stream" {
		t.Fatalf("connection budget closed an established stream: %q %v", reply, err)
	}
	if len(base.targets) != 0 {
		t.Fatal("proxy setup fell back to direct")
	}
}

type boundedConnectionProxy struct{}

func (*boundedConnectionProxy) DialContext(ctx context.Context, _ egressnetwork.ProxyPolicy, _, _ string) (net.Conn, error) {
	deadline, bounded := ctx.Deadline()
	if !bounded || time.Until(deadline) <= 0 || time.Until(deadline) > 30*time.Second {
		return nil, errors.New("external proxy handshake may wait indefinitely")
	}
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		_, _ = io.Copy(server, server)
	}()
	return client, nil
}

type recoveringAddressProxy struct {
	mu        sync.Mutex
	addresses []string
	policy    egressnetwork.ProxyPolicy
}

func (p *recoveringAddressProxy) DialContext(ctx context.Context, policy egressnetwork.ProxyPolicy, _, address string) (net.Conn, error) {
	p.mu.Lock()
	p.addresses = append(p.addresses, address)
	p.policy = policy
	p.mu.Unlock()
	if address == "203.0.113.41:443" {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if address != "203.0.113.42:443" {
		return nil, errors.New("unexpected target")
	}
	client, server := net.Pipe()
	server.Close()
	return client, nil
}
