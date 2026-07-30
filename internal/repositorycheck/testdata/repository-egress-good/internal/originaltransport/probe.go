package originaltransport

import "net"

func TLSOnlyProbeDialer() *net.Dialer {
	return &net.Dialer{}
}
