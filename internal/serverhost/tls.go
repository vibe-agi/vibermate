package serverhost

import (
	"crypto/tls"
	"net"
)

func newTLSListener(listener net.Listener, certificate tls.Certificate) net.Listener {
	return tls.NewListener(listener, &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{certificate},
		NextProtos:   []string{"http/1.1"},
	})
}
