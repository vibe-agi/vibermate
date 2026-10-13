package desktoptrust

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// ClientCAStatus observes the current device only, not the remote Runtime's
// machine. A fresh platform verification is required after installation.
func ClientCAStatus(certificate *x509.Certificate) error {
	if certificate == nil {
		return errors.New("CA certificate is missing")
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return err
	}
	if runtime.GOOS == "linux" {
		// Go caches its first system-pool snapshot. Re-read native bundles so a
		// successful installation can be verified in this same CLI process.
		roots = x509.NewCertPool()
		for _, path := range []string{"/etc/ssl/certs/ca-certificates.crt", "/etc/pki/tls/certs/ca-bundle.crt", "/etc/ssl/ca-bundle.pem", "/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem"} {
			if material, e := os.ReadFile(path); e == nil {
				roots.AppendCertsFromPEM(material)
			}
		}
	}
	_, err = certificate.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	return err
}

func InstallClientCA(ctx context.Context, certificate *x509.Certificate) error {
	if ctx == nil || certificate == nil || !certificate.IsCA {
		return errors.New("a validated public CA is required")
	}
	if ClientCAStatus(certificate) == nil {
		return nil
	}
	directory, err := os.MkdirTemp("", "vibermate-client-ca-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	if err = os.Chmod(directory, 0700); err != nil {
		return err
	}
	path := filepath.Join(directory, "root.cer")
	if err = os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}), 0600); err != nil {
		return err
	}
	digest := sha256.Sum256(certificate.Raw)
	fingerprint := hex.EncodeToString(digest[:])
	installation, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err = installClientCertificate(installation, path, fingerprint); err != nil {
		return err
	}
	if err = ClientCAStatus(certificate); err != nil {
		return errors.New("certificate was added but this device did not verify it as trusted")
	}
	return nil
}

func ClientCAPlatform() string { return runtime.GOOS }
