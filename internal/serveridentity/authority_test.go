package serveridentity

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/localca"
)

type caTestClock func() time.Time

func (clock caTestClock) Now() time.Time { return clock() }

func testRootAuthority(t *testing.T, directory string, now func() time.Time) *localca.Authority {
	t.Helper()
	options := localca.DefaultOptions(filepath.Join(directory, "test-root"), context.Background())
	options.Clock = caTestClock(now)
	root, err := localca.Open(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return root
}

func openManagerForTest(t *testing.T, ctx context.Context, directory string, random io.Reader, now func() time.Time, hosts ...string) (*Manager, error) {
	t.Helper()
	return OpenManager(ctx, directory, random, now, testRootAuthority(t, directory, now), hosts...)
}

func TestUnifiedRootIsPersistentAndSignsStartupAddresses(t *testing.T) {
	t.Parallel()
	ctx, directory := context.Background(), t.TempDir()
	rootAuthority := testRootAuthority(t, directory, time.Now)
	manager, err := OpenManager(ctx, directory, rand.Reader, time.Now, rootAuthority, "192.168.1.20", "192.168.1.30", "runtime.example.test")
	if err != nil {
		t.Fatal(err)
	}
	ca := manager.Authority()
	if !bytes.Equal(ca.CertificatePEM, rootAuthority.CertificatePEM()) || ca.Fingerprint != rootAuthority.Identity().Fingerprint() {
		t.Fatal("HTTPS export is not the exact Runtime/traffic Root")
	}
	block, rest := pem.Decode(ca.CertificatePEM)
	if block == nil || block.Type != "CERTIFICATE" || len(rest) != 0 || strings.Contains(string(ca.CertificatePEM), "PRIVATE") {
		t.Fatal("CA export is not exclusively one public certificate")
	}
	root, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !root.IsCA || root.CheckSignatureFrom(root) != nil {
		t.Fatal("invalid shared Root")
	}
	if ca.Fingerprint == manager.Current().Fingerprint() {
		t.Fatal("listener is the Root certificate")
	}
	roots := x509.NewCertPool()
	roots.AddCert(root)
	verify := func(identity Identity, host string) {
		t.Helper()
		pair, err := identity.Certificate()
		if err != nil || pair.Leaf.IsCA || !manager.IssuedByAuthority(identity) {
			t.Fatal("listener is not a Root-issued leaf")
		}
		if _, err := pair.Leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: host}); err != nil {
			t.Fatal(err)
		}
		if pair.Leaf.NotAfter.After(root.NotAfter) || pair.Leaf.NotAfter.After(time.Now().Add(365*24*time.Hour)) {
			t.Fatal("leaf lifetime exceeds its limit")
		}
		if _, err := pair.Leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: "not-configured.example.test"}); err == nil {
			t.Fatal("name validation bypassed")
		}
		if _, err := pair.Leaf.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err == nil {
			t.Fatal("client-auth certificate issued")
		}
	}
	verify(manager.Current(), "localhost")
	initial := manager.Current().Fingerprint()
	for _, host := range []string{"192.168.1.20", "192.168.1.30", "runtime.example.test"} {
		manager, err = OpenManager(ctx, directory, rand.Reader, time.Now, rootAuthority)
		if err != nil {
			t.Fatal(err)
		}
		if manager.Authority().Fingerprint != ca.Fingerprint || manager.Current().Fingerprint() != initial {
			t.Fatal("restart replaced the Root or HTTPS identity")
		}
		verify(manager.Current(), host)
	}
	if _, err := os.Stat(filepath.Join(directory, authorityName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a second HTTPS CA was created")
	}
	clear(ca.CertificatePEM)
	if bytes.Equal(manager.Authority().CertificatePEM, ca.CertificatePEM) {
		t.Fatal("public export aliases authority memory")
	}
}

