package localca

import (
	"bytes"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/vibe-agi/vibermate/internal/certidentity"
)

// ReadRootCertificate reads the exact public Root from an initialized Runtime
// without opening its signing key or starting ProductRuntime. It is intended
// for a server-local, out-of-band trust bootstrap.
func ReadRootCertificate(directory string, now time.Time) (RootCertificate, error) {
	if directory == "" || !filepath.IsAbs(directory) ||
		filepath.Clean(directory) != directory || now.IsZero() {
		return RootCertificate{}, ErrInvalidOptions
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return RootCertificate{}, ErrRootStateInvalid
	}
	certPath := filepath.Join(directory, rootCertFile)
	manifestPath := filepath.Join(directory, rootManifestFile)
	for _, path := range []string{certPath, manifestPath} {
		if err := requirePrivateRegularFile(path); err != nil {
			return RootCertificate{}, err
		}
	}
	encoded, err := readBoundedFile(certPath, maxCertificatePEM)
	if err != nil {
		return RootCertificate{}, err
	}
	manifest, err := readBoundedFile(manifestPath, maxCertificatePEM)
	if err != nil {
		return RootCertificate{}, err
	}
	certificate, err := parseCertificate(encoded)
	if err != nil || !certificate.IsCA || !certificate.BasicConstraintsValid ||
		certificate.MaxPathLen != 0 || !certificate.MaxPathLenZero ||
		len(certificate.DNSNames) != 0 || len(certificate.IPAddresses) != 0 ||
		now.Before(certificate.NotBefore) || !now.Before(certificate.NotAfter) ||
		certificate.KeyUsage&x509.KeyUsageCertSign == 0 ||
		certificate.CheckSignatureFrom(certificate) != nil {
		return RootCertificate{}, fmt.Errorf("%w: public Root certificate is invalid", ErrRootStateInvalid)
	}
	digest, err := certidentity.DigestRootCertificate(certificate.Raw)
	if err != nil {
		return RootCertificate{}, err
	}
	if _, err := decodeRootManifest(manifest, digest); err != nil {
		return RootCertificate{}, err
	}
	return RootCertificate{
		certificatePEM: bytes.Clone(encoded),
		path:           certPath,
	}, nil
}
