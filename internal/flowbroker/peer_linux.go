//go:build linux

package flowbroker

import "golang.org/x/sys/unix"

func peerOfFD(fd int) (Peer, error) {
	credentials, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return Peer{}, err
	}
	return Peer{PID: int(credentials.Pid), UID: int(credentials.Uid)}, nil
}
