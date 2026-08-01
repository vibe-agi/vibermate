package certidentity_test

import (
	"errors"
	"testing"

	"github.com/vibe-agi/vibermate/internal/certidentity"
)

func TestRootDigestHasOneCanonicalMachineAndDisplayIdentity(t *testing.T) {
	t.Parallel()

	digest, err := certidentity.DigestRootCertificate([]byte("certificate DER"))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := certidentity.ParseRootDigest(digest.String())
	if err != nil {
		t.Fatal(err)
	}
	if parsed != digest || digest.Fingerprint() != digest.String() ||
		len(digest.Bytes()) != 32 {
		t.Fatalf("digest identity is inconsistent: %q", digest.String())
	}
	bytes := digest.Bytes()
	bytes[0] ^= 0xff
	if parsed != digest {
		t.Fatal("returned digest bytes alias the identity")
	}
	if _, err := certidentity.ParseRootDigest("ABC"); !errors.Is(
		err,
		certidentity.ErrInvalidRootDigest,
	) {
		t.Fatalf("noncanonical digest error = %v", err)
	}
}

func TestSubjectAlternativeNameCanonicalizesByConstruction(t *testing.T) {
	t.Parallel()

	dns, err := certidentity.NewDNSName("api.example.test")
	if err != nil || dns.Kind() != certidentity.SANKindDNS || !dns.Valid() {
		t.Fatalf("DNS SAN = %+v error=%v", dns, err)
	}
	ip, err := certidentity.NewIPAddress("127.0.0.1")
	if err != nil || ip.Kind() != certidentity.SANKindIP || !ip.Valid() {
		t.Fatalf("IP SAN = %+v error=%v", ip, err)
	}
	for _, invalid := range []string{
		"API.example.test",
		"*.example.test",
		"api.example.test.",
		"127.0.0.1",
		"127.0.0.1:443",
		"2001:db8::1",
	} {
		if _, err := certidentity.NewDNSName(invalid); !errors.Is(
			err,
			certidentity.ErrInvalidSAN,
		) {
			t.Fatalf("NewDNSName(%q) error = %v", invalid, err)
		}
	}
	if _, err := certidentity.NewIPAddress("::ffff:127.0.0.1"); !errors.Is(
		err,
		certidentity.ErrInvalidSAN,
	) {
		t.Fatalf("mapped IP error = %v", err)
	}
}
