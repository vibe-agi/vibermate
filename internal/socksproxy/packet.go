package socksproxy

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

var zeroBind = netip.MustParseAddrPort("0.0.0.0:0")

func readTarget(r io.Reader) (Target, error) {
	var a [1]byte
	if _, e := io.ReadFull(r, a[:]); e != nil {
		return Target{}, e
	}
	var host string
	switch a[0] {
	case 1, 4:
		n := 4
		if a[0] == 4 {
			n = 16
		}
		b := make([]byte, n)
		if _, e := io.ReadFull(r, b); e != nil {
			return Target{}, e
		}
		ip, _ := netip.AddrFromSlice(b)
		host = ip.Unmap().String()
	case 3:
		if _, e := io.ReadFull(r, a[:]); e != nil || a[0] == 0 {
			return Target{}, errors.New("socksproxy: invalid domain")
		}
		b := make([]byte, int(a[0]))
		if _, e := io.ReadFull(r, b); e != nil {
			return Target{}, e
		}
		host = string(b)
		for _, c := range b {
			if c <= 32 || c >= 127 || strings.ContainsRune(":/\\[]@?#%", rune(c)) {
				return Target{}, errors.New("socksproxy: invalid domain")
			}
		}
	default:
		return Target{}, errors.New("socksproxy: unsupported address")
	}
	var p [2]byte
	if _, e := io.ReadFull(r, p[:]); e != nil {
		return Target{}, e
	}
	return Target{Host: host, Port: binary.BigEndian.Uint16(p[:])}, nil
}
func (t Target) authority() string { return net.JoinHostPort(t.Host, strconv.Itoa(int(t.Port))) }
func encodePeer(peer netip.AddrPort) []byte {
	ip := peer.Addr().Unmap()
	b := []byte{1}
	if ip.Is6() {
		b[0] = 4
	}
	b = append(b, ip.AsSlice()...)
	return binary.BigEndian.AppendUint16(b, peer.Port())
}
func writeReply(w io.Writer, status byte, peer netip.AddrPort) error {
	_, e := w.Write(append([]byte{5, status, 0}, encodePeer(peer)...))
	return e
}
