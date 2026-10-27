//go:build !linux && !darwin

package flowbroker

func peerOfFD(int) (Peer, error) { return Peer{}, errPeerUnavailable }
