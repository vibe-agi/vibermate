package serveridentity

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenPersistsOneServerTransportIdentity(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "server-identity")
	now := time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC)
	first, err := Open(context.Background(), directory, rand.Reader, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(context.Background(), directory, rand.Reader, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !first.Valid() || first.Fingerprint() != second.Fingerprint() {
		t.Fatalf("fingerprints %q and %q", first.Fingerprint(), second.Fingerprint())
	}
	info, err := os.Stat(filepath.Join(directory, identityName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("identity mode = %o", info.Mode().Perm())
	}
}

func TestOpenFilesLoadsAnOperatorManagedCertificateWithoutCopyingItsSecret(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	now := time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC)
	generatedDirectory := filepath.Join(root, "generated")
	generated, err := Open(context.Background(), generatedDirectory, rand.Reader, now)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(filepath.Join(generatedDirectory, identityName))
	if err != nil {
		t.Fatal(err)
	}
	var stored document
	if err := json.Unmarshal(payload, &stored); err != nil {
		t.Fatal(err)
	}
	certificatePath := filepath.Join(root, "server.crt")
	privateKeyPath := filepath.Join(root, "server.key")
	if err := os.WriteFile(certificatePath, []byte(stored.CertificatePEM), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(privateKeyPath, []byte(stored.PrivateKeyPEM), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := OpenFiles(certificatePath, privateKeyPath, now)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Valid() || loaded.Fingerprint() != generated.Fingerprint() {
		t.Fatalf("loaded fingerprint = %q, want %q", loaded.Fingerprint(), generated.Fingerprint())
	}
	if _, err := os.Stat(filepath.Join(root, identityName)); !os.IsNotExist(err) {
		t.Fatalf("operator key was copied into Server storage: %v", err)
	}
}

func TestOpenFilesRejectsAnInsecurePrivateKeyFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	certificatePath := filepath.Join(root, "server.crt")
	privateKeyPath := filepath.Join(root, "server.key")
	if err := os.WriteFile(certificatePath, []byte("certificate"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(privateKeyPath, []byte("private key"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFiles(certificatePath, privateKeyPath, time.Now().UTC()); err == nil {
		t.Fatal("world-readable private key was accepted")
	}
}

func TestOpenRejectsCorruptPersistedIdentity(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "server-identity")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, identityName), []byte(`{"schema":"wrong"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), directory, rand.Reader, time.Now().UTC()); err == nil {
		t.Fatal("corrupt persisted identity was replaced silently")
	}
}
