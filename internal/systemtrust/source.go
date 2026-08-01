package systemtrust

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/pem"
	"errors"

	"github.com/vibe-agi/vibermate/internal/certidentity"
	"github.com/vibe-agi/vibermate/internal/localca"
)

type publicRoot struct {
	identity       localca.RootIdentity
	certificateDER []byte
}

func (root publicRoot) clone() publicRoot {
	root.certificateDER = bytes.Clone(root.certificateDER)
	return root
}

func (root publicRoot) valid() bool {
	if !root.identity.Valid() || len(root.certificateDER) == 0 {
		return false
	}
	digest, err := certidentity.DigestRootCertificate(root.certificateDER)
	if err != nil || digest != root.identity.Digest() {
		return false
	}
	certificate, err := x509.ParseCertificate(root.certificateDER)
	if err != nil || !certificate.IsCA || !certificate.BasicConstraintsValid {
		return false
	}
	key, ok := certificate.PublicKey.(*ecdsa.PublicKey)
	return ok && key.Curve == elliptic.P256() &&
		root.identity.Algorithm() == certidentity.RootAlgorithmECDSAP256
}

// CurrentPublicRootSource is deliberately sealed. A plan never accepts a
// caller-asserted Root identity or certificate.
type CurrentPublicRootSource interface {
	currentPublicRoot(context.Context) (publicRoot, error)
}

type localAuthorityRootSource struct {
	authority *localca.Authority
}

// NewLocalAuthorityRootSource adapts the existing local authority's public
// identity and certificate. It does not expose or read Root private material.
func NewLocalAuthorityRootSource(
	authority *localca.Authority,
) (CurrentPublicRootSource, error) {
	if authority == nil {
		return nil, ErrCurrentRootInvalid
	}
	source := &localAuthorityRootSource{authority: authority}
	if _, err := source.currentPublicRoot(context.Background()); err != nil {
		return nil, err
	}
	return source, nil
}

func (source *localAuthorityRootSource) currentPublicRoot(
	ctx context.Context,
) (publicRoot, error) {
	if source == nil || source.authority == nil || ctx == nil {
		return publicRoot{}, ErrCurrentRootInvalid
	}
	if err := ctx.Err(); err != nil {
		return publicRoot{}, context.Cause(ctx)
	}
	identity := source.authority.Identity()
	material := source.authority.Certificate().CertificatePEM()
	der, err := parseSingleCertificatePEM(material)
	if err != nil {
		return publicRoot{}, errors.Join(ErrCurrentRootInvalid, err)
	}
	root := publicRoot{identity: identity, certificateDER: der}
	if !root.valid() {
		return publicRoot{}, ErrCurrentRootInvalid
	}
	return root.clone(), nil
}

func parseSingleCertificatePEM(material []byte) ([]byte, error) {
	block, rest := pem.Decode(material)
	if block == nil || block.Type != "CERTIFICATE" ||
		len(bytes.TrimSpace(rest)) != 0 || len(block.Bytes) == 0 {
		return nil, ErrCurrentRootInvalid
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		return nil, ErrCurrentRootInvalid
	}
	return bytes.Clone(block.Bytes), nil
}
