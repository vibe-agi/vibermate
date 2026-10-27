//go:build darwin

package flowbroker

import "golang.org/x/sys/unix"

func peerOfFD(fd int) (Peer, error) {
	pid, err := unix.GetsockoptInt(fd, unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	if err != nil {
		return Peer{}, err
	}
	credentials, err := unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil {
		return Peer{}, err
	}
	return Peer{PID: pid, UID: int(credentials.Uid)}, nil
}
