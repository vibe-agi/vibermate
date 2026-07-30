// Package localca owns one persistent installation Root and exact-host leaf
// issuance. It never installs trust into the operating system; that
// user-authorized host action is a separate boundary.
package localca

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
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	rootKeyFile       = "root-key.pem"
	rootCertFile      = "root-certificate.pem"
	rootManifestFile  = "root-manifest.json"
	manifestSchema    = "vibermate-local-root-v1"
	rootLifetime      = 10 * 365 * 24 * time.Hour
	leafLifetime      = 24 * time.Hour
	clockSkew         = 5 * time.Minute
	maxCertificatePEM = 64 << 10
)

var (
	ErrInvalidOptions   = errors.New("invalid local CA options")
	ErrRootStateInvalid = errors.New("local Root state is invalid")
	ErrAuthorityClosed  = errors.New("local CA is closed")
	ErrInvalidLeafHost  = errors.New("leaf host is invalid")
)

type Clock interface {
	Now() time.Time
}

type SystemClock struct{}

func (SystemClock) Now() time.Time {
	return time.Now().UTC()
}

type Options struct {
	Directory string
	Clock     Clock
	Random    io.Reader
}

func DefaultOptions(directory string) Options {
	return Options{
		Directory: directory,
		Clock:     SystemClock{},
		Random:    rand.Reader,
	}
}

// Root is a public-only immutable view safe for launcher trust configuration.
type Root struct {
	certificatePEM []byte
	path           string
	fingerprint    string
	notAfter       time.Time
}

func (root Root) CertificatePEM() []byte {
	return bytes.Clone(root.certificatePEM)
}

func (root Root) Path() string {
	return root.path
}

func (root Root) Fingerprint() string {
	return root.fingerprint
}

func (root Root) NotAfter() time.Time {
	return root.notAfter
}

type cachedLeaf struct {
	certificate tlsCertificate
	notAfter    time.Time
}

// tlsCertificate mirrors the owned pieces of tls.Certificate without making
// the internal cache mutable through a returned value.
type tlsCertificate struct {
	chain [][]byte
	key   *ecdsa.PrivateKey
	leaf  *x509.Certificate
}

type Authority struct {
	mu sync.Mutex

	directory string
	clock     Clock
	random    io.Reader
	rootKey   *ecdsa.PrivateKey
	rootCert  *x509.Certificate
	root      Root
	leaves    map[string]cachedLeaf
	closed    bool
}

type rootManifest struct {
	Schema      string `json:"schema"`
	Fingerprint string `json:"fingerprint"`
}

func Open(ctx context.Context, options Options) (*Authority, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: context is nil", ErrInvalidOptions)
	}
	if options.Directory == "" ||
		!filepath.IsAbs(options.Directory) ||
		filepath.Clean(options.Directory) != options.Directory ||
		options.Clock == nil ||
		options.Random == nil {
		return nil, ErrInvalidOptions
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ensurePrivateDirectory(options.Directory); err != nil {
		return nil, err
	}
	key, certificate, root, err := loadOrCreateRoot(ctx, options)
	if err != nil {
		return nil, err
	}
	return &Authority{
		directory: options.Directory,
		clock:     options.Clock,
		random:    options.Random,
		rootKey:   key,
		rootCert:  certificate,
		root:      root,
		leaves:    make(map[string]cachedLeaf),
	}, nil
}

