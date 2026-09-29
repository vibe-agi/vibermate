// Package servertransport owns every outbound socket used to reach one exact
// Runtime Server. HTTP control requests and proxy relay streams share the same
// explicit target, TLS policy, persistent certificate pin, and timeout budget.
package servertransport

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/serverconnection"
)

type Clock interface {
	Now() time.Time
}

type Options struct {
	Target         serverconnection.Target
	TrustDirectory string
	Clock          Clock
	Timeout        time.Duration
	// RootCAs is nil in production so the platform trust store remains
	// authoritative. Tests and explicitly scoped private-PKI callers may provide
	// a pool without weakening hostname or validity verification.
	RootCAs *x509.CertPool
}

type Transport struct {
	target        serverconnection.Target
	dialer        *net.Dialer
	client        *http.Client
	httpTransport *http.Transport
	tlsConfig     *tls.Config

	verificationMu sync.Mutex
	firstUse       bool
	fingerprint    string
	trustMode      serverconnection.TrustMode
}

type SystemTrustProbeOptions struct {
	Target  serverconnection.Target
	Clock   Clock
	Timeout time.Duration
	RootCAs *x509.CertPool
}

// ProbeSystemTrust performs a normal PKI handshake without consulting or
// mutating the legacy leaf-pin store. Callers use it before deliberately
// migrating one exact pin to renewable system-root trust.
func ProbeSystemTrust(
	ctx context.Context,
	options SystemTrustProbeOptions,
) (string, error) {
	certificates, err := probeTLS(ctx, options, &tls.Config{RootCAs: options.RootCAs})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(certificates[0])
	return hex.EncodeToString(digest[:]), nil
}

// TrustPrivateCA verifies possession of the Server key, then validates the
// hostname, chain and independently supplied CA fingerprint before saving
// address-scoped trust. It sends no HTTP request, password or session token.
func TrustPrivateCA(ctx context.Context, options Options, expectedFingerprint string) (string, error) {
	certificates, err := probeTLS(ctx, SystemTrustProbeOptions{
		Target: options.Target, Clock: options.Clock, Timeout: options.Timeout,
	}, &tls.Config{InsecureSkipVerify: true}) // verified below, before trust is changed or application data is sent
	if err != nil {
		return "", err
	}
	store, err := serverconnection.OpenPinStore(options.TrustDirectory)
	if err != nil {
		return "", err
	}
	return store.UsePrivateCA(options.Target.Address(), certificates, options.Clock.Now().UTC(), expectedFingerprint)
}

func probeTLS(ctx context.Context, options SystemTrustProbeOptions, config *tls.Config) ([][]byte, error) {
	if ctx == nil || !options.Target.Valid() ||
		options.Target.Transport() != serverconnection.TransportHTTPS ||
		options.Clock == nil || options.Timeout <= 0 {
		return nil, errors.New("Runtime Server trust probe is incomplete")
	}
	host, _, err := net.SplitHostPort(options.Target.Address().String())
	if err != nil {
		return nil, serverconnection.ErrInvalidAddress
	}
	networkDialer := &net.Dialer{
		Timeout: min(options.Timeout, 5*time.Second), KeepAlive: 15 * time.Second,
	}
	config.MinVersion = tls.VersionTLS13
	config.NextProtos = []string{"http/1.1"}
	config.ServerName = host
	config.Time = func() time.Time { return options.Clock.Now().UTC() }
	tlsDialer := &tls.Dialer{NetDialer: networkDialer, Config: config}
	probeContext, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()
	connection, err := tlsDialer.DialContext(
		probeContext,
		"tcp",
		options.Target.Address().String(),
	)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	tlsConnection, ok := connection.(*tls.Conn)
	if !ok {
		return nil, errors.New("Runtime Server trust probe did not negotiate TLS")
	}
	peers := tlsConnection.ConnectionState().PeerCertificates
	if len(peers) == 0 {
		return nil, serverconnection.ErrInvalidCertificate
	}
	certificates := make([][]byte, len(peers))
	for index, peer := range peers {
		certificates[index] = peer.Raw
	}
	return certificates, nil
}

