package egressnetwork

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"
)

// DatagramConn carries complete packets to one admitted, pinned destination.
type DatagramConn interface {
	ReadPacket(context.Context, []byte) (int, error)
	WritePacket(context.Context, []byte) error
	Close() error
}
type VLESSDatagramConnector interface {
	DialUDP(context.Context, ProxyPolicy, netip.AddrPort) (DatagramConn, error)
}

// OpenUDP makes the same original-name route decision and resolver selection
// as TCP. A selected upstream without packet support fails before target DNS.
func (b *Builder) OpenUDP(ctx context.Context, policy Policy, host string, port uint16) (DatagramConn, netip.AddrPort, error) {
	if ctx == nil || port == 0 {
		return nil, netip.AddrPort{}, errors.New("datagram target is incomplete")
	}
	host, err := normalizeHost(host)
	if err != nil {
		return nil, netip.AddrPort{}, err
	}
	normalized, err := policy.Normalize()
	if err != nil {
		return nil, netip.AddrPort{}, err
	}
	direct := normalized.DirectDomains.Matches(host)
	if direct {
		normalized = Policy{Proxy: ProxyPolicy{Kind: ProxyDirect}, Resolver: ResolverPolicy{Kind: ResolverSystem}}
	}
	if normalized.Proxy.Kind != ProxyDirect && normalized.Proxy.Kind != ProxyVLESS {
		return nil, netip.AddrPort{}, errors.New("selected egress has no supported datagram transport")
	}
	d, err := b.Dialer(normalized)
	if err != nil {
		return nil, netip.AddrPort{}, err
	}
	resolver := d.(*policyDialer).resolver
	var addresses []netip.Addr
	if a, e := netip.ParseAddr(host); e == nil {
		addresses = []netip.Addr{a}
	} else {
		addresses, err = resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, netip.AddrPort{}, fmt.Errorf("resolve datagram target: %w", err)
		}
	}
	// Reject the complete answer before constructing any packet channel.
	for _, a := range addresses {
		if !a.IsValid() || a.Zone() != "" || a.IsUnspecified() {
			return nil, netip.AddrPort{}, errors.New("invalid datagram DNS answer")
		}
		if publicTargetsOnly(ctx) && !publicAddress(a) {
			return nil, netip.AddrPort{}, ErrPrivateTarget
		}
	}
	if len(addresses) == 0 {
		return nil, netip.AddrPort{}, errors.New("datagram resolver returned no address")
	}
	peer := netip.AddrPortFrom(addresses[0].Unmap(), port)
	if normalized.Proxy.Kind == ProxyVLESS {
		connector, ok := b.vless.(VLESSDatagramConnector)
		if !ok {
			return nil, netip.AddrPort{}, errors.New("VLESS datagram transport is unavailable")
		}
		c, e := connector.DialUDP(ctx, normalized.Proxy, peer)
		return c, peer, e
	}
	c, err := b.base.DialContext(ctx, "udp", net.JoinHostPort(peer.Addr().String(), strconv.Itoa(int(port))))
	if err != nil {
		return nil, netip.AddrPort{}, err
	}
	udp, ok := c.(*net.UDPConn)
	if !ok {
		c.Close()
		return nil, netip.AddrPort{}, errors.New("Direct datagram connector must return a connected UDP socket")
	}
	return &directDatagram{conn: udp}, peer, nil
}

type directDatagram struct {
	conn            *net.UDPConn
	readMu, writeMu sync.Mutex
}

func (d *directDatagram) Close() error { return d.conn.Close() }
func (d *directDatagram) ReadPacket(ctx context.Context, buffer []byte) (int, error) {
	d.readMu.Lock()
	defer d.readMu.Unlock()
	if ctx == nil {
		return 0, errors.New("missing packet context")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	release := datagramDeadline(ctx, d.conn.SetReadDeadline)
	defer release()
	// Read a full maximum-size UDP packet before copying. Socket truncation
	// cannot masquerade as a successful shortened caller packet.
	packet := make([]byte, 65535)
	n, err := d.conn.Read(packet)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, err
	}
	if n > len(buffer) {
		return 0, io.ErrShortBuffer
	}
	return copy(buffer, packet[:n]), nil
}
func (d *directDatagram) WritePacket(ctx context.Context, p []byte) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	if ctx == nil {
		return errors.New("missing packet context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	release := datagramDeadline(ctx, d.conn.SetWriteDeadline)
	defer release()
	n, err := d.conn.Write(p)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	if n != len(p) {
		return io.ErrShortWrite
	}
	return nil
}
func datagramDeadline(ctx context.Context, set func(time.Time) error) func() {
	deadline, _ := ctx.Deadline()
	set(deadline)
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { set(time.Now()); close(done) })
	return func() {
		if !stop() {
			<-done
		}
		set(time.Time{})
	}
}
