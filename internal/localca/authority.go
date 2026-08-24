// Package localca owns one persistent installation Root and revision-authorized
// leaf issuance. It never installs trust into an operating system; that
// user-authorized Host action is a separate boundary.
package localca

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"golang.org/x/sync/singleflight"

	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/certidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/originidentity"
)

const (
	rootKeyFile                  = "root-key.pem"
	rootCertFile                 = "root-certificate.pem"
	rootManifestFile             = "root-manifest.json"
	manifestSchemaV1             = "vibermate-local-root-v1"
	manifestSchemaV2             = "vibermate-local-root-v2"
	rootLifetime                 = 10 * 365 * 24 * time.Hour
	leafLifetime                 = 24 * time.Hour
	clockSkew                    = 5 * time.Minute
	maxCertificatePEM            = 64 << 10
	DefaultLeafCacheCapacity     = 256
	DefaultLeafGenerationTimeout = 5 * time.Second
)

var (
	ErrInvalidOptions         = errors.New("invalid local CA options")
	ErrRootStateInvalid       = errors.New("local Root state is invalid")
	ErrAuthorityClosed        = errors.New("local CA is closed")
	ErrLeafRequestInvalid     = errors.New("leaf issuance request is invalid")
	ErrLeafGenerationFailed   = errors.New("leaf generation failed")
	ErrLeafGenerationTimedOut = errors.New("leaf generation timed out")
)

type RootRevision = certidentity.RootRevision
type RootDigest = certidentity.RootDigest
type RootAlgorithm = certidentity.RootAlgorithm
type LeafKeyAlgorithm = certidentity.LeafKeyAlgorithm

const (
	RootAlgorithmECDSAP256    = certidentity.RootAlgorithmECDSAP256
	LeafKeyAlgorithmECDSAP256 = certidentity.LeafKeyAlgorithmECDSAP256
)

type Clock interface {
	Now() time.Time
}

type SystemClock struct{}

func (SystemClock) Now() time.Time {
	return time.Now().UTC()
}

type Options struct {
	OwnerContext      context.Context
	Directory         string
	Clock             Clock
	Random            io.Reader
	LeafCacheCapacity int
	GenerationTimeout time.Duration
}

func DefaultOptions(
	directory string,
	ownerContext context.Context,
) Options {
	return Options{
		OwnerContext:      ownerContext,
		Directory:         directory,
		Clock:             SystemClock{},
		Random:            rand.Reader,
		LeafCacheCapacity: DefaultLeafCacheCapacity,
		GenerationTimeout: DefaultLeafGenerationTimeout,
	}
}

// RootIdentity is an immutable public identity. Certificate paths and private
// material are deliberately absent.
type RootIdentity struct {
	digest    RootDigest
	revision  RootRevision
	notBefore time.Time
	notAfter  time.Time
	algorithm RootAlgorithm
}

func (identity RootIdentity) Digest() RootDigest {
	return identity.digest
}

func (identity RootIdentity) Fingerprint() string {
	return identity.digest.Fingerprint()
}

func (identity RootIdentity) Revision() RootRevision {
	return identity.revision
}

func (identity RootIdentity) NotBefore() time.Time {
	return identity.notBefore
}

func (identity RootIdentity) NotAfter() time.Time {
	return identity.notAfter
}

func (identity RootIdentity) Algorithm() RootAlgorithm {
	return identity.algorithm
}

func (identity RootIdentity) Valid() bool {
	return identity.digest.Valid() && identity.revision.Valid() &&
		!identity.notBefore.IsZero() && identity.notAfter.After(identity.notBefore) &&
		identity.algorithm.Valid()
}

// RootCertificate is public delivery material. Its filesystem location is not
// part of Root identity or signing authorization.
type RootCertificate struct {
	certificatePEM []byte
	path           string
}

func (certificate RootCertificate) CertificatePEM() []byte {
	return bytes.Clone(certificate.certificatePEM)
}

