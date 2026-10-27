//go:build linux

package main

import (
	"net"
	"net/netip"
	"time"
)

// dialDirect is the emulator's equivalent of the extension declining a flow:
// the connection goes to its original destination untouched. Root's own
// connections are not redirected, so this cannot loop.
func dialDirect(destination netip.AddrPort) (net.Conn, error) {
	return (&net.Dialer{Timeout: 10 * time.Second}).Dial("tcp4", destination.String())
}
