package runlauncher

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/serverconnection"
)

// remoteTransport is the one exact Server wire adapter shared by login,
// Capture control, and the local proxy relay. It never redirects or changes
// the Target's explicitly selected HTTP/HTTPS transport.
type remoteTransport struct {
	target     serverconnection.Target
	httpClient *http.Client
	transport  *http.Transport
	tlsConfig  *tls.Config

	verificationMu sync.Mutex
	firstUse       bool
	fingerprint    string
}

func openRemoteTransport(
	config RemoteConfig,
	timeout time.Duration,
) (*remoteTransport, error) {
	if err := config.validate(); err != nil || timeout <= 0 {
		return nil, errors.New("remote Runtime Server transport configuration is incomplete")
	}
	result := &remoteTransport{target: config.Target}
	if config.Target.Transport() == serverconnection.TransportHTTPS {
		pins, err := serverconnection.OpenPinStore(filepath.Join(config.StateDirectory, "trust"))
		if err != nil {
			return nil, err
		}
		result.tlsConfig = &tls.Config{
			MinVersion:         tls.VersionTLS13,
			NextProtos:         []string{"http/1.1"},
			InsecureSkipVerify: true, // replaced by the exact persistent pin callback below
			VerifyPeerCertificate: func(rawCertificates [][]byte, _ [][]*x509.Certificate) error {
				verified, verifyErr := pins.Verify(
					config.Target.Address(), rawCertificates, config.Clock.Now().UTC(),
				)
				if verifyErr != nil {
					return verifyErr
				}
				result.verificationMu.Lock()
				result.firstUse = result.firstUse || verified.FirstUse
				result.fingerprint = verified.Fingerprint
				result.verificationMu.Unlock()
				return nil
			},
		}
	}
	dialer := &net.Dialer{Timeout: min(timeout, 5*time.Second), KeepAlive: 15 * time.Second}
	result.transport = &http.Transport{
		Proxy: nil, DisableCompression: true, ForceAttemptHTTP2: false,
		MaxIdleConns: 4, MaxIdleConnsPerHost: 4, MaxConnsPerHost: 4,
		IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: timeout,
		TLSClientConfig: result.tlsConfig,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if address != config.Target.Address().String() {
				return nil, errors.New("remote control dial escaped the selected Server")
			}
			return dialer.DialContext(ctx, "tcp", config.Target.Address().String())
		},
	}
	result.httpClient = &http.Client{
		Transport: result.transport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("Runtime Server redirects are forbidden")
		},
	}
	return result, nil
}

func (transport *remoteTransport) close() {
	if transport != nil && transport.transport != nil {
		transport.transport.CloseIdleConnections()
	}
}

func (transport *remoteTransport) trust() (bool, string) {
	if transport == nil {
		return false, ""
	}
	transport.verificationMu.Lock()
	defer transport.verificationMu.Unlock()
	return transport.firstUse, transport.fingerprint
}
