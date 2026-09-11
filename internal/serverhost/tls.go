package serverhost

import (
	"crypto/tls"
	"net"

	"github.com/vibe-agi/vibermate/internal/serveridentity"
)

func newTLSListener(listener net.Listener, certificate tls.Certificate) net.Listener {
	return tls.NewListener(listener, &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{certificate},
		NextProtos:   []string{"http/1.1"},
	})
}

func newManagedTLSListener(listener net.Listener, manager *serveridentity.Manager) net.Listener {
	return tls.NewListener(listener, &tls.Config{
		MinVersion:     tls.VersionTLS13,
		GetCertificate: manager.GetCertificate,
		NextProtos:     []string{"http/1.1"},
		// New connections must authenticate the currently applied identity,
		// rather than resume a session authenticated with its predecessor.
		SessionTicketsDisabled: true,
	})
}
