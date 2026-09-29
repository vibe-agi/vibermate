package serverconnection

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"path/filepath"
	"testing"
	"time"
)

func newTestIntermediate(
	t *testing.T,
	root *x509.Certificate,
	rootKey *ecdsa.PrivateKey,
	now time.Time,
) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(7),
		Subject:               pkix.Name{CommonName: "ViberMate test intermediate"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(48 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, root, &key.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return certificate, key
}

// Private PKI commonly serves leaf + intermediate and keeps its root offline.
// The CLI pins the topmost presented CA, so a renewed leaf from the same
// intermediate keeps working while a certificate from another CA does not.
func TestPrivateChainWithoutRootPinsItsTopmostCA(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	root, rootKey := newTestCertificateAuthority(t, now)
	intermediate, intermediateKey := newTestIntermediate(t, root, rootKey, now)
	first := newTestServerCertificate(t, intermediate, intermediateKey, now, "runtime.example", 10)
	renewed := newTestServerCertificate(t, intermediate, intermediateKey, now, "runtime.example", 11)
	otherRoot, otherKey := newTestCertificateAuthority(t, now)
	otherIntermediate, otherIntermediateKey := newTestIntermediate(t, otherRoot, otherKey, now)
	foreign := newTestServerCertificate(t, otherIntermediate, otherIntermediateKey, now, "runtime.example", 12)

	address, err := ParseAddress("runtime.example:9666")
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenPinStore(filepath.Join(t.TempDir(), "pins"))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	result, err := store.VerifyPeer(address, [][]byte{first.Raw, intermediate.Raw}, now, roots)
	if err != nil || !result.FirstUse || result.Mode != TrustModePinnedCA {
		t.Fatalf("first leaf+intermediate verify = %+v, %v", result, err)
	}
	if _, err := store.VerifyPeer(address, [][]byte{renewed.Raw, intermediate.Raw}, now, roots); err != nil {
		t.Fatalf("renewed leaf under the pinned intermediate: %v", err)
	}
	if _, err := store.VerifyPeer(address, [][]byte{foreign.Raw, otherIntermediate.Raw}, now, roots); !errors.Is(err, ErrServerIdentityChanged) {
		t.Fatalf("certificate from another CA error = %v", err)
	}
	// A leaf that does not chain to the presented CA is malformed, not trusted.
	if _, err := store.VerifyPeer(address, [][]byte{foreign.Raw, intermediate.Raw}, now, roots); err == nil {
		t.Fatal("accepted a leaf that the presented intermediate did not sign")
	}
}
