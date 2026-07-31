package localca

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/certidentity"
)

func TestLocalCAReopensOneRootAndIssuesRevisionAuthorizedLeaf(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "ca")
	authority := openAuthority(t, directory, nil)
	identity := authority.Identity()
	delivery := authority.Certificate()
	if !identity.Valid() || !delivery.Valid() ||
		identity.Revision() != certidentity.InitialRootRevision {
		t.Fatalf("incomplete Root identity or delivery: %+v %+v", identity, delivery)
	}
	assertPermissions(t, directory, 0o700)
	assertPermissions(t, delivery.Path(), 0o600)

	block, rest := pem.Decode(delivery.CertificatePEM())
	if block == nil || len(rest) != 0 {
		t.Fatal("Root PEM is invalid")
	}
	rootCertificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if !rootCertificate.IsCA ||
		rootCertificate.CheckSignatureFrom(rootCertificate) != nil ||
		len(rootCertificate.DNSNames) != 0 ||
		identity.Digest().String() == "" ||
		identity.Fingerprint() != identity.Digest().String() {
		t.Fatalf("invalid Root certificate identity: %+v", rootCertificate)
	}

	fixture := newAccessFixture(t, "api", 1)
	projection := newAccessProjection(t, authority, fixture)
	leaf, err := authority.Issue(
		context.Background(),
		leafAdmission(t, projection, authority, fixture),
	)
	if err != nil {
		t.Fatalf("issue leaf: %v", err)
	}
	if leaf.Leaf == nil ||
		len(leaf.Leaf.DNSNames) != 1 ||
		leaf.Leaf.DNSNames[0] != fixture.origin.TLSServerName() ||
		len(leaf.Leaf.IPAddresses) != 0 {
		t.Fatalf("leaf SANs = dns:%v ip:%v", leaf.Leaf.DNSNames, leaf.Leaf.IPAddresses)
	}
	if err := verifyHandshake(
		leaf,
		delivery.CertificatePEM(),
		fixture.origin.TLSServerName(),
	); err != nil {
		t.Fatalf("verify issued leaf: %v", err)
	}
	if err := verifyHandshake(
		leaf,
		delivery.CertificatePEM(),
		"other.example.test",
	); err == nil {
		t.Fatal("exact-host leaf verified for another name")
	}

	// Returned public material and derived leaf values do not alias authority
	// state or the bounded cache.
	rootBytes := delivery.CertificatePEM()
	rootBytes[0] ^= 0xff
	leaf.Certificate[0][0] ^= 0xff
	leaf.PrivateKey.(*ecdsa.PrivateKey).D.SetInt64(1)
	fresh, err := authority.Issue(
		context.Background(),
		leafAdmission(t, projection, authority, fixture),
	)
	if err != nil {
		t.Fatalf("issue cached leaf after caller mutation: %v", err)
	}
	if err := verifyHandshake(
		fresh,
		authority.Certificate().CertificatePEM(),
		fixture.origin.TLSServerName(),
	); err != nil {
		t.Fatalf("caller mutation changed cached leaf: %v", err)
	}

	closedAdmission := leafAdmission(t, projection, authority, fixture)
	shutdownAuthority(t, authority)
	if _, err := authority.Issue(
		context.Background(),
		closedAdmission,
	); !errors.Is(err, ErrAuthorityClosed) {
		t.Fatalf("closed authority issue error = %v", err)
	}

	reopened := openAuthority(t, directory, nil)
	defer shutdownAuthority(t, reopened)
	if reopened.Identity() != identity ||
		string(reopened.Certificate().CertificatePEM()) !=
			string(delivery.CertificatePEM()) {
		t.Fatal("reopen changed installation Root identity or certificate")
	}
}