func (certificate RootCertificate) Path() string {
	return certificate.path
}

func (certificate RootCertificate) Valid() bool {
	return len(certificate.certificatePEM) != 0 && certificate.path != ""
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

type leafCacheKey struct {
	rootRevision        RootRevision
	environmentID       environment.EnvironmentID
	environmentRevision environment.Revision
	endpointID          environment.ClientEndpointID
	endpointRevision    environment.Revision
	clientOrigin        originidentity.ClientOrigin
	sanKind             certidentity.SANKind
	sanValue            string
	algorithm           LeafKeyAlgorithm
}

func leafKey(request captureassignment.LeafIssuanceRequest) leafCacheKey {
	return leafCacheKey{
		rootRevision:        request.RootRevision(),
		environmentID:       request.EnvironmentID(),
		environmentRevision: request.EnvironmentRevision(),
		endpointID:          request.ClientEndpointID(),
		endpointRevision:    request.ClientEndpointRevision(),
		clientOrigin:        request.ClientOrigin(),
		sanKind:             request.SAN().Kind(),
		sanValue:            request.SAN().Value(),
		algorithm:           request.Algorithm(),
	}
}

func (key leafCacheKey) flightKey() string {
	return fmt.Sprintf(
		"%d\x00%s\x00%d\x00%s\x00%d\x00%s\x00%s\x00%s\x00%s",
		key.rootRevision,
		key.environmentID.String(),
		key.environmentRevision,
		key.endpointID.String(),
		key.endpointRevision,
		key.clientOrigin.String(),
		key.sanKind,
		key.sanValue,
		key.algorithm,
	)
}

type leafGenerator interface {
	Generate(context.Context, captureassignment.LeafIssuanceRequest) (tlsCertificate, error)
}

type cryptoLeafGenerator struct {
	clock    Clock
	random   io.Reader
	randomMu sync.Mutex
	rootKey  *ecdsa.PrivateKey
	rootCert *x509.Certificate
}

func (generator *cryptoLeafGenerator) Generate(
	ctx context.Context,
	request captureassignment.LeafIssuanceRequest,
) (tlsCertificate, error) {
	if ctx == nil || generator == nil || generator.clock == nil ||
		generator.random == nil || generator.rootKey == nil ||
		generator.rootCert == nil {
		return tlsCertificate{}, ErrLeafGenerationFailed
	}
	if err := ctx.Err(); err != nil {
		return tlsCertificate{}, context.Cause(ctx)
	}
	reader := generationReader{
		ctx:    ctx,
		source: generator.random,
		mu:     &generator.randomMu,
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), reader)
	if err != nil {
		return tlsCertificate{}, fmt.Errorf("generate leaf key: %w", err)
	}
	serial, err := randomSerial(reader)
	if err != nil {
		return tlsCertificate{}, fmt.Errorf("generate leaf serial: %w", err)
	}
	now := generator.clock.Now().UTC()
	if now.Before(generator.rootCert.NotBefore) ||
		!now.Before(generator.rootCert.NotAfter) {
		return tlsCertificate{}, fmt.Errorf(
			"%w: Root certificate is not currently valid",
			ErrLeafGenerationFailed,
		)
	}
	notAfter := now.Add(leafLifetime)
	if generator.rootCert.NotAfter.Before(notAfter) {
		notAfter = generator.rootCert.NotAfter
	}
	if !now.Add(clockSkew).Before(notAfter) {
		return tlsCertificate{}, fmt.Errorf(
			"%w: Root expires too soon",
			ErrLeafGenerationFailed,
		)
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: request.SAN().Value()},
		NotBefore:    now.Add(-clockSkew),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{request.SAN().Value()},
	}
	encoded, err := x509.CreateCertificate(
		reader,
		template,
		generator.rootCert,
		&key.PublicKey,
		generator.rootKey,
	)
	if err != nil {
		return tlsCertificate{}, fmt.Errorf("sign exact-host leaf: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return tlsCertificate{}, context.Cause(ctx)
	}
	leaf, err := x509.ParseCertificate(encoded)
	if err != nil {
		return tlsCertificate{}, fmt.Errorf("parse exact-host leaf: %w", err)
	}
	return tlsCertificate{
		chain: [][]byte{
			bytes.Clone(leaf.Raw),
			bytes.Clone(generator.rootCert.Raw),
		},
		key:  key,
		leaf: leaf,
	}, nil
}

