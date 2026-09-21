package serverhost

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"time"

	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/serveridentity"
)

type TransportMode string

const (
	TransportHTTP         TransportMode = "http"
	TransportPrivateCATLS TransportMode = "private_ca_tls"
	// TransportSelfSignedTLS is the legacy spelling for the private-CA mode.
	// Keep accepting it so existing services and Compose files never change
	// transport during an upgrade.
	TransportSelfSignedTLS TransportMode = "self_signed_tls"
	TransportTLSFiles      TransportMode = "tls_files"
	TransportAutomaticTLS  TransportMode = "automatic_tls"
)

type TransportOptions struct {
	Mode            TransportMode
	CertificateFile string
	PrivateKeyFile  string
	// TLSHosts seeds a new identity only; persisted addresses win on restart.
	TLSHosts  []string
	Automatic serveridentity.AutomaticPolicy
}

func (options TransportOptions) Valid() bool { return options.validate() == nil }

func (options TransportOptions) validate() error {
	if len(options.TLSHosts) != 0 {
		if options.Mode != TransportPrivateCATLS &&
			options.Mode != TransportSelfSignedTLS {
			return errors.New("TLS hosts apply only to private_ca_tls transport")
		}
		if _, err := serveridentity.NormalizeHosts(options.TLSHosts); err != nil {
			return err
		}
	}
	automaticConfigured := options.Automatic != (serveridentity.AutomaticPolicy{})
	switch options.Mode {
	case TransportHTTP, TransportPrivateCATLS, TransportSelfSignedTLS:
		if options.CertificateFile != "" || options.PrivateKeyFile != "" ||
			automaticConfigured {
			return errors.New("Runtime Server transport files do not match its mode")
		}
		return nil
	case TransportTLSFiles:
		if automaticConfigured {
			return errors.New("Runtime Server automatic HTTPS does not match tls_files mode")
		}
		if !validAbsolutePath(options.CertificateFile) ||
			!validAbsolutePath(options.PrivateKeyFile) ||
			options.CertificateFile == options.PrivateKeyFile {
			return errors.New("Runtime Server TLS file configuration is invalid")
		}
		return nil
	case TransportAutomaticTLS:
		if options.CertificateFile != "" || options.PrivateKeyFile != "" ||
			len(options.TLSHosts) != 0 {
			return errors.New("automatic HTTPS cannot use static TLS files or private-CA hosts")
		}
		return options.Automatic.Validate()
	default:
		return errors.New("Runtime Server transport mode is invalid")
	}
}

func validAbsolutePath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path
}

type preparedTransport struct {
	listener net.Listener
	scheme   string
	status   func() transportTLSStatus
	close    func()
}

type transportTLSStatus struct {
	mode        TransportMode
	state       string
	serverName  string
	challenge   string
	fingerprint string
	issuer      string
	notBefore   string
	notAfter    string
	lastError   string
}

type runtimeCertificateAuthority struct{ runtime *productruntime.Runtime }

func (ca runtimeCertificateAuthority) CertificatePEM() []byte {
	return ca.runtime.LocalRootCertificate().CertificatePEM()
}

func (ca runtimeCertificateAuthority) SignServerCertificate(ctx context.Context, key *ecdsa.PublicKey, hosts []string) ([]byte, error) {
	return ca.runtime.SignServerCertificate(ctx, key, hosts)
}

