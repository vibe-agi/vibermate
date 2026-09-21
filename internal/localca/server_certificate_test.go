package localca

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOneRootValidatesServerAndAuthorizedTrafficHandshakes(t *testing.T) {
	t.Parallel()
	authority := openAuthority(t, filepath.Join(t.TempDir(), "root"), nil)
	defer shutdownAuthority(t, authority)
	root := authority.CertificatePEM()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := authority.SignServerCertificate(context.Background(), &key.PublicKey, []string{"runtime.example.test", "192.168.1.20"})
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	server := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
	if leaf.IsCA || leaf.NotAfter.After(time.Now().Add(365*24*time.Hour)) {
		t.Fatal("invalid server leaf")
	}
	if err := verifyHandshake(server, root, "runtime.example.test"); err != nil {
		t.Fatal(err)
	}
	if err := verifyHandshake(server, root, "192.168.1.20"); err != nil {
		t.Fatal(err)
	}
	fixture := newEnvironmentFixture(t, "api", 1)
	projection := newEnvironmentProjection(t, authority, fixture)
	traffic, err := authority.Issue(context.Background(), leafAdmission(t, projection, authority, fixture))
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyHandshake(traffic, root, fixture.origin.Host()); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(server.Certificate[0], traffic.Certificate[0]) || !bytes.Equal(root, authority.CertificatePEM()) {
		t.Fatal("leaves were conflated or the Root changed")
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(root)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: fixture.origin.Host()}); err == nil {
		t.Fatal("server leaf accepted for traffic domain")
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err == nil {
		t.Fatal("client authentication enabled")
	}
}

func TestServerSigningValidatesRequestsAndStopsWithRuntime(t *testing.T) {
	authority := openAuthority(t, filepath.Join(t.TempDir(), "root"), nil)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, hosts := range [][]string{{"https://localhost:9667"}, {"*.example.test"}, {"0.0.0.0"}, {"::"}, {"fe80::1%eth0"}, {"224.0.0.1"}, strings.Split(strings.Repeat("localhost,", 33), ",")} {
		if _, err := authority.SignServerCertificate(context.Background(), &key.PublicKey, hosts); !errors.Is(err, ErrLeafRequestInvalid) {
			t.Fatalf("invalid hosts accepted: %v", err)
		}
	}
	if _, err := authority.SignServerCertificate(nil, &key.PublicKey, nil); !errors.Is(err, ErrLeafRequestInvalid) {
		t.Fatal("nil context accepted")
	}
	if _, err := authority.SignServerCertificate(context.Background(), nil, nil); !errors.Is(err, ErrLeafRequestInvalid) {
		t.Fatal("nil key accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := authority.SignServerCertificate(ctx, &key.PublicKey, nil); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled request signed")
	}
	var signing sync.WaitGroup
	for range 6 {
		signing.Add(1)
		go func() {
			defer signing.Done()
			if _, err := authority.SignServerCertificate(context.Background(), &key.PublicKey, []string{"localhost"}); err != nil {
				t.Error(err)
			}
		}()
	}
	signing.Wait()
	shutdownAuthority(t, authority)
	if _, err := authority.SignServerCertificate(context.Background(), &key.PublicKey, nil); !errors.Is(err, ErrAuthorityClosed) {
		t.Fatal("closed Runtime continued signing")
	}
}
