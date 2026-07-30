package originaltransport

import "net"

func UngatedDialer() *net.Dialer {
	return &net.Dialer{}
}