type generationReader struct {
	ctx    context.Context
	source io.Reader
	mu     *sync.Mutex
}

func (reader generationReader) Read(destination []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, context.Cause(reader.ctx)
	}
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if err := reader.ctx.Err(); err != nil {
		return 0, context.Cause(reader.ctx)
	}
	count, err := reader.source.Read(destination)
	if err == nil && reader.ctx.Err() != nil {
		return 0, context.Cause(reader.ctx)
	}
	return count, err
}

type Authority struct {
	stateMu sync.Mutex
	cacheMu sync.Mutex

	identity            RootIdentity
	certificate         RootCertificate
	clock               Clock
	generator           leafGenerator
	cache               *lru.Cache[leafCacheKey, cachedLeaf]
	flights             singleflight.Group
	ownerContext        context.Context
	cancelOwner         context.CancelCauseFunc
	generationTimeout   time.Duration
	closed              bool
	finalizing          bool
	drained             bool
	activeWaiters       int
	activeFlightWaiters int
	activeGenerations   int
	changed             chan struct{}
}

type rootManifestV1 struct {
	Schema      string `json:"schema"`
	Fingerprint string `json:"fingerprint"`
}

type rootManifestV2 struct {
	Schema            string       `json:"schema"`
	Revision          RootRevision `json:"revision"`
	CertificateSHA256 string       `json:"certificateSha256"`
}

func Open(ctx context.Context, options Options) (*Authority, error) {
	return openWithFileOperations(ctx, options, systemAtomicFileOperations{})
}

