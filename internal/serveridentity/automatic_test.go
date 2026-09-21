package serveridentity

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestAutomaticPolicyRequiresPublicNameAndExplicitTerms(t *testing.T) {
	t.Parallel()
	valid := AutomaticPolicy{
		ServerName:        "runtime.example.com",
		TermsAgreed:       true,
		Challenge:         AutomaticChallengeHTTP01,
		HTTPChallengePort: 8080,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid policy: %v", err)
	}
	for _, mutate := range []func(*AutomaticPolicy){
		func(policy *AutomaticPolicy) { policy.ServerName = "" },
		func(policy *AutomaticPolicy) { policy.ServerName = "localhost" },
		func(policy *AutomaticPolicy) { policy.ServerName = "192.0.2.10" },
		func(policy *AutomaticPolicy) { policy.ServerName = "*.example.com" },
		func(policy *AutomaticPolicy) { policy.TermsAgreed = false },
		func(policy *AutomaticPolicy) { policy.Challenge = "other" },
		func(policy *AutomaticPolicy) { policy.HTTPChallengePort = 0 },
	} {
		candidate := valid
		mutate(&candidate)
		if err := candidate.Validate(); err == nil {
			t.Fatalf("invalid policy was accepted: %+v", candidate)
		}
	}
	tlsALPN := AutomaticPolicy{
		ServerName:  "runtime.example.com",
		TermsAgreed: true,
		Challenge:   AutomaticChallengeTLSALPN01,
	}
	if err := tlsALPN.Validate(); err != nil {
		t.Fatalf("TLS-ALPN policy: %v", err)
	}
}

func TestAutomaticIdentityTracksLiveCertificateAndStopsManager(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 21, 11, 0, 0, 0, time.UTC)
	first := automaticTestCertificate(t, now, "first")
	renewed := automaticTestCertificate(t, now, "renewed")
	manager := &fakeAutomaticManager{certificate: first}
	options := AutomaticOptions{
		Policy: AutomaticPolicy{
			ServerName:        "runtime.example.com",
			TermsAgreed:       true,
			Challenge:         AutomaticChallengeHTTP01,
			HTTPChallengePort: 8080,
		},
		DataDirectory: filepath.Join(t.TempDir(), "automatic-https"),
	}
	identity, err := openAutomaticWithManager(
		context.Background(),
		options,
		manager,
	)
	if err != nil {
		t.Fatal(err)
	}
	initial := identity.Status()
	if initial.State != AutomaticStateReady || initial.Fingerprint == "" ||
		initial.ServerName != "runtime.example.com" ||
		manager.managedName != "runtime.example.com" || manager.asynchronous {
		t.Fatalf("initial status = %+v manager=%+v", initial, manager)
	}
	manager.certificate = renewed
	rotated := identity.Status()
	if rotated.Fingerprint == "" || rotated.Fingerprint == initial.Fingerprint {
		t.Fatalf("rotated status = %+v initial=%+v", rotated, initial)
	}
	config := identity.TLSConfig()
	if config.MinVersion != tls.VersionTLS13 ||
		len(config.NextProtos) != 1 || config.NextProtos[0] != "http/1.1" {
		t.Fatalf("TLS config = %+v", config)
	}
	identity.Close()
	identity.Close()
	if manager.closeCalls != 1 {
		t.Fatalf("manager close calls = %d", manager.closeCalls)
	}
}

func TestTLSALPNAutomaticIdentityStartsAsynchronously(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 11, 0, 0, 0, time.UTC)
	manager := &fakeAutomaticManager{certificate: automaticTestCertificate(t, now, "alpn")}
	identity, err := openAutomaticWithManager(
		context.Background(),
		AutomaticOptions{
			Policy: AutomaticPolicy{
				ServerName:  "runtime.example.com",
				TermsAgreed: true,
				Challenge:   AutomaticChallengeTLSALPN01,
			},
			DataDirectory: filepath.Join(t.TempDir(), "automatic-https"),
		},
		manager,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer identity.Close()
	if !manager.asynchronous {
		t.Fatal("TLS-ALPN management did not start asynchronously")
	}
	config := identity.TLSConfig()
	if len(config.NextProtos) != 2 || config.NextProtos[0] != "http/1.1" ||
		config.NextProtos[1] != "acme-tls/1" {
		t.Fatalf("TLS-ALPN protocols = %#v", config.NextProtos)
	}
}

