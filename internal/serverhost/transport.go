package serverhost

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"io"
	"net"
	"path/filepath"
	"time"

	"github.com/vibe-agi/vibermate/internal/productruntime"
	"github.com/vibe-agi/vibermate/internal/serveridentity"
)

type TransportMode string

const (
	TransportHTTP          TransportMode = "http"
	TransportSelfSignedTLS TransportMode = "self_signed_tls"
	TransportTLSFiles      TransportMode = "tls_files"
)

type TransportOptions struct {
	Mode            TransportMode
	CertificateFile string
	PrivateKeyFile  string
	// TLSHosts seeds a new identity only; persisted addresses win on restart.
	TLSHosts []string
}

func (options TransportOptions) Valid() bool { return options.validate() == nil }

func (options TransportOptions) validate() error {
	if len(options.TLSHosts) != 0 {
		if options.Mode != TransportSelfSignedTLS {
			return errors.New("TLS hosts apply only to self_signed_tls transport")
		}
		if _, err := serveridentity.NormalizeHosts(options.TLSHosts); err != nil {
			return err
		}
	}
	switch options.Mode {
	case TransportHTTP, TransportSelfSignedTLS:
		if options.CertificateFile != "" || options.PrivateKeyFile != "" {
			return errors.New("Runtime Server transport files do not match its mode")
		}
		return nil
	case TransportTLSFiles:
		if !validAbsolutePath(options.CertificateFile) ||
			!validAbsolutePath(options.PrivateKeyFile) ||
			options.CertificateFile == options.PrivateKeyFile {
			return errors.New("Runtime Server TLS file configuration is invalid")
		}
		return nil
	default:
		return errors.New("Runtime Server transport mode is invalid")
	}
}

func validAbsolutePath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path
}

type preparedTransport struct {
	listener    net.Listener
	scheme      string
	fingerprint string
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
	dataDirectory string,
	random io.Reader,
	now func() time.Time,
	issuer serveridentity.CertificateAuthority,
) (preparedTransport, error) {
	if ctx == nil || listener == nil || options.validate() != nil {
		return preparedTransport{}, errors.New("Runtime Server transport is invalid")
	}
	if options.Mode == TransportHTTP {
		return preparedTransport{listener: listener, scheme: "http"}, nil
	}
	var (
		identity serveridentity.Identity
		manager  *serveridentity.Manager
		err      error
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
	tlsListener := newTLSListener(listener, certificate)
	return preparedTransport{
		listener:    tlsListener,
		scheme:      "https",
		fingerprint: identity.Fingerprint(),
	}, nil
}