func openWithFileOperations(
	ctx context.Context,
	options Options,
	operations atomicFileOperations,
) (*Authority, error) {
	if ctx == nil || options.OwnerContext == nil ||
		options.Directory == "" ||
		!filepath.IsAbs(options.Directory) ||
		filepath.Clean(options.Directory) != options.Directory ||
		options.Clock == nil || options.Random == nil ||
		options.LeafCacheCapacity <= 0 || options.GenerationTimeout <= 0 ||
		operations == nil {
		return nil, ErrInvalidOptions
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := options.OwnerContext.Err(); err != nil {
		return nil, context.Cause(options.OwnerContext)
	}
	if err := ensurePrivateDirectory(options.Directory); err != nil {
		return nil, err
	}
	key, certificate, identity, delivery, err := loadOrCreateRoot(
		ctx,
		options,
		operations,
	)
	if err != nil {
		return nil, err
	}
	cache, err := lru.New[leafCacheKey, cachedLeaf](options.LeafCacheCapacity)
	if err != nil {
		return nil, fmt.Errorf("construct leaf cache: %w", err)
	}
	ownerContext, cancelOwner := context.WithCancelCause(options.OwnerContext)
	return &Authority{
		identity:    identity,
		certificate: delivery,
		clock:       options.Clock,
		generator: &cryptoLeafGenerator{
			clock:    options.Clock,
			random:   options.Random,
			rootKey:  key,
			rootCert: certificate,
		},
		cache:             cache,
		ownerContext:      ownerContext,
		cancelOwner:       cancelOwner,
		generationTimeout: options.GenerationTimeout,
		changed:           make(chan struct{}),
	}, nil
}

func (authority *Authority) Identity() RootIdentity {
	if authority == nil {
		return RootIdentity{}
	}
	return authority.identity
}

func (authority *Authority) Certificate() RootCertificate {
	if authority == nil {
		return RootCertificate{}
	}
	certificate := authority.certificate
	certificate.certificatePEM = bytes.Clone(certificate.certificatePEM)
	return certificate
}

// Issue consumes one projection-owned admission and returns an independently
// owned certificate. A request value by itself is never accepted.
func (authority *Authority) Issue(
	ctx context.Context,
	admission captureassignment.LeafIssuanceAdmission,
) (tls.Certificate, error) {
	if ctx == nil {
		return tls.Certificate{}, errors.New("leaf issuance context is nil")
	}
	finishWaiter, err := authority.beginWaiter()
	if err != nil {
		return tls.Certificate{}, err
	}
	defer finishWaiter()
	if err := ctx.Err(); err != nil {
		return tls.Certificate{}, context.Cause(ctx)
	}
	request, err := admission.ClaimForIssuance()
	if err != nil {
		return tls.Certificate{}, errors.Join(ErrLeafRequestInvalid, err)
	}
	if err := authority.validateRequest(request); err != nil {
		return tls.Certificate{}, err
	}
	key := leafKey(request)
	if cached, ok := authority.cached(key); ok {
		return cloneTLSCertificate(cached.certificate)
	}
	result := authority.flights.DoChan(key.flightKey(), func() (value any, resultErr error) {
		if cached, ok := authority.cached(key); ok {
			return cached.certificate, nil
		}
		generationContext, generator, finishGeneration, beginErr :=
			authority.beginGeneration()
		if beginErr != nil {
			return nil, beginErr
		}
		defer finishGeneration()
		generated, generateErr := generateLeafSafely(
			generationContext,
			generator,
			request,
		)
		if generateErr != nil {
			return nil, generateErr
		}
		if err := generationContext.Err(); err != nil {
			return nil, context.Cause(generationContext)
		}
		authority.cacheGenerated(key, generated, admission)
		return generated, nil
	})
	finishFlightWaiter := authority.beginFlightWaiter()
	defer finishFlightWaiter()
	select {
	case completed := <-result:
		if completed.Err != nil {
			return tls.Certificate{}, completed.Err
		}
		generated, ok := completed.Val.(tlsCertificate)
		if !ok {
			return tls.Certificate{}, ErrLeafGenerationFailed
		}
		return cloneTLSCertificate(generated)
	case <-ctx.Done():
		return tls.Certificate{}, context.Cause(ctx)
	}
}

func (authority *Authority) beginFlightWaiter() func() {
	authority.stateMu.Lock()
	authority.activeFlightWaiters++
	authority.notifyStateChangedLocked()
	authority.stateMu.Unlock()
	return func() {
		authority.stateMu.Lock()
		authority.activeFlightWaiters--
		authority.notifyStateChangedLocked()
		authority.stateMu.Unlock()
	}
}

func (authority *Authority) validateRequest(
	request captureassignment.LeafIssuanceRequest,
) error {
	if authority == nil || request.RootRevision() != authority.identity.revision ||
		request.SAN().Kind() != certidentity.SANKindDNS ||
		request.SAN().Value() != request.ClientOrigin().Host() ||
		request.Algorithm() != LeafKeyAlgorithmECDSAP256 {
		return ErrLeafRequestInvalid
	}
	return nil
}

func (authority *Authority) cached(key leafCacheKey) (cachedLeaf, bool) {
	if authority == nil {
		return cachedLeaf{}, false
	}
	authority.cacheMu.Lock()
	defer authority.cacheMu.Unlock()
	if authority.cache == nil {
		return cachedLeaf{}, false
	}
	cached, ok := authority.cache.Get(key)
	if !ok || cached.certificate.leaf == nil ||
		cached.notAfter.After(authority.identity.notAfter) ||
		!authority.clock.Now().UTC().Add(clockSkew).Before(cached.notAfter) {
		if ok {
			authority.cache.Remove(key)
		}
		return cachedLeaf{}, false
	}
	return cached, true
}

func (authority *Authority) cacheGenerated(
	key leafCacheKey,
	generated tlsCertificate,
	admission captureassignment.LeafIssuanceAdmission,
) {
	authority.cacheMu.Lock()
	defer authority.cacheMu.Unlock()
	if authority.cache == nil || authority.isClosed() ||
		!admission.Cacheable() {
		return
	}
	authority.cache.Add(key, cachedLeaf{
		certificate: generated,
		notAfter:    generated.leaf.NotAfter,
	})
}

func (authority *Authority) beginWaiter() (func(), error) {
	if authority == nil {
		return nil, ErrAuthorityClosed
	}
	authority.stateMu.Lock()
	defer authority.stateMu.Unlock()
	if authority.closed || authority.drained {
		return nil, ErrAuthorityClosed
	}
	if cause := context.Cause(authority.ownerContext); cause != nil {
		authority.closed = true
		authority.notifyStateChangedLocked()
		return nil, errors.Join(ErrAuthorityClosed, cause)
	}
	authority.activeWaiters++
	authority.notifyStateChangedLocked()
	return func() {
		authority.stateMu.Lock()
		authority.activeWaiters--
		authority.notifyStateChangedLocked()
		authority.stateMu.Unlock()
	}, nil
}

func (authority *Authority) beginGeneration() (
	context.Context,
	leafGenerator,
	func(),
	error,
) {
	authority.stateMu.Lock()
	if authority.closed || authority.drained || authority.generator == nil {
		authority.stateMu.Unlock()
		return nil, nil, nil, ErrAuthorityClosed
	}
	authority.activeGenerations++
	authority.notifyStateChangedLocked()
	generator := authority.generator
	ownerContext := authority.ownerContext
	timeout := authority.generationTimeout
	authority.stateMu.Unlock()
	generationContext, cancel := context.WithTimeoutCause(
		ownerContext,
		timeout,
		ErrLeafGenerationTimedOut,
	)
	return generationContext, generator, func() {
		cancel()
		authority.stateMu.Lock()
		authority.activeGenerations--
		authority.notifyStateChangedLocked()
		authority.stateMu.Unlock()
	}, nil
}

func (authority *Authority) isClosed() bool {
	authority.stateMu.Lock()
	defer authority.stateMu.Unlock()
	return authority.closed || authority.drained
}

func (authority *Authority) notifyStateChangedLocked() {
	close(authority.changed)
	authority.changed = make(chan struct{})
}

func generateLeafSafely(
	ctx context.Context,
	generator leafGenerator,
	request captureassignment.LeafIssuanceRequest,
) (certificate tlsCertificate, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			certificate = tlsCertificate{}
			err = fmt.Errorf("%w: signer panic: %v", ErrLeafGenerationFailed, recovered)
		}
	}()
	certificate, err = generator.Generate(ctx, request)
	if err != nil {
		return tlsCertificate{}, errors.Join(ErrLeafGenerationFailed, err)
	}
	return certificate, nil
}

