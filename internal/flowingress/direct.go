package flowingress

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"time"
)

// This file holds the client's only own outbound connections: destinations an
// Environment's capture scope keeps off ViberMate (ADR 0025) are dialed
// directly from the user's machine by `vibermate run`, which runs outside the
// Agent's capture. Every other flow goes through the run's proxy.

const directDialTimeout = 30 * time.Second

// synthetic is RFC 2544's benchmarking range, which the Linux namespace's
// resolver uses for fake addresses. Such an address names nothing on the
// network, so a direct dial must use the flow's name instead.
var synthetic = netip.MustParsePrefix("198.18.0.0/15")

var errNoDirectTarget = errors.New("bypassed flow has no dialable destination")

// directTarget is where a bypassed flow is dialed: the address the Agent
// chose, unless it is synthetic, then the name.
func directTarget(name string, address netip.AddrPort) (string, error) {
	addr := address.Addr().Unmap()
	if addr.IsValid() && !synthetic.Contains(addr) {
		return netip.AddrPortFrom(addr, address.Port()).String(), nil
	}
	if name == "" || address.Port() == 0 {
		return "", errNoDirectTarget
	}
	return net.JoinHostPort(name, strconv.Itoa(int(address.Port()))), nil
}

func dialDirectTCP(ctx context.Context, target string) (net.Conn, error) {
	dialer := net.Dialer{Timeout: directDialTimeout}
	return dialer.DialContext(ctx, "tcp", target)
}

// DirectDatagrams is a UDP association that leaves directly from the user's
// machine, with the same shape as a connect-udp session.
type DirectDatagrams struct {
	conn net.Conn
}

// DialDirectUDP opens a UDP association to a bypassed destination.
func DialDirectUDP(ctx context.Context, name string, address netip.AddrPort) (*DirectDatagrams, error) {
	target, err := directTarget(name, address)
	if err != nil {
		return nil, err
	}
	dialer := net.Dialer{Timeout: directDialTimeout}
	conn, err := dialer.DialContext(ctx, "udp", target)
	if err != nil {
		return nil, err
	}
	return &DirectDatagrams{conn: conn}, nil
}

// ReadDatagram returns the next datagram from the destination.
func (direct *DirectDatagrams) ReadDatagram() ([]byte, error) {
	buffer := make([]byte, 65535)
	n, err := direct.conn.Read(buffer)
	if err != nil {
		return nil, err
	}
	return buffer[:n], nil
}

// WriteDatagram sends one datagram to the destination.
func (direct *DirectDatagrams) WriteDatagram(payload []byte) error {
	_, err := direct.conn.Write(payload)
	return err
}

// Close ends the association.
func (direct *DirectDatagrams) Close() error { return direct.conn.Close() }
