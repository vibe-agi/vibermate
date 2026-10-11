package vlesscore

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"time"
)

// Interrupt only this socket and join the cancellation callback before clearing deadlines.
func bindOperation(ctx context.Context, setDeadline func(time.Time) error) func() {
	if deadline, ok := ctx.Deadline(); ok {
		setDeadline(deadline)
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { setDeadline(time.Now()); close(done) })
	return func() {
		if !stop() {
			<-done
		}
		setDeadline(time.Time{})
	}
}

func (c *Client) authenticate(ctx context.Context) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.isClosed() {
		return nil, ErrClosed
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp4", c.endpoint)
	if err != nil {
		return nil, errors.New("vlesscore: private core connection failed")
	}
	if err := c.track(conn); err != nil {
		conn.Close()
		return nil, err
	}
	defer c.untrack(conn)
	clear := bindOperation(ctx, conn.SetDeadline)
	defer clear()
	ok := false
	defer func() {
		if !ok {
			conn.Close()
		}
	}()
	if err := writeAll(conn, []byte{5, 1, 2}); err != nil {
		return nil, errors.New("vlesscore: private SOCKS negotiation failed")
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil || reply[0] != 5 || reply[1] != 2 {
		return nil, errors.New("vlesscore: private SOCKS authentication unavailable")
	}
	auth := append([]byte{1, byte(len(c.username))}, []byte(c.username)...)
	auth = append(auth, byte(len(c.password)))
	auth = append(auth, []byte(c.password)...)
	if err := writeAll(conn, auth); err != nil {
		return nil, errors.New("vlesscore: private SOCKS authentication failed")
	}
	if _, err := io.ReadFull(conn, reply); err != nil || reply[0] != 1 || reply[1] != 0 {
		return nil, errors.New("vlesscore: private SOCKS authentication rejected")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ok = true
	return conn, nil
}
func (c *Client) command(ctx context.Context, command byte, target netip.AddrPort) (net.Conn, netip.AddrPort, error) {
	conn, err := c.authenticate(ctx)
	if err != nil {
		return nil, netip.AddrPort{}, err
	}
	if err := c.track(conn); err != nil {
		conn.Close()
		return nil, netip.AddrPort{}, err
	}
	defer c.untrack(conn)
	clear := bindOperation(ctx, conn.SetDeadline)
	defer clear()
	ok := false
	defer func() {
		if !ok {
			conn.Close()
		}
	}()
	request := append([]byte{5, command, 0}, encodeAddress(target)...)
	if err := writeAll(conn, request); err != nil {
		return nil, netip.AddrPort{}, errors.New("vlesscore: private SOCKS request failed")
	}
	head := make([]byte, 3)
	if _, err := io.ReadFull(conn, head); err != nil || head[0] != 5 || head[1] != 0 || head[2] != 0 {
		return nil, netip.AddrPort{}, errors.New("vlesscore: private SOCKS request rejected")
	}
	bound, err := readAddress(conn)
	if err != nil {
		return nil, netip.AddrPort{}, err
	}
	if err := ctx.Err(); err != nil {
		return nil, netip.AddrPort{}, err
	}
	ok = true
	return conn, bound, nil
}
func writeAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}
func encodeAddress(target netip.AddrPort) []byte {
	addr := target.Addr().Unmap()
	b := []byte{4}
	if addr.Is4() {
		b[0] = 1
	}
	b = append(b, addr.AsSlice()...)
	return binary.BigEndian.AppendUint16(b, target.Port())
}
func readAddress(r io.Reader) (netip.AddrPort, error) {
	var kind [1]byte
	if _, err := io.ReadFull(r, kind[:]); err != nil {
		return netip.AddrPort{}, errors.New("vlesscore: invalid SOCKS address")
	}
	size := 0
	switch kind[0] {
	case 1:
		size = 4
	case 4:
		size = 16
	default:
		return netip.AddrPort{}, errors.New("vlesscore: nonliteral SOCKS reply address")
	}
	b := make([]byte, size+2)
	if _, err := io.ReadFull(r, b); err != nil {
		return netip.AddrPort{}, errors.New("vlesscore: incomplete SOCKS address")
	}
	addr, _ := netip.AddrFromSlice(b[:size])
	return netip.AddrPortFrom(addr.Unmap(), binary.BigEndian.Uint16(b[size:])), nil
}
