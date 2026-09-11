package serveridentity

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfiguredHostsAreVerifiedAndPersistAcrossRestarts(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	now := time.Now().UTC()
	first, err := Open(context.Background(), directory, rand.Reader, now,
		"192.168.1.20", "vibermate.example.test", "2001:db8::20")
	if err != nil {
		t.Fatal(err)
	}
	certificate, _ := first.Certificate()
	roots := x509.NewCertPool()
	roots.AddCert(certificate.Leaf)
	for _, host := range []string{"localhost", "127.0.0.1", "::1", "192.168.1.20", "vibermate.example.test", "2001:db8::20"} {
		if _, err := certificate.Leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: host, CurrentTime: now}); err != nil {
			t.Fatalf("certificate does not verify for %s: %v", host, err)
		}
	}
	if err := certificate.Leaf.VerifyHostname("192.168.1.21"); err == nil {
		t.Fatal("certificate unexpectedly covers a different server")
	}
	second, err := Open(context.Background(), directory, rand.Reader, now.Add(time.Hour),
		"2001:db8::20", "ViberMate.Example.Test", "192.168.1.20", "192.168.1.20")
	if err != nil || second.Fingerprint() != first.Fingerprint() {
		t.Fatalf("unchanged hosts rotated the certificate: %v", err)
	}
	public := first.CertificatePEM()
	block, rest := pem.Decode(public)
	if block == nil || block.Type != "CERTIFICATE" || len(rest) != 0 || strings.Contains(string(public), "PRIVATE KEY") {
		t.Fatal("export must contain only the public certificate")
	}
}

func TestChangingConfiguredHostsReissuesAndPreservesPreviousIdentity(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	now := time.Now().UTC()
	first, err := Open(context.Background(), directory, rand.Reader, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(context.Background(), directory, rand.Reader, now, "192.168.1.20")
	if err != nil || first.Fingerprint() == second.Fingerprint() {
		t.Fatalf("changed hosts did not reissue the certificate: %v", err)
	}
	backupPath := filepath.Join(directory, identityName+".previous")
	payload, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := parseDocument(payload, now)
	if err != nil || previous.Fingerprint() != first.Fingerprint() {
		t.Fatalf("previous identity was not preserved: %v", err)
	}
	info, err := os.Stat(backupPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("previous identity must be owner-only")
	}
	third, err := Open(context.Background(), directory, rand.Reader, now)
	if err != nil {
		t.Fatal(err)
	}
	certificate, _ := third.Certificate()
	if certificate.Leaf.VerifyHostname("192.168.1.20") == nil {
		t.Fatal("removed host remains in the certificate")
	}
}

func TestInvalidConfiguredHostsCannotReplaceExistingIdentity(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	now := time.Now().UTC()
	first, err := Open(context.Background(), directory, rand.Reader, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"", "https://192.168.1.20", "192.168.1.20:9667", "0.0.0.0", "::", "224.0.0.1", "fe80::1%en0", "*.example.test"} {
		if _, err := Open(context.Background(), directory, rand.Reader, now, host); err == nil {
			t.Fatalf("accepted invalid host %q", host)
		}
	}
	after, err := Open(context.Background(), directory, rand.Reader, now)
	if err != nil || after.Fingerprint() != first.Fingerprint() {
		t.Fatal("invalid configuration changed the identity")
	}
}
