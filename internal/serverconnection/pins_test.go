package serverconnection

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/serveridentity"
)

func TestTrustStorePreservesLegacyPinDocument(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
	identity, err := serveridentity.Open(
		context.Background(), filepath.Join(t.TempDir(), "identity"), rand.Reader, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := identity.Certificate()
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(t.TempDir(), "trust")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	address, _ := ParseAddress("localhost:9666")
	payload := []byte(`{"schema":"vibermate-server-pins-v1","pins":{"localhost:9666":"` +
		identity.Fingerprint() + `"}}`)
	if err := os.WriteFile(filepath.Join(directory, pinFileName), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenPinStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.VerifyPeer(address, certificate.Certificate, now, nil)
	if err != nil || result.Mode != TrustModePinnedLeaf || result.FirstUse {
		t.Fatalf("legacy pin verification = %+v, %v", result, err)
	}
}

func TestTrustStoreUsesSystemRootsAcrossLeafRenewal(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
	root, rootKey := newTestCertificateAuthority(t, now)
	first := newTestServerCertificate(t, root, rootKey, now, "runtime.example.test", 2)
	renewed := newTestServerCertificate(t, root, rootKey, now, "runtime.example.test", 3)
	roots := x509.NewCertPool()
	roots.AddCert(root)
	address, err := ParseAddress("runtime.example.test:9666")
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenPinStore(filepath.Join(t.TempDir(), "trust"))
	if err != nil {
		t.Fatal(err)
	}

	firstResult, err := store.VerifyPeer(address, [][]byte{first.Raw}, now, roots)
	if err != nil {
		t.Fatalf("first public verification: %v", err)
	}
	if firstResult.Mode != TrustModeSystemRoots || firstResult.FirstUse {
		t.Fatalf("first public verification = %+v", firstResult)
	}
	renewedResult, err := store.VerifyPeer(address, [][]byte{renewed.Raw}, now, roots)
	if err != nil {
		t.Fatalf("renewed public verification: %v", err)
	}
	if renewedResult.Mode != TrustModeSystemRoots || renewedResult.FirstUse ||
		renewedResult.Fingerprint == firstResult.Fingerprint {
		t.Fatalf("renewed public verification = %+v", renewedResult)
	}
}

func TestSystemRootTrustRejectsWrongNameAndUntrustedReplacement(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
	root, rootKey := newTestCertificateAuthority(t, now)
	wrongName := newTestServerCertificate(t, root, rootKey, now, "other.example.test", 4)
	trusted := newTestServerCertificate(t, root, rootKey, now, "runtime.example.test", 5)
	otherRoot, otherKey := newTestCertificateAuthority(t, now)
	untrusted := newTestServerCertificate(t, otherRoot, otherKey, now, "runtime.example.test", 6)
	roots := x509.NewCertPool()
	roots.AddCert(root)
	address, _ := ParseAddress("runtime.example.test:9666")
	store, err := OpenPinStore(filepath.Join(t.TempDir(), "trust"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.VerifyPeer(address, [][]byte{wrongName.Raw}, now, roots); err == nil {
		t.Fatal("system trust accepted a certificate for the wrong DNS name")
	}
	if _, err := store.VerifyPeer(address, [][]byte{trusted.Raw}, now, roots); err != nil {
		t.Fatalf("trusted certificate: %v", err)
	}
	if _, err := store.VerifyPeer(address, [][]byte{untrusted.Raw}, now, roots); err == nil {
		t.Fatal("system trust fell back to a new leaf pin after enrollment")
	}
}

func TestPinnedTrustMigratesDeliberatelyToSystemRoots(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
	root, rootKey := newTestCertificateAuthority(t, now)
	first := newTestServerCertificate(t, root, rootKey, now, "runtime.example.test", 7)
	renewed := newTestServerCertificate(t, root, rootKey, now, "runtime.example.test", 8)
	address, _ := ParseAddress("runtime.example.test:9666")
	store, err := OpenPinStore(filepath.Join(t.TempDir(), "trust"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.Verify(address, [][]byte{first.Raw}, now)
	if err != nil || result.Mode != TrustModePinnedLeaf || !result.FirstUse {
		t.Fatalf("initial pin = %+v, %v", result, err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(root)
	if _, err := store.VerifyPeer(address, [][]byte{renewed.Raw}, now, roots); !errors.Is(
		err,
		ErrServerIdentityChanged,
	) {
		t.Fatalf("renewal before migration error = %v", err)
	}
	if err := store.UseSystemRoots(address); err != nil {
		t.Fatalf("UseSystemRoots: %v", err)
	}
	result, err = store.VerifyPeer(address, [][]byte{renewed.Raw}, now, roots)
	if err != nil || result.Mode != TrustModeSystemRoots || result.FirstUse {
		t.Fatalf("renewal after migration = %+v, %v", result, err)
	}
}

func TestExplicitLeafPinRejectsCertificateForAnotherHost(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
	root, rootKey := newTestCertificateAuthority(t, now)
	wrongName := newTestServerCertificate(t, root, rootKey, now, "other.example.test", 9)
	address, _ := ParseAddress("runtime.example.test:9666")
	store, err := OpenPinStore(filepath.Join(t.TempDir(), "trust"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.Verify(address, [][]byte{wrongName.Raw}, now); !errors.Is(
		err,
		ErrInvalidCertificate,
	) {
		t.Fatalf("wrong-name leaf pin error = %v", err)
	}
}

func newTestCertificateAuthority(
	t *testing.T,
	now time.Time,
) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "ViberMate test root"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(48 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return certificate, key
}

func newTestServerCertificate(
	t *testing.T,
	root *x509.Certificate,
	rootKey *ecdsa.PrivateKey,
	now time.Time,
	name string,
	serial int64,
) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     []string{name},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(
		rand.Reader,
		template,
		root,
		&key.PublicKey,
		rootKey,
	)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return certificate
}

func TestPinStoreTrustsFirstCertificateAndRejectsReplacement(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	firstIdentity, err := serveridentity.Open(
		context.Background(), filepath.Join(t.TempDir(), "identity-one"), rand.Reader, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	secondIdentity, err := serveridentity.Open(
		context.Background(), filepath.Join(t.TempDir(), "identity-two"), rand.Reader, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	firstCertificate, _ := firstIdentity.Certificate()
	secondCertificate, _ := secondIdentity.Certificate()
	address, _ := ParseAddress("localhost:9666")
	directory := filepath.Join(t.TempDir(), "pins")
	store, err := OpenPinStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.Verify(address, firstCertificate.Certificate, now)
	if err != nil || !result.FirstUse || result.Fingerprint != firstIdentity.Fingerprint() {
		t.Fatalf("first verify = %+v, %v", result, err)
	}
	reopened, err := OpenPinStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	result, err = reopened.Verify(address, firstCertificate.Certificate, now)
	if err != nil || result.FirstUse {
		t.Fatalf("reopened verify = %+v, %v", result, err)
	}
	if _, err := reopened.Verify(address, secondCertificate.Certificate, now); !errors.Is(err, ErrServerIdentityChanged) {
		t.Fatalf("replacement error = %v", err)
	}
}

func TestIndependentPinStoresMergeFirstUseForDifferentServers(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	firstIdentity, err := serveridentity.Open(
		context.Background(), filepath.Join(t.TempDir(), "identity-one"), rand.Reader, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	secondIdentity, err := serveridentity.Open(
		context.Background(), filepath.Join(t.TempDir(), "identity-two"), rand.Reader, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	firstCertificate, _ := firstIdentity.Certificate()
	secondCertificate, _ := secondIdentity.Certificate()
	firstAddress, _ := ParseAddress("localhost:9666")
	secondAddress, _ := ParseAddress("localhost:9667")
	directory := filepath.Join(t.TempDir(), "pins")
	left, err := OpenPinStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	right, err := OpenPinStore(directory)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := left.Verify(firstAddress, firstCertificate.Certificate, now); err != nil {
		t.Fatal(err)
	}
	if _, err := right.Verify(secondAddress, secondCertificate.Certificate, now); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenPinStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		address     Address
		certificate [][]byte
	}{
		{firstAddress, firstCertificate.Certificate},
		{secondAddress, secondCertificate.Certificate},
	} {
		result, verifyErr := reopened.Verify(check.address, check.certificate, now)
		if verifyErr != nil || result.FirstUse {
			t.Fatalf("reopened Verify(%s) = %+v, %v", check.address, result, verifyErr)
		}
	}
}