func (authority *Authority) Root() Root {
	if authority == nil {
		return Root{}
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	root := authority.root
	root.certificatePEM = bytes.Clone(root.certificatePEM)
	return root
}

// Issue returns an independently owned certificate for one canonical DNS name
// or IP literal. Wildcards, authorities containing ports, and alternate DNS
// spellings are rejected.
func (authority *Authority) Issue(host string) (tls.Certificate, error) {
	if authority == nil {
		return tls.Certificate{}, ErrAuthorityClosed
	}
	canonical, ip, err := canonicalLeafHost(host)
	if err != nil {
		return tls.Certificate{}, err
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.closed || authority.rootKey == nil || authority.rootCert == nil {
		return tls.Certificate{}, ErrAuthorityClosed
	}
	now := authority.clock.Now().UTC()
	if cached, exists := authority.leaves[canonical]; exists &&
		now.Add(clockSkew).Before(cached.notAfter) {
		return cloneTLSCertificate(cached.certificate)
	}
	leaf, err := authority.issueLocked(canonical, ip, now)
	if err != nil {
		return tls.Certificate{}, err
	}
	authority.leaves[canonical] = cachedLeaf{
		certificate: leaf,
		notAfter:    leaf.leaf.NotAfter,
	}
	return cloneTLSCertificate(leaf)
}

func (authority *Authority) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return errors.New("local CA shutdown context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.closed {
		return nil
	}
	authority.closed = true
	authority.leaves = nil
	authority.rootKey = nil
	authority.rootCert = nil
	return nil
}

func (authority *Authority) issueLocked(
	host string,
	ip net.IP,
	now time.Time,
) (tlsCertificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), authority.random)
	if err != nil {
		return tlsCertificate{}, fmt.Errorf("generate leaf key: %w", err)
	}
	serial, err := randomSerial(authority.random)
	if err != nil {
		return tlsCertificate{}, fmt.Errorf("generate leaf serial: %w", err)
	}
	notAfter := now.Add(leafLifetime)
	if authority.rootCert.NotAfter.Before(notAfter) {
		notAfter = authority.rootCert.NotAfter
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    now.Add(-clockSkew),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip != nil {
		template.IPAddresses = []net.IP{append(net.IP(nil), ip...)}
	} else {
		template.DNSNames = []string{host}
	}
	encoded, err := x509.CreateCertificate(
		authority.random,
		template,
		authority.rootCert,
		&key.PublicKey,
		authority.rootKey,
	)
	if err != nil {
		return tlsCertificate{}, fmt.Errorf("sign exact-host leaf: %w", err)
	}
	leaf, err := x509.ParseCertificate(encoded)
	if err != nil {
		return tlsCertificate{}, fmt.Errorf("parse exact-host leaf: %w", err)
	}
	return tlsCertificate{
		chain: [][]byte{
			bytes.Clone(leaf.Raw),
			bytes.Clone(authority.rootCert.Raw),
		},
		key:  key,
		leaf: leaf,
	}, nil
}

func loadOrCreateRoot(
	ctx context.Context,
	options Options,
) (*ecdsa.PrivateKey, *x509.Certificate, Root, error) {
	keyPath := filepath.Join(options.Directory, rootKeyFile)
	certPath := filepath.Join(options.Directory, rootCertFile)
	manifestPath := filepath.Join(options.Directory, rootManifestFile)
	existence := make([]bool, 3)
	for index, path := range []string{keyPath, certPath, manifestPath} {
		info, err := os.Lstat(path)
		switch {
		case err == nil:
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return nil, nil, Root{}, fmt.Errorf(
					"%w: %q is not a regular file",
					ErrRootStateInvalid,
					filepath.Base(path),
				)
			}
			existence[index] = true
		case errors.Is(err, os.ErrNotExist):
		default:
			return nil, nil, Root{}, fmt.Errorf("inspect local Root file: %w", err)
		}
	}
	if existence[0] || existence[1] || existence[2] {
		if !existence[0] || !existence[1] || !existence[2] {
			return nil, nil, Root{}, fmt.Errorf(
				"%w: Root file set is incomplete",
				ErrRootStateInvalid,
			)
		}
		return loadRoot(options.Clock.Now().UTC(), keyPath, certPath, manifestPath)
	}
	return createRoot(ctx, options, keyPath, certPath, manifestPath)
}

