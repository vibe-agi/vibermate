package serveridentity

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

const legacyPendingIdentityName = "server-tls-pending.json"

func TestManagedCertificateStartupAndRestartPreserveIdentity(t *testing.T) {
	t.Parallel()
	ctx, directory := context.Background(), t.TempDir()
	root := testRootAuthority(t, directory, time.Now)
	manager, err := OpenManager(ctx, directory, rand.Reader, time.Now, root, "192.168.1.20", "runtime.example.test")
	if err != nil {
		t.Fatal(err)
	}
	initial := manager.Current()
	before, err := os.ReadFile(filepath.Join(directory, identityName))
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := OpenManager(ctx, directory, rand.Reader, time.Now, root, "another.example.test")
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(directory, identityName))
	if err != nil || !bytes.Equal(before, after) || restarted.Current().Fingerprint() != initial.Fingerprint() {
		t.Fatal("restart changed the persisted HTTPS identity")
	}
	certificate, err := restarted.Current().Certificate()
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"localhost", "127.0.0.1", "::1", "192.168.1.20", "runtime.example.test"} {
		if err := certificate.Leaf.VerifyHostname(host); err != nil {
			t.Fatal(err)
		}
	}
	if certificate.Leaf.VerifyHostname("another.example.test") == nil {
		t.Fatal("bootstrap hosts changed an existing certificate")
	}
	info, err := os.Stat(filepath.Join(directory, identityName))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("identity permissions are not private")
	}
	for _, name := range []string{authorityName, legacyPendingIdentityName, identityName + ".previous"} {
		if _, err := os.Stat(filepath.Join(directory, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("startup created obsolete file %s", name)
		}
	}
}

func TestManagedCertificateIgnoresHistoricalPendingFiles(t *testing.T) {
	for _, existing := range []bool{false, true} {
		for _, corrupt := range []bool{false, true} {
			t.Run(strconv.FormatBool(existing)+"-corrupt-"+strconv.FormatBool(corrupt), func(t *testing.T) {
				ctx, directory := context.Background(), t.TempDir()
				root := testRootAuthority(t, directory, time.Now)
				var initial Identity
				if existing {
					manager, err := OpenManager(ctx, directory, rand.Reader, time.Now, root)
					if err != nil {
						t.Fatal(err)
					}
					initial = manager.Current()
				}
				doc, pending, err := createDocument(rand.Reader, time.Now(), "obsolete.example.test")
				if err != nil {
					t.Fatal(err)
				}
				payload := mustJSON(doc)
				if corrupt {
					payload = []byte("obsolete incomplete pending file")
				}
				pendingPath := filepath.Join(directory, legacyPendingIdentityName)
				if err := os.WriteFile(pendingPath, payload, 0600); err != nil {
					t.Fatal(err)
				}
				manager, err := OpenManager(ctx, directory, rand.Reader, time.Now, root)
				if err != nil {
					t.Fatal(err)
				}
				after, err := os.ReadFile(pendingPath)
				if err != nil || !bytes.Equal(payload, after) {
					t.Fatal("startup modified a historical pending file")
				}
				if manager.Current().Fingerprint() == pending.Fingerprint() || (existing && manager.Current().Fingerprint() != initial.Fingerprint()) {
					t.Fatal("startup loaded a pending identity")
				}
			})
		}
	}
}

func TestManagedCertificateRejectsInvalidStartupWithoutOverwritingIdentity(t *testing.T) {
	for _, mutation := range []string{"invalid-host", "corrupt-active", "permissions", "directory", "cancelled"} {
		t.Run(mutation, func(t *testing.T) {
			ctx, directory := context.Background(), t.TempDir()
			root := testRootAuthority(t, directory, time.Now)
			path := filepath.Join(directory, identityName)
			var hosts []string
			var original []byte
			switch mutation {
			case "invalid-host":
				hosts = []string{"https://192.168.1.20"}
			case "corrupt-active":
				original = []byte("invalid active identity")
				if err := os.WriteFile(path, original, 0600); err != nil {
					t.Fatal(err)
				}
			case "permissions":
				if _, err := OpenManager(ctx, directory, rand.Reader, time.Now, root); err != nil {
					t.Fatal(err)
				}
				original, _ = os.ReadFile(path)
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if _, err := OpenManager(ctx, directory, rand.Reader, time.Now, root, hosts...); err == nil {
				t.Fatal("invalid startup accepted")
			}
			if original != nil {
				current, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(current, original) {
					t.Fatal("failed startup overwrote the active identity")
				}
			} else if mutation != "directory" {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("failed startup persisted an identity")
				}
			}
		})
	}
}

func TestManagedCertificateConcurrentReads(t *testing.T) {
	manager, err := openManagerForTest(t, context.Background(), t.TempDir(), rand.Reader, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	initial := manager.Current().Fingerprint()
	var readers sync.WaitGroup
	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for range 100 {
				identity := manager.Current()
				certificate, err := identity.Certificate()
				if err != nil || certificate.Leaf == nil || identity.Fingerprint() != initial {
					t.Error("incomplete or changed TLS identity")
					return
				}
				// Callers may modify the returned DER without mutating the listener.
				clear(certificate.Certificate[0])
				fresh, err := identity.Certificate()
				if err != nil || bytes.Equal(certificate.Certificate[0], fresh.Certificate[0]) {
					t.Error("certificate DER aliases listener memory")
				}
			}
		}()
	}
	readers.Wait()
}
