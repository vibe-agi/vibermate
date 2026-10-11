package socksproxy

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"sync"
	"time"
)

func netipPeer(a net.Addr) (netip.AddrPort, error) {
	p, e := netip.ParseAddrPort(a.String())
	if e != nil {
		return netip.AddrPort{}, e
	}
	return netip.AddrPortFrom(p.Addr().Unmap(), p.Port()), nil
}

type udpFlow struct {
	owner  *session
	target Target
	opened OpenedUDP
	once   sync.Once
}

func (f *udpFlow) close() {
	f.once.Do(func() {
		f.opened.Flow.Close()
		f.owner.mu.Lock()
		if f.owner.flows[f.target] == f {
			delete(f.owner.flows, f.target)
		}
		f.owner.mu.Unlock()
	})
}
func (x *session) associate(reader *bufio.Reader, source Target) {
	peer, e := netipPeer(x.control.RemoteAddr())
	if e != nil {
		writeReply(x.control, 1, zeroBind)
		return
	}
	sourceIP, e := netip.ParseAddr(source.Host)
	if e != nil || (!sourceIP.IsUnspecified() && sourceIP.Unmap() != peer.Addr()) {
		writeReply(x.control, 2, zeroBind)
		return
	}
	local, e := netipPeer(x.control.LocalAddr())
	if e != nil || local.Addr().IsUnspecified() {
		writeReply(x.control, 1, zeroBind)
		return
	}
	network := "udp4"
	if local.Addr().Is6() {
		network = "udp6"
	}
	socket, e := net.ListenUDP(network, net.UDPAddrFromAddrPort(netip.AddrPortFrom(local.Addr(), 0)))
	if e != nil {
		writeReply(x.control, 1, zeroBind)
		return
	}
	x.mu.Lock()
	x.udp = socket
	x.mu.Unlock()
	if x.ctx.Err() != nil {
		socket.Close()
		return
	}
	bind := socket.LocalAddr().(*net.UDPAddr).AddrPort()
	if e := writeReply(x.control, 0, bind); e != nil {
		return
	}
	x.workers.Add(1)
	go func() { defer x.workers.Done(); io.Copy(io.Discard, reader); x.close() }()
	x.workers.Add(1)
	go func() {
		defer x.workers.Done()
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-x.ctx.Done():
				return
			case <-tick.C:
				if !x.reauthorize() {
					return
				}
			}
		}
	}()
	clientPort := source.Port
	buffer := make([]byte, 65535)
	for {
		n, _, flags, from, e := socket.ReadMsgUDPAddrPort(buffer, nil)
		if e != nil {
			return
		}
		from = netip.AddrPortFrom(from.Addr().Unmap(), from.Port())
		if from.Addr() != peer.Addr() || (clientPort != 0 && from.Port() != clientPort) || udpMessageTruncated(flags) || n < 4 || buffer[0] != 0 || buffer[1] != 0 || buffer[2] != 0 {
			continue
		}
		packet := bytes.NewReader(buffer[3:n])
		target, e := readTarget(packet)
		if e != nil || target.Port == 0 || packet.Len() == 0 {
			continue
		}
		if clientPort == 0 {
			clientPort = from.Port()
		}
		if !x.reauthorize() {
			return
		}
		x.mu.Lock()
		flow := x.flows[target]
		count := len(x.flows)
		x.mu.Unlock()
		if flow == nil {
			if count >= 32 {
				continue
			}
			opened, e := x.server.options.UDP.OpenUDP(x.ctx, x.admission, target)
			if e != nil {
				continue
			}
			if opened.Flow == nil || !opened.Peer.IsValid() || opened.Peer.Addr().IsUnspecified() || opened.Peer.Addr().Zone() != "" || opened.Peer.Port() == 0 {
				if opened.Flow != nil {
					opened.Flow.Close()
				}
				continue
			}
			flow = &udpFlow{owner: x, target: target, opened: opened}
			x.mu.Lock()
			if x.ctx.Err() != nil {
				x.mu.Unlock()
				flow.close()
				return
			}
			x.flows[target] = flow
			x.mu.Unlock()
			x.workers.Add(1)
			go flow.receive(socket, from)
		}
		if e := flow.opened.Flow.WritePacket(x.ctx, buffer[n-packet.Len():n]); e != nil {
			flow.close()
			if errors.Is(e, context.Canceled) {
				return
			}
		}
	}
}
func (f *udpFlow) receive(socket *net.UDPConn, client netip.AddrPort) {
	defer f.owner.workers.Done()
	defer f.close()
	b := make([]byte, 65535)
	for {
		n, e := f.opened.Flow.ReadPacket(f.owner.ctx, b)
		if e != nil || n <= 0 || n > len(b) {
			return
		}
		if !f.owner.reauthorize() {
			return
		}
		packet := append(append([]byte{0, 0, 0}, encodePeer(f.opened.Peer)...), b[:n]...)
		if len(packet) > 65507 {
			return
		}
		if _, e := socket.WriteToUDPAddrPort(packet, client); e != nil {
			return
		}
	}
}
