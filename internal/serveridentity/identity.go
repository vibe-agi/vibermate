// Package serveridentity owns the persistent TLS identity of one ViberMate
// Runtime Server. It is deliberately separate from the interception Root CA:
// trusting the Server transport never grants authority to mint upstream leaf
// certificates, and replacing one identity cannot silently replace the other.
package serveridentity

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	identitySchema         = "vibermate-server-tls-identity-v1"
	identityName           = "server-tls-identity.json"
	maxIdentitySize        = 64 << 10
	maxCertificateFileSize = 4 << 20
	maxPrivateKeyFileSize  = 1 << 20
)

var ErrInvalidIdentity = errors.New("Runtime Server TLS identity is invalid")

type document struct {
	Schema         string `json:"schema"`
	CertificatePEM string `json:"certificatePem"`
	PrivateKeyPEM  string `json:"privateKeyPem"`
}

type Identity struct {
	certificate tls.Certificate
	fingerprint string
}

func Open(ctx context.Context, dataDirectory string, random io.Reader, now time.Time) (Identity, error) {
	if ctx == nil || dataDirectory == "" || !filepath.IsAbs(dataDirectory) ||
		filepath.Clean(dataDirectory) != dataDirectory || random == nil || now.IsZero() {
		return Identity{}, ErrInvalidIdentity
	}
	if err := ctx.Err(); err != nil {
		return Identity{}, err
	}
	if err := os.MkdirAll(dataDirectory, 0o700); err != nil {
		return Identity{}, fmt.Errorf("prepare Runtime Server identity directory: %w", err)
	}
	if err := os.Chmod(dataDirectory, 0o700); err != nil {
		return Identity{}, fmt.Errorf("protect Runtime Server identity directory: %w", err)
	}
	path := filepath.Join(dataDirectory, identityName)
	payload, err := os.ReadFile(path)
	switch {
	case err == nil:
		return parseDocument(payload, now.UTC())
	case !errors.Is(err, os.ErrNotExist):
		return Identity{}, fmt.Errorf("read Runtime Server TLS identity: %w", err)
	}
	doc, identity, err := createDocument(random, now.UTC())
	if err != nil {
		return Identity{}, err
	}
	payload, err = json.Marshal(doc)
	if err != nil {
		return Identity{}, err
	}
	temporary, err := os.CreateTemp(dataDirectory, ".server-tls-identity-*")
	if err != nil {
		return Identity{}, err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return Identity{}, err
	}
	if _, err := temporary.Write(payload); err != nil {
		return Identity{}, err
	}
	if err := temporary.Sync(); err != nil {
		return Identity{}, err
	}
	if err := temporary.Close(); err != nil {
		return Identity{}, err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			stored, readErr := os.ReadFile(path)
			if readErr != nil {
				return Identity{}, readErr
			}
			return parseDocument(stored, now.UTC())
		}
		return Identity{}, err
	}
	committed = true
	return identity, nil
}

// OpenFiles loads an operator-managed certificate chain and private key in
// place. ViberMate never copies or rewrites either file, so certbot or another
// certificate manager remains the sole owner of rotation.
func OpenFiles(certificatePath, privateKeyPath string, now time.Time) (Identity, error) {
	if !validManagedPath(certificatePath) || !validManagedPath(privateKeyPath) ||
		certificatePath == privateKeyPath || now.IsZero() {
		return Identity{}, ErrInvalidIdentity
	}
	certificateInfo, err := os.Lstat(certificatePath)
	if err != nil || !certificateInfo.Mode().IsRegular() ||
		certificateInfo.Mode()&os.ModeSymlink != 0 ||
		certificateInfo.Size() <= 0 || certificateInfo.Size() > maxCertificateFileSize {
		return Identity{}, ErrInvalidIdentity
	}
	privateKeyInfo, err := os.Lstat(privateKeyPath)
	if err != nil || !privateKeyInfo.Mode().IsRegular() ||
		privateKeyInfo.Mode()&os.ModeSymlink != 0 ||
		privateKeyInfo.Mode().Perm()&0o077 != 0 ||
		privateKeyInfo.Size() <= 0 || privateKeyInfo.Size() > maxPrivateKeyFileSize {
		return Identity{}, ErrInvalidIdentity
	}
	certificatePEM, err := os.ReadFile(certificatePath)
	if err != nil {
		return Identity{}, fmt.Errorf("read Runtime Server certificate: %w", err)
	}
	privateKeyPEM, err := os.ReadFile(privateKeyPath)
	if err != nil {
		return Identity{}, fmt.Errorf("read Runtime Server private key: %w", err)
	}
	defer clear(privateKeyPEM)
	pair, err := tls.X509KeyPair(certificatePEM, privateKeyPEM)
	if err != nil {
		return Identity{}, ErrInvalidIdentity
	}
	return identityOf(pair, now.UTC())
}