// Shutdown closes issuance admission, cancels owned generation, and drains all
// waiters and workers within the supplied deadline. A timed-out shutdown stays
// closed and can be retried.
func (authority *Authority) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return errors.New("local CA shutdown context is nil")
	}
	if authority == nil {
		return nil
	}
	for {
		authority.stateMu.Lock()
		if authority.drained {
			authority.stateMu.Unlock()
			return nil
		}
		if !authority.closed {
			authority.closed = true
			authority.cancelOwner(ErrAuthorityClosed)
			authority.notifyStateChangedLocked()
		}
		if authority.activeWaiters == 0 &&
			authority.activeGenerations == 0 && !authority.finalizing {
			authority.finalizing = true
			authority.stateMu.Unlock()

			authority.cacheMu.Lock()
			if authority.cache != nil {
				authority.cache.Purge()
			}
			authority.cacheMu.Unlock()

			authority.stateMu.Lock()
			authority.generator = nil
			authority.drained = true
			authority.finalizing = false
			authority.notifyStateChangedLocked()
			authority.stateMu.Unlock()
			return nil
		}
		changed := authority.changed
		authority.stateMu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
}

func loadOrCreateRoot(
	ctx context.Context,
	options Options,
	operations atomicFileOperations,
) (*ecdsa.PrivateKey, *x509.Certificate, RootIdentity, RootCertificate, error) {
	keyPath := filepath.Join(options.Directory, rootKeyFile)
	certPath := filepath.Join(options.Directory, rootCertFile)
	manifestPath := filepath.Join(options.Directory, rootManifestFile)
	existence := make([]bool, 3)
	for index, path := range []string{keyPath, certPath, manifestPath} {
		info, err := os.Lstat(path)
		switch {
		case err == nil:
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return nil, nil, RootIdentity{}, RootCertificate{}, fmt.Errorf(
					"%w: %q is not a regular file",
					ErrRootStateInvalid,
					filepath.Base(path),
				)
			}
			existence[index] = true
		case errors.Is(err, os.ErrNotExist):
		default:
			return nil, nil, RootIdentity{}, RootCertificate{}, fmt.Errorf(
				"inspect local Root file: %w",
				err,
			)
		}
	}
	if existence[0] || existence[1] || existence[2] {
		if !existence[0] || !existence[1] || !existence[2] {
			return nil, nil, RootIdentity{}, RootCertificate{}, fmt.Errorf(
				"%w: Root file set is incomplete",
				ErrRootStateInvalid,
			)
		}
		return loadRoot(
			options.Clock.Now().UTC(),
			keyPath,
			certPath,
			manifestPath,
			operations,
		)
	}
	return createRoot(ctx, options, keyPath, certPath, manifestPath)
}

