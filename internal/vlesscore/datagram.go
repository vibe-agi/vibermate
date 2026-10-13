package vlesscore

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"sync"
)

type DatagramConn interface {
	ReadPacket(context.Context, []byte) (int, error)
	WritePacket(context.Context, []byte) error
	Close() error
}
type datagramConn struct {
	client          *Client
	stream          net.Conn
	responseRead    bool
	readMu, writeMu sync.Mutex
	once            sync.Once
}

func (c *Client) DialUDP(ctx context.Context, target netip.AddrPort) (DatagramConn, error) {
	if !validTarget(target) {
		return nil, errors.New("vlesscore: UDP target must be a literal address with a nonzero port")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.isClosed() {
		return nil, ErrClosed
	}
	stream, err := (&net.Dialer{}).DialContext(ctx, "unix", c.udpPath)
	if err != nil {
		return nil, errors.New("vlesscore: private datagram connection failed")
	}
	if err := c.track(stream); err != nil {
		stream.Close()
		return nil, err
	}
	defer c.untrack(stream)
	clear := bindOperation(ctx, stream.SetDeadline)
	defer clear()
	if err := writeAll(stream, vlessUDPHeader(c.udpID, target)); err != nil {
		stream.Close()
		return nil, errors.New("vlesscore: private datagram authentication write failed")
	}
	if err := ctx.Err(); err != nil {
		stream.Close()
		return nil, err
	}
	d := &datagramConn{client: c, stream: stream}
	if err := c.track(d); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}
func (d *datagramConn) Close() error {
	d.once.Do(func() { d.stream.Close(); d.client.untrack(d) })
	return nil
}

// Ordinary VLESS v0 UDP framing inside our owned Unix stream. REALITY and
// Vision remain exclusively implemented by the official outward core.
func vlessUDPHeader(id [16]byte, target netip.AddrPort) []byte {
	b := append([]byte{0}, id[:]...)
	b = append(b, 0, 2) // no addons; UDP command
	b = binary.BigEndian.AppendUint16(b, target.Port())
	addr := target.Addr().Unmap()
	if addr.Is4() {
		b = append(b, 1)
	} else {
		b = append(b, 3)
	}
	return append(b, addr.AsSlice()...)
}

// WritePacket preserves one packet boundary; pinned XUDP accepts 1..7526 bytes.
func (d *datagramConn) WritePacket(ctx context.Context, payload []byte) error {
	if len(payload) == 0 || len(payload) > 7526 {
		return errors.New("vlesscore: UDP payload must contain 1 to 7526 bytes")
	}
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	clear := bindOperation(ctx, d.stream.SetWriteDeadline)
	defer clear()
	packet := binary.BigEndian.AppendUint16(make([]byte, 0, len(payload)+2), uint16(len(payload)))
	packet = append(packet, payload...)
	err := writeAll(d.stream, packet)
	if err != nil {
		d.Close() // A partial write loses framing: terminate, never replay.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("vlesscore: UDP frame write failed")
	}
	return nil
}
func (d *datagramConn) ReadPacket(ctx context.Context, buffer []byte) (int, error) {
	d.readMu.Lock()
	defer d.readMu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	clear := bindOperation(ctx, d.stream.SetReadDeadline)
	defer clear()
	fail := func() (int, error) {
		d.Close() // Incomplete header/payload or canceled read loses framing.
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, errors.New("vlesscore: incomplete or invalid UDP frame")
	}
	if !d.responseRead {
		var header [2]byte
		if _, err := io.ReadFull(d.stream, header[:]); err != nil || header[0] != 0 {
			return fail()
		}
		if _, err := io.ReadFull(d.stream, make([]byte, int(header[1]))); err != nil {
			return fail()
		}
		d.responseRead = true
	}
	var length [2]byte
	if _, err := io.ReadFull(d.stream, length[:]); err != nil {
		return fail()
	}
	n := int(binary.BigEndian.Uint16(length[:]))
	if n == 0 || n > 7526 {
		return fail()
	}
	packet := make([]byte, n)
	if _, err := io.ReadFull(d.stream, packet); err != nil {
		return fail()
	}
	if n > len(buffer) {
		return 0, io.ErrShortBuffer
	}
	return copy(buffer, packet), nil
}