func TestAutomaticIdentityDistinguishesFirstIssuanceFromRenewal(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 11, 0, 0, 0, time.UTC)
	manager := &fakeAutomaticManager{state: AutomaticStateRenewing}
	identity, err := openAutomaticWithManager(
		context.Background(),
		AutomaticOptions{
			Policy: AutomaticPolicy{
				ServerName:  "runtime.example.com",
				TermsAgreed: true,
				Challenge:   AutomaticChallengeTLSALPN01,
			},
			DataDirectory: filepath.Join(t.TempDir(), "automatic-https"),
		},
		manager,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer identity.Close()
	if status := identity.Status(); status.State != AutomaticStatePending {
		t.Fatalf("first issuance status = %q", status.State)
	}
	manager.certificate = automaticTestCertificate(t, now, "renewing")
	if status := identity.Status(); status.State != AutomaticStateRenewing {
		t.Fatalf("renewal status = %q", status.State)
	}
}

func TestCertMagicManagerReadsCurrentCertificateWithoutAHandshake(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	options := AutomaticOptions{
		Policy: AutomaticPolicy{
			ServerName:        "runtime.example.com",
			TermsAgreed:       true,
			Challenge:         AutomaticChallengeHTTP01,
			HTTPChallengePort: 8080,
		},
		DataDirectory: filepath.Join(t.TempDir(), "automatic-https"),
	}
	manager, err := newCertMagicManager(options)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if _, err := manager.Current(options.Policy.ServerName); err == nil {
		t.Fatal("empty automatic certificate cache appeared ready")
	}
	certificate := automaticTestCertificate(t, now, "cached")
	if _, err := manager.config.CacheUnmanagedTLSCertificate(
		context.Background(), certificate, nil,
	); err != nil {
		t.Fatal(err)
	}
	current, err := manager.Current(options.Policy.ServerName)
	if err != nil {
		t.Fatal(err)
	}
	if len(current.Certificate) == 0 ||
		!bytes.Equal(current.Certificate[0], certificate.Certificate[0]) {
		t.Fatal("automatic certificate status did not read the live cache")
	}
}

type fakeAutomaticManager struct {
	certificate  tls.Certificate
	state        AutomaticState
	lastError    string
	managedName  string
	asynchronous bool
	closeCalls   int
}

func (manager *fakeAutomaticManager) Manage(
	_ context.Context,
	name string,
	asynchronous bool,
) error {
	manager.managedName = name
	manager.asynchronous = asynchronous
	return nil
}

func (manager *fakeAutomaticManager) TLSConfig() *tls.Config {
	return &tls.Config{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			certificate := manager.certificate
			return &certificate, nil
		},
	}
}

func (manager *fakeAutomaticManager) Current(string) (tls.Certificate, error) {
	if len(manager.certificate.Certificate) == 0 {
		return tls.Certificate{}, errors.New("certificate is not available")
	}
	return manager.certificate, nil
}

func (manager *fakeAutomaticManager) EventStatus() (AutomaticState, string) {
	if manager.state == "" {
		return AutomaticStateReady, manager.lastError
	}
	return manager.state, manager.lastError
}

func (manager *fakeAutomaticManager) Close() { manager.closeCalls++ }

func automaticTestCertificate(
	t *testing.T,
	now time.Time,
	suffix string,
) tls.Certificate {
	t.Helper()
	identity, err := Open(
		context.Background(),
		filepath.Join(t.TempDir(), "identity-"+suffix),
		rand.Reader,
		now,
		"runtime.example.com",
	)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := identity.Certificate()
	if err != nil {
		t.Fatal(err)
	}
	return certificate
}