func createRoot(
	ctx context.Context,
	options Options,
	keyPath, certPath, manifestPath string,
) (*ecdsa.PrivateKey, *x509.Certificate, RootIdentity, RootCertificate, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, RootIdentity{}, RootCertificate{}, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), options.Random)
	if err != nil {
		return nil, nil, RootIdentity{}, RootCertificate{}, fmt.Errorf(
			"generate local Root key: %w",
			err,
		)
	}
	serial, err := randomSerial(options.Random)
	if err != nil {
		return nil, nil, RootIdentity{}, RootCertificate{}, fmt.Errorf(
			"generate local Root serial: %w",
			err,
		)
	}
	now := options.Clock.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"ViberMate"},
			CommonName:   "ViberMate Local Root",
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
		return nil, nil, RootIdentity{}, RootCertificate{}, fmt.Errorf(
			"create local Root certificate: %w",
			err,
		)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, RootIdentity{}, RootCertificate{}, fmt.Errorf(
			"parse created local Root: %w",
			err,
		)
	}
	digest, err := certidentity.DigestRootCertificate(certificate.Raw)
	if err != nil {
		return nil, nil, RootIdentity{}, RootCertificate{}, err
	}
	identity := rootIdentity(certificate, digest, certidentity.InitialRootRevision)
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, RootIdentity{}, RootCertificate{}, fmt.Errorf(
			"encode local Root key: %w",
			err,
		)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	manifest, err := encodeManifestV2(identity)
	if err != nil {
		return nil, nil, RootIdentity{}, RootCertificate{}, err
	}
	if err := writeExclusive(keyPath, keyPEM, 0o600); err != nil {
		return nil, nil, RootIdentity{}, RootCertificate{}, err
	}
	if err := writeExclusive(certPath, certPEM, 0o600); err != nil {
		return nil, nil, RootIdentity{}, RootCertificate{}, err
	}
	if err := writeExclusive(manifestPath, manifest, 0o600); err != nil {
		return nil, nil, RootIdentity{}, RootCertificate{}, err
	}
	if err := syncDirectory(options.Directory); err != nil {
		return nil, nil, RootIdentity{}, RootCertificate{}, err
	}
	return key, certificate, identity, RootCertificate{
		certificatePEM: bytes.Clone(certPEM),
		path:           certPath,
	}, nil
}