func TestUnifiedRootUpgradePreservesLegacySelfSignedActiveAndIgnoresPending(t *testing.T) {
	t.Parallel()
	ctx, directory := context.Background(), t.TempDir()
	old, err := Open(ctx, directory, rand.Reader, time.Now(), "192.168.1.20")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(directory, identityName))
	doc, legacyPending, err := createDocument(rand.Reader, time.Now(), "192.168.1.30")
	if err != nil {
		t.Fatal(err)
	}
	pendingPayload := mustJSON(doc)
	if err := os.WriteFile(filepath.Join(directory, legacyPendingIdentityName), pendingPayload, 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := openManagerForTest(t, ctx, directory, rand.Reader, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	active := manager.Current()
	after, _ := os.ReadFile(filepath.Join(directory, identityName))
	pendingAfter, err := os.ReadFile(filepath.Join(directory, legacyPendingIdentityName))
	if err != nil || !bytes.Equal(pendingPayload, pendingAfter) || !bytes.Equal(before, after) || active.Fingerprint() != old.Fingerprint() || active.Fingerprint() == legacyPending.Fingerprint() {
		t.Fatal("upgrade rotated TLS without confirmation")
	}
	if manager.IssuedByAuthority(active) {
		t.Fatal("legacy leaf claimed as unified")
	}
}

// The old two-CA format is constructed only as a migration fixture.
type legacyTestCA struct {
	key         *ecdsa.PrivateKey
	certificate *x509.Certificate
	encoded     []byte
}

func (ca *legacyTestCA) CertificatePEM() []byte { return bytes.Clone(ca.encoded) }
func (ca *legacyTestCA) SignServerCertificate(_ context.Context, key *ecdsa.PublicKey, hosts []string) ([]byte, error) {
	names, err := NormalizeHosts(hosts)
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "ViberMate Runtime Server"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	for _, host := range names {
		if ip := net.ParseIP(host); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, host)
		}
	}
	return x509.CreateCertificate(rand.Reader, template, ca.certificate, key, ca.key)
}

func legacyCAFixture(t *testing.T, directory string) *legacyTestCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ViberMate Runtime HTTPS CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(365 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	doc := document{Schema: authoritySchema, CertificatePEM: string(encoded), PrivateKeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))}
	if err := os.WriteFile(filepath.Join(directory, authorityName), mustJSON(doc), 0600); err != nil {
		t.Fatal(err)
	}
	return &legacyTestCA{key: key, certificate: certificate, encoded: encoded}
}

