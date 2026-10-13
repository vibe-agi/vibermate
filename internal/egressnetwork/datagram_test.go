package egressnetwork

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"
)

type packetFixtureNetwork struct {
	addresses      []netip.Addr
	lookups, dials int
}

func (n *packetFixtureNetwork) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	n.lookups++
	return n.addresses, nil
}
func (n *packetFixtureNetwork) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	n.dials++
	return (&net.Dialer{}).DialContext(ctx, network, address)
}

func TestDirectPacketsPreserveBoundariesTruncationAndCancellation(t *testing.T) {
	echo, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		b := make([]byte, 65535)
		for {
			n, a, e := echo.ReadFromUDPAddrPort(b)
			if e != nil {
				return
			}
			echo.WriteToUDPAddrPort(b[:n], a)
		}
	}()
	defer func() { echo.Close(); <-done }()
	peer := echo.LocalAddr().(*net.UDPAddr).AddrPort()
	b, _ := NewBuilder(BuilderOptions{})
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	ctx = WithTargetAccess(ctx, TargetAccessOwnerConfigured)
	c, got, e := b.OpenUDP(ctx, DefaultPolicy(), "127.0.0.1", peer.Port())
	if e != nil || got != peer {
		t.Fatal(e, got)
	}
	defer c.Close()
	if e = c.WritePacket(ctx, []byte{0, 255, 1, 128}); e != nil {
		t.Fatal(e)
	}
	if _, e = c.ReadPacket(ctx, make([]byte, 2)); !errors.Is(e, io.ErrShortBuffer) {
		t.Fatal("truncation disguised as complete packet", e)
	}
	for _, p := range [][]byte{{7}, bytes.Repeat([]byte{0, 255}, 100)} {
		if e = c.WritePacket(ctx, p); e != nil {
			t.Fatal(e)
		}
		buffer := make([]byte, 300)
		n, e := c.ReadPacket(ctx, buffer)
		if e != nil || !bytes.Equal(buffer[:n], p) {
			t.Fatal("packet boundary", e)
		}
	}
	readCtx, cancelRead := context.WithCancel(ctx)
	readDone := make(chan error, 1)
	go func() { _, e := c.ReadPacket(readCtx, make([]byte, 100)); readDone <- e }()
	cancelRead()
	select {
	case e := <-readDone:
		if !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
	case <-ctx.Done():
		t.Fatal("cancel did not unblock UDP socket read")
	}
}

func TestDatagramWholeAnswerAdmissionAndUnsupportedExitHaveNoFallback(t *testing.T) {
	for _, kind := range []ProxyKind{ProxyDirect, ProxyVLESS, ProxySOCKS5} {
		n := &packetFixtureNetwork{addresses: []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("127.0.0.1")}}
		b, _ := NewBuilder(BuilderOptions{BaseDialer: n, SystemResolver: n})
		p := DefaultPolicy()
		p.Proxy.Kind = kind
		if kind == ProxyVLESS {
			p.Proxy.CredentialRef = "secret://egress-vless/fixture"
			p.Proxy.CredentialRevision = 1
		}
		if kind == ProxySOCKS5 {
			p.Proxy.Endpoint = "127.0.0.1:12345"
		}
		c, _, e := b.OpenUDP(WithTargetAccess(t.Context(), TargetAccessPublicOnly), p, "mixed.fixture", 9)
		if c != nil || e == nil || n.dials != 0 {
			t.Fatal("datagram answer/exit bypass", kind, e, n.dials)
		}
		if kind == ProxySOCKS5 {
			if n.lookups != 0 {
				t.Fatal("unsupported packets performed target DNS")
			}
		} else if !errors.Is(e, ErrPrivateTarget) {
			t.Fatal("whole DNS answer not admitted", kind, e)
		}
	}
}

func TestDirectDomainsCompactIdentityEdges(t *testing.T) {
	for _, c := range []struct {
		rules, host string
		want        bool
	}{
		{"example.com", "example.com", true}, {"example.com", "a.example.com", true}, {"*.example.com", "example.com", false}, {"*.example.com", "a.example.com", true},
		{"example.com", "evilexample.com", false}, {"example.com", "example.com.evil", false}, {"example.com", "127.0.0.1", false},
		{"B\u00dcCHER.example.", "xn--bcher-kva.example", true}, {"b\u00fccher.example", "A.B\u00dcCHER.example.", true},
	} {
		rules, e := DirectDomains(c.rules).Normalize()
		if e != nil || rules.Matches(c.host) != c.want {
			t.Fatal("domain identity mismatch", c, e)
		}
	}
	if _, e := DirectDomains("127.0.0.1").Normalize(); e == nil {
		t.Fatal("literal selector invented domain identity")
	}
}