func loadRoot(
	now time.Time,
	keyPath, certPath, manifestPath string,
	operations atomicFileOperations,
) (*ecdsa.PrivateKey, *x509.Certificate, RootIdentity, RootCertificate, error) {
	for _, path := range []string{keyPath, certPath, manifestPath} {
		if err := requirePrivateRegularFile(path); err != nil {
			return nil, nil, RootIdentity{}, RootCertificate{}, err
		}
	}
	keyPEM, err := readBoundedFile(keyPath, maxCertificatePEM)
	if err != nil {
		return nil, nil, RootIdentity{}, RootCertificate{}, err
	}
	certPEM, err := readBoundedFile(certPath, maxCertificatePEM)
	if err != nil {
		return nil, nil, RootIdentity{}, RootCertificate{}, err
	}
	manifestBytes, err := readBoundedFile(manifestPath, maxCertificatePEM)
	if err != nil {
		return nil, nil, RootIdentity{}, RootCertificate{}, err
	}
	key, err := parsePrivateKey(keyPEM)
	if err != nil {
		return nil, nil, RootIdentity{}, RootCertificate{}, err
	}
	certificate, err := parseCertificate(certPEM)
	if err != nil {
		return nil, nil, RootIdentity{}, RootCertificate{}, err
	}
	if err := validateRoot(now, key, certificate); err != nil {
		return nil, nil, RootIdentity{}, RootCertificate{}, err
	}
	digest, err := certidentity.DigestRootCertificate(certificate.Raw)
	if err != nil {
		return nil, nil, RootIdentity{}, RootCertificate{}, err
	}
	revision, migrate, err := decodeRootManifest(manifestBytes, digest)
	if err != nil {
		return nil, nil, RootIdentity{}, RootCertificate{}, err
	}
	identity := rootIdentity(certificate, digest, revision)
	if migrate {
		manifest, encodeErr := encodeManifestV2(identity)
		if encodeErr != nil {
			return nil, nil, RootIdentity{}, RootCertificate{}, encodeErr
		}
		if err := replacePrivateAtomic(operations, manifestPath, manifest); err != nil {
			return nil, nil, RootIdentity{}, RootCertificate{}, fmt.Errorf(
				"migrate local Root manifest: %w",
				err,
			)
		}
	}
	return key, certificate, identity, RootCertificate{
		certificatePEM: bytes.Clone(certPEM),
		path:           certPath,
	}, nil
}

func rootIdentity(
	certificate *x509.Certificate,
	digest RootDigest,
	revision RootRevision,
) RootIdentity {
	return RootIdentity{
		digest:    digest,
		revision:  revision,
		notBefore: certificate.NotBefore.UTC(),
		notAfter:  certificate.NotAfter.UTC(),
		algorithm: RootAlgorithmECDSAP256,
	}
}

func encodeManifestV2(identity RootIdentity) ([]byte, error) {
	if !identity.Valid() {
		return nil, ErrRootStateInvalid
	}
	encoded, err := json.Marshal(rootManifestV2{
		Schema:            manifestSchemaV2,
		Revision:          identity.revision,
		CertificateSHA256: identity.digest.String(),
	})
	if err != nil {
		return nil, fmt.Errorf("encode local Root manifest: %w", err)
	}
	return append(encoded, '\n'), nil
}