func writeLegacyLeaf(t *testing.T, directory, name string, ca CertificateAuthority) Identity {
	t.Helper()
	authority, err := newAuthority(ca, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	doc, identity, err := createLeafDocument(context.Background(), rand.Reader, time.Now(), authority, "192.168.1.20")
	if err != nil {
		t.Fatal(err)
	}
	doc.IssuerCertificatePEM = ""
	if err := os.WriteFile(filepath.Join(directory, name), mustJSON(doc), 0600); err != nil {
		t.Fatal(err)
	}
	return identity
}

func TestUnifiedRootMigratesTwoCAInstallationAndRetiresOldKey(t *testing.T) {
	t.Parallel()
	ctx, directory := context.Background(), t.TempDir()
	legacy := legacyCAFixture(t, directory)
	active := writeLegacyLeaf(t, directory, identityName, legacy)
	writeLegacyLeaf(t, directory, legacyPendingIdentityName, legacy)
	pendingBefore, _ := os.ReadFile(filepath.Join(directory, legacyPendingIdentityName))
	originalCA, _ := os.ReadFile(filepath.Join(directory, authorityName))
	root := testRootAuthority(t, directory, time.Now)
	manager, err := OpenManager(ctx, directory, rand.Reader, time.Now, root)
	if err != nil {
		t.Fatal(err)
	}
	current := manager.Current()
	pendingAfter, err := os.ReadFile(filepath.Join(directory, legacyPendingIdentityName))
	if err != nil || current.Fingerprint() != active.Fingerprint() || !bytes.Equal(pendingBefore, pendingAfter) {
		t.Fatal("migration changed the active identity or historical pending file")
	}
	if !bytes.Equal(manager.Authority().CertificatePEM, root.CertificatePEM()) || manager.IssuedByAuthority(current) {
		t.Fatal("old CA still active")
	}
	if _, err := os.Stat(filepath.Join(directory, authorityName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("old CA still in signing location")
	}
	retired, err := os.ReadFile(filepath.Join(directory, authorityName+".retired"))
	if err != nil || !bytes.Equal(retired, originalCA) {
		t.Fatal("old CA backup not preserved")
	}
	// Restart no longer depends on the retired private key.
	if err := os.Rename(filepath.Join(directory, authorityName+".retired"), filepath.Join(directory, "rollback-ca")); err != nil {
		t.Fatal(err)
	}
	manager, err = OpenManager(ctx, directory, rand.Reader, time.Now, root)
	if err != nil {
		t.Fatal(err)
	}
	if manager.Current().Fingerprint() != active.Fingerprint() || manager.IssuedByAuthority(manager.Current()) {
		t.Fatal("restart replaced the legacy HTTPS identity")
	}
}

func TestUnifiedRootMigrationRejectsInvalidLegacyIssuer(t *testing.T) {
	for _, mutation := range []string{"missing", "corrupt", "wrong-ca", "permissions", "symlink"} {
		t.Run(mutation, func(t *testing.T) {
			directory := t.TempDir()
			legacy := legacyCAFixture(t, directory)
			writeLegacyLeaf(t, directory, identityName, legacy)
			original, _ := os.ReadFile(filepath.Join(directory, identityName))
			path := filepath.Join(directory, authorityName)
			var err error
			switch mutation {
			case "missing":
				err = os.Remove(path)
			case "corrupt":
				err = os.WriteFile(path, []byte("{}"), 0600)
			case "wrong-ca":
				legacyCAFixture(t, directory)
			case "permissions":
				err = os.Chmod(path, 0644)
			case "symlink":
				if err = os.Rename(path, path+".other"); err == nil {
					err = os.Symlink(path+".other", path)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			root := testRootAuthority(t, directory, time.Now)
			if _, err := OpenManager(context.Background(), directory, rand.Reader, time.Now, root); !errors.Is(err, ErrInvalidAuthority) {
				t.Fatalf("invalid migration accepted: %v", err)
			}
			current, _ := os.ReadFile(filepath.Join(directory, identityName))
			if !bytes.Equal(original, current) {
				t.Fatal("failed migration rewrote active identity")
			}
		})
	}
}

func TestUnifiedRootReplacementDoesNotRotateExistingHTTPSIdentity(t *testing.T) {
	directory := t.TempDir()
	ctx := context.Background()
	first := testRootAuthority(t, directory, time.Now)
	manager, err := OpenManager(ctx, directory, rand.Reader, time.Now, first)
	if err != nil {
		t.Fatal(err)
	}
	active := manager.Current()
	replacement := testRootAuthority(t, t.TempDir(), time.Now)
	manager, err = OpenManager(ctx, directory, rand.Reader, time.Now, replacement)
	if err != nil {
		t.Fatal(err)
	}
	if manager.Current().Fingerprint() != active.Fingerprint() || manager.IssuedByAuthority(active) {
		t.Fatal("Root replacement silently switched HTTPS")
	}
	if manager.Authority().Fingerprint != replacement.Identity().Fingerprint() {
		t.Fatal("export still exposes a retired Root")
	}
}

func TestUnifiedRootDoesNotIssueBeyondItsValidity(t *testing.T) {
	now := time.Now().UTC()
	clock := func() time.Time { return now }
	manager, err := openManagerForTest(t, context.Background(), t.TempDir(), rand.Reader, clock)
	if err != nil {
		t.Fatal(err)
	}
	now = manager.Authority().NotAfter.Add(-time.Hour)
	_, leaf, err := createLeafDocument(context.Background(), rand.Reader, now, manager.ca)
	if err != nil {
		t.Fatal(err)
	}
	if !leaf.certificate.Leaf.NotAfter.Equal(manager.Authority().NotAfter) {
		t.Fatal("leaf outlives Root")
	}
	now = manager.Authority().NotAfter.Add(time.Second)
	if _, _, err := createLeafDocument(context.Background(), rand.Reader, now, manager.ca); !errors.Is(err, ErrInvalidAuthority) {
		t.Fatalf("expired Root: %v", err)
	}
}
