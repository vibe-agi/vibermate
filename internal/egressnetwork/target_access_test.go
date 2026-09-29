package egressnetwork_test

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
)

func TestPublicTargetAccessRejectsPrivateDNSBeforeDirectOrSOCKSDial(t *testing.T) {
	for _, proxy := range []egressnetwork.ProxyPolicy{
		{Kind: egressnetwork.ProxyDirect},
		{Kind: egressnetwork.ProxySOCKS5, Endpoint: "127.0.0.1:1080"},
	} {
		for _, address := range []string{
			"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254",
			"100.100.100.200", "0.0.0.0", "224.0.0.1", "240.0.0.1", "::1", "fd00::1",
			"fe80::1", "::ffff:127.0.0.1", "64:ff9b::a9fe:a9fe", "2002:7f00:1::1",
		} {
			t.Run(string(proxy.Kind)+"/"+address, func(t *testing.T) {
				network := &disabledNetwork{}
				resolver := &recordingResolver{addresses: []netip.Addr{
					netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr(address),
				}}
				builder, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{BaseDialer: network, SystemResolver: resolver})
				if err != nil {
					t.Fatal(err)
				}
				dialer, err := builder.Dialer(egressnetwork.Policy{Proxy: proxy})
				if err != nil {
					t.Fatal(err)
				}
				ctx := egressnetwork.WithTargetAccess(context.Background(), egressnetwork.TargetAccessPublicOnly)
				for _, target := range []string{"rebind.example:443", net.JoinHostPort(address, "443")} {
					connection, err := dialer.DialContext(ctx, "tcp", target)
					if connection != nil {
						connection.Close()
					}
					if !errors.Is(err, egressnetwork.ErrPrivateTarget) || connection != nil || len(network.targets) != 0 {
						t.Fatalf("private target was dialed: target=%s dials=%v error=%v", target, network.targets, err)
					}
				}
			})
		}
	}
}

func TestPublicTargetAccessPinsTheResolvedAddress(t *testing.T) {
	network := &disabledNetwork{}
	resolver := &recordingResolver{addresses: []netip.Addr{netip.MustParseAddr("1.1.1.1")}}
	builder, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{BaseDialer: network, SystemResolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	dialer, err := builder.Dialer(egressnetwork.DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	ctx := egressnetwork.WithTargetAccess(context.Background(), egressnetwork.TargetAccessPublicOnly)
	_, err = dialer.DialContext(ctx, "tcp", "rebind.example:443")
	if err == nil || len(network.targets) != 1 || network.targets[0] != "1.1.1.1:443" || resolver.callCount() != 1 {
		t.Fatalf("admitted name was resolved again at dial: targets=%v lookups=%d error=%v", network.targets, resolver.callCount(), err)
	}
	proxy := newSOCKSServer(t, false)
	builder, err = egressnetwork.NewBuilder(egressnetwork.BuilderOptions{SystemResolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	dialer, err = builder.Dialer(egressnetwork.Policy{Proxy: egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: proxy.address()}})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := dialer.DialContext(ctx, "tcp", "rebind.example:443")
	if err != nil {
		t.Fatal(err)
	}
	connection.Close()
	if target := proxy.nextTarget(t); target != "1.1.1.1:443" {
		t.Fatalf("SOCKS target was not pinned: %s", target)
	}
}

func TestPublicTargetAccessAlsoChecksDoHAnswers(t *testing.T) {
	doh := newDoHServer(t, netip.MustParseAddr("127.0.0.1"))
	builder, err := egressnetwork.NewBuilder(egressnetwork.BuilderOptions{TLSClientConfig: doh.clientTLSConfig()})
	if err != nil {
		t.Fatal(err)
	}
	dialer, err := builder.Dialer(egressnetwork.Policy{
		Resolver: egressnetwork.ResolverPolicy{Kind: egressnetwork.ResolverDoH, DoHURL: doh.url(), Transport: egressnetwork.ResolverTransportDirect},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := egressnetwork.WithTargetAccess(context.Background(), egressnetwork.TargetAccessPublicOnly)
	connection, err := dialer.DialContext(ctx, "tcp4", "rebind.example:443")
	if connection != nil {
		connection.Close()
	}
	if !errors.Is(err, egressnetwork.ErrPrivateTarget) || connection != nil {
		t.Fatalf("DoH private target accepted: %v", err)
	}
}

// This external network boundary never creates a socket; private-address
// regression fixtures must not probe the developer's real LAN or metadata.
type disabledNetwork struct{ targets []string }

func (network *disabledNetwork) DialContext(_ context.Context, _, target string) (net.Conn, error) {
	network.targets = append(network.targets, target)
	return nil, errors.New("synthetic network disabled")
}