func decodeRootManifest(
	encoded []byte,
	digest RootDigest,
) (RootRevision, bool, error) {
	var envelope struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		return 0, false, fmt.Errorf(
			"%w: decode manifest schema: %v",
			ErrRootStateInvalid,
			err,
		)
	}
	switch envelope.Schema {
	case manifestSchemaV1:
		var manifest rootManifestV1
		if err := decodeStrictJSON(encoded, &manifest); err != nil ||
			manifest.Fingerprint != digest.String() {
			return 0, false, fmt.Errorf(
				"%w: v1 manifest does not match certificate",
				ErrRootStateInvalid,
			)
		}
		return certidentity.InitialRootRevision, true, nil
	case manifestSchemaV2:
		var manifest rootManifestV2
		if err := decodeStrictJSON(encoded, &manifest); err != nil ||
			!manifest.Revision.Valid() ||
			manifest.CertificateSHA256 != digest.String() {
			return 0, false, fmt.Errorf(
				"%w: v2 manifest does not match certificate",
				ErrRootStateInvalid,
			)
		}
		return manifest.Revision, false, nil
	default:
		return 0, false, fmt.Errorf(
			"%w: unsupported manifest schema",
			ErrRootStateInvalid,
		)
	}
}

func decodeStrictJSON(encoded []byte, destination any) error {
	if err := rejectDuplicateJSONFields(encoded); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("manifest has trailing data")
	}
	return nil
}

func rejectDuplicateJSONFields(encoded []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return errors.New("manifest must be one JSON object")
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		field, err := decoder.Token()
		if err != nil {
			return err
		}
		name, ok := field.(string)
		if !ok {
			return errors.New("manifest field name is invalid")
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("manifest field %q is duplicated", name)
		}
		seen[name] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return errors.New("manifest JSON object is incomplete")
	}
	return nil
}

func validateRoot(
	now time.Time,
	key *ecdsa.PrivateKey,
	certificate *x509.Certificate,
) error {
	if key == nil || key.Curve != elliptic.P256() || certificate == nil ||
		!certificate.IsCA || !certificate.BasicConstraintsValid ||
		certificate.MaxPathLen != 0 || !certificate.MaxPathLenZero ||
		len(certificate.DNSNames) != 0 || len(certificate.IPAddresses) != 0 ||
		now.Before(certificate.NotBefore) || !now.Before(certificate.NotAfter) ||
		certificate.KeyUsage&x509.KeyUsageCertSign == 0 {
		return ErrRootStateInvalid
	}
	if err := certificate.CheckSignatureFrom(certificate); err != nil {
		return fmt.Errorf("%w: Root is not self-signed", ErrRootStateInvalid)
	}
	public, ok := certificate.PublicKey.(*ecdsa.PublicKey)
	if !ok || public.Curve != elliptic.P256() ||
		public.X.Cmp(key.PublicKey.X) != 0 ||
		public.Y.Cmp(key.PublicKey.Y) != 0 {
		return fmt.Errorf(
			"%w: certificate and private key differ",
			ErrRootStateInvalid,
		)
	}
	return nil
}

func parsePrivateKey(encoded []byte) (*ecdsa.PrivateKey, error) {
	block, rest := pem.Decode(encoded)
	if block == nil || block.Type != "PRIVATE KEY" ||
		len(bytes.TrimSpace(rest)) != 0 {
		return nil, fmt.Errorf(
			"%w: private key PEM is invalid",
			ErrRootStateInvalid,
		)
	}
	value, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf(
			"%w: parse private key: %v",
			ErrRootStateInvalid,
			err,
		)
	}
	key, ok := value.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf(
			"%w: private key algorithm is invalid",
			ErrRootStateInvalid,
		)
	}
	return key, nil
}

func parseCertificate(encoded []byte) (*x509.Certificate, error) {
	block, rest := pem.Decode(encoded)
	if block == nil || block.Type != "CERTIFICATE" ||
		len(bytes.TrimSpace(rest)) != 0 {
		return nil, fmt.Errorf(
			"%w: certificate PEM is invalid",
			ErrRootStateInvalid,
		)
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf(
			"%w: parse certificate: %v",
			ErrRootStateInvalid,
			err,
		)
	}
	return certificate, nil
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