func (identity Identity) Certificate() (tls.Certificate, error) {
	if !identity.Valid() {
		return tls.Certificate{}, ErrInvalidIdentity
	}
	copy := identity.certificate
	copy.Certificate = make([][]byte, len(identity.certificate.Certificate))
	for index := range identity.certificate.Certificate {
		copy.Certificate[index] = bytes.Clone(identity.certificate.Certificate[index])
	}
	return copy, nil
}

func (identity Identity) Fingerprint() string { return identity.fingerprint }

func (identity Identity) Valid() bool {
	return len(identity.certificate.Certificate) >= 1 &&
		identity.certificate.PrivateKey != nil && len(identity.fingerprint) == sha256.Size*2
}

func createDocument(source io.Reader, now time.Time) (document, Identity, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), source)
	if err != nil {
		return document{}, Identity{}, fmt.Errorf("generate Runtime Server TLS key: %w", err)
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(source, serialLimit)
	if err != nil {
		return document{}, Identity{}, err
	}
	if serial.Sign() == 0 {
		serial.SetInt64(1)
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "ViberMate Runtime Server"},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     now.Add(10 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(source, template, template, &key.PublicKey, key)
	if err != nil {
		return document{}, Identity{}, fmt.Errorf("create Runtime Server TLS certificate: %w", err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return document{}, Identity{}, err
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	doc := document{
		Schema:         identitySchema,
		CertificatePEM: string(certificatePEM),
		PrivateKeyPEM:  string(privatePEM),
	}
	identity, err := parseDocument(mustJSON(doc), now)
	return doc, identity, err
}

func parseDocument(payload []byte, now time.Time) (Identity, error) {
	if len(payload) == 0 || len(payload) > maxIdentitySize {
		return Identity{}, ErrInvalidIdentity
	}
	var doc document
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&doc); err != nil || doc.Schema != identitySchema {
		return Identity{}, ErrInvalidIdentity
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Identity{}, ErrInvalidIdentity
	}
	certificate, err := tls.X509KeyPair([]byte(doc.CertificatePEM), []byte(doc.PrivateKeyPEM))
	if err != nil || len(certificate.Certificate) != 1 {
		return Identity{}, ErrInvalidIdentity
	}
	return identityOf(certificate, now)
}

func identityOf(certificate tls.Certificate, now time.Time) (Identity, error) {
	if len(certificate.Certificate) == 0 || certificate.PrivateKey == nil || now.IsZero() {
		return Identity{}, ErrInvalidIdentity
	}
	parsed, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil || now.Before(parsed.NotBefore) || !now.Before(parsed.NotAfter) {
		return Identity{}, ErrInvalidIdentity
	}
	certificate.Leaf = parsed
	digest := sha256.Sum256(certificate.Certificate[0])
	return Identity{certificate: certificate, fingerprint: hex.EncodeToString(digest[:])}, nil
}

func validManagedPath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path
}

func mustJSON(value any) []byte {
	payload, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return payload
}
