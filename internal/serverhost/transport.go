package serverhost

import (
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"time"

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
}

func (options TransportOptions) Valid() bool { return options.validate() == nil }

func (options TransportOptions) validate() error {
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

func prepareTransport(
	ctx context.Context,
	listener net.Listener,
	options TransportOptions,
	dataDirectory string,
	random io.Reader,
	now time.Time,
) (preparedTransport, error) {
	if ctx == nil || listener == nil || options.validate() != nil {
		return preparedTransport{}, errors.New("Runtime Server transport is invalid")
	}
	if options.Mode == TransportHTTP {
		return preparedTransport{listener: listener, scheme: "http"}, nil
	}
	var (
		identity serveridentity.Identity
		err      error
	)
	if options.Mode == TransportTLSFiles {
		identity, err = serveridentity.OpenFiles(
			options.CertificateFile,
			options.PrivateKeyFile,
			now,
		)
	} else {
		identity, err = serveridentity.Open(
			ctx,
			filepath.Join(dataDirectory, "server-transport"),
			random,
			now,
		)
	}
	if err != nil {
		return preparedTransport{}, err
	}
	certificate, err := identity.Certificate()
	if err != nil {
		return preparedTransport{}, err
	}
	return preparedTransport{
		listener:    newTLSListener(listener, certificate),
		scheme:      "https",
		fingerprint: identity.Fingerprint(),
	}, nil
}
