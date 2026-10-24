package flowbroker

import (
	"errors"
	"net"
)

// Peer is the kernel-reported process on the other end of a Unix socket. It
// cannot be forged by the peer, unlike anything it writes.
type Peer struct {
	PID int
	UID int
}

var errPeerUnavailable = errors.New("socket peer credentials are unavailable")

func peerOf(conn *net.UnixConn) (Peer, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return Peer{}, err
	}
	var peer Peer
	var peerErr error
	if err := raw.Control(func(fd uintptr) { peer, peerErr = peerOfFD(int(fd)) }); err != nil {
		return Peer{}, err
	}
	if peerErr != nil {
		return Peer{}, peerErr
	}
	if peer.PID <= 0 {
		return Peer{}, errPeerUnavailable
	}
	return peer, nil
}