func TestLocalCARejectsForgedAdmissionAndIncompletePersistentState(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "ca")
	authority := openAuthority(t, directory, nil)
	if _, err := authority.Issue(
		context.Background(),
		access.LeafIssuanceAdmission{},
	); !errors.Is(err, ErrLeafRequestInvalid) {
		t.Fatalf("forged admission error = %v", err)
	}
	certificatePath := authority.Certificate().Path()
	shutdownAuthority(t, authority)
	if err := os.Remove(certificatePath); err != nil {
		t.Fatalf("remove Root certificate fixture: %v", err)
	}
	if _, err := Open(
		context.Background(),
		DefaultOptions(directory, context.Background()),
	); !errors.Is(err, ErrRootStateInvalid) {
		t.Fatalf("incomplete Root state error = %v", err)
	}
}

func TestRootIdentityDoesNotIncludeCertificateDeliveryPath(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	firstDirectory := filepath.Join(base, "first", "ca")
	first := openAuthority(t, firstDirectory, nil)
	identity := first.Identity()
	firstPath := first.Certificate().Path()
	shutdownAuthority(t, first)

	secondDirectory := filepath.Join(base, "second", "ca")
	if err := os.MkdirAll(secondDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{rootKeyFile, rootCertFile, rootManifestFile} {
		data, err := os.ReadFile(filepath.Join(firstDirectory, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(secondDirectory, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	second := openAuthority(t, secondDirectory, nil)
	defer shutdownAuthority(t, second)
	if second.Identity() != identity ||
		second.Certificate().Path() == firstPath {
		t.Fatalf(
			"relocated Root identity/path = %+v %q",
			second.Identity(),
			second.Certificate().Path(),
		)
	}
}

func TestLocalCAOptionsRequireExplicitOwnerAndBoundedPolicy(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "ca")
	for name, mutate := range map[string]func(*Options){
		"owner":      func(options *Options) { options.OwnerContext = nil },
		"capacity":   func(options *Options) { options.LeafCacheCapacity = 0 },
		"timeout":    func(options *Options) { options.GenerationTimeout = 0 },
		"relative":   func(options *Options) { options.Directory = "relative" },
		"clock":      func(options *Options) { options.Clock = nil },
		"randomness": func(options *Options) { options.Random = nil },
	} {
		t.Run(name, func(t *testing.T) {
			options := DefaultOptions(directory, context.Background())
			mutate(&options)
			if _, err := Open(context.Background(), options); !errors.Is(
				err,
				ErrInvalidOptions,
			) {
				t.Fatalf("Open() error = %v", err)
			}
		})
	}
}

func openAuthority(
	t *testing.T,
	directory string,
	mutate func(*Options),
) *Authority {
	t.Helper()
	options := DefaultOptions(directory, context.Background())
	if mutate != nil {
		mutate(&options)
	}
	authority, err := Open(context.Background(), options)
	if err != nil {
		t.Fatalf("open local CA: %v", err)
	}
	return authority
}

func shutdownAuthority(t *testing.T, authority *Authority) {
	t.Helper()
	if err := authority.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown local CA: %v", err)
	}
}

func assertPermissions(t *testing.T, path string, expected os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	if info.Mode().Perm() != expected {
		t.Fatalf("%q permissions = %o, want %o", path, info.Mode().Perm(), expected)
	}
}

func verifyHandshake(
	certificate tls.Certificate,
	rootPEM []byte,
	serverName string,
) error {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(rootPEM) {
		return errors.New("append Root certificate")
	}
	serverSide, clientSide := net.Pipe()
	server := tls.Server(serverSide, &tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS13,
	})
	client := tls.Client(clientSide, &tls.Config{
		RootCAs:    pool,
		ServerName: serverName,
		MinVersion: tls.VersionTLS13,
	})
	defer server.Close()
	defer client.Close()
	serverResult := make(chan error, 1)
	go func() {
		serverResult <- server.Handshake()
	}()
	clientErr := client.Handshake()
	serverErr := <-serverResult
	return errors.Join(clientErr, serverErr)
}

type fixedClock struct {
	now time.Time
}

func (clock fixedClock) Now() time.Time {
	return clock.now
}
