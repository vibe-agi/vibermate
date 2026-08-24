package serverconnection

import (
	"context"
	"crypto/rand"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/serveridentity"
)

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
	address, _ := ParseAddress("server.lan:9666")
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
