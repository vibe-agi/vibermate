package runlauncher

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"net/http"
	"path/filepath"
	"time"

	"github.com/vibe-agi/vibermate/internal/servercontrol"
	"github.com/vibe-agi/vibermate/internal/servertransport"
)

// RemoteRootCertificate fetches only public CA material through the existing
// authenticated, pinned login transport. It creates no Capture or OS trust.
func RemoteRootCertificate(ctx context.Context, config RemoteConfig, timeout time.Duration) (servercontrol.RuntimeRootCA, error) {
	var material servercontrol.RuntimeRootCA
	if ctx == nil || !config.Target.Valid() || config.Clock == nil || timeout <= 0 || !filepath.IsAbs(config.StateDirectory) || filepath.Clean(config.StateDirectory) != config.StateDirectory {
		return material, errors.New("CA connection is incomplete")
	}
	store, err := openRemoteLoginStore(config.StateDirectory)
	if err != nil {
		return material, err
	}
	login, err := store.Load(config.Target, config.Clock.Now().UTC())
	if err != nil {
		return material, errors.Join(ErrRemoteLoginRequired, err)
	}
	random := config.Random
	if random == nil {
		random = rand.Reader
	}
	machineID, err := remoteLoginMachine(ctx, config.StateDirectory, random, config.Clock.Now())
	if err != nil {
		return material, err
	}
	transport, err := servertransport.Open(servertransport.Options{Target: config.Target, TrustDirectory: filepath.Join(config.StateDirectory, "trust"), Clock: config.Clock, Timeout: timeout})
	if err != nil {
		return material, err
	}
	client, err := newRemoteControlClient(config.Target.Origin(), login.SessionToken().Value(), transport, transport.Close)
	if err != nil {
		transport.Close()
		return material, err
	}
	defer client.close()
	if _, err := authenticateRemoteLogin(ctx, client, login, machineID); err != nil {
		return material, err
	}
	if err := client.jsonRequest(ctx, http.MethodGet, servercontrol.RuntimeRootCAPath, client.credential, "", nil, http.StatusOK, &material); err != nil {
		return material, err
	}
	if material.Schema != servercontrol.RuntimeRootCASchema {
		return material, errors.New("CA response is invalid")
	}
	if _, err := ValidatePublicCA([]byte(material.CertificatePEM), material.Fingerprint, config.Clock.Now().UTC()); err != nil {
		return material, err
	}
	return material, nil
}

func ValidatePublicCA(encoded []byte, fingerprint string, now time.Time) (*x509.Certificate, error) {
	if len(encoded) == 0 || len(encoded) > 64<<10 || !bytes.HasPrefix(bytes.TrimSpace(encoded), []byte("-----BEGIN CERTIFICATE-----")) {
		return nil, errors.New("CA certificate exceeds its limit")
	}
	block, rest := pem.Decode(encoded)
	if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) > 0 || len(bytes.TrimSpace(rest)) > 0 {
		return nil, errors.New("expected one public CA certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	digest := sha256.Sum256(block.Bytes)
	if err != nil || !cert.IsCA || !cert.BasicConstraintsValid || cert.KeyUsage&x509.KeyUsageCertSign == 0 || len(cert.UnhandledCriticalExtensions) > 0 ||
		!bytes.Equal(cert.RawIssuer, cert.RawSubject) || cert.CheckSignatureFrom(cert) != nil || now.Before(cert.NotBefore) || !now.Before(cert.NotAfter) ||
		hex.EncodeToString(digest[:]) != fingerprint {
		return nil, errors.New("CA identity, validity or fingerprint is invalid")
	}
	return cert, nil
}
