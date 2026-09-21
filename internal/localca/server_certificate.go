package localca

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/vibe-agi/vibermate/internal/certidentity"
)

// CertificatePEM exposes only the shared public Root, never its signing key.
func (authority *Authority) CertificatePEM() []byte {
	return authority.Certificate().CertificatePEM()
}

// SignServerCertificate is the host-management signing boundary. Its caller
// owns the listener key and authorizes explicit Server addresses. This method
// always constructs a non-CA, server-auth-only leaf; it cannot sign arbitrary
// templates or return the Root key. Traffic issuance still requires its own
// projection-owned admission through Issue.
func (authority *Authority) SignServerCertificate(ctx context.Context, publicKey *ecdsa.PublicKey, hosts []string) ([]byte, error) {
	if ctx == nil || publicKey == nil || publicKey.Curve != elliptic.P256() ||
		publicKey.X == nil || publicKey.Y == nil || !publicKey.Curve.IsOnCurve(publicKey.X, publicKey.Y) {
		return nil, ErrLeafRequestInvalid
	}
	names, err := certidentity.NormalizeServerHosts(hosts)
	if err != nil {
		return nil, errors.Join(ErrLeafRequestInvalid, err)
	}
	finish, err := authority.beginWaiter()
	if err != nil {
		return nil, err
	}
	defer finish()
	if err := ctx.Err(); err != nil {
		return nil, context.Cause(ctx)
	}
	bounded, cancel := context.WithTimeoutCause(ctx, authority.generationTimeout, ErrLeafGenerationTimedOut)
	defer cancel()
	stop := context.AfterFunc(authority.ownerContext, cancel)
	defer stop()
	generator := authority.serverSigner
	if generator == nil {
		return nil, ErrAuthorityClosed
	}
	reader := generationReader{ctx: bounded, source: generator.random, mu: &generator.randomMu}
	serial, err := randomSerial(reader)
	if err != nil {
		return nil, err
	}
	now, root := generator.clock.Now().UTC(), generator.rootCert
	if now.Before(root.NotBefore) || !now.Add(clockSkew).Before(root.NotAfter) {
		return nil, fmt.Errorf("%w: Root is expired or expires too soon", ErrLeafGenerationFailed)
	}
	notBefore, notAfter := now.Add(-clockSkew), now.Add(365*24*time.Hour)
	if notBefore.Before(root.NotBefore) {
		notBefore = root.NotBefore
	}
	if notAfter.After(root.NotAfter) {
		notAfter = root.NotAfter
	}
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "ViberMate Runtime Server"},
		NotBefore: notBefore, NotAfter: notAfter, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, name := range names {
		if address := net.ParseIP(name); address != nil {
			template.IPAddresses = append(template.IPAddresses, address)
		} else {
			template.DNSNames = append(template.DNSNames, name)
		}
	}
	der, err := x509.CreateCertificate(reader, template, root, publicKey, generator.rootKey)
	if err != nil {
		return nil, fmt.Errorf("sign Runtime Server certificate: %w", err)
	}
	if err := bounded.Err(); err != nil {
		return nil, context.Cause(bounded)
	}
	return der, nil
}