func createRoot(
	ctx context.Context,
	options Options,
	keyPath, certPath, manifestPath string,
) (*ecdsa.PrivateKey, *x509.Certificate, Root, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, Root{}, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), options.Random)
	if err != nil {
		return nil, nil, Root{}, fmt.Errorf("generate local Root key: %w", err)
	}
	serial, err := randomSerial(options.Random)
	if err != nil {
		return nil, nil, Root{}, fmt.Errorf("generate local Root serial: %w", err)
	}
	now := options.Clock.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"VibeMate"},
			CommonName:   "VibeMate Local Root",
		},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              now.Add(rootLifetime),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
		KeyUsage: x509.KeyUsageCertSign |
			x509.KeyUsageCRLSign |
			x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(
		options.Random,
		template,
		template,
		&key.PublicKey,
		key,
	)
	if err != nil {
		return nil, nil, Root{}, fmt.Errorf("create local Root certificate: %w", err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, Root{}, fmt.Errorf("parse created local Root: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, Root{}, fmt.Errorf("encode local Root key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	fingerprint := certificateFingerprint(certificate)
	manifest, err := json.Marshal(rootManifest{
		Schema:      manifestSchema,
		Fingerprint: fingerprint,
	})
	if err != nil {
		return nil, nil, Root{}, fmt.Errorf("encode local Root manifest: %w", err)
	}
	manifest = append(manifest, '\n')
	if err := writeExclusive(keyPath, keyPEM, 0o600); err != nil {
		return nil, nil, Root{}, err
	}
	if err := writeExclusive(certPath, certPEM, 0o600); err != nil {
		return nil, nil, Root{}, err
	}
	if err := writeExclusive(manifestPath, manifest, 0o600); err != nil {
		return nil, nil, Root{}, err
	}
	if err := syncDirectory(options.Directory); err != nil {
		return nil, nil, Root{}, err
	}
	return key, certificate, Root{
		certificatePEM: bytes.Clone(certPEM),
		path:           certPath,
		fingerprint:    fingerprint,
		notAfter:       certificate.NotAfter,
	}, nil
}

func loadRoot(
	now time.Time,
	keyPath, certPath, manifestPath string,
) (*ecdsa.PrivateKey, *x509.Certificate, Root, error) {
	if err := requirePrivateRegularFile(keyPath); err != nil {
		return nil, nil, Root{}, err
	}
	if err := requirePrivateRegularFile(certPath); err != nil {
		return nil, nil, Root{}, err
	}
	if err := requirePrivateRegularFile(manifestPath); err != nil {
		return nil, nil, Root{}, err
	}
	keyPEM, err := readBoundedFile(keyPath, maxCertificatePEM)
	if err != nil {
		return nil, nil, Root{}, err
	}
	certPEM, err := readBoundedFile(certPath, maxCertificatePEM)
	if err != nil {
		return nil, nil, Root{}, err
	}
	manifestBytes, err := readBoundedFile(manifestPath, maxCertificatePEM)
	if err != nil {
		return nil, nil, Root{}, err
	}
	key, err := parsePrivateKey(keyPEM)
	if err != nil {
		return nil, nil, Root{}, err
	}
	certificate, err := parseCertificate(certPEM)
	if err != nil {
		return nil, nil, Root{}, err
	}
	if err := validateRoot(now, key, certificate); err != nil {
		return nil, nil, Root{}, err
	}
	var manifest rootManifest
	decoder := json.NewDecoder(bytes.NewReader(manifestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return nil, nil, Root{}, fmt.Errorf("%w: decode manifest: %v", ErrRootStateInvalid, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, nil, Root{}, fmt.Errorf(
			"%w: manifest has trailing data",
			ErrRootStateInvalid,
		)
	}
	fingerprint := certificateFingerprint(certificate)
	if manifest.Schema != manifestSchema || manifest.Fingerprint != fingerprint {
		return nil, nil, Root{}, fmt.Errorf("%w: manifest does not match certificate", ErrRootStateInvalid)
	}
	return key, certificate, Root{
		certificatePEM: bytes.Clone(certPEM),
		path:           certPath,
		fingerprint:    fingerprint,
		notAfter:       certificate.NotAfter,
	}, nil
}

func validateRoot(
	now time.Time,
	key *ecdsa.PrivateKey,
	certificate *x509.Certificate,
) error {
	if key == nil ||
		key.Curve != elliptic.P256() ||
		certificate == nil ||
		!certificate.IsCA ||
		!certificate.BasicConstraintsValid ||
		certificate.MaxPathLen != 0 ||
		!certificate.MaxPathLenZero ||
		len(certificate.DNSNames) != 0 ||
		len(certificate.IPAddresses) != 0 ||
		!now.Before(certificate.NotAfter) {
		return ErrRootStateInvalid
	}
	if err := certificate.CheckSignatureFrom(certificate); err != nil {
		return fmt.Errorf("%w: Root is not self-signed", ErrRootStateInvalid)
	}
	public, ok := certificate.PublicKey.(*ecdsa.PublicKey)
	if !ok ||
		public.X.Cmp(key.PublicKey.X) != 0 ||
		public.Y.Cmp(key.PublicKey.Y) != 0 {
		return fmt.Errorf("%w: certificate and private key differ", ErrRootStateInvalid)
	}
	return nil
}

func parsePrivateKey(encoded []byte) (*ecdsa.PrivateKey, error) {
	block, rest := pem.Decode(encoded)
	if block == nil ||
		block.Type != "PRIVATE KEY" ||
		len(bytes.TrimSpace(rest)) != 0 {
		return nil, fmt.Errorf("%w: private key PEM is invalid", ErrRootStateInvalid)
	}
	value, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: parse private key: %v", ErrRootStateInvalid, err)
	}
	key, ok := value.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%w: private key algorithm is invalid", ErrRootStateInvalid)
	}
	return key, nil
}

func parseCertificate(encoded []byte) (*x509.Certificate, error) {
	block, rest := pem.Decode(encoded)
	if block == nil ||
		block.Type != "CERTIFICATE" ||
		len(bytes.TrimSpace(rest)) != 0 {
		return nil, fmt.Errorf("%w: certificate PEM is invalid", ErrRootStateInvalid)
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: parse certificate: %v", ErrRootStateInvalid, err)
	}
	return certificate, nil
}

func certificateFingerprint(certificate *x509.Certificate) string {
	sum := sha256.Sum256(certificate.Raw)
	return hex.EncodeToString(sum[:])
}

func randomSerial(source io.Reader) (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(source, limit)
	if err != nil {
		return nil, err
	}
	if serial.Sign() == 0 {
		serial.SetInt64(1)
	}
	return serial, nil
}

func canonicalLeafHost(host string) (string, net.IP, error) {
	if host == "" ||
		strings.TrimSpace(host) != host ||
		strings.ContainsAny(host, "*:/@[]\t\r\n ") {
		return "", nil, ErrInvalidLeafHost
	}
	if address, err := netip.ParseAddr(host); err == nil {
		canonical := address.String()
		return canonical, net.ParseIP(canonical), nil
	}
	canonical := strings.ToLower(host)
	if canonical != host ||
		len(canonical) > 253 ||
		strings.HasSuffix(canonical, ".") {
		return "", nil, ErrInvalidLeafHost
	}
	for _, label := range strings.Split(canonical, ".") {
		if len(label) == 0 ||
			len(label) > 63 ||
			label[0] == '-' ||
			label[len(label)-1] == '-' {
			return "", nil, ErrInvalidLeafHost
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') &&
				(character < '0' || character > '9') &&
				character != '-' {
				return "", nil, ErrInvalidLeafHost
			}
		}
	}
	return canonical, nil, nil
}

func cloneTLSCertificate(source tlsCertificate) (tls.Certificate, error) {
	if source.key == nil || source.leaf == nil {
		return tls.Certificate{}, ErrRootStateInvalid
	}
	key := &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{
			Curve: source.key.Curve,
			X:     new(big.Int).Set(source.key.X),
			Y:     new(big.Int).Set(source.key.Y),
		},
		D: new(big.Int).Set(source.key.D),
	}
	chain := make([][]byte, len(source.chain))
	for index := range source.chain {
		chain[index] = bytes.Clone(source.chain[index])
	}
	leaf, err := x509.ParseCertificate(chain[0])
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{
		Certificate: chain,
		PrivateKey:  key,
		Leaf:        leaf,
	}, nil
}
