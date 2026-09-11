package serveridentity

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"time"
)

const (
	// Read-only migration input. New installations never create a second CA.
	authorityName   = "server-tls-ca.json"
	authoritySchema = "vibermate-server-tls-ca-v1"
)

var ErrInvalidAuthority = errors.New("Runtime Root CA is invalid or does not match the certificate; restore the original identity from backup")

// CertificateAuthority is supplied by the owning ProductRuntime. The Root
// private key never leaves localca; Server identity owns only its leaf key.
type CertificateAuthority interface {
	CertificatePEM() []byte
	SignServerCertificate(context.Context, *ecdsa.PublicKey, []string) ([]byte, error)
}

type authority struct {
	issuer      CertificateAuthority
	certificate *x509.Certificate
	public      PublicAuthority
}

// PublicAuthority cannot carry signing capability or private material.
type PublicAuthority struct {
	CertificatePEM []byte
	Fingerprint    string
	NotBefore      time.Time
	NotAfter       time.Time
}

func (manager *Manager) Authority() PublicAuthority {
	public := manager.ca.public
	public.CertificatePEM = bytes.Clone(public.CertificatePEM)
	return public
}

func (manager *Manager) IssuedByAuthority(identity Identity) bool {
	return manager.ca.signs(identity)
}

func (ca *authority) signs(identity Identity) bool {
	return ca != nil && certificateSignedBy(identity, ca.certificate)
}

func certificateSignedBy(identity Identity, issuer *x509.Certificate) bool {
	if !identity.Valid() || identity.certificate.Leaf == nil || issuer == nil {
		return false
	}
	leaf := identity.certificate.Leaf
	return !leaf.IsCA && bytes.Equal(leaf.RawIssuer, issuer.RawSubject) && leaf.CheckSignatureFrom(issuer) == nil
}

func parseRootCertificate(encoded []byte, now time.Time) (*x509.Certificate, error) {
	if len(encoded) == 0 || len(encoded) > maxIdentitySize {
		return nil, ErrInvalidAuthority
	}
	block, rest := pem.Decode(encoded)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, ErrInvalidAuthority
	}
	root, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !root.IsCA || !root.BasicConstraintsValid ||
		root.KeyUsage&x509.KeyUsageCertSign == 0 || len(root.UnhandledCriticalExtensions) != 0 ||
		!bytes.Equal(root.RawIssuer, root.RawSubject) || root.CheckSignatureFrom(root) != nil ||
		now.Before(root.NotBefore) || !now.Before(root.NotAfter) {
		return nil, ErrInvalidAuthority
	}
	return root, nil
}

func newAuthority(issuer CertificateAuthority, now time.Time) (*authority, error) {
	if issuer == nil {
		return nil, ErrInvalidAuthority
	}
	encoded := issuer.CertificatePEM()
	root, err := parseRootCertificate(encoded, now)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(root.Raw)
	return &authority{issuer: issuer, certificate: root, public: PublicAuthority{
		CertificatePEM: bytes.Clone(encoded), Fingerprint: hex.EncodeToString(digest[:]),
		NotBefore: root.NotBefore, NotAfter: root.NotAfter,
	}}, nil
}

func (ca *authority) validate(now time.Time) error {
	if ca == nil || ca.issuer == nil || ca.certificate == nil ||
		now.Before(ca.certificate.NotBefore) || !now.Before(ca.certificate.NotAfter) ||
		!bytes.Equal(ca.public.CertificatePEM, ca.issuer.CertificatePEM()) {
		return ErrInvalidAuthority
	}
	return nil
}

func legacySelfSigned(identity Identity) bool {
	if !identity.Valid() || identity.certificate.Leaf == nil {
		return false
	}
	cert := identity.certificate.Leaf
	return !cert.IsCA && bytes.Equal(cert.RawIssuer, cert.RawSubject) &&
		cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature) == nil
}

// migrateAuthority preserves active/pending leaf fingerprints. Public issuer
// provenance lets an explicitly reset Runtime Root coexist with the old HTTPS
// leaf until the owner prepares trust and applies a replacement. It does not
// authorize the old Root for new issuance or for traffic.
func (manager *Manager) migrateAuthority(active, pending *storedIdentity) error {
	legacyPath := filepath.Join(manager.directory, authorityName)
	legacy, err := readStoredDocument(legacyPath, manager.now(), authoritySchema)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrInvalidAuthority
	}
	var legacyRoot *x509.Certificate
	if legacy != nil {
		legacyRoot, err = parseRootCertificate(legacy.identity.CertificatePEM(), manager.now())
		if err != nil {
			return err
		}
	}
	type migration struct {
		name   string
		stored *storedIdentity
	}
	var updates []migration
	for _, item := range []migration{{identityName, active}, {pendingIdentityName, pending}} {
		stored := item.stored
		if stored == nil {
			continue
		}
		if stored.doc.IssuerCertificatePEM != "" {
			root, err := parseRootCertificate([]byte(stored.doc.IssuerCertificatePEM), manager.now())
			if err != nil || !certificateSignedBy(stored.identity, root) {
				return ErrInvalidAuthority
			}
			continue
		}
		if legacySelfSigned(stored.identity) {
			continue
		}
		switch {
		case manager.ca.signs(stored.identity):
			stored.doc.IssuerCertificatePEM = string(manager.ca.public.CertificatePEM)
		case certificateSignedBy(stored.identity, legacyRoot):
			stored.doc.IssuerCertificatePEM = string(legacy.identity.CertificatePEM())
		default:
			return ErrInvalidAuthority
		}
		updates = append(updates, item)
	}
	retired := legacyPath + ".retired"
	if legacy != nil {
		// Keep the retired key recoverable for rollback, but never load it as a
		// signing authority. Do not overwrite a previous migration backup.
		if _, err := os.Lstat(retired); err == nil {
			return errors.New("retired HTTPS CA backup already exists; preserve it before migrating")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for _, item := range updates {
		if err := manager.backupBeforeMigration(item.name); err != nil {
			return err
		}
		if err := manager.persist(item.name, item.stored.doc); err != nil {
			return err
		}
	}
	if legacy != nil {
		if err := os.Rename(legacyPath, retired); err != nil {
			return err
		}
	}
	return nil
}

// Back up the original format exactly before adding issuer provenance. The old
// binary rejects unknown JSON fields, so restoring only its CA is insufficient
// for rollback. A complete, synced backup is published without overwriting one.
func (manager *Manager) backupBeforeMigration(name string) error {
	path := filepath.Join(manager.directory, name)
	payload, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	defer clear(payload)
	backup := path + ".before-unified-root"
	if info, err := os.Lstat(backup); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > maxIdentitySize {
			return ErrInvalidIdentity
		}
		prior, err := os.ReadFile(backup)
		defer clear(prior)
		if err != nil {
			return err
		}
		if !bytes.Equal(prior, payload) {
			return errors.New("certificate migration backup differs; preserve it before retrying")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.CreateTemp(manager.directory, ".unified-root-backup-*")
	if err != nil {
		return err
	}
	defer file.Close()
	defer os.Remove(file.Name())
	if err := file.Chmod(0600); err != nil {
		return err
	}
	if _, err := file.Write(payload); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Link(file.Name(), backup)
}