func prepareTransport(
	ctx context.Context,
	listener net.Listener,
	options TransportOptions,
	accessAddress string,
	dataDirectory string,
	random io.Reader,
	now func() time.Time,
	issuer serveridentity.CertificateAuthority,
) (preparedTransport, error) {
	resolvedOptions, err := options.forAccessAddress(accessAddress)
	options = resolvedOptions
	if ctx == nil || listener == nil || err != nil || options.validate() != nil {
		return preparedTransport{}, errors.New("Runtime Server transport is invalid")
	}
	if options.Mode == TransportHTTP {
		return preparedTransport{
			listener: listener, scheme: "http",
			status: func() transportTLSStatus {
				return transportTLSStatus{mode: TransportHTTP, state: "disabled"}
			},
		}, nil
	}
	if options.Mode == TransportAutomaticTLS {
		listenHost, portText, err := net.SplitHostPort(listener.Addr().String())
		if err != nil {
			return preparedTransport{}, err
		}
		port, err := strconv.Atoi(portText)
		if err != nil || port < 1 || port > 65535 {
			return preparedTransport{}, errors.New("automatic HTTPS listener port is invalid")
		}
		if parsed := net.ParseIP(listenHost); parsed != nil && parsed.IsUnspecified() {
			listenHost = ""
		}
		automatic, err := serveridentity.OpenAutomatic(
			ctx,
			serveridentity.AutomaticOptions{
				Policy:        options.Automatic,
				DataDirectory: filepath.Join(dataDirectory, "server-https"),
				ListenHost:    listenHost, TLSALPNChallengePort: port,
			},
		)
		if err != nil {
			return preparedTransport{}, err
		}
		return preparedTransport{
			listener: tls.NewListener(listener, automatic.TLSConfig()),
			scheme:   "https",
			close:    automatic.Close,
			status: func() transportTLSStatus {
				current := automatic.Status()
				return transportTLSStatus{
					mode: options.Mode, state: string(current.State),
					serverName: current.ServerName, challenge: string(current.Challenge),
					fingerprint: current.Fingerprint, issuer: current.Issuer,
					notBefore: formatTransportTime(current.NotBefore),
					notAfter:  formatTransportTime(current.NotAfter),
					lastError: current.LastError,
				}
			},
		}, nil
	}
	var (
		identity serveridentity.Identity
		manager  *serveridentity.Manager
	)
	if options.Mode == TransportTLSFiles {
		identity, err = serveridentity.OpenFiles(
			options.CertificateFile,
			options.PrivateKeyFile,
			now(),
		)
	} else {
		manager, err = serveridentity.OpenManager(
			ctx,
			filepath.Join(dataDirectory, "server-transport"),
			random,
			now,
			issuer,
			options.TLSHosts...,
		)
		if err == nil {
			identity = manager.Current()
		}
	}
	if err != nil {
		return preparedTransport{}, err
	}
	certificate, err := identity.Certificate()
	if err != nil {
		return preparedTransport{}, err
	}
	if accessAddress != "" {
		accessHost, _, splitErr := net.SplitHostPort(accessAddress)
		if splitErr != nil || certificate.Leaf == nil ||
			certificate.Leaf.VerifyHostname(accessHost) != nil {
			return preparedTransport{}, errors.New(
				"Runtime Server certificate does not cover its access address",
			)
		}
	}
	tlsListener := newTLSListener(listener, certificate)
	leaf := certificate.Leaf
	return preparedTransport{
		listener: tlsListener,
		scheme:   "https",
		status: func() transportTLSStatus {
			status := transportTLSStatus{
				mode: options.Mode, state: "ready",
				fingerprint: identity.Fingerprint(),
			}
			if leaf != nil {
				status.issuer = leaf.Issuer.String()
				status.notBefore = formatTransportTime(leaf.NotBefore)
				status.notAfter = formatTransportTime(leaf.NotAfter)
			}
			return status
		},
	}, nil
}

func (options TransportOptions) forAccessAddress(
	accessAddress string,
) (TransportOptions, error) {
	if accessAddress == "" || (options.Mode != TransportPrivateCATLS &&
		options.Mode != TransportSelfSignedTLS) || len(options.TLSHosts) != 0 {
		return options, nil
	}
	host, _, err := net.SplitHostPort(accessAddress)
	if err != nil {
		return TransportOptions{}, err
	}
	options.TLSHosts = []string{host}
	return options, nil
}

func formatTransportTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}