func Open(options Options) (*Transport, error) {
	if !options.Target.Valid() || options.TrustDirectory == "" ||
		!filepath.IsAbs(options.TrustDirectory) ||
		filepath.Clean(options.TrustDirectory) != options.TrustDirectory ||
		options.Clock == nil || options.Timeout <= 0 {
		return nil, errors.New("Runtime Server transport configuration is incomplete")
	}
	result := &Transport{
		target: options.Target,
		dialer: &net.Dialer{
			Timeout:   min(options.Timeout, 5*time.Second),
			KeepAlive: 15 * time.Second,
		},
	}
	if options.Target.Transport() == serverconnection.TransportHTTPS {
		pins, err := serverconnection.OpenPinStore(options.TrustDirectory)
		if err != nil {
			return nil, err
		}
		result.tlsConfig = &tls.Config{
			MinVersion:         tls.VersionTLS13,
			NextProtos:         []string{"http/1.1"},
			InsecureSkipVerify: true, // the exact persistent pin below is authoritative
			VerifyPeerCertificate: func(
				rawCertificates [][]byte,
				_ [][]*x509.Certificate,
			) error {
				verified, verifyErr := pins.VerifyPeer(
					options.Target.Address(),
					rawCertificates,
					options.Clock.Now().UTC(),
					options.RootCAs,
				)
				if verifyErr != nil {
					return verifyErr
				}
				result.verificationMu.Lock()
				result.firstUse = result.firstUse || verified.FirstUse
				result.fingerprint = verified.Fingerprint
				result.trustMode = verified.Mode
				result.verificationMu.Unlock()
				return nil
			},
		}
	}
	result.httpTransport = &http.Transport{
		Proxy: nil, DisableCompression: true, ForceAttemptHTTP2: false,
		MaxIdleConns: 4, MaxIdleConnsPerHost: 4, MaxConnsPerHost: 4,
		IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: options.Timeout,
		TLSClientConfig: result.tlsConfig,
		DialContext: func(ctx context.Context, _, address string) (net.Conn, error) {
			if address != options.Target.Address().String() {
				return nil, errors.New("Runtime Server dial escaped the selected target")
			}
			return result.dialer.DialContext(
				ctx, "tcp", options.Target.Address().String(),
			)
		},
	}
	result.client = &http.Client{
		Transport: result.httpTransport,
		Timeout:   options.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("Runtime Server redirects are forbidden")
		},
	}
	return result, nil
}

func (transport *Transport) Do(request *http.Request) (*http.Response, error) {
	if transport == nil || request == nil || request.URL == nil ||
		request.URL.Scheme+"://"+request.URL.Host != transport.target.Origin() ||
		request.URL.User != nil {
		return nil, errors.New("request escaped the selected Runtime Server")
	}
	return transport.client.Do(request)
}

func (transport *Transport) Dial(ctx context.Context) (net.Conn, error) {
	if transport == nil || ctx == nil {
		return nil, errors.New("Runtime Server stream transport is unavailable")
	}
	if transport.target.Transport() == serverconnection.TransportHTTP {
		return transport.dialer.DialContext(
			ctx, "tcp", transport.target.Address().String(),
		)
	}
	dialer := &tls.Dialer{
		NetDialer: transport.dialer,
		Config:    transport.tlsConfig.Clone(),
	}
	return dialer.DialContext(
		ctx, "tcp", transport.target.Address().String(),
	)
}

func (transport *Transport) Trust() (bool, string) {
	if transport == nil {
		return false, ""
	}
	transport.verificationMu.Lock()
	defer transport.verificationMu.Unlock()
	return transport.firstUse, transport.fingerprint
}

func (transport *Transport) TrustMode() serverconnection.TrustMode {
	if transport == nil {
		return ""
	}
	transport.verificationMu.Lock()
	defer transport.verificationMu.Unlock()
	return transport.trustMode
}

func (transport *Transport) Close() {
	if transport != nil && transport.httpTransport != nil {
		transport.httpTransport.CloseIdleConnections()
	}
}
