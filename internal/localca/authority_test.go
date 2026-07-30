package localca_test

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
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/localca"
)

func TestLocalCAReopensOneRootAndIssuesExactHostLeaves(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "ca")
	authority := openAuthority(t, directory)
	root := authority.Root()
	if root.Fingerprint() == "" || root.Path() == "" ||
		len(root.CertificatePEM()) == 0 {
		t.Fatalf("incomplete Root view: %+v", root)
	}
	assertPermissions(t, directory, 0o700)
	assertPermissions(t, root.Path(), 0o600)

	block, rest := pem.Decode(root.CertificatePEM())
	if block == nil || len(rest) != 0 {
		t.Fatal("Root PEM is invalid")
	}
	rootCertificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if !rootCertificate.IsCA ||
		rootCertificate.CheckSignatureFrom(rootCertificate) != nil ||
		len(rootCertificate.DNSNames) != 0 {
		t.Fatalf("invalid Root certificate: %+v", rootCertificate)
	}

	leaf, err := authority.Issue("api.anthropic.com")
	if err != nil {
		t.Fatalf("issue leaf: %v", err)
	}
	if leaf.Leaf == nil ||
		len(leaf.Leaf.DNSNames) != 1 ||
		leaf.Leaf.DNSNames[0] != "api.anthropic.com" ||
		len(leaf.Leaf.IPAddresses) != 0 {
		t.Fatalf("leaf SANs = dns:%v ip:%v", leaf.Leaf.DNSNames, leaf.Leaf.IPAddresses)
	}
	if err := verifyHandshake(leaf, root.CertificatePEM(), "api.anthropic.com"); err != nil {
		t.Fatalf("verify issued leaf: %v", err)
	}
	if err := verifyHandshake(leaf, root.CertificatePEM(), "other.example.test"); err == nil {
		t.Fatal("exact-host leaf verified for another name")
	}

	// Returned values are independent from the cache and public Root view.
	rootBytes := root.CertificatePEM()
	rootBytes[0] ^= 0xff
	leaf.Certificate[0][0] ^= 0xff
	leaf.PrivateKey.(*ecdsa.PrivateKey).D.SetInt64(1)
	fresh, err := authority.Issue("api.anthropic.com")
	if err != nil {
		t.Fatalf("issue cached leaf after caller mutation: %v", err)
	}
	if err := verifyHandshake(
		fresh,
		authority.Root().CertificatePEM(),
		"api.anthropic.com",
	); err != nil {
		t.Fatalf("caller mutation changed cached leaf: %v", err)
	}

	if err := authority.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown authority: %v", err)
	}
	if _, err := authority.Issue("api.anthropic.com"); !errors.Is(
		err,
		localca.ErrAuthorityClosed,
	) {
		t.Fatalf("closed authority issue error = %v", err)
	}

	reopened := openAuthority(t, directory)
	defer shutdownAuthority(t, reopened)
	if reopened.Root().Fingerprint() != root.Fingerprint() ||
		string(reopened.Root().CertificatePEM()) != string(root.CertificatePEM()) {
		t.Fatal("reopen changed installation Root")
	}
}

func TestLocalCARejectsWildcardPortAndIncompletePersistentState(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "ca")
	authority := openAuthority(t, directory)
	for _, host := range []string{
		"*.example.test",
		"api.example.test:443",
		"API.example.test",
		"api.example.test.",
	} {
		if _, err := authority.Issue(host); !errors.Is(err, localca.ErrInvalidLeafHost) {
			t.Fatalf("Issue(%q) error = %v", host, err)
		}
	}
	rootPath := authority.Root().Path()
	shutdownAuthority(t, authority)
	if err := os.Remove(rootPath); err != nil {
		t.Fatalf("remove Root certificate fixture: %v", err)
	}
	if _, err := localca.Open(
		context.Background(),
		localca.DefaultOptions(directory),
	); !errors.Is(err, localca.ErrRootStateInvalid) {
		t.Fatalf("incomplete Root state error = %v", err)
	}
}

func TestLocalCALeafIssuanceIsRaceSafe(t *testing.T) {
	t.Parallel()

	authority := openAuthority(t, filepath.Join(t.TempDir(), "ca"))
	defer shutdownAuthority(t, authority)
	const callers = 32
	var wait sync.WaitGroup
	failures := make(chan error, callers)
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			certificate, err := authority.Issue("api.anthropic.com")
			if err == nil {
				err = certificate.Leaf.VerifyHostname("api.anthropic.com")
			}
			failures <- err
		}()
	}
	wait.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatalf("concurrent issuance: %v", err)
		}
	}
}

func openAuthority(t *testing.T, directory string) *localca.Authority {
	t.Helper()
	authority, err := localca.Open(
		context.Background(),
		localca.DefaultOptions(directory),
	)
	if err != nil {
		t.Fatalf("open local CA: %v", err)
	}
	return authority
}

func shutdownAuthority(t *testing.T, authority *localca.Authority) {
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
