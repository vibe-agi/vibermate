package flowingress

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/connectudp"
)

// A UDP flow from the macOS extension is one application socket, which may
// send to many destinations. After its header, each direction carries
// datagram frames: a 4-byte big-endian length of the rest, a 1-byte endpoint
// length, the remote endpoint as ip:port ([ip]:port for IPv6), and the
// payload. Each destination becomes one connect-udp session through the run's
// proxy, so the datagrams reach the Runtime's UDP policy and egress.
const (
	maxEndpointBytes    = 255
	maxDatagramFrame    = 1 + maxEndpointBytes + connectudp.MaxDatagram
	maxDatagramSessions = 64
	datagramQueue       = 64
	datagramIdleTimeout = 2 * time.Minute
	datagramDialTimeout = 30 * time.Second
)

var errInvalidDatagram = errors.New("datagram frame is invalid")

// WriteDatagram frames payload as sent to or received from endpoint.
func WriteDatagram(w io.Writer, endpoint netip.AddrPort, payload []byte) error {
	address := endpoint.String()
	if !endpoint.IsValid() || endpoint.Port() == 0 || len(address) > maxEndpointBytes ||
		len(payload) > connectudp.MaxDatagram {
		return errInvalidDatagram
	}
	frame := make([]byte, 4+1+len(address)+len(payload))
	binary.BigEndian.PutUint32(frame, uint32(len(frame)-4))
	frame[4] = byte(len(address))
	copy(frame[5:], address)
	copy(frame[5+len(address):], payload)
	_, err := w.Write(frame)
	return err
}

// ReadDatagram reads one frame and rejects anything it does not model.
func ReadDatagram(r io.Reader) (netip.AddrPort, []byte, error) {
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return netip.AddrPort{}, nil, err
	}
	n := binary.BigEndian.Uint32(size[:])
	if n < 1 || n > maxDatagramFrame {
		return netip.AddrPort{}, nil, errInvalidDatagram
	}
	frame := make([]byte, n)
	if _, err := io.ReadFull(r, frame); err != nil {
		return netip.AddrPort{}, nil, err
	}
	length := int(frame[0])
	if length == 0 || 1+length > len(frame) {
		return netip.AddrPort{}, nil, errInvalidDatagram
	}
	endpoint, err := netip.ParseAddrPort(string(frame[1 : 1+length]))
	if err != nil || endpoint.Port() == 0 {
		return netip.AddrPort{}, nil, errInvalidDatagram
	}
	return endpoint, frame[1+length:], nil
}

// datagramFlow relays one UDP flow's frames through per-destination
// connect-udp sessions.
type datagramFlow struct {
	ingress *Ingress
	ctx     context.Context
	cancel  context.CancelFunc
	conn    net.Conn

	writeMu sync.Mutex
	mu      sync.Mutex
	open    map[netip.AddrPort]*datagramSession
	wait    sync.WaitGroup
}

type datagramSession struct {
	queue chan []byte
}

func (ingress *Ingress) serveDatagrams(ctx context.Context, conn net.Conn, reader *bufio.Reader) error {
	ctx, cancel := context.WithCancel(ctx)
	flow := &datagramFlow{ingress: ingress, ctx: ctx, cancel: cancel, conn: conn,
		open: map[netip.AddrPort]*datagramSession{}}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer func() {
		cancel()
		stop()
		flow.wait.Wait()
	}()
	for {
		endpoint, payload, err := ReadDatagram(reader)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		flow.send(endpoint, payload)
	}
}

// send queues payload for endpoint's session, starting it when there is room.
// A full queue or session table drops the datagram, as a network would.
func (flow *datagramFlow) send(endpoint netip.AddrPort, payload []byte) {
	endpoint = netip.AddrPortFrom(endpoint.Addr().Unmap(), endpoint.Port())
	flow.mu.Lock()
	session, exists := flow.open[endpoint]
	if !exists {
		if len(flow.open) >= maxDatagramSessions || flow.ctx.Err() != nil {
			flow.mu.Unlock()
			return
		}
		session = &datagramSession{queue: make(chan []byte, datagramQueue)}
		flow.open[endpoint] = session
		flow.wait.Add(1)
		go flow.run(endpoint, session)
	}
	flow.mu.Unlock()
	select {
	case session.queue <- payload:
	default:
	}
}

func (flow *datagramFlow) run(endpoint netip.AddrPort, session *datagramSession) {
	defer flow.wait.Done()
	defer flow.forget(endpoint, session)
	idle := time.NewTimer(datagramIdleTimeout)
	defer idle.Stop()
	upstream, err := flow.dial(endpoint)
	if err != nil {
		// A refused destination stays refused until it goes quiet, so the
		// Runtime is not asked again for every datagram.
		for {
			select {
			case <-session.queue:
				idle.Reset(datagramIdleTimeout)
			case <-idle.C:
				return
			case <-flow.ctx.Done():
				return
			}
		}
	}
	ctx, done := context.WithCancel(flow.ctx)
	defer done()
	stop := context.AfterFunc(ctx, func() { _ = upstream.Close() })
	defer stop()
	go func() {
		defer done()
		for {
			payload, err := upstream.ReadDatagram()
			if err != nil {
				return
			}
			flow.writeMu.Lock()
			err = WriteDatagram(flow.conn, endpoint, payload)
			flow.writeMu.Unlock()
			if err != nil {
				flow.cancel()
				return
			}
		}
	}()
	for {
		select {
		case payload := <-session.queue:
			if upstream.WriteDatagram(payload) != nil {
				return
			}
			idle.Reset(datagramIdleTimeout)
		case <-idle.C:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (flow *datagramFlow) dial(endpoint netip.AddrPort) (*connectudp.Conn, error) {
	proxy, err := flow.ingress.proxy()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(flow.ctx, datagramDialTimeout)
	defer cancel()
	return connectudp.Dial(ctx, proxy.Origin, proxy.Token, endpoint.Addr().String(), endpoint.Port())
}

func (flow *datagramFlow) forget(endpoint netip.AddrPort, session *datagramSession) {
	flow.mu.Lock()
	if flow.open[endpoint] == session {
		delete(flow.open, endpoint)
	}
	flow.mu.Unlock()
}
